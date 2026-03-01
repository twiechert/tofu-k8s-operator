package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

const snapshotPrefix = "snapshot:"

// createRevision creates an immutable revision ConfigMap for audit trail.
// The ConfigMap is NOT owned by the project so it survives deletion.
func (r *TofuProjectReconciler) createRevision(ctx context.Context, project *tofuv1alpha1.TofuProject, appliedHash, jobName, applyStatus, planOutput string, cmData map[string]string) error {
	revNum := project.Status.Revision
	cmName := fmt.Sprintf("%s-rev-%d", project.Name, revNum)

	outputsJSON := "{}"
	if project.Status.Outputs != nil {
		b, err := json.Marshal(project.Status.Outputs)
		if err == nil {
			outputsJSON = string(b)
		}
	}

	data := map[string]string{
		"revision":    strconv.Itoa(int(revNum)),
		"appliedHash": appliedHash,
		"jobName":     jobName,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"status":      applyStatus,
		"planSummary": project.Status.PlanSummary,
		"planOutput":  planOutput,
		"outputs":     outputsJSON,
	}

	// Store snapshot of TF files (only for successful applies — failed ones may have stale data)
	if applyStatus == "succeeded" {
		for k, v := range cmData {
			data[snapshotPrefix+k] = v
		}
	}

	revCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":   "tofu-k8s-operator",
				"tofu.example.com/project":       project.Name,
				"tofu.example.com/revision":      strconv.Itoa(int(revNum)),
				"tofu.example.com/resource-type": "revision",
			},
		},
		Data: data,
	}

	return r.Create(ctx, revCM)
}

// revisionHistoryLimit returns the effective revision history limit (default 10).
func revisionHistoryLimit(project *tofuv1alpha1.TofuProject) int32 {
	if project.Spec.RevisionHistoryLimit == nil {
		return 10
	}
	return *project.Spec.RevisionHistoryLimit
}

