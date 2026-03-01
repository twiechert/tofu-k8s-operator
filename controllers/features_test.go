package controllers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

// --- Env Vars ---

func TestAddEnvToJob(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Env = []corev1.EnvVar{
		{Name: "AWS_REGION", Value: "us-east-1"},
		{Name: "TF_LOG", Value: "DEBUG"},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	addEnvToJob(job, &project)

	envs := job.Spec.Template.Spec.Containers[0].Env
	found := map[string]string{}
	for _, e := range envs {
		found[e.Name] = e.Value
	}
	if found["AWS_REGION"] != "us-east-1" {
		t.Fatalf("expected AWS_REGION=us-east-1, got %q", found["AWS_REGION"])
	}
	if found["TF_LOG"] != "DEBUG" {
		t.Fatalf("expected TF_LOG=DEBUG, got %q", found["TF_LOG"])
	}
}

func TestAddEnvToJob_WithValueFrom(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Env = []corev1.EnvVar{
		{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
					Key:                  "password",
				},
			},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	addEnvToJob(job, &project)

	envs := job.Spec.Template.Spec.Containers[0].Env
	var found *corev1.EnvVar
	for i, e := range envs {
		if e.Name == "DB_PASSWORD" {
			found = &envs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected DB_PASSWORD env var")
	}
	if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected DB_PASSWORD to have secretKeyRef")
	}
	if found.ValueFrom.SecretKeyRef.Name != "db-secret" {
		t.Fatalf("expected secretRef name=db-secret, got %q", found.ValueFrom.SecretKeyRef.Name)
	}
}

func TestAddEnvToJob_EnvFrom(t *testing.T) {
	project := fakeProject("test")
	project.Spec.EnvFrom = []corev1.EnvFromSource{
		{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
			},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	addEnvToJob(job, &project)

	envFrom := job.Spec.Template.Spec.Containers[0].EnvFrom
	if len(envFrom) != 1 {
		t.Fatalf("expected 1 envFrom entry, got %d", len(envFrom))
	}
	if envFrom[0].ConfigMapRef.Name != "app-config" {
		t.Fatalf("expected configMapRef name=app-config, got %q", envFrom[0].ConfigMapRef.Name)
	}
}

func TestAddEnvToJob_NoEnv(t *testing.T) {
	project := fakeProject("test")
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	origLen := len(job.Spec.Template.Spec.Containers[0].Env)
	addEnvToJob(job, &project)
	if len(job.Spec.Template.Spec.Containers[0].Env) != origLen {
		t.Fatal("expected no env change when no env/envFrom specified")
	}
}

// --- Resource Limits ---

func TestAddResourcesToJob(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Resources = &tofuv1alpha1.ResourceRequirements{
		Limits:   map[string]string{"cpu": "500m", "memory": "256Mi"},
		Requests: map[string]string{"cpu": "100m", "memory": "128Mi"},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	if err := addResourcesToJob(job, &project); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := job.Spec.Template.Spec.Containers[0].Resources
	if res.Limits.Cpu().String() != "500m" {
		t.Fatalf("expected cpu limit 500m, got %s", res.Limits.Cpu().String())
	}
	if res.Limits.Memory().String() != "256Mi" {
		t.Fatalf("expected memory limit 256Mi, got %s", res.Limits.Memory().String())
	}
	if res.Requests.Cpu().String() != "100m" {
		t.Fatalf("expected cpu request 100m, got %s", res.Requests.Cpu().String())
	}
	if res.Requests.Memory().String() != "128Mi" {
		t.Fatalf("expected memory request 128Mi, got %s", res.Requests.Memory().String())
	}
}

func TestAddResourcesToJob_InvalidQuantity(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Resources = &tofuv1alpha1.ResourceRequirements{
		Limits: map[string]string{"cpu": "not-a-quantity"},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addResourcesToJob(job, &project)
	if err == nil {
		t.Fatal("expected error for invalid quantity")
	}
}

func TestAddResourcesToJob_NilResources(t *testing.T) {
	project := fakeProject("test")
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "test-apply-abc", "test-tf", "opentofu:latest", program, "tofu-runner")
	if err := addResourcesToJob(job, &project); err != nil {
		t.Fatalf("unexpected error for nil resources: %v", err)
	}
}

// --- Retry Policy ---

func TestParseRetryDelay(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect time.Duration
	}{
		{name: "valid 30s", input: "30s", expect: 30 * time.Second},
		{name: "valid 1m", input: "1m", expect: time.Minute},
		{name: "valid 5m", input: "5m", expect: 5 * time.Minute},
		{name: "empty string", input: "", expect: 30 * time.Second},
		{name: "invalid", input: "bogus", expect: 30 * time.Second},
		{name: "zero", input: "0s", expect: 30 * time.Second},
		{name: "negative", input: "-5s", expect: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryDelay(tt.input)
			if got != tt.expect {
				t.Fatalf("parseRetryDelay(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// --- Webhook Notifications ---

func TestSendNotification(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "my-project", Namespace: "default"},
		Status: tofuv1alpha1.TofuProjectStatus{
			Phase:   "Succeeded",
			Message: "Apply completed",
		},
		Spec: tofuv1alpha1.TofuProjectSpec{
			Notifications: &tofuv1alpha1.NotificationSpec{
				Webhooks: []tofuv1alpha1.WebhookNotification{
					{URL: server.URL, Events: []string{"apply:success", "apply:error"}},
				},
			},
		},
	}

	sendNotification(context.Background(), project, "apply:success")

	if received == nil {
		t.Fatal("expected to receive notification")
	}
	if received["project"] != "my-project" {
		t.Fatalf("expected project=my-project, got %q", received["project"])
	}
	if received["namespace"] != "default" {
		t.Fatalf("expected namespace=default, got %q", received["namespace"])
	}
	if received["event"] != "apply:success" {
		t.Fatalf("expected event=apply:success, got %q", received["event"])
	}
	if received["phase"] != "Succeeded" {
		t.Fatalf("expected phase=Succeeded, got %q", received["phase"])
	}
}

func TestSendNotification_FiltersByEvent(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status:     tofuv1alpha1.TofuProjectStatus{Phase: "Error"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			Notifications: &tofuv1alpha1.NotificationSpec{
				Webhooks: []tofuv1alpha1.WebhookNotification{
					{URL: server.URL, Events: []string{"apply:success"}},
				},
			},
		},
	}

	// Send a non-matching event
	sendNotification(context.Background(), project, "apply:error")
	if callCount != 0 {
		t.Fatalf("expected 0 calls for non-matching event, got %d", callCount)
	}

	// Send a matching event
	sendNotification(context.Background(), project, "apply:success")
	if callCount != 1 {
		t.Fatalf("expected 1 call for matching event, got %d", callCount)
	}
}

func TestSendNotification_NoNotifications(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}
	// Should not panic
	sendNotification(context.Background(), project, "apply:success")
}

func TestSendNotification_MultipleWebhooks(t *testing.T) {
	callCount := 0
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server1.Close()
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server2.Close()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status:     tofuv1alpha1.TofuProjectStatus{Phase: "Succeeded"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			Notifications: &tofuv1alpha1.NotificationSpec{
				Webhooks: []tofuv1alpha1.WebhookNotification{
					{URL: server1.URL, Events: []string{"apply:success"}},
					{URL: server2.URL, Events: []string{"apply:success"}},
				},
			},
		},
	}

	sendNotification(context.Background(), project, "apply:success")
	if callCount != 2 {
		t.Fatalf("expected 2 webhook calls, got %d", callCount)
	}
}

// --- Drift Detection ---

func TestParseDriftInterval(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect time.Duration
	}{
		{name: "valid 10m", input: "10m", expect: 10 * time.Minute},
		{name: "valid 1h", input: "1h", expect: time.Hour},
		{name: "empty", input: "", expect: 15 * time.Minute},
		{name: "invalid", input: "bogus", expect: 15 * time.Minute},
		{name: "zero", input: "0s", expect: 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDriftInterval(tt.input)
			if got != tt.expect {
				t.Fatalf("parseDriftInterval(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// --- Backend & Provider Stripping ---

func TestRenderStripBackendStep(t *testing.T) {
	step := renderStripBackendStep()
	if !strings.Contains(step, "backend") {
		t.Fatal("strip backend step should reference 'backend' keyword")
	}
	if !strings.Contains(step, "awk") {
		t.Fatal("strip backend step should use awk")
	}
	if !strings.Contains(step, "backend.tf|providers.tf|additional-providers.tf") {
		t.Fatal("strip backend step should skip generated files")
	}
}

func TestRenderStripProvidersStep(t *testing.T) {
	step := renderStripProvidersStep()
	if !strings.Contains(step, "provider") {
		t.Fatal("strip providers step should reference 'provider' keyword")
	}
	if !strings.Contains(step, "awk") {
		t.Fatal("strip providers step should use awk")
	}
	if !strings.Contains(step, "backend.tf|providers.tf|additional-providers.tf") {
		t.Fatal("strip providers step should skip generated files")
	}
}

func TestRenderCommandAlwaysStripsBackend(t *testing.T) {
	cmd := renderCommand("", true, false, nil, false)
	backendStep := renderStripBackendStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderCommand should always include backend strip step")
	}
}

func TestRenderCommandWithIgnoreProviders(t *testing.T) {
	cmd := renderCommand("", true, false, nil, true)
	backendStep := renderStripBackendStep()
	providerStep := renderStripProvidersStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderCommand with ignoreProviders should include backend strip step")
	}
	if !strings.Contains(cmd, providerStep) {
		t.Fatal("renderCommand with ignoreProviders should include provider strip step")
	}
}

func TestRenderCommandWithoutIgnoreProviders(t *testing.T) {
	cmd := renderCommand("", true, false, nil, false)
	providerStep := renderStripProvidersStep()
	if strings.Contains(cmd, providerStep) {
		t.Fatal("renderCommand without ignoreProviders should NOT include provider strip step")
	}
}

func TestRenderPlanCommandWithIgnoreProviders(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, true)
	providerStep := renderStripProvidersStep()
	if !strings.Contains(cmd, providerStep) {
		t.Fatal("renderPlanCommand with ignoreProviders should include provider strip step")
	}
	backendStep := renderStripBackendStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderPlanCommand with ignoreProviders should include backend strip step")
	}
}

func TestRenderPlanCommandAlwaysStripsBackend(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, false)
	backendStep := renderStripBackendStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderPlanCommand should always include backend strip step")
	}
	providerStep := renderStripProvidersStep()
	if strings.Contains(cmd, providerStep) {
		t.Fatal("renderPlanCommand without ignoreProviders should NOT include provider strip step")
	}
}

