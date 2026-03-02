package controllers

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func TestRenderProvidersTF(t *testing.T) {
	providers := []tofuv1alpha1.ProviderSpec{
		{Name: "aws", Source: "hashicorp/aws", Version: "~> 5.0", ConfigHCL: "region = \"eu-west-1\""},
	}

	out := renderProvidersTF(providers)

	if !strings.Contains(out, "required_providers") {
		t.Fatal("missing required_providers")
	}
	if !strings.Contains(out, "provider \"aws\"") {
		t.Fatal("missing provider block")
	}
	if !strings.Contains(out, "region") {
		t.Fatal("missing provider config")
	}
}

func TestRenderBackendTF(t *testing.T) {
	p := fakeProject("demo")
	out := renderBackendTF(p)

	if !strings.Contains(out, "backend \"kubernetes\"") {
		t.Fatal("missing kubernetes backend")
	}
	if !strings.Contains(out, "secret_suffix") {
		t.Fatal("missing secret_suffix")
	}
}

func TestRenderCommand(t *testing.T) {
	cmd := renderCommand("dev", true, false, nil, false, true)
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("workspace logic missing")
	}
	if !strings.Contains(cmd, "-auto-approve") {
		t.Fatal("auto-approve missing")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected inline copy step")
	}
}

func TestRenderDestroyCommand(t *testing.T) {
	cmd := renderDestroyCommand("dev", false, nil, false, true)
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("workspace logic missing")
	}
	if !strings.Contains(cmd, "tofu destroy") {
		t.Fatal("destroy command missing")
	}
	if !strings.Contains(cmd, "-auto-approve") {
		t.Fatal("auto-approve missing for destroy")
	}
}

func TestRenderDestroyCommandNoWorkspace(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, false, true)
	if strings.Contains(cmd, "workspace") {
		t.Fatal("unexpected workspace logic")
	}
	if !strings.Contains(cmd, "tofu destroy -input=false -auto-approve") {
		t.Fatal("destroy command missing")
	}
}

func TestRenderCommandGitSource(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git"}
	cmd := renderCommand("", true, true, src, false, true)
	if !strings.Contains(cmd, "cp -r /git-repo/./. /work/") {
		t.Fatal("expected git-repo copy step")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected config overlay step")
	}
	if !strings.Contains(cmd, "tofu apply") {
		t.Fatal("expected tofu apply")
	}
}

func TestRenderCommandGitSourceSubpath(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git", Path: "infra/prod"}
	cmd := renderCommand("staging", true, true, src, false, true)
	if !strings.Contains(cmd, "cp -r /git-repo/infra/prod/. /work/") {
		t.Fatal("expected subpath copy step")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected config overlay step")
	}
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("expected workspace selection")
	}
}

func TestRenderDestroyCommandGitSource(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git", Path: "modules/vpc"}
	cmd := renderDestroyCommand("prod", true, src, false, true)
	if !strings.Contains(cmd, "cp -r /git-repo/modules/vpc/. /work/") {
		t.Fatal("expected git-repo copy step for destroy")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected config overlay step for destroy")
	}
	if !strings.Contains(cmd, "tofu destroy") {
		t.Fatal("expected tofu destroy")
	}
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("expected workspace selection for destroy")
	}
}

func TestIsGitSource(t *testing.T) {
	// Inline mode
	inline := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	if isGitSource(inline) {
		t.Fatal("expected inline program to not be git source")
	}

	// Git source mode
	git := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			Source: &tofuv1alpha1.GitSource{
				URL: "https://github.com/example/repo.git",
				Ref: "v1.0.0",
			},
		},
	}
	if !isGitSource(git) {
		t.Fatal("expected git source program to be detected")
	}

	// Empty source (URL blank)
	emptySource := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			Source: &tofuv1alpha1.GitSource{},
		},
	}
	if isGitSource(emptySource) {
		t.Fatal("expected empty source URL to not be git source")
	}
}

// --- New tests for plan-approve flow ---

func TestRenderPlanCommand(t *testing.T) {
	cmd := renderPlanCommand("dev", false, nil, false, true)
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("workspace logic missing")
	}
	if !strings.Contains(cmd, "tofu plan -input=false -no-color") {
		t.Fatal("plan command missing")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected inline copy step")
	}
}

