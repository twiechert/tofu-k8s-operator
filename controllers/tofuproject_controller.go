package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

const (
	finalizerName            = "tofu.example.com/destroy"
	approvedHashAnnotation   = "tofu.example.com/approved-hash"
	approvedDeleteAnnotation = "tofu.example.com/approved-delete"
	maxPlanOutputBytes       = 32 * 1024
	outputMarker             = "---TOFU-OUTPUTS---"
)

type TofuProjectReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Clientset kubernetes.Interface
}

func (r *TofuProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tofuv1alpha1.TofuProject{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&tofuv1alpha1.TofuProject{}, handler.EnqueueRequestsFromMapFunc(r.findDependentProjects)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.findProjectsReferencingConfigMap)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findProjectsReferencingSecret)).
		Complete(r)
}

// findDependentProjects returns reconcile requests for all TofuProjects that
// depend on the changed project (via spec.dependencies[].projectRef).
func (r *TofuProjectReconciler) findDependentProjects(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*tofuv1alpha1.TofuProject)
	if !ok {
		return nil
	}

	var allProjects tofuv1alpha1.TofuProjectList
	if err := r.List(ctx, &allProjects); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range allProjects.Items {
		p := &allProjects.Items[i]
		for _, dep := range p.Spec.Dependencies {
			depNs := dep.ProjectRef.Namespace
			if depNs == "" {
				depNs = p.Namespace
			}
			if dep.ProjectRef.Name == changed.Name && depNs == changed.Namespace {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      p.Name,
						Namespace: p.Namespace,
					},
				})
				break
			}
		}
	}
	return requests
}