func TestRenderDestroyCommandWithIgnoreProviders(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, true)
	providerStep := renderStripProvidersStep()
	if !strings.Contains(cmd, providerStep) {
		t.Fatal("renderDestroyCommand with ignoreProviders should include provider strip step")
	}
	backendStep := renderStripBackendStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderDestroyCommand with ignoreProviders should include backend strip step")
	}
}

func TestRenderDestroyCommandAlwaysStripsBackend(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, false)
	backendStep := renderStripBackendStep()
	if !strings.Contains(cmd, backendStep) {
		t.Fatal("renderDestroyCommand should always include backend strip step")
	}
	providerStep := renderStripProvidersStep()
	if strings.Contains(cmd, providerStep) {
		t.Fatal("renderDestroyCommand without ignoreProviders should NOT include provider strip step")
	}
}

func TestAdditionalProvidersHCLInConfigMap(t *testing.T) {
	project := fakeProject("test")
	project.Spec.AdditionalProvidersHCL = `provider "aws" { region = "us-east-1" }`
	if project.Spec.AdditionalProvidersHCL == "" {
		t.Fatal("additionalProvidersHCL should be set")
	}
	expected := project.Spec.AdditionalProvidersHCL + "\n"
	if expected != "provider \"aws\" { region = \"us-east-1\" }\n" {
		t.Fatalf("unexpected ConfigMap value: %s", expected)
	}
}

func TestHashIncludesIgnoreProviders(t *testing.T) {
	hashInput1 := "base"
	hashInput2 := "base"

	// project1: ignoreProviders=false
	// project2: ignoreProviders=true
	hashInput2 += "|ignoreProviders"

	if hashInput1 == hashInput2 {
		t.Fatal("hash inputs should differ when ignoreProviders differs")
	}
}

func TestHashIncludesAdditionalProvidersHCL(t *testing.T) {
	hashInput1 := "base"
	hashInput2 := "base"

	// project2 has additionalProvidersHCL set
	hashInput2 += "|additionalProviders:" + `provider "aws" {}`

	if hashInput1 == hashInput2 {
		t.Fatal("hash inputs should differ when additionalProvidersHCL differs")
	}
}
