//go:build integration
// +build integration

package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTofuOperatorE2E(t *testing.T) {
	ctx := context.Background()

	// 1. Apply CRDs and deploy the operator
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-k", "../../deploy/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to deploy operator: %v\n%s", err, out)
	}

	// 2. Wait for operator pod to be ready
	cmd = exec.CommandContext(ctx, "kubectl", "-n", "tofu-system", "wait", "--for=condition=Ready", "pod", "-l", "app=tofu-operator", "--timeout=60s")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("operator pod not ready: %v\n%s", err, out)
	}

	// 3. Apply example TofuProgram and TofuProject
	cmd = exec.CommandContext(ctx, "kubectl", "apply", "-k", "../../examples/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to apply examples: %v\n%s", err, out)
	}

	// 4. Wait for Job to be created by the operator, then wait for completion
	deadline := time.Now().Add(120 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Job to be created by operator")
		}
		cmd = exec.CommandContext(ctx, "kubectl", "-n", "default", "get", "jobs", "-l", "app.kubernetes.io/managed-by=tofu-operator", "-o", "name")
		out, _ := cmd.CombinedOutput()
		if strings.TrimSpace(string(out)) != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	cmd = exec.CommandContext(ctx, "kubectl", "-n", "default", "wait", "--for=condition=complete", "job", "-l", "app.kubernetes.io/managed-by=tofu-operator", "--timeout=120s")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("job did not complete: %v\n%s", err, out)
	}

	// 5. Check TofuProject status (Succeeded)
	dynClient := newDynamicClient(t)
	time.Sleep(5 * time.Second)
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(ctx, "hello-run", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get TofuProject: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _ := status["phase"].(string)
	if phase != "Succeeded" {
		t.Fatalf("TofuProject did not succeed, phase: %s", phase)
	}
}
