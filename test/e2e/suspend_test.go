//go:build integration
// +build integration

package e2e

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestSuspendMode(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	// Apply the program
	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	// Create a suspended project
	suspendedProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: suspend-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "test"
  backend:
    secretSuffix: suspend-test
    namespace: default
  autoApprove: true
  suspend: true
`
	applyYAML(t, suspendedProject)
	defer deleteYAML(t, suspendedProject)

	// Wait for phase to become Suspended
	waitForPhase(t, dynClient, "default", "suspend-test", "Suspended", 30*time.Second)

	// Verify no jobs were created
	noJobsWithLabel(t, "default", "tofu.example.com/project=suspend-test")

	// Resume by patching suspend=false
	patch := []byte(`{"spec":{"suspend":false}}`)
	_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
		context.Background(), "suspend-test", types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to resume project: %v", err)
	}

	// Wait for a job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/project=suspend-test", 60*time.Second)

	// Wait for Succeeded
	waitForPhase(t, dynClient, "default", "suspend-test", "Succeeded", 120*time.Second)
}

func TestSuspendReadyCondition(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: suspend-ready-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: suspend-ready-test
    namespace: default
  autoApprove: true
  suspend: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "suspend-ready-test", "Suspended", 30*time.Second)

	// With applyImmediately defaulting to true, Suspended → Ready=False
	status := getReadyConditionStatus(t, dynClient, "default", "suspend-ready-test")
	if status != "False" {
		t.Fatalf("expected Ready=False while suspended (applyImmediately default true), got %q", status)
	}

	// Resume
	patch := []byte(`{"spec":{"suspend":false}}`)
	_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
		context.Background(), "suspend-ready-test", types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to resume: %v", err)
	}

	waitForPhase(t, dynClient, "default", "suspend-ready-test", "Succeeded", 120*time.Second)
	waitForReadyCondition(t, dynClient, "default", "suspend-ready-test", "True", 10*time.Second)
}
