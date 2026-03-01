package controllers

import (
	"context"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func TestCreateRevision(t *testing.T) {
	r := newTestReconciler()
	ctx := context.Background()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: tofuv1alpha1.TofuProjectStatus{
			Revision:    1,
			PlanSummary: "Plan: 2 to add, 0 to change, 0 to destroy.",
			Outputs:     map[string]string{"pet_name": "tofu-cat"},
		},
	}

	cmData := map[string]string{
		"backend.tf":            "terraform { backend ... }",
		"terraform.tfvars.json": `{"key":"value"}`,
	}

	planOutput := "tofu plan output...\nPlan: 2 to add, 0 to change, 0 to destroy."
	err := r.createRevision(ctx, project, "abc123", "demo-apply-abc123", "succeeded", planOutput, cmData)
	if err != nil {
		t.Fatalf("createRevision failed: %v", err)
	}

	// Verify the revision CM was created
	var revCM corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: "demo-rev-1", Namespace: "default"}, &revCM)
	if err != nil {
		t.Fatalf("expected revision CM to exist: %v", err)
	}

	// Check labels
	if revCM.Labels["app.kubernetes.io/managed-by"] != "tofu-k8s-operator" {
		t.Error("missing managed-by label")
	}
	if revCM.Labels["tofu.example.com/project"] != "demo" {
		t.Error("missing project label")
	}
	if revCM.Labels["tofu.example.com/revision"] != "1" {
		t.Error("missing revision label")
	}
	if revCM.Labels["tofu.example.com/resource-type"] != "revision" {
		t.Error("missing resource-type label")
	}

	// Check data keys
	if revCM.Data["revision"] != "1" {
		t.Errorf("expected revision=1, got %s", revCM.Data["revision"])
	}
	if revCM.Data["appliedHash"] != "abc123" {
		t.Errorf("expected appliedHash=abc123, got %s", revCM.Data["appliedHash"])
	}
	if revCM.Data["jobName"] != "demo-apply-abc123" {
		t.Errorf("expected jobName=demo-apply-abc123, got %s", revCM.Data["jobName"])
	}
	if revCM.Data["status"] != "succeeded" {
		t.Errorf("expected status=succeeded, got %s", revCM.Data["status"])
	}
	if revCM.Data["planSummary"] != "Plan: 2 to add, 0 to change, 0 to destroy." {
		t.Errorf("unexpected planSummary: %s", revCM.Data["planSummary"])
	}
	if revCM.Data["planOutput"] != planOutput {
		t.Errorf("expected planOutput to be stored, got %q", revCM.Data["planOutput"])
	}

	// Verify no owner reference (survives project deletion)
	if len(revCM.OwnerReferences) != 0 {
		t.Error("revision CM should have no owner references")
	}
}

func TestCreateRevisionSnapshotData(t *testing.T) {
	r := newTestReconciler()
	ctx := context.Background()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: tofuv1alpha1.TofuProjectStatus{
			Revision: 1,
		},
	}

	cmData := map[string]string{
		"backend.tf":              "backend content",
		"terraform.tfvars.json":   `{"foo":"bar"}`,
		"main.tf":                 "resource \"null_resource\" {}",
		"providers.tf":            "provider content",
		"additional-providers.tf": "additional provider content",
	}

	err := r.createRevision(ctx, project, "hash1", "job1", "succeeded", "", cmData)
	if err != nil {
		t.Fatalf("createRevision failed: %v", err)
	}

	var revCM corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: "demo-rev-1", Namespace: "default"}, &revCM)
	if err != nil {
		t.Fatalf("expected revision CM to exist: %v", err)
	}

	// Check snapshot keys match input
	for k, v := range cmData {
		snapshotKey := "snapshot:" + k
		if revCM.Data[snapshotKey] != v {
			t.Errorf("snapshot:%s = %q, want %q", k, revCM.Data[snapshotKey], v)
		}
	}
}