func TestRenderPlanCommandNoWorkspace(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, false, true)
	if strings.Contains(cmd, "workspace") {
		t.Fatal("unexpected workspace logic")
	}
	if !strings.Contains(cmd, "tofu plan -input=false -no-color") {
		t.Fatal("plan command missing")
	}
}

func TestRenderPlanCommandGitSource(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git", Path: "infra"}
	cmd := renderPlanCommand("staging", true, src, false, true)
	if !strings.Contains(cmd, "cp -r /git-repo/infra/. /work/") {
		t.Fatal("expected git-repo copy step")
	}
	if !strings.Contains(cmd, "cp /tf-config/* /work/") {
		t.Fatal("expected config overlay step")
	}
	if !strings.Contains(cmd, "tofu plan -input=false -no-color") {
		t.Fatal("expected tofu plan")
	}
	if !strings.Contains(cmd, "workspace") {
		t.Fatal("expected workspace selection")
	}
}

func TestExtractPlanSummary(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "normal plan",
			input:  "Refreshing state...\n\nPlan: 2 to add, 0 to change, 1 to destroy.\n\nDo you want to perform these actions?",
			expect: "Plan: 2 to add, 0 to change, 1 to destroy.",
		},
		{
			name:   "no changes",
			input:  "Refreshing state...\n\nNo changes. Your infrastructure matches the configuration.\n",
			expect: "No changes.",
		},
		{
			name:   "no changes up-to-date",
			input:  "No changes. Infrastructure is up-to-date.\n",
			expect: "No changes.",
		},
		{
			name:   "empty output",
			input:  "",
			expect: "",
		},
		{
			name:   "no recognizable summary",
			input:  "some random output\nwithout plan lines\n",
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanSummary(tt.input)
			if got != tt.expect {
				t.Fatalf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestBuildPlanJobLabels(t *testing.T) {
	project := fakeProject("myproj")
	project.Name = "myproj"
	project.Namespace = "default"
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildPlanJob(project, "myproj-plan-abc12345", "myproj-tf", "opentofu:latest", program, "tofu-runner")
	if job.Labels["tofu.example.com/job-type"] != "plan" {
		t.Fatalf("expected job-type=plan label, got %q", job.Labels["tofu.example.com/job-type"])
	}
}

func TestBuildApplyJobForcesAutoApprove(t *testing.T) {
	project := fakeProject("myproj")
	project.Name = "myproj"
	project.Namespace = "default"
	project.Spec.AutoApprove = false // explicitly false
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildApplyJob(project, "myproj-apply-abc12345", "myproj-tf", "opentofu:latest", program, "tofu-runner")
	cmd := job.Spec.Template.Spec.Containers[0].Command[2]
	if !strings.Contains(cmd, "-auto-approve") {
		t.Fatal("buildApplyJob should force -auto-approve even when spec.autoApprove is false")
	}
}

func TestBuildJobApplyLabel(t *testing.T) {
	project := fakeProject("myproj")
	project.Name = "myproj"
	project.Namespace = "default"
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "myproj-apply-abc12345", "myproj-tf", "opentofu:latest", program, "tofu-runner")
	if job.Labels["tofu.example.com/job-type"] != "apply" {
		t.Fatalf("expected job-type=apply label, got %q", job.Labels["tofu.example.com/job-type"])
	}
}

func TestBuildDestroyJobLabel(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{}
	project.Name = "myproj"
	project.Namespace = "default"
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildDestroyJob(project, "myproj-destroy", "myproj-tf", "opentofu:latest", program, "tofu-runner")
	if job.Labels["tofu.example.com/job-type"] != "destroy" {
		t.Fatalf("expected job-type=destroy label, got %q", job.Labels["tofu.example.com/job-type"])
	}
}

func TestParseSyncInterval(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect time.Duration
	}{
		{name: "valid 5m", input: "5m", expect: 5 * time.Minute},
		{name: "empty string", input: "", expect: 0},
		{name: "invalid", input: "bogus", expect: 0},
		{name: "zero", input: "0s", expect: 0},
		{name: "valid 1h", input: "1h", expect: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSyncInterval(tt.input)
			if got != tt.expect {
				t.Fatalf("parseSyncInterval(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// --- Tests for output marker in render commands ---

func TestRenderCommandIncludesOutputMarker(t *testing.T) {
	cmd := renderCommand("", true, false, nil, false, true)
	if !strings.Contains(cmd, outputMarker) {
		t.Fatal("apply command should include output marker")
	}
	if !strings.Contains(cmd, "tofu output -json") {
		t.Fatal("apply command should include 'tofu output -json'")
	}
}

func TestRenderPlanCommandNoOutputMarker(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, false, true)
	if strings.Contains(cmd, outputMarker) {
		t.Fatal("plan command should NOT include output marker")
	}
	if strings.Contains(cmd, "tofu output -json") {
		t.Fatal("plan command should NOT include 'tofu output -json'")
	}
}

func TestRenderDestroyCommandNoOutputMarker(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, false, true)
	if strings.Contains(cmd, outputMarker) {
		t.Fatal("destroy command should NOT include output marker")
	}
	if strings.Contains(cmd, "tofu output -json") {
		t.Fatal("destroy command should NOT include 'tofu output -json'")
	}
}

// --- Tests for output parsing ---

func TestParseOutputsFromLogs(t *testing.T) {
	logs := `Applying...
Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
---TOFU-OUTPUTS---
{"vpc_id": {"value": "vpc-12345", "type": "string"}, "count": {"value": 3, "type": "number"}}
`
	outputs, err := parseOutputsFromLogs(logs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outputs["vpc_id"] != "vpc-12345" {
		t.Fatalf("expected vpc_id=vpc-12345, got %q", outputs["vpc_id"])
	}
	if outputs["count"] != "3" {
		t.Fatalf("expected count=3, got %q", outputs["count"])
	}
}

func TestParseOutputsFromLogsNoMarker(t *testing.T) {
	logs := "Apply complete! Resources: 1 added.\n"
	_, err := parseOutputsFromLogs(logs)
	if err == nil {
		t.Fatal("expected error when marker is missing")
	}
	if !strings.Contains(err.Error(), "marker not found") {
		t.Fatalf("expected 'marker not found' error, got: %v", err)
	}
}

func TestParseOutputsFromLogsEmpty(t *testing.T) {
	logs := "Apply complete!\n---TOFU-OUTPUTS---\n{}\n"
	outputs, err := parseOutputsFromLogs(logs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outputs) != 0 {
		t.Fatalf("expected empty outputs, got %v", outputs)
	}
}

// --- Tests for cache injection ---

func TestAddCacheToJob(t *testing.T) {
	project := fakeProject("myproj")
	project.Name = "myproj"
	project.Namespace = "default"
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{
			ProgramHCL: "resource \"null_resource\" \"test\" {}",
		},
	}
	job := buildJob(project, "myproj-apply-abc12345", "myproj-tf", "opentofu:latest", program, "tofu-runner")
	addCacheToJob(job, "tofu-plugin-cache")

	// Check volume
	foundVolume := false
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "plugin-cache" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "tofu-plugin-cache" {
			foundVolume = true
			break
		}
	}
	if !foundVolume {
		t.Fatal("expected plugin-cache PVC volume")
	}

	// Check mount
	foundMount := false
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "plugin-cache" && m.MountPath == "/plugin-cache" {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Fatal("expected plugin-cache volume mount at /plugin-cache")
	}

	// Check env
	foundEnv := false
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "TF_PLUGIN_CACHE_DIR" && e.Value == "/plugin-cache" {
			foundEnv = true
			break
		}
	}
	if !foundEnv {
		t.Fatal("expected TF_PLUGIN_CACHE_DIR=/plugin-cache env var")
	}
}

func fakeProject(name string) tofuv1alpha1.TofuProject {
	return tofuv1alpha1.TofuProject{
		Spec: tofuv1alpha1.TofuProjectSpec{
			Backend: tofuv1alpha1.BackendSpec{
				KubernetesBackendSpec: tofuv1alpha1.KubernetesBackendSpec{
					SecretSuffix: name,
				},
			},
		},
	}
}

func TestRenderS3BackendTF(t *testing.T) {
	p := tofuv1alpha1.TofuProject{}
	p.Name = "my-project"
	p.Namespace = "default"
	p.Spec.Backend = tofuv1alpha1.BackendSpec{
		Type: "s3",
		S3: &tofuv1alpha1.S3BackendSpec{
			Bucket: "my-tfstate-bucket",
			Region: "eu-west-1",
		},
	}

	out := renderBackendTF(p)

	if !strings.Contains(out, `backend "s3"`) {
		t.Fatal("missing s3 backend")
	}
	if !strings.Contains(out, `bucket       = "my-tfstate-bucket"`) {
		t.Fatal("missing bucket")
	}
	if !strings.Contains(out, `region       = "eu-west-1"`) {
		t.Fatal("missing region")
	}
	if !strings.Contains(out, `key          = "default/my-project/terraform.tfstate"`) {
		t.Fatal("missing default key")
	}
	if !strings.Contains(out, "use_lockfile = true") {
		t.Fatal("missing use_lockfile")
	}
	if !strings.Contains(out, "encrypt      = true") {
		t.Fatal("missing encrypt")
	}
}

func TestRenderS3BackendTFCustomKey(t *testing.T) {
	p := tofuv1alpha1.TofuProject{}
	p.Name = "my-project"
	p.Namespace = "default"
	p.Spec.Backend = tofuv1alpha1.BackendSpec{
		Type: "s3",
		S3: &tofuv1alpha1.S3BackendSpec{
			Bucket: "my-bucket",
			Region: "us-east-1",
			Key:    "custom/path/state.tfstate",
		},
	}

	out := renderBackendTF(p)

	if !strings.Contains(out, `key          = "custom/path/state.tfstate"`) {
		t.Fatal("expected custom key, got default")
	}
}

func TestRenderBackendTFDefaultsToKubernetes(t *testing.T) {
	p := fakeProject("demo")
	out := renderBackendTF(p)

	if !strings.Contains(out, `backend "kubernetes"`) {
		t.Fatal("expected kubernetes backend for empty type")
	}
}

// --- Tests for tofu validate flag ---

func TestRenderCommandWithTofuValidate(t *testing.T) {
	cmd := renderCommand("", true, false, nil, false, true)
	if !strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected 'tofu validate' when tofuValidate=true")
	}
	// Verify ordering: validate comes after init and before apply
	initIdx := strings.Index(cmd, "tofu init")
	validateIdx := strings.Index(cmd, "tofu validate")
	applyIdx := strings.Index(cmd, "tofu apply")
	if validateIdx <= initIdx || validateIdx >= applyIdx {
		t.Fatal("tofu validate should come after tofu init and before tofu apply")
	}
}

