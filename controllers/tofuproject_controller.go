package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

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

	// Pinned revision — use stored snapshot instead of computing from current spec
	if project.Spec.PinnedRevision > 0 {
		return r.reconcilePinned(ctx, &project)
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

	// Validate S3 backend
	if project.Spec.Backend.Type == "s3" {
		if project.Spec.Backend.S3 == nil || project.Spec.Backend.S3.Bucket == "" || project.Spec.Backend.S3.Region == "" {
			project.Status.Phase = "Error"
			project.Status.Message = "S3 backend requires s3.bucket and s3.region to be set"
			r.updateStatusWithCondition(ctx, &project)
			return ctrl.Result{}, fmt.Errorf("S3 backend missing required fields for TofuProject %s/%s", project.Namespace, project.Name)
		}
	}

	// Validate validation steps
	if project.Spec.Validation != nil {
		for _, step := range project.Spec.Validation.Steps {
			if step.Standard != "" && step.Custom != nil {
				project.Status.Phase = "Error"
				project.Status.Message = fmt.Sprintf("validation step %q must set either standard or custom, not both", step.Name)
				r.updateStatusWithCondition(ctx, &project)
				return ctrl.Result{}, fmt.Errorf("validation step %q has both standard and custom set", step.Name)
			}
			if step.Standard == "" && step.Custom == nil {
				project.Status.Phase = "Error"
				project.Status.Message = fmt.Sprintf("validation step %q must set either standard or custom", step.Name)
				r.updateStatusWithCondition(ctx, &project)
				return ctrl.Result{}, fmt.Errorf("validation step %q has neither standard nor custom set", step.Name)
			}
			if step.Standard != "" {
				if _, ok := standardValidators[step.Standard]; !ok {
					project.Status.Phase = "Error"
					project.Status.Message = fmt.Sprintf("unknown standard validator %q in step %q", step.Standard, step.Name)
					r.updateStatusWithCondition(ctx, &project)
					return ctrl.Result{}, fmt.Errorf("unknown standard validator %q", step.Standard)
				}
			}
		}
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
	if project.Spec.IgnoreProviders {
		hashInput += "|ignoreProviders"
	}
	if project.Spec.AdditionalProvidersHCL != "" {
		hashInput += "|additionalProviders:" + project.Spec.AdditionalProvidersHCL
	}
	if project.Spec.Validation != nil {
		valJSON, _ := json.Marshal(project.Spec.Validation)
		hashInput += "|validation:" + string(valJSON)
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
		if project.Spec.AdditionalProvidersHCL != "" {
			cm.Data["additional-providers.tf"] = project.Spec.AdditionalProvidersHCL + "\n"
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
		gitMode := isGitSource(program)
		var source *tofuv1alpha1.GitSource
		if gitMode {
			source = program.Spec.Source
		}
		if err := addValidationToJob(newJob, project, image, gitMode, source); err != nil {
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
		// Read job logs for outputs and revision audit
		jobLogs, logsErr := r.readJobLogs(ctx, job)
		if logsErr != nil {
			log.Error(logsErr, "failed to read apply job logs (non-fatal)")
		}
		var outputs map[string]string
		if jobLogs != "" {
			outputs, _ = parseOutputsFromLogs(jobLogs)
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
		// Create revision for audit trail
		r.createRevisionFromCM(ctx, project, appliedHash, jobName, "succeeded", jobLogs)
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
		// Read job logs for failure audit
		failLogs, logsErr := r.readJobLogs(ctx, job)
		if logsErr != nil {
			log.Error(logsErr, "failed to read failed job logs (non-fatal)")
		}
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = "Job failed"
		// Create revision for failed apply audit trail
		r.createRevisionFromCM(ctx, project, appliedHash, jobName, "failed", failLogs)
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
	gitMode := isGitSource(program)
	var source *tofuv1alpha1.GitSource
	if gitMode {
		source = program.Spec.Source
	}
	if err := addValidationToJob(newJob, project, image, gitMode, source); err != nil {
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
		gitMode := isGitSource(program)
		var source *tofuv1alpha1.GitSource
		if gitMode {
			source = program.Spec.Source
		}
		if err := addValidationToJob(newJob, project, image, gitMode, source); err != nil {
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
		// Preserve plan output for revision before clearing
		revPlanOutput := project.Status.PlanOutput
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
		// Create revision for audit trail (uses preserved plan output)
		r.createRevisionFromCM(ctx, project, appliedHash, jobName, "succeeded", revPlanOutput)
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
		// Read job logs for failure audit
		failLogs, logsErr := r.readJobLogs(ctx, job)
		if logsErr != nil {
			log.Error(logsErr, "failed to read failed job logs (non-fatal)")
		}
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = "Apply job failed"
		// Create revision for failed apply audit trail
		r.createRevisionFromCM(ctx, project, appliedHash, jobName, "failed", failLogs)
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
		destroyGitMode := programPtr != nil && isGitSource(programPtr)
		var destroySource *tofuv1alpha1.GitSource
		if destroyGitMode {
			destroySource = programPtr.Spec.Source
		}
		if err := addValidationToJob(newJob, project, image, destroyGitMode, destroySource); err != nil {
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
