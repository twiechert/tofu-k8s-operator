//go:build integration
// +build integration

package e2e

import (
	"os"
	"testing"
	"time"
)

// TestGitSourceProgram tests the git source flow by self-referencing this repository.
// Set TOFU_E2E_GIT_URL to the repo URL (e.g. https://github.com/twiechert/tofu-k8s-operator.git).
// Set TOFU_E2E_GIT_REF to the branch/tag to use (defaults to "main").
// The test uses the program at test/e2e/testdata/simple-program within the repo.
func TestGitSourceProgram(t *testing.T) {
	t.Parallel()
	gitURL := os.Getenv("TOFU_E2E_GIT_URL")
	if gitURL == "" {
		t.Skip("TOFU_E2E_GIT_URL not set, skipping git source test")
	}
	gitRef := os.Getenv("TOFU_E2E_GIT_REF")
	if gitRef == "" {
		gitRef = "main"
	}

	dynClient := newDynamicClient(t)

	gitProgram := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: git-program
  namespace: default
spec:
  source:
    url: ` + gitURL + `
    ref: ` + gitRef + `
    path: test/e2e/testdata/simple-program
`
	applyYAML(t, gitProgram)
	defer deleteYAML(t, gitProgram)

	gitProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: git-source-test
  namespace: default
spec:
  programRef:
    name: git-program
  params:
    environment: "e2e-git"
  backend:
    secretSuffix: git-source-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, gitProject)
	defer deleteYAML(t, gitProject)

	// Wait for job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/project=git-source-test", 60*time.Second)

	// Wait for Succeeded
	waitForPhase(t, dynClient, "default", "git-source-test", "Succeeded", 180*time.Second)
}
