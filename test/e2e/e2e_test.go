//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
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
	"k8s.io/apimachinery/pkg/types"
)

var tofuProjectGVR = schema.GroupVersionResource{
	Group:    "tofu.example.com",
	Version:  "v1alpha1",
	Resource: "tofuprojects",
}

// --- Shared helpers ---

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

// --- Tests ---

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
	clientset := newClientset(t)
	time.Sleep(5 * time.Second)
	proj, err := clientset.RESTClient().Get().AbsPath("/apis/tofu.example.com/v1alpha1/namespaces/default/tofuprojects/hello-run").DoRaw(ctx)
	if err != nil {
		t.Fatalf("failed to get TofuProject: %v", err)
	}
	if !containsStatusSucceeded(string(proj)) {
		t.Fatalf("TofuProject did not succeed: %s", proj)
	}
}

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

func TestPlanApproveFlow(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	// Apply the program
	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

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

func TestKubectlPlugin(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	// Build the plugin binary
	buildCmd := exec.CommandContext(context.Background(), "go", "build", "-o", "../../bin/kubectl-tofu", "../../cmd/kubectl-tofu/")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build kubectl-tofu: %v\n%s", err, out)
	}
	pluginBin, _ := filepath.Abs("../../bin/kubectl-tofu")

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

func containsStatusSucceeded(json string) bool {
	return strings.Contains(json, `"phase":"Succeeded"`) || strings.Contains(json, `"Phase":"Succeeded"`)
}
