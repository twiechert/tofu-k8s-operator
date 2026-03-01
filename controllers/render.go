package controllers

import (
	"fmt"
	"strings"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func isGitSource(program *tofuv1alpha1.TofuProgram) bool {
	return program.Spec.Source != nil && program.Spec.Source.URL != ""
}

func tofuValidateEnabled(project *tofuv1alpha1.TofuProject) bool {
	if project.Spec.Validation == nil || project.Spec.Validation.TofuValidate == nil {
		return true // default true
	}
	return *project.Spec.Validation.TofuValidate
}

func renderCommand(workspace string, autoApprove bool, gitMode bool, source *tofuv1alpha1.GitSource, ignoreProviders bool, tofuValidate bool) string {
	approve := ""
	if autoApprove {
		approve = " -auto-approve"
	}
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	stripSteps := renderStripBackendStep()
	if ignoreProviders {
		stripSteps += renderStripProvidersStep()
	}
	validate := ""
	if tofuValidate {
		validate = "tofu validate\n"
	}
	return fmt.Sprintf(`set -euo pipefail
%s
%stofu version
tofu init -input=false
%s%stofu apply -input=false%s
echo '%s'
tofu output -json
`, copyStep, stripSteps, validate, ws, approve, outputMarker)
}

// renderPlanCommand generates the shell command for tofu plan.
func renderPlanCommand(workspace string, gitMode bool, source *tofuv1alpha1.GitSource, ignoreProviders bool, tofuValidate bool) string {
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	stripSteps := renderStripBackendStep()
	if ignoreProviders {
		stripSteps += renderStripProvidersStep()
	}
	validate := ""
	if tofuValidate {
		validate = "tofu validate\n"
	}
	return fmt.Sprintf(`set -euo pipefail
%s
%stofu version
tofu init -input=false
%s%stofu plan -input=false -no-color
`, copyStep, stripSteps, validate, ws)
}

func renderDestroyCommand(workspace string, gitMode bool, source *tofuv1alpha1.GitSource, ignoreProviders bool, tofuValidate bool) string {
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	stripSteps := renderStripBackendStep()
	if ignoreProviders {
		stripSteps += renderStripProvidersStep()
	}
	validate := ""
	if tofuValidate {
		validate = "tofu validate\n"
	}
	return fmt.Sprintf(`set -euo pipefail
%s
%stofu version
tofu init -input=false
%s%stofu destroy -input=false -auto-approve
`, copyStep, stripSteps, validate, ws)
}

func renderProvidersTF(providers []tofuv1alpha1.ProviderSpec) string {
	var b strings.Builder
	b.WriteString("terraform {\n  required_providers {\n")
	for _, p := range providers {
		name := p.Name
		source := p.Source
		if source == "" {
			source = name
		}
		b.WriteString(fmt.Sprintf("    %s = {\n      source = %q\n", name, source))
		if p.Version != "" {
			b.WriteString(fmt.Sprintf("      version = %q\n", p.Version))
		}
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n}\n\n")
	for _, p := range providers {
		b.WriteString(fmt.Sprintf("provider %q {\n", p.Name))
		if strings.TrimSpace(p.ConfigHCL) != "" {
			b.WriteString(indentHCL(p.ConfigHCL, 2))
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

func renderBackendTF(project tofuv1alpha1.TofuProject) string {
	if project.Spec.Backend.Type == "s3" {
		return renderS3BackendTF(project)
	}
	return renderKubernetesBackendTF(project)
}

func renderKubernetesBackendTF(project tofuv1alpha1.TofuProject) string {
	ns := project.Spec.Backend.Namespace
	if ns == "" {
		ns = project.Namespace
	}
	suffix := project.Spec.Backend.SecretSuffix
	if suffix == "" {
		suffix = project.Name
	}
	// Kubernetes backend docs vary by implementation; this matches the commonly used options.
	return fmt.Sprintf(`terraform {
  backend "kubernetes" {
    secret_suffix     = %q
    namespace         = %q
    in_cluster_config = true
  }
}
`, suffix, ns)
}

func renderS3BackendTF(project tofuv1alpha1.TofuProject) string {
	s3 := project.Spec.Backend.S3
	key := s3.Key
	if key == "" {
		key = fmt.Sprintf("%s/%s/terraform.tfstate", project.Namespace, project.Name)
	}
	return fmt.Sprintf(`terraform {
  backend "s3" {
    bucket       = %q
    key          = %q
    region       = %q
    use_lockfile = true
    encrypt      = true
  }
}
`, s3.Bucket, key, s3.Region)
}

// renderStripBackendStep returns shell commands to strip backend blocks from .tf files.
// Skips generated files (backend.tf, providers.tf, additional-providers.tf).
func renderStripBackendStep() string {
	return renderStripBlockStep("backend")
}

// renderStripProvidersStep returns shell commands to strip provider blocks from .tf files.
// Skips generated files (backend.tf, providers.tf, additional-providers.tf).
func renderStripProvidersStep() string {
	return renderStripBlockStep("provider")
}

// renderStripBlockStep returns shell commands to strip top-level HCL blocks of the given keyword
// (e.g. "backend" or "provider") from .tf files using awk brace-counting.
func renderStripBlockStep(keyword string) string {
	return fmt.Sprintf(`for f in /work/*.tf; do [ -f "$f" ] || continue
  case "$(basename "$f")" in backend.tf|providers.tf|additional-providers.tf) continue ;; esac
  awk 'BEGIN{skip=0; depth=0} /^[[:space:]]*%s[[:space:]]+"[^"]*"[[:space:]]*\{/{skip=1; depth=1; next} /^[[:space:]]*%s[[:space:]]*\{/{skip=1; depth=1; next} skip{if(/\{/)depth++; if(/\}/)depth--; if(depth<=0){skip=0}; next} {print}' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
done
`, keyword, keyword)
}

func shellEscape(s string) string {
	// minimal safe quoting for sh -lc
	s = strings.ReplaceAll(s, "'", "'\\''")
	return "'" + s + "'"
}

func indentHCL(hcl string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(hcl, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}