// cleanupOldRevisions deletes revision ConfigMaps beyond the configured limit,
// protecting the currently pinned revision.
func (r *TofuProjectReconciler) cleanupOldRevisions(ctx context.Context, project *tofuv1alpha1.TofuProject) error {
	limit := revisionHistoryLimit(project)
	if limit == 0 {
		return nil // 0 = keep all
	}

	var cmList corev1.ConfigMapList
	if err := r.List(ctx, &cmList, client.InNamespace(project.Namespace), client.MatchingLabelsSelector{
		Selector: labels.SelectorFromSet(map[string]string{
			"tofu.example.com/project":       project.Name,
			"tofu.example.com/resource-type": "revision",
		}),
	}); err != nil {
		return err
	}

	if int32(len(cmList.Items)) <= limit {
		return nil
	}

	// Sort by revision number ascending
	sort.Slice(cmList.Items, func(i, j int) bool {
		ri, _ := strconv.Atoi(cmList.Items[i].Labels["tofu.example.com/revision"])
		rj, _ := strconv.Atoi(cmList.Items[j].Labels["tofu.example.com/revision"])
		return ri < rj
	})

	toDelete := int32(len(cmList.Items)) - limit
	pinnedRev := project.Spec.PinnedRevision

	for i := 0; int32(i) < toDelete; i++ {
		cm := &cmList.Items[i]
		rev, _ := strconv.Atoi(cm.Labels["tofu.example.com/revision"])
		if int32(rev) == pinnedRev {
			continue // protect pinned revision
		}
		if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// computePinnedHash returns a deterministic hash for a pinned revision.
func computePinnedHash(revNum int32, storedHash string) string {
	input := fmt.Sprintf("pinned:%d:%s", revNum, storedHash)
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// createRevisionFromCM reads the project's -tf ConfigMap data, increments the revision number,
// and creates a revision ConfigMap + cleans up old ones. Errors are non-fatal.
func (r *TofuProjectReconciler) createRevisionFromCM(ctx context.Context, project *tofuv1alpha1.TofuProject, appliedHash, jobName, applyStatus, planOutput string) {
	log := ctrl.LoggerFrom(ctx)

	cmName := fmt.Sprintf("%s-tf", project.Name)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: project.Namespace}, &cm); err != nil {
		log.Error(err, "failed to read ConfigMap for revision snapshot (non-fatal)")
		return
	}

	project.Status.Revision++
	if err := r.createRevision(ctx, project, appliedHash, jobName, applyStatus, planOutput, cm.Data); err != nil {
		log.Error(err, "failed to create revision ConfigMap (non-fatal)")
		return
	}

	if err := r.cleanupOldRevisions(ctx, project); err != nil {
		log.Error(err, "failed to cleanup old revisions (non-fatal)")
	}
}

// reconcilePinned handles the pinned-revision flow.
// It reads the stored revision ConfigMap, overwrites the project's -tf ConfigMap
// with the snapshot data, and runs a simplified auto-approve apply.
func (r *TofuProjectReconciler) reconcilePinned(ctx context.Context, project *tofuv1alpha1.TofuProject) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	revNum := project.Spec.PinnedRevision

	// Read the revision ConfigMap
	revCMName := fmt.Sprintf("%s-rev-%d", project.Name, revNum)
	var revCM corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: revCMName, Namespace: project.Namespace}, &revCM); err != nil {
		if apierrors.IsNotFound(err) {
			project.Status.Phase = "Error"
			project.Status.Message = fmt.Sprintf("Pinned revision %d not found (ConfigMap %s)", revNum, revCMName)
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	storedHash := revCM.Data["appliedHash"]
	pinnedHash := computePinnedHash(revNum, storedHash)

	// Already applied this pinned revision
	if project.Status.LastAppliedHash == pinnedHash {
		return ctrl.Result{}, nil
	}

	// Extract snapshot data from the revision CM
	snapshotData := map[string]string{}
	for k, v := range revCM.Data {
		if strings.HasPrefix(k, snapshotPrefix) {
			snapshotData[strings.TrimPrefix(k, snapshotPrefix)] = v
		}
	}

	if len(snapshotData) == 0 {
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("Revision %d has no snapshot data", revNum)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	// Overwrite the project's -tf ConfigMap with snapshot data
	cmName := fmt.Sprintf("%s-tf", project.Name)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: project.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = mergeLabels(cm.Labels, map[string]string{
			"app.kubernetes.io/managed-by": "tofu-k8s-operator",
			"tofu.example.com/project":     project.Name,
		})
		cm.Data = snapshotData
		return controllerutil.SetControllerReference(project, cm, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Ensure ServiceAccount
	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		saName = project.Spec.ServiceAccount.Name
	}

	// Ensure cache PVC if configured
	cacheMode := r.cacheMode(project)
	var cachePVCName string
	if cacheMode != "" {
		pvcName, err := r.ensureCachePVC(ctx, project, cacheMode)
		if err != nil {
			return ctrl.Result{}, err
		}
		cachePVCName = pvcName
	}

	// Check for active jobs (same logic as main reconcile)
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(project.Namespace), client.MatchingLabelsSelector{
		Selector: labels.SelectorFromSet(map[string]string{
			"tofu.example.com/project": project.Name,
		}),
	}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range jobList.Items {
		j := &jobList.Items[i]
		if j.Labels["tofu.example.com/job-type"] == "drift" {
			continue
		}
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			log.Info("waiting for active Job to complete before pinned apply", "job", j.Name)
			project.Status.Phase = "Running"
			project.Status.LastJobName = j.Name
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Build and run the pinned apply job (always inline, always auto-approve)
	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}

	jobName := fmt.Sprintf("%s-apply-%s", project.Name, pinnedHash[:8])
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		// Create a dummy inline program for the job builder
		dummyProgram := &tofuv1alpha1.TofuProgram{}

		// Build job with auto-approve, inline mode (no git source)
		projectCopy := *project
		projectCopy.Spec.AutoApprove = true
		newJob := buildJob(projectCopy, jobName, cmName, image, dummyProgram, saName)
		if cacheMode != "" {
			addCacheToJob(newJob, cachePVCName)
		}
		addEnvToJob(newJob, project)
		if err := addResourcesToJob(newJob, project); err != nil {
			return ctrl.Result{}, err
		}
		if err := controllerutil.SetControllerReference(project, newJob, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, newJob); err != nil {
			return ctrl.Result{}, err
		}
		project.Status.Phase = "Running"
		project.Status.LastJobName = jobName
		project.Status.Message = fmt.Sprintf("Applying pinned revision %d", revNum)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Job exists — check status
	if job.Status.Succeeded > 0 {
		outputs, err := r.captureOutputs(ctx, job)
		if err != nil {
			log.Error(err, "failed to capture outputs from pinned apply job (non-fatal)")
		}
		project.Status.Phase = "Succeeded"
		project.Status.LastAppliedHash = pinnedHash
		project.Status.LastJobName = jobName
		project.Status.SyncStatus = "sync"
		project.Status.Message = fmt.Sprintf("Pinned revision %d applied", revNum)
		project.Status.RetryCount = 0
		project.Status.DriftDetected = false
		if outputs != nil {
			project.Status.Outputs = outputs
		}
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "apply:success")
		return ctrl.Result{}, nil
	}
	if job.Status.Failed > 0 {
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = fmt.Sprintf("Pinned revision %d apply failed", revNum)
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "apply:error")
		return ctrl.Result{}, nil
	}

	// Job still running
	project.Status.Phase = "Running"
	project.Status.LastJobName = jobName
	r.updateStatusWithCondition(ctx, project)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
