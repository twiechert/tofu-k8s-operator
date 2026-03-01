//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDeleteProtection(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: delete-protect-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: delete-protect-test
    namespace: default
  autoApprove: true
  deleteProtection: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Wait for successful apply
	waitForPhase(t, dynClient, "default", "delete-protect-test", "Succeeded", 120*time.Second)

	// Delete the resource — should block at WaitingDeleteApproval
	go func() {
		kubectlMayFail("delete", "tofuproject", "delete-protect-test", "-n", "default", "--timeout=5s")
	}()

	waitForPhase(t, dynClient, "default", "delete-protect-test", "WaitingDeleteApproval", 30*time.Second)

	// Verify no destroy job exists yet
	noJobsWithLabel(t, "default", "tofu.example.com/job-type=destroy,tofu.example.com/project=delete-protect-test")

	// Approve deletion by setting the annotation
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"tofu.example.com/approved-delete": "true",
			},
		},
	}
	patchBytes, _ := json.Marshal(patch)
	_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
		context.Background(), "delete-protect-test", types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to approve delete: %v", err)
	}

	// Wait for the destroy job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=destroy,tofu.example.com/project=delete-protect-test", 60*time.Second)

	// Wait for the resource to be fully deleted (destroy completes, finalizer removed)
	deadline := time.Now().Add(120 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for project to be deleted after approval")
		}
		_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "delete-protect-test", metav1.GetOptions{})
		if err != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
}

func TestDeleteWithoutProtection(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: delete-noprotect-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: delete-noprotect-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Wait for successful apply
	waitForPhase(t, dynClient, "default", "delete-noprotect-test", "Succeeded", 120*time.Second)

	// Delete — should proceed directly to Destroying without needing approval
	go func() {
		kubectlMayFail("delete", "tofuproject", "delete-noprotect-test", "-n", "default", "--timeout=120s")
	}()

	// Should transition to Destroying (not WaitingDeleteApproval)
	waitForPhase(t, dynClient, "default", "delete-noprotect-test", "Destroying", 30*time.Second)

	// Wait for full deletion
	deadline := time.Now().Add(120 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for project to be deleted")
		}
		_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "delete-noprotect-test", metav1.GetOptions{})
		if err != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
}