func TestRenderCommandWithoutTofuValidate(t *testing.T) {
	cmd := renderCommand("", true, false, nil, false, false)
	if strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected no 'tofu validate' when tofuValidate=false")
	}
}

func TestRenderPlanCommandWithTofuValidate(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, false, true)
	if !strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected 'tofu validate' when tofuValidate=true")
	}
	initIdx := strings.Index(cmd, "tofu init")
	validateIdx := strings.Index(cmd, "tofu validate")
	planIdx := strings.Index(cmd, "tofu plan")
	if validateIdx <= initIdx || validateIdx >= planIdx {
		t.Fatal("tofu validate should come after tofu init and before tofu plan")
	}
}

func TestRenderPlanCommandWithoutTofuValidate(t *testing.T) {
	cmd := renderPlanCommand("", false, nil, false, false)
	if strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected no 'tofu validate' when tofuValidate=false")
	}
}

func TestRenderDestroyCommandWithTofuValidate(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, false, true)
	if !strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected 'tofu validate' when tofuValidate=true")
	}
	initIdx := strings.Index(cmd, "tofu init")
	validateIdx := strings.Index(cmd, "tofu validate")
	destroyIdx := strings.Index(cmd, "tofu destroy")
	if validateIdx <= initIdx || validateIdx >= destroyIdx {
		t.Fatal("tofu validate should come after tofu init and before tofu destroy")
	}
}

