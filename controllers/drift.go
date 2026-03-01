package controllers

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

// reconcileDriftDetection runs periodic plan-only jobs to detect drift.
func (r *TofuProjectReconciler) reconcileDriftDetection(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	interval := parseDriftInterval(project.Spec.DriftDetection.Interval)

	// Check if it's too early for a drift check
	if project.Status.LastDriftCheckTime != nil {
		elapsed := time.Since(project.Status.LastDriftCheckTime.Time)
		if elapsed < interval {
			remaining := interval - elapsed
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	// Look for existing drift job
	ts := time.Now().Unix()
	driftJobName := fmt.Sprintf("%s-drift-%d", project.Name, ts)

	// Find existing drift jobs
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(project.Namespace), client.MatchingLabelsSelector{
		Selector: labels.SelectorFromSet(map[string]string{
			"tofu.example.com/project":  project.Name,
			"tofu.example.com/job-type": "drift",
		}),
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Check existing drift jobs
	for i := range jobList.Items {
		j := &jobList.Items[i]
		if j.Status.Succeeded > 0 {
			// Read logs to check for drift
			output, err := r.readJobLogs(ctx, j)
			if err != nil {
				log.Error(err, "failed to read drift job logs")
			} else {
				summary := extractPlanSummary(output)
				blastRadius := parsePlanCounts(summary)
				now := metav1.Now()
				project.Status.LastDriftCheckTime = &now
				project.Status.BlastRadius = blastRadius
				if summary == "No changes." || summary == "" {
					project.Status.DriftDetected = false
					project.Status.Phase = "Succeeded"
					project.Status.Message = ""
					log.Info("drift check: no drift detected")
				} else {
					project.Status.DriftDetected = true
					project.Status.SyncStatus = "not in sync"
					log.Info("drift check: drift detected", "summary", summary)
					sendNotification(ctx, project, "drift:detected")
				}
				r.updateStatusWithCondition(ctx, project)
			}
			// Clean up completed drift job
			_ = r.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground))
			if project.Status.DriftDetected && project.Spec.KeepInSync {
				// Force re-apply by clearing last applied hash
				project.Status.LastAppliedHash = ""
				r.updateStatusWithCondition(ctx, project)
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{RequeueAfter: interval}, nil
		}
		if j.Status.Failed > 0 {
			log.Error(nil, "drift check job failed", "job", j.Name)
			now := metav1.Now()
			project.Status.LastDriftCheckTime = &now
			r.updateStatusWithCondition(ctx, project)
			_ = r.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground))
			return ctrl.Result{RequeueAfter: interval}, nil
		}
		// Job still running — wait
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// No drift job exists — create one
	log.Info("creating drift detection plan job", "name", driftJobName)
	newJob := buildPlanJob(*project, driftJobName, cmName, image, program, saName)
	newJob.Labels["tofu.example.com/job-type"] = "drift"
	if cacheEnabled {
		addCacheToJob(newJob, cachePVCName)
	}
	addEnvToJob(newJob, project)
	addExtraVolumesToJob(newJob, project)
	if err := addResourcesToJob(newJob, project); err != nil {
		return ctrl.Result{}, err
	}
	driftGitMode := isGitSource(program)
	var driftSource *tofuv1alpha1.GitSource
	if driftGitMode {
		driftSource = program.Spec.Source
	}
	if err := addValidationToJob(newJob, project, image, driftGitMode, driftSource); err != nil {
		return ctrl.Result{}, err
	}
	if err := controllerutil.SetControllerReference(project, newJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, newJob); err != nil {
		return ctrl.Result{}, err
	}

	project.Status.Phase = "DriftChecking"
	project.Status.Message = "Running drift detection"
	r.updateStatusWithCondition(ctx, project)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// parseDriftInterval parses a drift detection interval string. Default 15m.
func parseDriftInterval(s string) time.Duration {
	if s == "" {
		return 15 * time.Minute
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 15 * time.Minute
	}
	return d
}