func (r *TofuProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("reconciling TofuProject", "name", req.Name, "namespace", req.Namespace)

	var project tofuv1alpha1.TofuProject
	if err := r.Get(ctx, req.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion — run tofu destroy before allowing CR removal
	if !project.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&project, finalizerName) {
			return r.reconcileDestroy(ctx, &project)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present on active resources
	if !controllerutil.ContainsFinalizer(&project, finalizerName) {
		controllerutil.AddFinalizer(&project, finalizerName)
		if err := r.Update(ctx, &project); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Suspend check — pause reconciliation entirely
	if project.Spec.Suspend {
		if project.Status.Phase != "Suspended" {
			project.Status.Phase = "Suspended"
			project.Status.Message = "Reconciliation is suspended"
			r.updateStatusWithCondition(ctx, &project)
		}
		return ctrl.Result{}, nil
	}
	// If previously suspended and now resumed, clear the message and fall through
	if project.Status.Phase == "Suspended" {
		project.Status.Phase = ""
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, &project)
	}

	// Fetch referenced program
	progNs := project.Spec.ProgramRef.Namespace
	if progNs == "" {
		progNs = project.Namespace
	}
	var program tofuv1alpha1.TofuProgram
	if err := r.Get(ctx, types.NamespacedName{Name: project.Spec.ProgramRef.Name, Namespace: progNs}, &program); err != nil {
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("failed to get TofuProgram %s/%s: %v", progNs, project.Spec.ProgramRef.Name, err)
		r.updateStatusWithCondition(ctx, &project)
		return ctrl.Result{}, err
	}

	// Validate mutually exclusive fields
	gitMode := isGitSource(&program)
	if gitMode && program.Spec.ProgramHCL != "" {
		project.Status.Phase = "Error"
		project.Status.Message = "TofuProgram must set either programHCL or source, not both"
		r.updateStatusWithCondition(ctx, &project)
		return ctrl.Result{}, fmt.Errorf("TofuProgram %s/%s has both programHCL and source set", progNs, program.Name)
	}
	if !gitMode && program.Spec.ProgramHCL == "" {
		project.Status.Phase = "Error"
		project.Status.Message = "TofuProgram must set either programHCL or source"
		r.updateStatusWithCondition(ctx, &project)
		return ctrl.Result{}, fmt.Errorf("TofuProgram %s/%s has neither programHCL nor source set", progNs, program.Name)
	}

	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}

	// Resolve cross-project dependencies
	effectiveParams, depHashStr, depResult, depErr := r.resolveDependencies(ctx, &project)
	if depErr != nil {
		return depResult, depErr
	}
	if effectiveParams == nil {
		// Upstream not ready — WaitingDependency phase already set
		return depResult, nil
	}

	// Build generated files
	backendTf := renderBackendTF(project)

	varsJSON, err := json.MarshalIndent(effectiveParams, "", "  ")
	if err != nil {
		return ctrl.Result{}, err
	}
	varsFile := string(varsJSON) + "\n"

	// Hash computation differs by mode
	var hashInput string
	if gitMode {
		src := program.Spec.Source
		hashInput = src.URL + "|" + src.Ref + "|" + src.Path + backendTf + varsFile + project.Spec.Workspace
	} else {
		providersTf := renderProvidersTF(program.Spec.Providers)
		hashInput = providersTf + backendTf + program.Spec.ProgramHCL + varsFile + project.Spec.Workspace
	}
	if depHashStr != "" {
		hashInput += "|deps:" + depHashStr
	}
	hash := sha256.Sum256([]byte(hashInput))
	appliedHash := hex.EncodeToString(hash[:])

	// ConfigMap with TF files
	cmName := fmt.Sprintf("%s-tf", project.Name)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: project.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = mergeLabels(cm.Labels, map[string]string{
			"app.kubernetes.io/managed-by": "tofu-k8s-operator",
			"tofu.example.com/project":     project.Name,
		})
		cm.Data = map[string]string{
			"backend.tf":            backendTf,
			"terraform.tfvars.json": varsFile,
		}
		if !gitMode {
			cm.Data["main.tf"] = program.Spec.ProgramHCL + "\n"
		}
		if len(program.Spec.Providers) > 0 {
			cm.Data["providers.tf"] = renderProvidersTF(program.Spec.Providers)
		}
		return controllerutil.SetControllerReference(&project, cm, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Determine ServiceAccount name and ensure SA + RoleBinding exist
	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		// Use an existing SA — skip creation and RoleBinding
		saName = project.Spec.ServiceAccount.Name
	} else {
		// Auto-create the SA, optionally with custom annotations
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: project.Namespace}}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
			if project.Spec.ServiceAccount != nil && len(project.Spec.ServiceAccount.Annotations) > 0 {
				if sa.Annotations == nil {
					sa.Annotations = map[string]string{}
				}
				for k, v := range project.Spec.ServiceAccount.Annotations {
					sa.Annotations[k] = v
				}
			}
			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: project.Namespace}}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
			rb.RoleRef = rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "tofu-runner",
			}
			rb.Subjects = []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: project.Namespace,
			}}
			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure cache PVC if configured
	cacheMode := r.cacheMode(&project)
	var cachePVCName string
	if cacheMode != "" {
		pvcName, err := r.ensureCachePVC(ctx, &project, cacheMode)
		if err != nil {
			return ctrl.Result{}, err
		}
		cachePVCName = pvcName
	}

	// Check for any active Jobs before creating a new one.
	// Shared cache mode → namespace-wide serialization.
	// Dedicated or no cache → per-project locking (unchanged).
	if cacheMode == "shared" {
		active, err := r.hasActiveNamespaceJobs(ctx, project.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if active {
			log.Info("shared cache mode: waiting for namespace-wide job to complete")
			if project.Status.Phase != "Queued" {
				project.Status.Phase = "Queued"
				project.Status.Message = "Waiting for other jobs in namespace (shared cache)"
				r.updateStatusWithCondition(ctx, &project)
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	} else {
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
			// Skip drift detection jobs — they run independently
			if j.Labels["tofu.example.com/job-type"] == "drift" {
				continue
			}
			if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
				// A Job is still active — wait for it to finish
				log.Info("waiting for active Job to complete before creating a new one", "job", j.Name)
				project.Status.Phase = "Running"
				project.Status.LastJobName = j.Name
				r.updateStatusWithCondition(ctx, &project)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}

	syncInterval := parseSyncInterval(project.Spec.SyncInterval)

	// Branch based on autoApprove
	cacheEnabled := cacheMode != ""
	if project.Spec.AutoApprove {
		return r.reconcileAutoApprove(ctx, &project, &program, appliedHash, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
	}
	return r.reconcilePlanApprove(ctx, &project, &program, appliedHash, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
}

// reconcileAutoApprove handles the existing auto-approve flow unchanged.
func (r *TofuProjectReconciler) reconcileAutoApprove(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Skip if this hash was already successfully applied
	if project.Status.LastAppliedHash == appliedHash {
		// Check for drift if enabled
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			return r.reconcileDriftDetection(ctx, project, program, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
		}
		return requeueAfter(syncInterval), nil
	}

	// Look up the Job for the current hash
	jobName := fmt.Sprintf("%s-apply-%s", project.Name, appliedHash[:8])
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Create a new Job
		newJob := buildJob(*project, jobName, cmName, image, program, saName)
		if cacheEnabled {
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
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Job exists — check its status
	if job.Status.Succeeded > 0 {
		// Capture outputs from apply logs
		outputs, err := r.captureOutputs(ctx, job)
		if err != nil {
			log.Error(err, "failed to capture outputs from apply job (non-fatal)")
		}
		project.Status.Phase = "Succeeded"
		project.Status.LastAppliedHash = appliedHash
		project.Status.LastJobName = jobName
		project.Status.SyncStatus = "sync"
		project.Status.Message = ""
		project.Status.RetryCount = 0
		project.Status.DriftDetected = false
		// Set drift check time so drift detection doesn't fire immediately after apply
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			now := metav1.Now()
			project.Status.LastDriftCheckTime = &now
		}
		if outputs != nil {
			project.Status.Outputs = outputs
		}
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "apply:success")
		return ctrl.Result{}, nil
	}
	if job.Status.Failed > 0 {
		// Retry if policy is configured
		if project.Spec.RetryPolicy != nil && project.Status.RetryCount < project.Spec.RetryPolicy.MaxRetries {
			project.Status.RetryCount++
			delay := parseRetryDelay(project.Spec.RetryPolicy.Delay)
			project.Status.Phase = "Retrying"
			project.Status.Message = fmt.Sprintf("Retry %d/%d after failure", project.Status.RetryCount, project.Spec.RetryPolicy.MaxRetries)
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = "Job failed"
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

// reconcilePlanApprove implements the plan-then-approve flow.
// States:
// 1. No plan yet (or hash changed) → create plan job → "Planning"
// 2. Plan done → read logs, store output → "WaitingApproval"
// 3. Approved (annotation matches hash) → create apply job → "Running"
func (r *TofuProjectReconciler) reconcilePlanApprove(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Skip if this hash was already successfully applied
	if project.Status.LastAppliedHash == appliedHash {
		// Check for drift if enabled
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			return r.reconcileDriftDetection(ctx, project, program, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
		}
		return requeueAfter(syncInterval), nil
	}

	// Check if the spec changed since the last plan — invalidate stale plan
	if project.Status.PendingPlanHash != "" && project.Status.PendingPlanHash != appliedHash {
		log.Info("spec changed, invalidating stale plan", "oldHash", project.Status.PendingPlanHash, "newHash", appliedHash)
		project.Status.PendingPlanHash = ""
		project.Status.PlanOutput = ""
		project.Status.PlanSummary = ""
		project.Status.LastPlanJobName = ""
		// Clear stale approval annotation
		if ann := project.GetAnnotations(); ann != nil {
			if ann[approvedHashAnnotation] != appliedHash {
				delete(ann, approvedHashAnnotation)
				project.SetAnnotations(ann)
				if err := r.Update(ctx, project); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
		r.updateStatusWithCondition(ctx, project)
	}

	// Check if approved
	approvedHash := ""
	if ann := project.GetAnnotations(); ann != nil {
		approvedHash = ann[approvedHashAnnotation]
	}
	if approvedHash == appliedHash {
		// Approved — create apply job
		return r.createApplyAfterApproval(ctx, project, program, appliedHash, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
	}

	// Check if we already have a plan for this hash
	planJobName := fmt.Sprintf("%s-plan-%s", project.Name, appliedHash[:8])

	if project.Status.Phase == "WaitingApproval" && project.Status.PendingPlanHash == appliedHash {
		// Already waiting for approval — nothing to do
		return ctrl.Result{}, nil
	}

	// Check for existing plan job
	planJob := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: planJobName, Namespace: project.Namespace}, planJob); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// No plan job yet — create one
		return r.ensurePlanJob(ctx, project, program, appliedHash, cmName, image, planJobName, cacheEnabled, cachePVCName, saName)
	}

	// Plan job exists — check status
	return r.handlePlanJobStatus(ctx, project, planJob, appliedHash)
}

// ensurePlanJob creates a plan job for the given hash.
func (r *TofuProjectReconciler) ensurePlanJob(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image, planJobName string, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	newJob := buildPlanJob(*project, planJobName, cmName, image, program, saName)
	if cacheEnabled {
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
	project.Status.Phase = "Planning"
	project.Status.LastPlanJobName = planJobName
	project.Status.Message = ""
	r.updateStatusWithCondition(ctx, project)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// handlePlanJobStatus processes the result of a completed plan job.
func (r *TofuProjectReconciler) handlePlanJobStatus(ctx context.Context, project *tofuv1alpha1.TofuProject, planJob *batchv1.Job, appliedHash string) (ctrl.Result, error) {
	if planJob.Status.Succeeded > 0 {
		// Plan succeeded — read logs and transition to WaitingApproval
		output, err := r.readJobLogs(ctx, planJob)
		if err != nil {
			output = fmt.Sprintf("(failed to read plan logs: %v)", err)
		}
		// Truncate to maxPlanOutputBytes
		if len(output) > maxPlanOutputBytes {
			output = output[len(output)-maxPlanOutputBytes:]
		}

		project.Status.Phase = "WaitingApproval"
		project.Status.PendingPlanHash = appliedHash
		project.Status.PlanOutput = output
		project.Status.PlanSummary = extractPlanSummary(output)
		project.Status.LastPlanJobName = planJob.Name
		project.Status.Message = "Plan complete. Approve to apply."
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "plan:complete")
		return ctrl.Result{}, nil
	}
	if planJob.Status.Failed > 0 {
		output, err := r.readJobLogs(ctx, planJob)
		if err != nil {
			output = fmt.Sprintf("(failed to read plan logs: %v)", err)
		}
		if len(output) > maxPlanOutputBytes {
			output = output[len(output)-maxPlanOutputBytes:]
		}
		project.Status.Phase = "Error"
		project.Status.LastPlanJobName = planJob.Name
		project.Status.PlanOutput = output
		project.Status.Message = "Plan job failed"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	// Still running
	project.Status.Phase = "Planning"
	project.Status.LastPlanJobName = planJob.Name
	r.updateStatusWithCondition(ctx, project)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// createApplyAfterApproval creates an apply job after the plan has been approved.
func (r *TofuProjectReconciler) createApplyAfterApproval(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	jobName := fmt.Sprintf("%s-apply-%s", project.Name, appliedHash[:8])
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Create apply job — always with auto-approve since plan was already reviewed
		newJob := buildApplyJob(*project, jobName, cmName, image, program, saName)
		if cacheEnabled {
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
		project.Status.Message = "Applying approved plan"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Job exists — check its status
	if job.Status.Succeeded > 0 {
		// Capture outputs from apply logs
		outputs, err := r.captureOutputs(ctx, job)
		if err != nil {
			log.Error(err, "failed to capture outputs from apply job (non-fatal)")
		}
		project.Status.Phase = "Succeeded"
		project.Status.LastAppliedHash = appliedHash
		project.Status.LastJobName = jobName
		project.Status.SyncStatus = "sync"
		project.Status.Message = ""
		project.Status.RetryCount = 0
		project.Status.DriftDetected = false
		// Set drift check time so drift detection doesn't fire immediately after apply
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			now := metav1.Now()
			project.Status.LastDriftCheckTime = &now
		}
		// Clear plan fields after successful apply
		project.Status.PendingPlanHash = ""
		project.Status.PlanOutput = ""
		project.Status.PlanSummary = ""
		if outputs != nil {
			project.Status.Outputs = outputs
		}
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "apply:success")
		return requeueAfter(syncInterval), nil
	}
	if job.Status.Failed > 0 {
		// Retry if policy is configured
		if project.Spec.RetryPolicy != nil && project.Status.RetryCount < project.Spec.RetryPolicy.MaxRetries {
			project.Status.RetryCount++
			delay := parseRetryDelay(project.Spec.RetryPolicy.Delay)
			project.Status.Phase = "Retrying"
			project.Status.Message = fmt.Sprintf("Retry %d/%d after failure", project.Status.RetryCount, project.Spec.RetryPolicy.MaxRetries)
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = "Apply job failed"
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

// readJobLogs reads the logs from the first pod of a Job.
func (r *TofuProjectReconciler) readJobLogs(ctx context.Context, job *batchv1.Job) (string, error) {
	if r.Clientset == nil {
		return "", fmt.Errorf("clientset not configured")
	}

	pods, err := r.Clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
	})
	if err != nil {
		return "", fmt.Errorf("listing pods for job %s: %w", job.Name, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for job %s", job.Name)
	}

	podName := pods.Items[0].Name
	req := r.Clientset.CoreV1().Pods(job.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: "tofu",
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("streaming logs for pod %s: %w", podName, err)
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", fmt.Errorf("reading logs for pod %s: %w", podName, err)
	}
	return buf.String(), nil
}

// extractPlanSummary extracts the summary line from tofu plan output.
func extractPlanSummary(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Plan:") {
			return trimmed
		}
		if trimmed == "No changes. Your infrastructure matches the configuration." ||
			trimmed == "No changes. Infrastructure is up-to-date." {
			return "No changes."
		}
	}
	return ""
}

// buildPlanJob creates a Job that runs `tofu plan`.
func buildPlanJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	backoff := int32(0)
	workspace := project.Spec.Workspace
	gitMode := isGitSource(program)
	cmd := []string{"/bin/sh", "-c", renderPlanCommand(workspace, gitMode, program.Spec.Source)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "plan",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(program.Spec.Source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if program.Spec.Source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: program.Spec.Source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

// buildApplyJob creates an apply Job that always uses -auto-approve (for plan-approve flow).
func buildApplyJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	// Force auto-approve since the plan has already been reviewed
	projectCopy := project
	projectCopy.Spec.AutoApprove = true
	job := buildJob(projectCopy, jobName, cmName, image, program, saName)
	return job
}

// renderPlanCommand generates the shell command for tofu plan.
func renderPlanCommand(workspace string, gitMode bool, source *tofuv1alpha1.GitSource) string {
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	return fmt.Sprintf(`set -euo pipefail
%s
tofu version
tofu init -input=false
%stofu plan -input=false -no-color
`, copyStep, ws)
}

func buildJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	backoff := int32(0)
	workspace := project.Spec.Workspace
	gitMode := isGitSource(program)
	cmd := []string{"/bin/sh", "-c", renderCommand(workspace, project.Spec.AutoApprove, gitMode, program.Spec.Source)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "apply",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(program.Spec.Source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if program.Spec.Source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: program.Spec.Source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

func gitCloneInitContainer(source *tofuv1alpha1.GitSource) corev1.Container {
	ref := source.Ref
	if ref == "" {
		ref = "main"
	}
	// Clone script supports two auth modes detected from env/volume:
	//   1. HTTPS token (PAT / GitHub App): Secret key "token"
	//   2. SSH deploy key: Secret key "sshPrivateKey" (+ optional "known_hosts")
	// Falls back to unauthenticated clone for public repos.
	script := fmt.Sprintf(`set -euo pipefail
REPO_URL="%s"
if [ -n "${GIT_TOKEN:-}" ]; then
  REPO_URL=$(echo "$REPO_URL" | sed "s|https://|https://x-access-token:${GIT_TOKEN}@|")
elif [ -f /git-credentials/sshPrivateKey ]; then
  mkdir -p ~/.ssh
  cp /git-credentials/sshPrivateKey ~/.ssh/id_key
  chmod 600 ~/.ssh/id_key
  if [ -f /git-credentials/known_hosts ]; then
    cp /git-credentials/known_hosts ~/.ssh/known_hosts
  else
    export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=no -i ~/.ssh/id_key"
  fi
  if [ -z "${GIT_SSH_COMMAND:-}" ]; then
    export GIT_SSH_COMMAND="ssh -i ~/.ssh/id_key"
  fi
fi
git clone --branch %s --depth 1 "$REPO_URL" /git-repo
`, source.URL, shellEscape(ref))

	container := corev1.Container{
		Name:    "git-clone",
		Image:   "alpine/git:latest",
		Command: []string{"/bin/sh", "-c", script},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "git-repo",
			MountPath: "/git-repo",
		}},
	}

	if source.CredentialsSecretRef != nil {
		secretName := source.CredentialsSecretRef.Name
		// Mount token as env var for HTTPS auth
		optional := true
		container.Env = []corev1.EnvVar{{
			Name: "GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  "token",
					Optional:             &optional,
				},
			},
		}}
		// Mount full Secret as volume for SSH key access
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "git-credentials",
			MountPath: "/git-credentials",
			ReadOnly:  true,
		})
	}

	return container
}

func (r *TofuProjectReconciler) reconcileDestroy(ctx context.Context, project *tofuv1alpha1.TofuProject) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("handling destroy for TofuProject", "name", project.Name)

	// Delete protection — block until explicitly approved
	if project.Spec.DeleteProtection {
		approved := false
		if ann := project.GetAnnotations(); ann != nil {
			approved = ann[approvedDeleteAnnotation] == "true"
		}
		if !approved {
			if project.Status.Phase != "WaitingDeleteApproval" {
				project.Status.Phase = "WaitingDeleteApproval"
				project.Status.Message = "Delete protection enabled. Run 'kubectl tofu delete <name>' to approve."
				r.updateStatusWithCondition(ctx, project)
			}
			return ctrl.Result{}, nil
		}
	}

	// The ConfigMap must still exist (owned by the project, protected by finalizer).
	cmName := fmt.Sprintf("%s-tf", project.Name)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: project.Namespace}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ConfigMap not found, cannot run destroy — removing finalizer", "configmap", cmName)
			controllerutil.RemoveFinalizer(project, finalizerName)
			return ctrl.Result{}, r.Update(ctx, project)
		}
		return ctrl.Result{}, err
	}

	// Fetch referenced program for git source info (fall back to inline if not found)
	progNs := project.Spec.ProgramRef.Namespace
	if progNs == "" {
		progNs = project.Namespace
	}
	var program tofuv1alpha1.TofuProgram
	programFound := true
	if err := r.Get(ctx, types.NamespacedName{Name: project.Spec.ProgramRef.Name, Namespace: progNs}, &program); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		programFound = false
	}

	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}

	var programPtr *tofuv1alpha1.TofuProgram
	if programFound {
		programPtr = &program
	}

	// Determine SA name for destroy job
	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		saName = project.Spec.ServiceAccount.Name
	}

	jobName := fmt.Sprintf("%s-destroy", project.Name)
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, &job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Create the destroy Job
		newJob := buildDestroyJob(project, jobName, cmName, image, programPtr, saName)
		cacheMode := r.cacheMode(project)
		if cacheMode != "" {
			pvcName, err := r.ensureCachePVC(ctx, project, cacheMode)
			if err != nil {
				return ctrl.Result{}, err
			}
			addCacheToJob(newJob, pvcName)
		}
		addEnvToJob(newJob, project)
		if err := addResourcesToJob(newJob, project); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, newJob); err != nil {
			return ctrl.Result{}, err
		}
		project.Status.Phase = "Destroying"
		project.Status.LastJobName = jobName
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Destroy Job exists — check status
	if job.Status.Succeeded > 0 {
		log.Info("destroy succeeded, removing finalizer", "name", project.Name)
		controllerutil.RemoveFinalizer(project, finalizerName)
		return ctrl.Result{}, r.Update(ctx, project)
	}
	if job.Status.Failed > 0 {
		project.Status.Phase = "DestroyFailed"
		project.Status.Message = "Destroy job failed"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	// Still running
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func buildDestroyJob(project *tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	backoff := int32(0)
	gitMode := program != nil && isGitSource(program)
	var source *tofuv1alpha1.GitSource
	if gitMode {
		source = program.Spec.Source
	}
	cmd := []string{"/bin/sh", "-c", renderDestroyCommand(project.Spec.Workspace, gitMode, source)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "destroy",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

func isGitSource(program *tofuv1alpha1.TofuProgram) bool {
	return program.Spec.Source != nil && program.Spec.Source.URL != ""
}

func renderDestroyCommand(workspace string, gitMode bool, source *tofuv1alpha1.GitSource) string {
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	return fmt.Sprintf(`set -euo pipefail
%s
tofu version
tofu init -input=false
%stofu destroy -input=false -auto-approve
`, copyStep, ws)
}

func renderCommand(workspace string, autoApprove bool, gitMode bool, source *tofuv1alpha1.GitSource) string {
	approve := ""
	if autoApprove {
		approve = " -auto-approve"
	}
	ws := ""
	if strings.TrimSpace(workspace) != "" {
		ws = fmt.Sprintf("tofu workspace select %s || tofu workspace new %s\n", shellEscape(workspace), shellEscape(workspace))
	}
	copyStep := "cp /tf-config/* /work/"
	if gitMode && source != nil {
		path := source.Path
		if path == "" {
			path = "."
		}
		copyStep = fmt.Sprintf("cp -r /git-repo/%s/. /work/\ncp /tf-config/* /work/", path)
	}
	return fmt.Sprintf(`set -euo pipefail
%s
tofu version
tofu init -input=false
%stofu apply -input=false%s
echo '%s'
tofu output -json
`, copyStep, ws, approve, outputMarker)
}

func shellEscape(s string) string {
	// minimal safe quoting for sh -lc
	s = strings.ReplaceAll(s, "'", "'\\''")
	return "'" + s + "'"
}

func renderProvidersTF(providers []tofuv1alpha1.ProviderSpec) string {
	var b strings.Builder
	b.WriteString("terraform {\n  required_providers {\n")
	for _, p := range providers {
		name := p.Name
		source := p.Source
		if source == "" {
			source = name
		}
		b.WriteString(fmt.Sprintf("    %s = {\n      source = %q\n", name, source))
		if p.Version != "" {
			b.WriteString(fmt.Sprintf("      version = %q\n", p.Version))
		}
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n}\n\n")
	for _, p := range providers {
		b.WriteString(fmt.Sprintf("provider %q {\n", p.Name))
		if strings.TrimSpace(p.ConfigHCL) != "" {
			b.WriteString(indentHCL(p.ConfigHCL, 2))
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

func indentHCL(hcl string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(hcl, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func renderBackendTF(project tofuv1alpha1.TofuProject) string {
	ns := project.Spec.Backend.Namespace
	if ns == "" {
		ns = project.Namespace
	}
	suffix := project.Spec.Backend.SecretSuffix
	if suffix == "" {
		suffix = project.Name
	}
	// Kubernetes backend docs vary by implementation; this matches the commonly used options.
	return fmt.Sprintf(`terraform {
  backend "kubernetes" {
    secret_suffix     = %q
    namespace         = %q
    in_cluster_config = true
  }
}
`, suffix, ns)
}

func mergeLabels(existing, add map[string]string) map[string]string {
	if existing == nil {
		existing = map[string]string{}
	}
	for k, v := range add {
		existing[k] = v
	}
	return existing
}

// parseSyncInterval parses a duration string for the sync interval.
// Returns 0 on empty string or parse error (meaning no periodic requeue).
func parseSyncInterval(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// requeueAfter returns a ctrl.Result that requeues after the given duration.
// If d is 0, it returns an empty Result (no requeue).
func requeueAfter(d time.Duration) ctrl.Result {
	if d <= 0 {
		return ctrl.Result{}
	}
	return ctrl.Result{RequeueAfter: d}
}

// parseOutputsFromLogs extracts tofu output values from job logs.
// It looks for the outputMarker and parses the JSON that follows it.
func parseOutputsFromLogs(logs string) (map[string]string, error) {
	idx := strings.LastIndex(logs, outputMarker)
	if idx < 0 {
		return nil, fmt.Errorf("output marker not found in logs")
	}
	jsonPart := strings.TrimSpace(logs[idx+len(outputMarker):])
	if jsonPart == "" {
		return nil, fmt.Errorf("no JSON after output marker")
	}

	// tofu output -json produces: {"key": {"value": <val>, "type": ...}, ...}
	var raw map[string]struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal([]byte(jsonPart), &raw); err != nil {
		return nil, fmt.Errorf("parsing tofu output JSON: %w", err)
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprintf("%v", v.Value)
	}
	return result, nil
}

// captureOutputs reads job logs and extracts tofu outputs.
func (r *TofuProjectReconciler) captureOutputs(ctx context.Context, job *batchv1.Job) (map[string]string, error) {
	logs, err := r.readJobLogs(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("reading job logs: %w", err)
	}
	return parseOutputsFromLogs(logs)
}

// resolveDependencies resolves cross-project dependencies.
// Returns:
//   - effectiveParams: merged params (spec.Params + dependency outputs); nil if upstream not ready
//   - depHashStr: deterministic hash string of resolved dependency values
//   - result/err: if effectiveParams is nil, return this result to the caller
func (r *TofuProjectReconciler) resolveDependencies(ctx context.Context, project *tofuv1alpha1.TofuProject) (map[string]string, string, ctrl.Result, error) {
	// Resolve external params (paramFrom + paramBindings) first
	externalParams, externalHashStr, err := r.resolveExternalParams(ctx, project)
	if err != nil {
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("Failed to resolve external params: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return nil, "", ctrl.Result{}, err
	}

	// Build effectiveParams with precedence:
	// 1. paramFrom values (lowest)
	// 2. paramBindings values (override paramFrom) — already merged in externalParams
	// 3. params inline values (override both)
	// 4. dependency outputs (highest — applied below)
	effectiveParams := map[string]string{}
	for k, v := range externalParams {
		effectiveParams[k] = v
	}
	for k, v := range project.Spec.Params {
		effectiveParams[k] = v
	}

	if len(project.Spec.Dependencies) == 0 {
		return effectiveParams, externalHashStr, ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)

	// Collect resolved dependency values for hash
	var depParts []string

	for _, dep := range project.Spec.Dependencies {
		upstreamNs := dep.ProjectRef.Namespace
		if upstreamNs == "" {
			upstreamNs = project.Namespace
		}

		var upstream tofuv1alpha1.TofuProject
		if err := r.Get(ctx, types.NamespacedName{Name: dep.ProjectRef.Name, Namespace: upstreamNs}, &upstream); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("upstream dependency not found, waiting", "upstream", dep.ProjectRef.Name)
				project.Status.Phase = "WaitingDependency"
				project.Status.Message = fmt.Sprintf("Waiting for upstream project %s/%s", upstreamNs, dep.ProjectRef.Name)
				r.updateStatusWithCondition(ctx, project)
				return nil, "", ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
			return nil, "", ctrl.Result{}, err
		}

		if upstream.Status.Phase != "Succeeded" {
			log.Info("upstream dependency not yet succeeded, waiting", "upstream", dep.ProjectRef.Name, "phase", upstream.Status.Phase)
			project.Status.Phase = "WaitingDependency"
			project.Status.Message = fmt.Sprintf("Waiting for upstream project %s/%s (phase: %s)", upstreamNs, dep.ProjectRef.Name, upstream.Status.Phase)
			r.updateStatusWithCondition(ctx, project)
			return nil, "", ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}

		for upstreamOutput, downstreamParam := range dep.Outputs {
			val, ok := upstream.Status.Outputs[upstreamOutput]
			if !ok {
				log.Info("upstream output not found, waiting", "upstream", dep.ProjectRef.Name, "output", upstreamOutput)
				project.Status.Phase = "WaitingDependency"
				project.Status.Message = fmt.Sprintf("Waiting for output %q from upstream %s/%s", upstreamOutput, upstreamNs, dep.ProjectRef.Name)
				r.updateStatusWithCondition(ctx, project)
				return nil, "", ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
			effectiveParams[downstreamParam] = val
			depParts = append(depParts, fmt.Sprintf("%s=%s", downstreamParam, val))
		}
	}

	// Build deterministic hash string combining external and dependency hashes
	sort.Strings(depParts)
	depHashStr := strings.Join(depParts, ",")
	combinedHash := depHashStr
	if externalHashStr != "" && combinedHash != "" {
		combinedHash = externalHashStr + "|" + combinedHash
	} else if externalHashStr != "" {
		combinedHash = externalHashStr
	}

	return effectiveParams, combinedHash, ctrl.Result{}, nil
}

// resolveExternalParams fetches ConfigMap/Secret data referenced by paramFrom and paramBindings.
// Returns (resolved params map, deterministic hash string, error).
func (r *TofuProjectReconciler) resolveExternalParams(ctx context.Context, project *tofuv1alpha1.TofuProject) (map[string]string, string, error) {
	resolved := map[string]string{}

	// 1. paramFrom: bulk-read all keys from each ConfigMap/Secret (lowest precedence)
	for _, pf := range project.Spec.ParamFrom {
		if pf.ConfigMapRef != nil {
			ns := pf.ConfigMapRef.Namespace
			if ns == "" {
				ns = project.Namespace
			}
			var cm corev1.ConfigMap
			if err := r.Get(ctx, types.NamespacedName{Name: pf.ConfigMapRef.Name, Namespace: ns}, &cm); err != nil {
				return nil, "", fmt.Errorf("paramFrom configMapRef %s/%s: %w", ns, pf.ConfigMapRef.Name, err)
			}
			for k, v := range cm.Data {
				resolved[k] = v
			}
		}
		if pf.SecretRef != nil {
			ns := pf.SecretRef.Namespace
			if ns == "" {
				ns = project.Namespace
			}
			var secret corev1.Secret
			if err := r.Get(ctx, types.NamespacedName{Name: pf.SecretRef.Name, Namespace: ns}, &secret); err != nil {
				return nil, "", fmt.Errorf("paramFrom secretRef %s/%s: %w", ns, pf.SecretRef.Name, err)
			}
			for k, v := range secret.Data {
				resolved[k] = string(v)
			}
		}
	}

	// 2. paramBindings: individual key refs (override paramFrom values)
	for _, pb := range project.Spec.ParamBindings {
		if pb.ConfigMapKeyRef != nil {
			ns := project.Namespace
			var cm corev1.ConfigMap
			if err := r.Get(ctx, types.NamespacedName{Name: pb.ConfigMapKeyRef.Name, Namespace: ns}, &cm); err != nil {
				return nil, "", fmt.Errorf("paramBindings configMapKeyRef %s/%s: %w", ns, pb.ConfigMapKeyRef.Name, err)
			}
			val, ok := cm.Data[pb.ConfigMapKeyRef.Key]
			if !ok {
				return nil, "", fmt.Errorf("paramBindings configMapKeyRef %s/%s: key %q not found", ns, pb.ConfigMapKeyRef.Name, pb.ConfigMapKeyRef.Key)
			}
			resolved[pb.Name] = val
		}
		if pb.SecretKeyRef != nil {
			ns := project.Namespace
			var secret corev1.Secret
			if err := r.Get(ctx, types.NamespacedName{Name: pb.SecretKeyRef.Name, Namespace: ns}, &secret); err != nil {
				return nil, "", fmt.Errorf("paramBindings secretKeyRef %s/%s: %w", ns, pb.SecretKeyRef.Name, err)
			}
			val, ok := secret.Data[pb.SecretKeyRef.Key]
			if !ok {
				return nil, "", fmt.Errorf("paramBindings secretKeyRef %s/%s: key %q not found", ns, pb.SecretKeyRef.Name, pb.SecretKeyRef.Key)
			}
			resolved[pb.Name] = string(val)
		}
	}

	if len(resolved) == 0 {
		return resolved, "", nil
	}

	// Build deterministic hash string of all resolved values (sorted)
	var parts []string
	for k, v := range resolved {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(parts)
	hashStr := "ext:" + strings.Join(parts, ",")

	return resolved, hashStr, nil
}

// findProjectsReferencingConfigMap returns reconcile requests for all TofuProjects
// that reference the changed ConfigMap via paramFrom or paramBindings.
func (r *TofuProjectReconciler) findProjectsReferencingConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}

	var allProjects tofuv1alpha1.TofuProjectList
	if err := r.List(ctx, &allProjects); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range allProjects.Items {
		p := &allProjects.Items[i]
		if projectReferencesConfigMap(p, cm.Name, cm.Namespace) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      p.Name,
					Namespace: p.Namespace,
				},
			})
		}
	}
	return requests
}

// findProjectsReferencingSecret returns reconcile requests for all TofuProjects
// that reference the changed Secret via paramFrom or paramBindings.
func (r *TofuProjectReconciler) findProjectsReferencingSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var allProjects tofuv1alpha1.TofuProjectList
	if err := r.List(ctx, &allProjects); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range allProjects.Items {
		p := &allProjects.Items[i]
		if projectReferencesSecret(p, secret.Name, secret.Namespace) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      p.Name,
					Namespace: p.Namespace,
				},
			})
		}
	}
	return requests
}