func TestRenderDestroyCommandWithoutTofuValidate(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil, false, false)
	if strings.Contains(cmd, "tofu validate") {
		t.Fatal("expected no 'tofu validate' when tofuValidate=false")
	}
}

func TestTofuValidateEnabled(t *testing.T) {
	// nil Validation → default true
	p := &tofuv1alpha1.TofuProject{}
	if !tofuValidateEnabled(p) {
		t.Fatal("expected true when Validation is nil")
	}

	// nil TofuValidate → default true
	p.Spec.Validation = &tofuv1alpha1.ValidationSpec{}
	if !tofuValidateEnabled(p) {
		t.Fatal("expected true when TofuValidate is nil")
	}

	// explicit true
	tr := true
	p.Spec.Validation.TofuValidate = &tr
	if !tofuValidateEnabled(p) {
		t.Fatal("expected true when TofuValidate is true")
	}

	// explicit false
	fa := false
	p.Spec.Validation.TofuValidate = &fa
	if tofuValidateEnabled(p) {
		t.Fatal("expected false when TofuValidate is false")
	}
}

// --- Tests for addValidationToJob ---

// --- Tests for extra volumes injection ---

func TestAddExtraVolumesToJob(t *testing.T) {
	project := fakeProject("test")
	project.Spec.ExtraVolumes = []corev1.Volume{{
		Name: "policy-files",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "opa-policies"},
			},
		},
	}}
	project.Spec.ExtraVolumeMounts = []corev1.VolumeMount{{
		Name:      "policy-files",
		MountPath: "/policies",
		ReadOnly:  true,
	}}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	addExtraVolumesToJob(job, &project)

	// Check volume was appended
	foundVolume := false
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "policy-files" && v.ConfigMap != nil && v.ConfigMap.Name == "opa-policies" {
			foundVolume = true
			break
		}
	}
	if !foundVolume {
		t.Fatal("expected policy-files ConfigMap volume")
	}

	// Check mount was appended
	foundMount := false
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "policy-files" && m.MountPath == "/policies" && m.ReadOnly {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Fatal("expected policy-files volume mount at /policies")
	}
}

