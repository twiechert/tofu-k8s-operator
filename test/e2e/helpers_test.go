//go:build integration
// +build integration

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var tofuProjectGVR = schema.GroupVersionResource{
	Group:    "tofu.example.com",
	Version:  "v1alpha1",
	Resource: "tofuprojects",
}

const tofuProgramYAML = `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: hello
  namespace: default
spec:
  providers:
    - name: random
      source: "hashicorp/random"
      version: "~> 3.6"
  programHCL: |
    resource "random_pet" "name" {
      length = 2
    }
    output "pet_name" {
      value = random_pet.name.id
    }
`

func getKubeConfig(t *testing.T) *rest.Config {
	t.Helper()
	var kubeconfig string
	var foundKubeconfig bool
	kubeconfigEnv := os.Getenv("KUBECONFIG")
	if kubeconfigEnv != "" {
		paths := filepath.SplitList(kubeconfigEnv)
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				kubeconfig = p
				foundKubeconfig = true
				break
			}
		}
	}
	if !foundKubeconfig {
		home, _ := os.UserHomeDir()
		defaultKubeconfig := filepath.Join(home, ".kube", "config")
		if fi, err := os.Stat(defaultKubeconfig); err == nil && !fi.IsDir() {
			kubeconfig = defaultKubeconfig
			foundKubeconfig = true
		}
	}
	if foundKubeconfig {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			config, err = rest.InClusterConfig()
			if err != nil {
				t.Fatalf("failed to get kubeconfig: %v", err)
			}
			return config
		}
		return config
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		t.Fatalf("failed to get kubeconfig: %v", err)
	}
	return config
}

func newClientset(t *testing.T) kubernetes.Interface {
	t.Helper()
	config := getKubeConfig(t)
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("failed to create clientset: %v", err)
	}
	return clientset
}

func newDynamicClient(t *testing.T) dynamic.Interface {
	t.Helper()
	config := getKubeConfig(t)
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatalf("failed to create dynamic client: %v", err)
	}
	return client
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func kubectlMayFail(args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "kubectl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func deployOperator(t *testing.T) {
	t.Helper()
	kubectl(t, "apply", "-k", "../../deploy/")
	kubectl(t, "-n", "tofu-system", "wait", "--for=condition=Ready", "pod", "-l", "app=tofu-operator", "--timeout=60s")
}

func applyYAML(t *testing.T, yaml string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl apply failed: %v\n%s", err, out)
	}
}

func deleteYAML(t *testing.T, yaml string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "kubectl", "delete", "--ignore-not-found", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, _ = cmd.CombinedOutput()
}

func waitForPhase(t *testing.T, dynClient dynamic.Interface, ns, name, phase string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			// Get current phase for debugging
			obj, err := dynClient.Resource(tofuProjectGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
			currentPhase := "unknown"
			if err == nil {
				status, _, _ := unstructured.NestedMap(obj.Object, "status")
				if p, ok := status["phase"].(string); ok {
					currentPhase = p
				}
			}
			t.Fatalf("timed out waiting for phase %q on %s/%s (current: %s)", phase, ns, name, currentPhase)
		}
		obj, err := dynClient.Resource(tofuProjectGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			if p, ok := status["phase"].(string); ok && p == phase {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForJobWithLabel(t *testing.T, ns, labelSelector string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job with label %s in %s", labelSelector, ns)
		}
		cmd := exec.CommandContext(context.Background(), "kubectl", "-n", ns, "get", "jobs", "-l", labelSelector, "-o", "name")
		out, _ := cmd.CombinedOutput()
		if strings.TrimSpace(string(out)) != "" {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func noJobsWithLabel(t *testing.T, ns, labelSelector string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "kubectl", "-n", ns, "get", "jobs", "-l", labelSelector, "-o", "name")
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected no jobs with label %s, but found: %s", labelSelector, out)
	}
}

// getReadyConditionStatus returns the status of the Ready condition ("True", "False", "Unknown", or "" if not found).
func getReadyConditionStatus(t *testing.T, dynClient dynamic.Interface, ns, name string) string {
	t.Helper()
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project %s/%s: %v", ns, name, err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	conditions, ok := status["conditions"]
	if !ok {
		return ""
	}
	condList, ok := conditions.([]interface{})
	if !ok {
		return ""
	}
	for _, c := range condList {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			s, _ := cond["status"].(string)
			return s
		}
	}
	return ""
}

// waitForReadyCondition waits until the Ready condition matches the expected status.
func waitForReadyCondition(t *testing.T, dynClient dynamic.Interface, ns, name, expectedStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			current := getReadyConditionStatus(t, dynClient, ns, name)
			t.Fatalf("timed out waiting for Ready=%s on %s/%s (current: %q)", expectedStatus, ns, name, current)
		}
		s := getReadyConditionStatus(t, dynClient, ns, name)
		if s == expectedStatus {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// buildPluginBinary compiles the kubectl-tofu plugin and returns the absolute path.
func buildPluginBinary(t *testing.T) string {
	t.Helper()
	buildCmd := exec.CommandContext(context.Background(), "go", "build", "-o", "../../bin/kubectl-tofu", "../../cmd/kubectl-tofu/")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build kubectl-tofu: %v\n%s", err, out)
	}
	pluginBin, _ := filepath.Abs("../../bin/kubectl-tofu")
	return pluginBin
}
