//go:build integration
// +build integration

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDestroyUsesPinnedGitSource verifies that destroy uses the git commit SHA
// that was recorded at apply time, not the current TofuProgram source ref/path.
//
// Flow:
//  1. Create a TofuProgram pointing to test/e2e/testdata/simple-program in this repo.
//  2. Create a TofuProject and wait for a successful apply.
//  3. Update the TofuProgram to point to a nonexistent path (simulating major structural changes).
//  4. Delete the TofuProject.
//  5. Verify the destroy job completes successfully (proving it used the stored source, not the updated one).
//
// Requires TOFU_E2E_GIT_URL (e.g. https://github.com/twiechert/tofu-k8s-operator.git).
func TestDestroyUsesPinnedGitSource(t *testing.T) {
	t.Parallel()
	gitURL := os.Getenv("TOFU_E2E_GIT_URL")
	if gitURL == "" {
		t.Skip("TOFU_E2E_GIT_URL not set, skipping destroy pinned source test")
	}
	gitRef := os.Getenv("TOFU_E2E_GIT_REF")
	if gitRef == "" {
		gitRef = "main"
	}

	dynClient := newDynamicClient(t)

	// Step 1: Create TofuProgram with valid git source
	programName := "destroy-pin-prog"
	projectName := "destroy-pin-test"

	validProgram := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: ` + programName + `
  namespace: default
spec:
  source:
    url: ` + gitURL + `
    ref: ` + gitRef + `
    path: test/e2e/testdata/simple-program
`
	applyYAML(t, validProgram)
	defer deleteYAML(t, validProgram)

	// Step 2: Create TofuProject and wait for successful apply
	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: ` + projectName + `
  namespace: default
spec:
  programRef:
    name: ` + programName + `
  params:
    environment: "e2e-destroy-pin"
  backend:
    secretSuffix: ` + projectName + `
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForJobWithLabel(t, "default", "tofu.example.com/project="+projectName, 60*time.Second)
	waitForPhase(t, dynClient, "default", projectName, "Succeeded", 180*time.Second)

	// Step 3: Update TofuProgram to point to a nonexistent path.
	// This simulates a major structural change in the source code.
	// If destroy reads from the live TofuProgram, it will fail because
	// "nonexistent/path" doesn't exist. If it uses the pinned source from
	// the ConfigMap, it will correctly use the original path.
	brokenProgram := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: ` + programName + `
  namespace: default
spec:
  source:
    url: ` + gitURL + `
    ref: ` + gitRef + `
    path: nonexistent/path/that/does/not/exist
`
	applyYAML(t, brokenProgram)

	// Step 4: Delete the TofuProject
	go func() {
		kubectlMayFail("delete", "tofuproject", projectName, "-n", "default", "--timeout=120s")
	}()

	// Wait for the destroy job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=destroy,tofu.example.com/project="+projectName, 60*time.Second)

	// Step 5: Wait for the resource to be fully deleted.
	// This proves the destroy used the pinned git source (original path + commit SHA)
	// rather than the live TofuProgram with the broken path.
	deadline := time.Now().Add(180 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for project to be deleted — destroy likely failed due to using live TofuProgram instead of pinned source")
		}
		_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(
			context.Background(), projectName, metav1.GetOptions{},
		)
		if err != nil {
			// Resource is gone — destroy succeeded
			break
		}
		time.Sleep(2 * time.Second)
	}
}
