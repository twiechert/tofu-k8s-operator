package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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
	case "history":
		requireArgs(2, "history <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdHistory(name, ns)
	case "pin":
		requireArgs(3, "pin <project> <revision> [-n namespace]")
		name := os.Args[2]
		revStr := os.Args[3]
		ns := parseNamespaceOnly(4)
		cmdPin(name, ns, revStr)
	case "unpin":
		requireArgs(2, "unpin <project> [-n namespace]")
		name, ns := parseNameAndNamespace(2)
		cmdUnpin(name, ns)
	case "show":
		requireArgs(3, "show <project> <revision> [-n namespace]")
		name := os.Args[2]
		revStr := os.Args[3]
		ns := parseNamespaceOnly(4)
		cmdShow(name, ns, revStr)
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
  history   Show revision history
  show      Show full details of a revision
  pin       Pin to a stored revision for rollback
  unpin     Resume normal flow (remove pin)
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

func cmdHistory(name, ns string) {
	clientset := newKubernetesClient()
	ctx := context.Background()

	cmList, err := clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("tofu.example.com/project=%s,tofu.example.com/resource-type=revision", name),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing revisions for %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}

	if len(cmList.Items) == 0 {
		fmt.Printf("No revisions found for %s/%s\n", ns, name)
		return
	}

	// Sort by revision number ascending
	sort.Slice(cmList.Items, func(i, j int) bool {
		ri, _ := strconv.Atoi(cmList.Items[i].Labels["tofu.example.com/revision"])
		rj, _ := strconv.Atoi(cmList.Items[j].Labels["tofu.example.com/revision"])
		return ri < rj
	})

	fmt.Printf("%-10s %-12s %-12s %-8s %s\n", "REVISION", "STATUS", "HASH", "AGE", "SUMMARY")
	for _, cm := range cmList.Items {
		rev := cm.Data["revision"]
		status := cm.Data["status"]
		if status == "" {
			status = "succeeded" // backward compat for revisions created before status field
		}
		hash := cm.Data["appliedHash"]
		if len(hash) > 8 {
			hash = hash[:8]
		}
		ts := cm.Data["timestamp"]
		age := formatAge(ts)
		summary := cm.Data["planSummary"]
		fmt.Printf("%-10s %-12s %-12s %-8s %s\n", rev, status, hash, age, summary)
	}
}

func cmdPin(name, ns, revStr string) {
	rev, err := strconv.Atoi(revStr)
	if err != nil || rev <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", revStr)
		os.Exit(1)
	}

	client := newDynamicClient()
	ctx := context.Background()

	patch := []byte(fmt.Sprintf(`{"spec":{"pinnedRevision":%d}}`, rev))
	_, err = client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error pinning %s/%s to revision %d: %v\n", ns, name, rev, err)
		os.Exit(1)
	}
	fmt.Printf("Pinned %s/%s to revision %d\n", ns, name, rev)
}

func cmdUnpin(name, ns string) {
	client := newDynamicClient()
	ctx := context.Background()

	patch := []byte(`{"spec":{"pinnedRevision":0}}`)
	_, err := client.Resource(tofuProjectGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error unpinning %s/%s: %v\n", ns, name, err)
		os.Exit(1)
	}
	fmt.Printf("Unpinned %s/%s (normal flow resumed)\n", ns, name)
}

func cmdShow(name, ns, revStr string) {
	rev, err := strconv.Atoi(revStr)
	if err != nil || rev <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", revStr)
		os.Exit(1)
	}

	clientset := newKubernetesClient()
	ctx := context.Background()

	cmName := fmt.Sprintf("%s-rev-%d", name, rev)
	cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting revision %d for %s/%s: %v\n", rev, ns, name, err)
		os.Exit(1)
	}

	status := cm.Data["status"]
	if status == "" {
		status = "succeeded"
	}

	fmt.Printf("Revision:    %s\n", cm.Data["revision"])
	fmt.Printf("Status:      %s\n", status)
	fmt.Printf("Hash:        %s\n", cm.Data["appliedHash"])
	fmt.Printf("Job:         %s\n", cm.Data["jobName"])
	fmt.Printf("Timestamp:   %s\n", cm.Data["timestamp"])
	if cm.Data["planSummary"] != "" {
		fmt.Printf("Summary:     %s\n", cm.Data["planSummary"])
	}
	if cm.Data["outputs"] != "" && cm.Data["outputs"] != "{}" {
		fmt.Printf("Outputs:     %s\n", cm.Data["outputs"])
	}
	if cm.Data["planOutput"] != "" {
		fmt.Printf("\n--- Plan/Apply Output ---\n%s\n", cm.Data["planOutput"])
	}
}

func parseNamespaceOnly(startIdx int) string {
	ns := "default"
	for i := startIdx; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-n" || os.Args[i] == "--namespace" {
			ns = os.Args[i+1]
			break
		}
	}
	return ns
}

func newKubernetesClient() *kubernetes.Clientset {
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

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating kubernetes client: %v\n", err)
		os.Exit(1)
	}
	return clientset
}

func formatAge(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days := int(math.Floor(d.Hours() / 24))
		return fmt.Sprintf("%dd", days)
	}
}