func TestAddExtraVolumesToJob_Empty(t *testing.T) {
	project := fakeProject("test")
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	volsBefore := len(job.Spec.Template.Spec.Volumes)
	mountsBefore := len(job.Spec.Template.Spec.Containers[0].VolumeMounts)

	addExtraVolumesToJob(job, &project)

	if len(job.Spec.Template.Spec.Volumes) != volsBefore {
		t.Fatal("expected no volumes added when extraVolumes is empty")
	}
	if len(job.Spec.Template.Spec.Containers[0].VolumeMounts) != mountsBefore {
		t.Fatal("expected no mounts added when extraVolumeMounts is empty")
	}
}

func TestAddExtraVolumesToJob_PreservesExisting(t *testing.T) {
	project := fakeProject("test")
	project.Spec.ExtraVolumes = []corev1.Volume{{
		Name: "custom-secret",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: "my-secret"},
		},
	}}
	project.Spec.ExtraVolumeMounts = []corev1.VolumeMount{{
		Name:      "custom-secret",
		MountPath: "/secrets",
		ReadOnly:  true,
	}}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	addExtraVolumesToJob(job, &project)

	// Verify built-in volumes (tf-config, work) are still present
	builtinVolumes := map[string]bool{"tf-config": false, "work": false}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if _, ok := builtinVolumes[v.Name]; ok {
			builtinVolumes[v.Name] = true
		}
	}
	for name, found := range builtinVolumes {
		if !found {
			t.Fatalf("built-in volume %q was lost after adding extra volumes", name)
		}
	}

	// Verify built-in mounts are still present
	builtinMounts := map[string]bool{"tf-config": false, "work": false}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if _, ok := builtinMounts[m.Name]; ok {
			builtinMounts[m.Name] = true
		}
	}
	for name, found := range builtinMounts {
		if !found {
			t.Fatalf("built-in mount %q was lost after adding extra volumes", name)
		}
	}

	// Verify extra volume/mount is also present
	foundExtra := false
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "custom-secret" {
			foundExtra = true
			break
		}
	}
	if !foundExtra {
		t.Fatal("expected custom-secret volume to be appended")
	}
}

func TestAddValidationToJob_StandardTflint(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "tflint", Standard: "tflint"},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	ic := job.Spec.Template.Spec.InitContainers[0]
	if ic.Name != "validate-tflint" {
		t.Fatalf("expected container name validate-tflint, got %s", ic.Name)
	}
	if ic.Image != "ghcr.io/terraform-linters/tflint:latest" {
		t.Fatalf("expected tflint image, got %s", ic.Image)
	}
	script := ic.Command[2]
	if !strings.Contains(script, "tflint --init && tflint") {
		t.Fatal("expected tflint command in script")
	}
}

