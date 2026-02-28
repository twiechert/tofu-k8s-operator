//go:build integration
// +build integration

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKubectlPlugin(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	pluginBin := buildPluginBinary(t)

	// Apply program
	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	// Create a project for plugin testing
	pluginProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: plugin-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "plugin"
  backend:
    secretSuffix: plugin-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, pluginProject)
	defer deleteYAML(t, pluginProject)

	// Test suspend command
	cmd := exec.CommandContext(context.Background(), pluginBin, "suspend", "plugin-test", "-n", "default")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", os.Getenv("KUBECONFIG")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl-tofu suspend failed: %v\n%s", err, out)
	}

	waitForPhase(t, dynClient, "default", "plugin-test", "Suspended", 30*time.Second)

	// Test resume command
	cmd = exec.CommandContext(context.Background(), pluginBin, "resume", "plugin-test", "-n", "default")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", os.Getenv("KUBECONFIG")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl-tofu resume failed: %v\n%s", err, out)
	}

	// Verify no longer Suspended
	time.Sleep(5 * time.Second)
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "plugin-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get plugin-test: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _ := status["phase"].(string)
	if phase == "Suspended" {
		t.Fatal("project should no longer be suspended after resume")
	}
}

func TestKubectlPluginDelete(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	pluginBin := buildPluginBinary(t)

	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	// Create project with delete protection
	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: plugin-delete-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: plugin-delete-test
    namespace: default
  autoApprove: true
  deleteProtection: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Wait for it to succeed first
	waitForPhase(t, dynClient, "default", "plugin-delete-test", "Succeeded", 120*time.Second)

	// Delete the resource — it should block at WaitingDeleteApproval
	go func() {
		kubectlMayFail("delete", "tofuproject", "plugin-delete-test", "-n", "default", "--timeout=5s")
	}()

	waitForPhase(t, dynClient, "default", "plugin-delete-test", "WaitingDeleteApproval", 30*time.Second)

	// Use the plugin delete command to approve
	cmd := exec.CommandContext(context.Background(), pluginBin, "delete", "plugin-delete-test", "-n", "default")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", os.Getenv("KUBECONFIG")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl-tofu delete failed: %v\n%s", err, out)
	}

	// Wait for the resource to be fully deleted
	deadline := time.Now().Add(120 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for project to be deleted")
		}
		_, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "plugin-delete-test", metav1.GetOptions{})
		if err != nil {
			break // resource is gone
		}
		time.Sleep(2 * time.Second)
	}
}