// projectReferencesConfigMap returns true if the project references the given ConfigMap
// via paramFrom or paramBindings.
func projectReferencesConfigMap(project *tofuv1alpha1.TofuProject, name, namespace string) bool {
	for _, pf := range project.Spec.ParamFrom {
		if pf.ConfigMapRef != nil {
			ns := pf.ConfigMapRef.Namespace
			if ns == "" {
				ns = project.Namespace
			}
			if pf.ConfigMapRef.Name == name && ns == namespace {
				return true
			}
		}
	}
	for _, pb := range project.Spec.ParamBindings {
		if pb.ConfigMapKeyRef != nil && pb.ConfigMapKeyRef.Name == name && project.Namespace == namespace {
			return true
		}
	}
	return false
}

// projectReferencesSecret returns true if the project references the given Secret
// via paramFrom or paramBindings.
func projectReferencesSecret(project *tofuv1alpha1.TofuProject, name, namespace string) bool {
	for _, pf := range project.Spec.ParamFrom {
		if pf.SecretRef != nil {
			ns := pf.SecretRef.Namespace
			if ns == "" {
				ns = project.Namespace
			}
			if pf.SecretRef.Name == name && ns == namespace {
				return true
			}
		}
	}
	for _, pb := range project.Spec.ParamBindings {
		if pb.SecretKeyRef != nil && pb.SecretKeyRef.Name == name && project.Namespace == namespace {
			return true
		}
	}
	return false
}

