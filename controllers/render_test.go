package controllers

import (
	"strings"
	"testing"
	"time"

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
	cmd := renderCommand("dev", true, false, nil)
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
	cmd := renderDestroyCommand("dev", false, nil)
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
	cmd := renderDestroyCommand("", false, nil)
	if strings.Contains(cmd, "workspace") {
		t.Fatal("unexpected workspace logic")
	}
	if !strings.Contains(cmd, "tofu destroy -input=false -auto-approve") {
		t.Fatal("destroy command missing")
	}
}

func TestRenderCommandGitSource(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git"}
	cmd := renderCommand("", true, true, src)
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
	cmd := renderCommand("staging", true, true, src)
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
	cmd := renderDestroyCommand("prod", true, src)
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
	cmd := renderPlanCommand("dev", false, nil)
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
	cmd := renderPlanCommand("", false, nil)
	if strings.Contains(cmd, "workspace") {
		t.Fatal("unexpected workspace logic")
	}
	if !strings.Contains(cmd, "tofu plan -input=false -no-color") {
		t.Fatal("plan command missing")
	}
}

func TestRenderPlanCommandGitSource(t *testing.T) {
	src := &tofuv1alpha1.GitSource{URL: "https://github.com/example/repo.git", Path: "infra"}
	cmd := renderPlanCommand("staging", true, src)
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
	cmd := renderCommand("", true, false, nil)
	if !strings.Contains(cmd, outputMarker) {
		t.Fatal("apply command should include output marker")
	}
	if !strings.Contains(cmd, "tofu output -json") {
		t.Fatal("apply command should include 'tofu output -json'")
	}
}

func TestRenderPlanCommandNoOutputMarker(t *testing.T) {
	cmd := renderPlanCommand("", false, nil)
	if strings.Contains(cmd, outputMarker) {
		t.Fatal("plan command should NOT include output marker")
	}
	if strings.Contains(cmd, "tofu output -json") {
		t.Fatal("plan command should NOT include 'tofu output -json'")
	}
}

func TestRenderDestroyCommandNoOutputMarker(t *testing.T) {
	cmd := renderDestroyCommand("", false, nil)
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
			Backend: tofuv1alpha1.KubernetesBackendSpec{
				SecretSuffix: name,
			},
		},
	}
}
