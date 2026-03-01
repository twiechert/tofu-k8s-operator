//go:build integration
// +build integration

package e2e

import (
	"testing"
	"time"
)

func TestApplyImmediatelyDefault(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: apply-imm-default-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: apply-imm-default-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// While running, Ready should be False (applyImmediately defaults to true)
	waitForPhase(t, dynClient, "default", "apply-imm-default-test", "Running", 30*time.Second)
	status := getReadyConditionStatus(t, dynClient, "default", "apply-imm-default-test")
	if status != "False" {
		t.Fatalf("expected Ready=False while running (applyImmediately default true), got %q", status)
	}

	// After success, Ready should be True
	waitForPhase(t, dynClient, "default", "apply-imm-default-test", "Succeeded", 120*time.Second)
	waitForReadyCondition(t, dynClient, "default", "apply-imm-default-test", "True", 10*time.Second)
}

func TestApplyImmediatelyFalse(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	// applyImmediately=false → Ready=True immediately (async/fire-and-forget)
	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: apply-imm-false-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: apply-imm-false-test
    namespace: default
  autoApprove: true
  applyImmediately: false
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Even while running, Ready should be True
	waitForPhase(t, dynClient, "default", "apply-imm-false-test", "Running", 30*time.Second)
	waitForReadyCondition(t, dynClient, "default", "apply-imm-false-test", "True", 10*time.Second)

	// After success, still True
	waitForPhase(t, dynClient, "default", "apply-imm-false-test", "Succeeded", 120*time.Second)
	status := getReadyConditionStatus(t, dynClient, "default", "apply-imm-false-test")
	if status != "True" {
		t.Fatalf("expected Ready=True after success, got %q", status)
	}
}