// cacheMode returns the effective cache mode for a project ("shared", "dedicated", or "").
func (r *TofuProjectReconciler) cacheMode(project *tofuv1alpha1.TofuProject) string {
	if project.Spec.Cache == nil {
		return ""
	}
	mode := project.Spec.Cache.Mode
	if mode == "shared" || mode == "dedicated" {
		return mode
	}
	return ""
}

// ensureCachePVC creates or verifies the PVC for provider plugin caching.
func (r *TofuProjectReconciler) ensureCachePVC(ctx context.Context, project *tofuv1alpha1.TofuProject, mode string) (string, error) {
	var pvcName string
	if mode == "shared" {
		pvcName = "tofu-plugin-cache"
	} else {
		pvcName = fmt.Sprintf("%s-plugin-cache", project.Name)
	}

	size := "1Gi"
	if project.Spec.Cache != nil && project.Spec.Cache.Size != "" {
		size = project.Spec.Cache.Size
	}

	storageSize := resource.MustParse(size)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: project.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		// Only set spec on creation — PVC spec is immutable after creation
		if pvc.CreationTimestamp.IsZero() {
			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
			}
			if project.Spec.Cache != nil && project.Spec.Cache.StorageClass != "" {
				pvc.Spec.StorageClassName = &project.Spec.Cache.StorageClass
			}
		}
		// Dedicated PVC is owned by the project; shared PVC is not owned
		if mode == "dedicated" {
			return controllerutil.SetControllerReference(project, pvc, r.Scheme)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ensuring cache PVC %s: %w", pvcName, err)
	}

	return pvcName, nil
}