func TestCleanupOldRevisions_UnderLimit(t *testing.T) {
	// Create 3 revision CMs, limit=10 → no deletions
	var objs []runtime.Object
	for i := 1; i <= 3; i++ {
		objs = append(objs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-rev-" + strconv.Itoa(i),
				Namespace: "default",
				Labels: map[string]string{
					"tofu.example.com/project":       "demo",
					"tofu.example.com/revision":      strconv.Itoa(i),
					"tofu.example.com/resource-type": "revision",
				},
			},
			Data: map[string]string{"revision": strconv.Itoa(i)},
		})
	}
	r := newTestReconciler(objs...)
	ctx := context.Background()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}

	err := r.cleanupOldRevisions(ctx, project)
	if err != nil {
		t.Fatalf("cleanupOldRevisions failed: %v", err)
	}

	// All 3 should still exist
	var cmList corev1.ConfigMapList
	_ = r.List(ctx, &cmList, client.InNamespace("default"), client.MatchingLabels{
		"tofu.example.com/project":       "demo",
		"tofu.example.com/resource-type": "revision",
	})
	if len(cmList.Items) != 3 {
		t.Errorf("expected 3 revisions, got %d", len(cmList.Items))
	}
}

func TestCleanupOldRevisions_OverLimit(t *testing.T) {
	// Create 5 revision CMs, limit=3 → oldest 2 deleted
	var objs []runtime.Object
	for i := 1; i <= 5; i++ {
		objs = append(objs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-rev-" + strconv.Itoa(i),
				Namespace: "default",
				Labels: map[string]string{
					"tofu.example.com/project":       "demo",
					"tofu.example.com/revision":      strconv.Itoa(i),
					"tofu.example.com/resource-type": "revision",
				},
			},
			Data: map[string]string{"revision": strconv.Itoa(i)},
		})
	}
	r := newTestReconciler(objs...)
	ctx := context.Background()

	limit := int32(3)
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			RevisionHistoryLimit: &limit,
		},
	}

	err := r.cleanupOldRevisions(ctx, project)
	if err != nil {
		t.Fatalf("cleanupOldRevisions failed: %v", err)
	}

	var cmList corev1.ConfigMapList
	_ = r.List(ctx, &cmList, client.InNamespace("default"), client.MatchingLabels{
		"tofu.example.com/project":       "demo",
		"tofu.example.com/resource-type": "revision",
	})
	if len(cmList.Items) != 3 {
		t.Errorf("expected 3 revisions remaining, got %d", len(cmList.Items))
	}

	// Revisions 1 and 2 should be deleted, 3-5 should remain
	remaining := map[string]bool{}
	for _, cm := range cmList.Items {
		remaining[cm.Name] = true
	}
	if remaining["demo-rev-1"] || remaining["demo-rev-2"] {
		t.Error("oldest revisions should have been deleted")
	}
	if !remaining["demo-rev-3"] || !remaining["demo-rev-4"] || !remaining["demo-rev-5"] {
		t.Error("newest revisions should remain")
	}
}

func TestCleanupOldRevisions_ProtectsPinnedRevision(t *testing.T) {
	// Create 5 CMs, limit=3, pin=1 → rev 1 protected, rev 2 deleted
	var objs []runtime.Object
	for i := 1; i <= 5; i++ {
		objs = append(objs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-rev-" + strconv.Itoa(i),
				Namespace: "default",
				Labels: map[string]string{
					"tofu.example.com/project":       "demo",
					"tofu.example.com/revision":      strconv.Itoa(i),
					"tofu.example.com/resource-type": "revision",
				},
			},
			Data: map[string]string{"revision": strconv.Itoa(i)},
		})
	}
	r := newTestReconciler(objs...)
	ctx := context.Background()

	limit := int32(3)
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			RevisionHistoryLimit: &limit,
			PinnedRevision:       1,
		},
	}

	err := r.cleanupOldRevisions(ctx, project)
	if err != nil {
		t.Fatalf("cleanupOldRevisions failed: %v", err)
	}

	var cmList corev1.ConfigMapList
	_ = r.List(ctx, &cmList, client.InNamespace("default"), client.MatchingLabels{
		"tofu.example.com/project":       "demo",
		"tofu.example.com/resource-type": "revision",
	})

	remaining := map[string]bool{}
	for _, cm := range cmList.Items {
		remaining[cm.Name] = true
	}

	// Pinned revision 1 should be protected
	if !remaining["demo-rev-1"] {
		t.Error("pinned revision 1 should be protected from deletion")
	}
	// Revision 2 should be deleted (oldest non-pinned)
	if remaining["demo-rev-2"] {
		t.Error("revision 2 should have been deleted")
	}
}

