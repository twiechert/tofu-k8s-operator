//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestPlanApproveFlow(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	// Create project with autoApprove=false
	planProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: plan-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "staging"
  backend:
    secretSuffix: plan-test
    namespace: default
  autoApprove: false
`
	applyYAML(t, planProject)
	defer deleteYAML(t, planProject)

	// Wait for plan job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=plan-test", 60*time.Second)

	// Wait for WaitingApproval phase
	waitForPhase(t, dynClient, "default", "plan-test", "WaitingApproval", 120*time.Second)

	// Verify pendingPlanHash is set
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "plan-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get plan-test: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	pendingHash, _ := status["pendingPlanHash"].(string)
	if pendingHash == "" {
		t.Fatal("expected pendingPlanHash to be set")
	}
	planSummary, _ := status["planSummary"].(string)
	t.Logf("Plan summary: %s", planSummary)

	// Verify no apply job exists yet
	noJobsWithLabel(t, "default", "tofu.example.com/job-type=apply,tofu.example.com/project=plan-test")

	// Approve by setting the annotation
	annPatch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"tofu.example.com/approved-hash": pendingHash,
			},
		},
	}
	patchBytes, _ := json.Marshal(annPatch)
	_, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
		context.Background(), "plan-test", types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to approve plan: %v", err)
	}

	// Wait for apply job
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=apply,tofu.example.com/project=plan-test", 60*time.Second)

	// Wait for Succeeded
	waitForPhase(t, dynClient, "default", "plan-test", "Succeeded", 120*time.Second)
}