// addCacheToJob injects the cache PVC volume, mount, and env var into a Job.
func addCacheToJob(job *batchv1.Job, pvcName string) {
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "plugin-cache",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		job.Spec.Template.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{
			Name:      "plugin-cache",
			MountPath: "/plugin-cache",
		},
	)
	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{
			Name:  "TF_PLUGIN_CACHE_DIR",
			Value: "/plugin-cache",
		},
	)
}

// addEnvToJob injects user-specified env vars and envFrom into the first container of a Job.
func addEnvToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject) {
	if len(project.Spec.Env) > 0 {
		job.Spec.Template.Spec.Containers[0].Env = append(
			job.Spec.Template.Spec.Containers[0].Env,
			project.Spec.Env...,
		)
	}
	if len(project.Spec.EnvFrom) > 0 {
		job.Spec.Template.Spec.Containers[0].EnvFrom = append(
			job.Spec.Template.Spec.Containers[0].EnvFrom,
			project.Spec.EnvFrom...,
		)
	}
}

// addResourcesToJob sets resource requests/limits on the first container of a Job.
func addResourcesToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject) error {
	if project.Spec.Resources == nil {
		return nil
	}
	res := corev1.ResourceRequirements{}
	if len(project.Spec.Resources.Limits) > 0 {
		res.Limits = corev1.ResourceList{}
		for k, v := range project.Spec.Resources.Limits {
			qty, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("parsing resource limit %s=%s: %w", k, v, err)
			}
			res.Limits[corev1.ResourceName(k)] = qty
		}
	}
	if len(project.Spec.Resources.Requests) > 0 {
		res.Requests = corev1.ResourceList{}
		for k, v := range project.Spec.Resources.Requests {
			qty, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("parsing resource request %s=%s: %w", k, v, err)
			}
			res.Requests[corev1.ResourceName(k)] = qty
		}
	}
	job.Spec.Template.Spec.Containers[0].Resources = res
	return nil
}