func TestCleanupOldRevisions_ZeroKeepsAll(t *testing.T) {
	var objs []runtime.Object
	for i := 1; i <= 20; i++ {
		objs = append(objs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-rev-" + strconv.Itoa(i),
				Namespace: "default",
				Labels: map[string]string{
					"tofu.example.com/project":       "demo",
					"tofu.example.com/revision":      strconv.Itoa(i),
					"tofu.example.com/resource-type": "revision",
				},
			},
			Data: map[string]string{"revision": strconv.Itoa(i)},
		})
	}
	r := newTestReconciler(objs...)
	ctx := context.Background()

	limit := int32(0)
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			RevisionHistoryLimit: &limit,
		},
	}

	err := r.cleanupOldRevisions(ctx, project)
	if err != nil {
		t.Fatalf("cleanupOldRevisions failed: %v", err)
	}

	var cmList corev1.ConfigMapList
	_ = r.List(ctx, &cmList, client.InNamespace("default"), client.MatchingLabels{
		"tofu.example.com/project":       "demo",
		"tofu.example.com/resource-type": "revision",
	})
	if len(cmList.Items) != 20 {
		t.Errorf("expected all 20 revisions to remain, got %d", len(cmList.Items))
	}
}

func TestCleanupOldRevisions_DefaultLimit(t *testing.T) {
	// nil RevisionHistoryLimit → default 10
	var objs []runtime.Object
	for i := 1; i <= 12; i++ {
		objs = append(objs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-rev-" + strconv.Itoa(i),
				Namespace: "default",
				Labels: map[string]string{
					"tofu.example.com/project":       "demo",
					"tofu.example.com/revision":      strconv.Itoa(i),
					"tofu.example.com/resource-type": "revision",
				},
			},
			Data: map[string]string{"revision": strconv.Itoa(i)},
		})
	}
	r := newTestReconciler(objs...)
	ctx := context.Background()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		// RevisionHistoryLimit is nil → default 10
	}

	err := r.cleanupOldRevisions(ctx, project)
	if err != nil {
		t.Fatalf("cleanupOldRevisions failed: %v", err)
	}

	var cmList corev1.ConfigMapList
	_ = r.List(ctx, &cmList, client.InNamespace("default"), client.MatchingLabels{
		"tofu.example.com/project":       "demo",
		"tofu.example.com/resource-type": "revision",
	})
	if len(cmList.Items) != 10 {
		t.Errorf("expected 10 revisions (default limit), got %d", len(cmList.Items))
	}
}

func TestCreateRevision_FailedStatus(t *testing.T) {
	r := newTestReconciler()
	ctx := context.Background()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: tofuv1alpha1.TofuProjectStatus{
			Revision: 1,
		},
	}

	cmData := map[string]string{
		"backend.tf":            "backend content",
		"terraform.tfvars.json": `{"foo":"bar"}`,
	}

	failLogs := "Error: some tofu error\nexit code 1"
	err := r.createRevision(ctx, project, "hash1", "demo-apply-hash1", "failed", failLogs, cmData)
	if err != nil {
		t.Fatalf("createRevision failed: %v", err)
	}

	var revCM corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: "demo-rev-1", Namespace: "default"}, &revCM)
	if err != nil {
		t.Fatalf("expected revision CM to exist: %v", err)
	}

	if revCM.Data["status"] != "failed" {
		t.Errorf("expected status=failed, got %s", revCM.Data["status"])
	}
	if revCM.Data["planOutput"] != failLogs {
		t.Errorf("expected planOutput to contain failure logs")
	}

	// Failed revisions should NOT include snapshot data
	for k := range revCM.Data {
		if len(k) > 9 && k[:9] == "snapshot:" {
			t.Errorf("failed revision should not include snapshot data, found key %s", k)
		}
	}
}

func TestComputePinnedHash(t *testing.T) {
	// Deterministic
	h1 := computePinnedHash(1, "abc123")
	h2 := computePinnedHash(1, "abc123")
	if h1 != h2 {
		t.Error("computePinnedHash should be deterministic")
	}

	// Differs by revision
	h3 := computePinnedHash(2, "abc123")
	if h1 == h3 {
		t.Error("different revisions should produce different hashes")
	}

	// Differs by stored hash
	h4 := computePinnedHash(1, "def456")
	if h1 == h4 {
		t.Error("different stored hashes should produce different hashes")
	}

	// Non-empty
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex string, got length %d", len(h1))
	}
}