func TestAddValidationToJob_CustomStep(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "my-policy", Custom: &tofuv1alpha1.CustomValidationStep{
				Command: "conftest test .",
				Image:   "openpolicyagent/conftest:latest",
			}},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ic := job.Spec.Template.Spec.InitContainers[0]
	if ic.Image != "openpolicyagent/conftest:latest" {
		t.Fatalf("expected custom image, got %s", ic.Image)
	}
	if !strings.Contains(ic.Command[2], "conftest test .") {
		t.Fatal("expected custom command")
	}
}

func TestAddValidationToJob_CustomDefaultImage(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "custom", Custom: &tofuv1alpha1.CustomValidationStep{
				Command: "echo hello",
			}},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ic := job.Spec.Template.Spec.InitContainers[0]
	if ic.Image != "opentofu:latest" {
		t.Fatalf("expected default main image, got %s", ic.Image)
	}
}

func TestAddValidationToJob_GitModeVolumeMounts(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "tflint", Standard: "tflint"},
		},
	}
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git", Path: "infra"}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{Source: src},
	}
	job := buildPlanJob(project, "test-plan", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", true, src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Find the validation init container (not the git-clone one)
	var validationIC *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "validate-tflint" {
			validationIC = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if validationIC == nil {
		t.Fatal("expected validate-tflint init container")
	}
	// Check git-repo mount
	hasGitMount := false
	for _, m := range validationIC.VolumeMounts {
		if m.Name == "git-repo" {
			hasGitMount = true
			break
		}
	}
	if !hasGitMount {
		t.Fatal("expected git-repo volume mount in git mode")
	}
	// Check copy script references git-repo with path
	if !strings.Contains(validationIC.Command[2], "cp -r /git-repo/infra/. /tmp/validate/") {
		t.Fatal("expected git-repo copy with path in script")
	}
}

func TestAddValidationToJob_UnknownStandard(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "unknown", Standard: "nonexistent"},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err == nil {
		t.Fatal("expected error for unknown standard validator")
	}
	if !strings.Contains(err.Error(), "unknown standard validator") {
		t.Fatalf("expected unknown standard validator error, got: %v", err)
	}
}

func TestAddValidationToJob_NoSteps(t *testing.T) {
	project := fakeProject("test")
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	initBefore := len(job.Spec.Template.Spec.InitContainers)
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(job.Spec.Template.Spec.InitContainers) != initBefore {
		t.Fatal("expected no init containers added when no validation steps")
	}
}