// parseRetryDelay parses a retry delay string. Returns 30s on empty or invalid input.
func parseRetryDelay(s string) time.Duration {
	if s == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// sendNotification sends webhook notifications for lifecycle events.
func sendNotification(ctx context.Context, project *tofuv1alpha1.TofuProject, event string) {
	if project.Spec.Notifications == nil {
		return
	}
	log := ctrl.LoggerFrom(ctx)
	payload := map[string]string{
		"project":   project.Name,
		"namespace": project.Namespace,
		"event":     event,
		"phase":     project.Status.Phase,
		"message":   project.Status.Message,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Error(err, "failed to marshal notification payload")
		return
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	for _, wh := range project.Spec.Notifications.Webhooks {
		matched := false
		for _, e := range wh.Events {
			if e == event {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
		if err != nil {
			log.Error(err, "failed to create notification request", "url", wh.URL)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			log.Error(err, "failed to send notification", "url", wh.URL, "event", event)
			continue
		}
		resp.Body.Close()
	}
}

// hasActiveNamespaceJobs checks if there are any active tofu-operator jobs in the namespace.
func (r *TofuProjectReconciler) hasActiveNamespaceJobs(ctx context.Context, namespace string) (bool, error) {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(namespace), client.MatchingLabelsSelector{
		Selector: labels.SelectorFromSet(map[string]string{
			"app.kubernetes.io/managed-by": "tofu-k8s-operator",
		}),
	}); err != nil {
		return false, err
	}
	for i := range jobList.Items {
		j := &jobList.Items[i]
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			return true, nil
		}
	}
	return false, nil
}

// sanitizeSecretKey ensures the key is a valid DNS subdomain and not too long for Kubernetes Secret keys
func sanitizeSecretKey(key string) string {
	const maxLen = 253
	var b strings.Builder
	for i := 0; i < len(key) && b.Len() < maxLen; i++ {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			b.WriteByte(c)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	out = strings.Trim(out, "-. ")
	if out == "" {
		return ""
	}
	return out
}

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
				now := metav1.Now()
				project.Status.LastDriftCheckTime = &now
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
	if err := addResourcesToJob(newJob, project); err != nil {
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

// applyImmediately returns the effective value of spec.applyImmediately (default true).
func applyImmediately(project *tofuv1alpha1.TofuProject) bool {
	if project.Spec.ApplyImmediately == nil {
		return true
	}
	return *project.Spec.ApplyImmediately
}

// updateReadyCondition derives the Ready condition from the current phase and applyImmediately setting.
func updateReadyCondition(project *tofuv1alpha1.TofuProject) {
	now := metav1.Now()
	var status metav1.ConditionStatus
	var reason, message string

	switch project.Status.Phase {
	case "Succeeded", "DriftChecking":
		status = metav1.ConditionTrue
		reason = "Applied"
		message = "Apply completed successfully"
		if project.Status.Phase == "DriftChecking" {
			message = "Running drift detection"
		}
	case "Error", "DestroyFailed":
		status = metav1.ConditionFalse
		reason = "Error"
		message = project.Status.Message
	default:
		// Running, Planning, WaitingApproval, WaitingDeleteApproval, Suspended, etc.
		if !applyImmediately(project) {
			status = metav1.ConditionTrue
			reason = "Accepted"
			message = "Resource accepted, execution is asynchronous"
		} else {
			status = metav1.ConditionFalse
			reason = "Progressing"
			message = project.Status.Message
			if message == "" {
				message = "Waiting for completion"
			}
		}
	}

	setCondition(project, "Ready", status, reason, message, now)
}

func setCondition(project *tofuv1alpha1.TofuProject, condType string, status metav1.ConditionStatus, reason, message string, now metav1.Time) {
	for i, c := range project.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				project.Status.Conditions[i].LastTransitionTime = now
			}
			project.Status.Conditions[i].Status = status
			project.Status.Conditions[i].Reason = reason
			project.Status.Conditions[i].Message = message
			project.Status.Conditions[i].ObservedGeneration = project.Generation
			return
		}
	}
	project.Status.Conditions = append(project.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: project.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

// updateStatusWithCondition updates the status subresource and automatically sets the Ready condition.
func (r *TofuProjectReconciler) updateStatusWithCondition(ctx context.Context, project *tofuv1alpha1.TofuProject) {
	updateReadyCondition(project)
	_ = r.Status().Update(ctx, project)
}
