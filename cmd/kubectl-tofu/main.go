package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var tofuProjectGVR = schema.GroupVersionResource{
	Group:    "tofu.example.com",
	Version:  "v1alpha1",
	Resource: "tofuprojects",
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "plan":
		requireArgs(2, "plan <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdPlan(name, ns)
	case "approve":
		requireArgs(2, "approve <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdApprove(name, ns)
	case "suspend":
		requireArgs(2, "suspend <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdSuspend(name, ns)
	case "resume":
		requireArgs(2, "resume <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdResume(name, ns)
	case "delete":
		requireArgs(2, "delete <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdDelete(name, ns)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: kubectl tofu <command> <project> [-n namespace]

Commands:
  plan      Show plan output and status
  approve   Approve a pending plan for apply
  delete    Approve deletion of a delete-protected project
  suspend   Pause reconciliation
  resume    Resume reconciliation
`)
}

func requireArgs(minArgs int, hint string) {
	if len(os.Args) < minArgs+1 {
		fmt.Fprintf(os.Stderr, "Usage: kubectl tofu %s\n", hint)
		os.Exit(1)
	}
}

func parseNameAndNamespace(nameIdx int) (string, string) {
	name := os.Args[nameIdx]
	ns := "default"
	for i := nameIdx + 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-n" || os.Args[i] == "--namespace" {
			ns = os.Args[i+1]
			break
		}
	}
	return name, ns
}

func newDynamicClient() dynamic.Interface {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	} else {
		home, _ := os.UserHomeDir()
		rules.ExplicitPath = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading kubeconfig: %v\n", err)
		os.Exit(1)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating client: %v\n", err)
		os.Exit(1)
	}
	return client
}

func cmdPlan(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	obj, err := client.Resource(tofuProjectGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting TofuProject %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _ := status["phase"].(string)
	planSummary, _ := status["planSummary"].(string)
	planOutput, _ := status["planOutput"].(string)
	pendingPlanHash, _ := status["pendingPlanHash"].(string)

	fmt.Printf("Project:  %s/%s\n", ns, name)
	fmt.Printf("Phase:    %s\n", phase)
	if pendingPlanHash != "" {
		fmt.Printf("Plan Hash: %s\n", pendingPlanHash)
	}
	if planSummary != "" {
		fmt.Printf("Summary:  %s\n", planSummary)
	}
	if planOutput != "" {
		fmt.Printf("\n--- Plan Output ---\n%s\n", planOutput)
	}
}

func cmdApprove(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	// Get the project to read pendingPlanHash
	obj, err := client.Resource(tofuProjectGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting TofuProject %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	pendingHash, _ := status["pendingPlanHash"].(string)
	if pendingHash == "" {
		fmt.Fprintf(os.Stderr, "No pending plan to approve for %s/%s\n", ns, name)
		os.Exit(1)
	}

	// Patch the annotation
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"tofu.example.com/approved-hash": pendingHash,
			},
		},
	}
	patchBytes, _ := json.Marshal(patch)
	_, err = client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error approving plan: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Approved plan %s for %s/%s\n", pendingHash[:8], ns, name)
}

func cmdDelete(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"tofu.example.com/approved-delete": "true",
			},
		},
	}
	patchBytes, _ := json.Marshal(patch)
	_, err := client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error approving delete for %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}
	fmt.Printf("Approved delete for %s/%s\n", ns, name)
}

func cmdSuspend(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	patch := []byte(`{"spec":{"suspend":true}}`)
	_, err := client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error suspending %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}
	fmt.Printf("Suspended %s/%s\n", ns, name)
}

func cmdResume(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	patch := []byte(`{"spec":{"suspend":false}}`)
	_, err := client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resuming %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}
	fmt.Printf("Resumed %s/%s\n", ns, name)
}