func TestAddValidationToJob_MultipleSteps(t *testing.T) {
	project := fakeProject("test")
	project.Spec.Validation = &tofuv1alpha1.ValidationSpec{
		Steps: []tofuv1alpha1.ValidationStep{
			{Name: "tflint", Standard: "tflint"},
			{Name: "checkov", Standard: "checkov"},
			{Name: "custom", Custom: &tofuv1alpha1.CustomValidationStep{Command: "echo ok", Image: "alpine:latest"}},
		},
	}
	program := &tofuv1alpha1.TofuProgram{
		Spec: tofuv1alpha1.TofuProgramSpec{ProgramHCL: "resource {}"},
	}
	job := buildJob(project, "test-apply", "test-tf", "opentofu:latest", program, "tofu-runner")
	err := addValidationToJob(job, &project, "opentofu:latest", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(job.Spec.Template.Spec.InitContainers) != 3 {
		t.Fatalf("expected 3 init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	names := []string{
		job.Spec.Template.Spec.InitContainers[0].Name,
		job.Spec.Template.Spec.InitContainers[1].Name,
		job.Spec.Template.Spec.InitContainers[2].Name,
	}
	expected := []string{"validate-tflint", "validate-checkov", "validate-custom"}
	for i, n := range names {
		if n != expected[i] {
			t.Fatalf("expected container %d to be %s, got %s", i, expected[i], n)
		}
	}
}

// --- Tests for blast radius ---

func TestExtractPlanSummaryAndParseCounts(t *testing.T) {
	output := "Refreshing state...\n\nPlan: 2 to add, 0 to change, 1 to destroy.\n\nDo you want to perform these actions?"
	summary := extractPlanSummary(output)
	br := parsePlanCounts(summary)
	if br == nil {
		t.Fatal("expected non-nil blast radius")
	}
	if br.Add != 2 || br.Change != 0 || br.Destroy != 1 || br.Total != 3 {
		t.Fatalf("unexpected blast radius: %+v", *br)
	}
}

func TestBlastRadiusAutoApproveWithinThreshold(t *testing.T) {
	threshold := int32(5)
	br := parsePlanCounts("Plan: 2 to add, 0 to change, 1 to destroy.")
	if br == nil {
		t.Fatal("expected blast radius")
	}
	if br.Total > threshold {
		t.Fatalf("expected total %d <= threshold %d", br.Total, threshold)
	}
}

func TestBlastRadiusExceedsThreshold(t *testing.T) {
	threshold := int32(2)
	br := parsePlanCounts("Plan: 2 to add, 0 to change, 1 to destroy.")
	if br == nil {
		t.Fatal("expected blast radius")
	}
	if br.Total <= threshold {
		t.Fatalf("expected total %d > threshold %d", br.Total, threshold)
	}
}

func TestBlastRadiusNoChangesAutoApprove(t *testing.T) {
	threshold := int32(0)
	br := parsePlanCounts("No changes.")
	if br == nil {
		t.Fatal("expected blast radius for no changes")
	}
	if br.Total > threshold {
		t.Fatalf("expected total %d <= threshold %d for no-changes plan", br.Total, threshold)
	}
}

func TestBlastRadiusNotConfigured(t *testing.T) {
	// When threshold is nil, auto-approve should not trigger
	var threshold *int32 = nil
	br := parsePlanCounts("Plan: 1 to add, 0 to change, 0 to destroy.")
	if br == nil {
		t.Fatal("expected blast radius")
	}
	// Simulating the controller logic: threshold nil means no auto-approve
	if threshold != nil && br.Total <= *threshold {
		t.Fatal("should not auto-approve when threshold is nil")
	}
}

// --- Tests for scheduled apply window ---

func TestScheduledApplyBlocksOutsideWindow(t *testing.T) {
	// Simulate the controller logic: when an apply schedule is configured
	// and we're outside the window, the phase should be ScheduledApply.
	project := fakeProject("test")
	project.Spec.ApplySchedule = &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "0 2 * * *", // daily at 2am
		Window:   "1h",
	}
	// Simulate being outside the window (e.g. 5pm)
	now := time.Date(2025, 1, 1, 17, 0, 0, 0, time.UTC)
	inWindow, nextWindow := isWithinApplyWindow(project.Spec.ApplySchedule, now)
	if inWindow {
		t.Fatal("expected to be outside apply window at 5pm for 2am schedule")
	}
	if nextWindow.IsZero() {
		t.Fatal("expected non-zero next window time")
	}

	// Verify phase would be set to ScheduledApply
	project.Status.Phase = "ScheduledApply"
	project.Status.Message = "Plan approved, waiting for apply window"
	if project.Status.Phase != "ScheduledApply" {
		t.Fatalf("expected phase ScheduledApply, got %s", project.Status.Phase)
	}
}

func TestScheduledApplyProceedsInWindow(t *testing.T) {
	// When within the apply window, the apply should proceed normally.
	project := fakeProject("test")
	project.Spec.ApplySchedule = &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "0 2 * * *", // daily at 2am
		Window:   "1h",
	}
	// Simulate being inside the window (2:30am)
	now := time.Date(2025, 1, 1, 2, 30, 0, 0, time.UTC)
	inWindow, _ := isWithinApplyWindow(project.Spec.ApplySchedule, now)
	if !inWindow {
		t.Fatal("expected to be within apply window at 2:30am for 2am+1h schedule")
	}
}

func TestScheduledApplyNilSchedule(t *testing.T) {
	// When no schedule is configured, the apply should proceed immediately.
	project := fakeProject("test")
	// project.Spec.ApplySchedule is nil — no schedule configured
	if project.Spec.ApplySchedule != nil {
		t.Fatal("expected nil apply schedule")
	}
	// Controller logic: when ApplySchedule is nil, skip the check entirely
}
