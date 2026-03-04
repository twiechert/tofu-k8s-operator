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
	forceUnlockAnnotation    = "tofu.example.com/force-unlock"
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

// reconcileParams bundles the parameters resolved during prepareReconcile.
type reconcileParams struct {
	program      tofuv1alpha1.TofuProgram
	appliedHash  string
	cmName       string
	saName       string
	image        string
	cacheMode    string
	cachePVCName string
}

func (r *TofuProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("reconciling TofuProject", "name", req.Name, "namespace", req.Namespace)

	var project tofuv1alpha1.TofuProject
	if err := r.Get(ctx, req.NamespacedName, &project); err != nil {
		if apierrors.IsNotFound(err) {
			deleteProjectMetrics(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	recordInfo(&project)
	defer func() {
		res := "success"
		if retErr != nil {
			res = "error"
		}
		reconcileTotal.WithLabelValues(project.Namespace, project.Name, res).Inc()
	}()

	done, result, err := r.handleLifecycle(ctx, &project)
	if err != nil || done {
		return result, err
	}

	rp, result, err := r.prepareReconcile(ctx, &project)
	if err != nil {
		return result, err
	}
	if rp == nil {
		return result, nil
	}

	syncInterval := parseSyncInterval(project.Spec.SyncInterval)
	cacheEnabled := rp.cacheMode != ""
	if project.Spec.AutoApprove {
		result, err = r.reconcileAutoApprove(ctx, &project, &rp.program, rp.appliedHash, rp.cmName, rp.image, syncInterval, cacheEnabled, rp.cachePVCName, rp.saName)
	} else {
		result, err = r.reconcilePlanApprove(ctx, &project, &rp.program, rp.appliedHash, rp.cmName, rp.image, syncInterval, cacheEnabled, rp.cachePVCName, rp.saName)
	}

	// Clamp RequeueAfter to not exceed TTL remaining
	if ttl := parseTTL(project.Spec.TTL); ttl > 0 {
		remaining := time.Until(project.CreationTimestamp.Add(ttl))
		if remaining > 0 && (result.RequeueAfter == 0 || remaining < result.RequeueAfter) {
			result.RequeueAfter = remaining
		}
	}

	return result, err
}

// handleLifecycle handles deletion, finalizer, suspend, and pinned revision.
// Returns (done=true, result, err) if the caller should return immediately.
func (r *TofuProjectReconciler) handleLifecycle(ctx context.Context, project *tofuv1alpha1.TofuProject) (bool, ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	// Handle deletion — run tofu destroy before allowing CR removal
	if !project.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(project, finalizerName) {
			result, err := r.reconcileDestroy(ctx, project)
			return true, result, err
		}
		return true, ctrl.Result{}, nil
	}

	// Ensure finalizer is present on active resources
	if !controllerutil.ContainsFinalizer(project, finalizerName) {
		controllerutil.AddFinalizer(project, finalizerName)
		if err := r.Update(ctx, project); err != nil {
			return true, ctrl.Result{}, err
		}
	}

	// TTL — auto-delete after configured duration
	if ttl := parseTTL(project.Spec.TTL); ttl > 0 {
		expiresAt := project.CreationTimestamp.Add(ttl)
		metaExpiry := metav1.NewTime(expiresAt)
		if project.Status.ExpiresAt == nil || !project.Status.ExpiresAt.Equal(&metaExpiry) {
			project.Status.ExpiresAt = &metaExpiry
			r.updateStatusWithCondition(ctx, project)
		}
		if time.Now().After(expiresAt) {
			log.Info("TTL expired, deleting TofuProject", "name", project.Name, "ttl", project.Spec.TTL)
			if err := r.Delete(ctx, project); err != nil {
				return true, ctrl.Result{}, err
			}
			return true, ctrl.Result{}, nil
		}
	} else if project.Status.ExpiresAt != nil {
		project.Status.ExpiresAt = nil
		r.updateStatusWithCondition(ctx, project)
	}

	// Force-unlock check — handle the force-unlock annotation
	if ann := project.GetAnnotations(); ann != nil && ann[forceUnlockAnnotation] == "true" {
		result, err := r.handleForceUnlock(ctx, project)
		return true, result, err
	}

	// Suspend check — pause reconciliation entirely
	if project.Spec.Suspend {
		if project.Status.Phase != "Suspended" {
			project.Status.Phase = "Suspended"
			project.Status.Message = "Reconciliation is suspended"
			r.updateStatusWithCondition(ctx, project)
		}
		return true, ctrl.Result{}, nil
	}
	// If previously suspended and now resumed, clear the message and fall through
	if project.Status.Phase == "Suspended" {
		project.Status.Phase = ""
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
	}

	// Pinned revision — use stored snapshot instead of computing from current spec
	if project.Spec.PinnedRevision > 0 {
		result, err := r.reconcilePinned(ctx, project)
		return true, result, err
	}

	return false, ctrl.Result{}, nil
}

// handleForceUnlock manages the force-unlock annotation lifecycle.
// It deletes active jobs, creates/monitors a force-unlock job, and cleans up on completion.
func (r *TofuProjectReconciler) handleForceUnlock(ctx context.Context, project *tofuv1alpha1.TofuProject) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("handling force-unlock for TofuProject", "name", project.Name)

	// Delete any active jobs for this project (they'd fail anyway with a lock)
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
		if j.Labels["tofu.example.com/job-type"] == "force-unlock" {
			continue
		}
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			log.Info("deleting active job before force-unlock", "job", j.Name)
			_ = r.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground))
		}
	}

	// Resolve ConfigMap and image for the force-unlock job
	cmName := fmt.Sprintf("%s-tf", project.Name)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: project.Namespace}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ConfigMap not found for force-unlock, clearing lock state")
			r.clearForceUnlockAnnotation(ctx, project)
			project.Status.StateLocked = false
			project.Status.Phase = ""
			project.Status.Message = ""
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}
	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		saName = project.Spec.ServiceAccount.Name
	}

	jobName := fmt.Sprintf("%s-force-unlock", project.Name)
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, &job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Create the force-unlock job
		newJob := buildForceUnlockJob(project, jobName, cmName, image, saName)
		addEnvToJob(newJob, project)
		if err := controllerutil.SetControllerReference(project, newJob, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, newJob); err != nil {
			return ctrl.Result{}, err
		}
		project.Status.Phase = "ForceUnlocking"
		project.Status.LastJobName = jobName
		project.Status.Message = "Running force-unlock"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Check force-unlock job status
	if job.Status.Succeeded > 0 {
		log.Info("force-unlock succeeded", "name", project.Name)
		_ = r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground))
		r.clearForceUnlockAnnotation(ctx, project)
		project.Status.StateLocked = false
		project.Status.Phase = ""
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{Requeue: true}, nil
	}
	if job.Status.Failed > 0 {
		log.Info("force-unlock failed", "name", project.Name)
		r.clearForceUnlockAnnotation(ctx, project)
		project.Status.Phase = "Error"
		project.Status.Message = "Force-unlock job failed"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	// Still running
	project.Status.Phase = "ForceUnlocking"
	project.Status.LastJobName = jobName
	r.updateStatusWithCondition(ctx, project)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// clearForceUnlockAnnotation removes the force-unlock annotation from the project.
func (r *TofuProjectReconciler) clearForceUnlockAnnotation(ctx context.Context, project *tofuv1alpha1.TofuProject) {
	ann := project.GetAnnotations()
	if ann != nil {
		delete(ann, forceUnlockAnnotation)
		project.SetAnnotations(ann)
		_ = r.Update(ctx, project)
	}
}

// prepareReconcile fetches the program, validates, resolves deps, computes hash,
// and ensures infrastructure (ConfigMap, SA, cache, job lock).
// Returns nil reconcileParams if the caller should return result early (deps not ready, job locked).
func (r *TofuProjectReconciler) prepareReconcile(ctx context.Context, project *tofuv1alpha1.TofuProject) (*reconcileParams, ctrl.Result, error) {
	progNs := project.Spec.ProgramRef.Namespace
	if progNs == "" {
		progNs = project.Namespace
	}
	var program tofuv1alpha1.TofuProgram
	if err := r.Get(ctx, types.NamespacedName{Name: project.Spec.ProgramRef.Name, Namespace: progNs}, &program); err != nil {
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("failed to get TofuProgram %s/%s: %v", progNs, project.Spec.ProgramRef.Name, err)
		r.updateStatusWithCondition(ctx, project)
		return nil, ctrl.Result{}, err
	}

	gitMode := isGitSource(&program)
	if err := r.validateProjectAndProgram(project, &program, gitMode); err != nil {
		r.updateStatusWithCondition(ctx, project)
		return nil, ctrl.Result{}, err
	}

	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}

	effectiveParams, depHashStr, depResult, depErr := r.resolveDependencies(ctx, project)
	if depErr != nil {
		return nil, depResult, depErr
	}
	if effectiveParams == nil {
		return nil, depResult, nil
	}

	backendTf := renderBackendTF(*project)

	varsJSON, err := json.MarshalIndent(effectiveParams, "", "  ")
	if err != nil {
		return nil, ctrl.Result{}, err
	}
	varsFile := string(varsJSON) + "\n"

	appliedHash := computeAppliedHash(project, &program, effectiveParams, depHashStr, backendTf, varsFile, gitMode)

	cmName, err := r.ensureTFConfigMap(ctx, project, &program, backendTf, varsFile, gitMode)
	if err != nil {
		return nil, ctrl.Result{}, err
	}

	saName, err := r.ensureServiceAccountAndRBAC(ctx, project)
	if err != nil {
		return nil, ctrl.Result{}, err
	}

	cacheMode := r.cacheMode(project)
	var cachePVCName string
	if cacheMode != "" {
		pvcName, err := r.ensureCachePVC(ctx, project, cacheMode)
		if err != nil {
			return nil, ctrl.Result{}, err
		}
		cachePVCName = pvcName
	}

	locked, lockResult, lockErr := r.checkJobLock(ctx, project, cacheMode)
	if lockErr != nil {
		return nil, lockResult, lockErr
	}
	if locked {
		return nil, lockResult, nil
	}

	return &reconcileParams{
		program:      program,
		appliedHash:  appliedHash,
		cmName:       cmName,
		saName:       saName,
		image:        image,
		cacheMode:    cacheMode,
		cachePVCName: cachePVCName,
	}, ctrl.Result{}, nil
}

// validateProjectAndProgram validates S3 backend, validation steps, and mutually exclusive fields.
// On error it sets project.Status.Phase="Error" and a message; the caller must call updateStatusWithCondition.
func (r *TofuProjectReconciler) validateProjectAndProgram(project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, gitMode bool) error {
	progNs := project.Spec.ProgramRef.Namespace
	if progNs == "" {
		progNs = project.Namespace
	}

	if project.Spec.Backend.Type == "s3" {
		if project.Spec.Backend.S3 == nil || project.Spec.Backend.S3.Bucket == "" || project.Spec.Backend.S3.Region == "" {
			project.Status.Phase = "Error"
			project.Status.Message = "S3 backend requires s3.bucket and s3.region to be set"
			return fmt.Errorf("S3 backend missing required fields for TofuProject %s/%s", project.Namespace, project.Name)
		}
	}

	if project.Spec.Validation != nil {
		if err := validateValidationSteps(project); err != nil {
			return err
		}
	}

	if gitMode && program.Spec.ProgramHCL != "" {
		project.Status.Phase = "Error"
		project.Status.Message = "TofuProgram must set either programHCL or source, not both"
		return fmt.Errorf("TofuProgram %s/%s has both programHCL and source set", progNs, program.Name)
	}
	if !gitMode && program.Spec.ProgramHCL == "" {
		project.Status.Phase = "Error"
		project.Status.Message = "TofuProgram must set either programHCL or source"
		return fmt.Errorf("TofuProgram %s/%s has neither programHCL nor source set", progNs, program.Name)
	}

	return nil
}

// validateValidationSteps checks that each validation step has exactly one of standard/custom set.
func validateValidationSteps(project *tofuv1alpha1.TofuProject) error {
	for _, step := range project.Spec.Validation.Steps {
		if step.Standard != "" && step.Custom != nil {
			project.Status.Phase = "Error"
			project.Status.Message = fmt.Sprintf("validation step %q must set either standard or custom, not both", step.Name)
			return fmt.Errorf("validation step %q has both standard and custom set", step.Name)
		}
		if step.Standard == "" && step.Custom == nil {
			project.Status.Phase = "Error"
			project.Status.Message = fmt.Sprintf("validation step %q must set either standard or custom", step.Name)
			return fmt.Errorf("validation step %q has neither standard nor custom set", step.Name)
		}
		if step.Standard != "" {
			if _, ok := standardValidators[step.Standard]; !ok {
				project.Status.Phase = "Error"
				project.Status.Message = fmt.Sprintf("unknown standard validator %q in step %q", step.Standard, step.Name)
				return fmt.Errorf("unknown standard validator %q", step.Standard)
			}
		}
	}
	return nil
}

// computeAppliedHash builds a SHA256 hash of all inputs that affect the desired state.
func computeAppliedHash(project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, effectiveParams map[string]string, depHashStr, backendTf, varsFile string, gitMode bool) string {
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
	return hex.EncodeToString(hash[:])
}

// ensureTFConfigMap creates or updates the TF ConfigMap and returns its name.
func (r *TofuProjectReconciler) ensureTFConfigMap(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, backendTf, varsFile string, gitMode bool) (string, error) {
	cmName := fmt.Sprintf("%s-tf", project.Name)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: project.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
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
		return controllerutil.SetControllerReference(project, cm, r.Scheme)
	})
	return cmName, err
}

// ensureServiceAccountAndRBAC ensures the SA and RoleBinding exist. Returns the SA name.
func (r *TofuProjectReconciler) ensureServiceAccountAndRBAC(ctx context.Context, project *tofuv1alpha1.TofuProject) (string, error) {
	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		return project.Spec.ServiceAccount.Name, nil
	}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: project.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
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
		return "", err
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
	return saName, err
}

// checkJobLock checks whether an active job prevents creating a new one.
// Returns (locked, result, error). If locked is true, the caller should return result.
func (r *TofuProjectReconciler) checkJobLock(ctx context.Context, project *tofuv1alpha1.TofuProject, cacheMode string) (bool, ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if cacheMode == "shared" {
		active, err := r.hasActiveNamespaceJobs(ctx, project.Namespace)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if active {
			log.Info("shared cache mode: waiting for namespace-wide job to complete")
			if project.Status.Phase != "Queued" {
				project.Status.Phase = "Queued"
				project.Status.Message = "Waiting for other jobs in namespace (shared cache)"
				r.updateStatusWithCondition(ctx, project)
			}
			return true, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return false, ctrl.Result{}, nil
	}

	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(project.Namespace), client.MatchingLabelsSelector{
		Selector: labels.SelectorFromSet(map[string]string{
			"tofu.example.com/project": project.Name,
		}),
	}); err != nil {
		return false, ctrl.Result{}, err
	}
	for i := range jobList.Items {
		j := &jobList.Items[i]
		// Skip drift detection jobs — they run independently
		if j.Labels["tofu.example.com/job-type"] == "drift" {
			continue
		}
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			log.Info("waiting for active Job to complete before creating a new one", "job", j.Name)
			project.Status.Phase = "Running"
			project.Status.LastJobName = j.Name
			r.updateStatusWithCondition(ctx, project)
			return true, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}
	return false, ctrl.Result{}, nil
}

// configureAndCreateJob builds a job, applies cache/env/volumes/resources/validation, sets owner ref, and creates it.
func (r *TofuProjectReconciler) configureAndCreateJob(ctx context.Context, job *batchv1.Job, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, image string, cacheEnabled bool, cachePVCName string) error {
	setJobTimeout(job, project)
	if cacheEnabled {
		addCacheToJob(job, cachePVCName)
	}
	addEnvToJob(job, project)
	addExtraVolumesToJob(job, project)
	if err := addResourcesToJob(job, project); err != nil {
		return err
	}
	gitMode := isGitSource(program)
	var source *tofuv1alpha1.GitSource
	if gitMode {
		source = program.Spec.Source
	}
	if err := addValidationToJob(job, project, image, gitMode, source); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(project, job, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, job)
}

// reconcileAutoApprove handles the existing auto-approve flow unchanged.
func (r *TofuProjectReconciler) reconcileAutoApprove(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	// Skip if this hash was already successfully applied
	if project.Status.LastAppliedHash == appliedHash {
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
		newJob := buildJob(*project, jobName, cmName, image, program, saName)
		if err := r.configureAndCreateJob(ctx, newJob, project, program, image, cacheEnabled, cachePVCName); err != nil {
			return ctrl.Result{}, err
		}
		project.Status.Phase = "Running"
		project.Status.LastJobName = jobName
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return r.handleApplyJobResult(ctx, project, job, appliedHash, jobName)
}

// handleApplyJobResult processes the result of a completed apply job.
func (r *TofuProjectReconciler) handleApplyJobResult(ctx context.Context, project *tofuv1alpha1.TofuProject, job *batchv1.Job, appliedHash, jobName string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if job.Status.Succeeded > 0 {
		applyTotal.WithLabelValues(project.Namespace, project.Name, "succeeded").Inc()
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
		project.Status.StateLocked = false
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			now := metav1.Now()
			project.Status.LastDriftCheckTime = &now
		}
		if outputs != nil {
			project.Status.Outputs = outputs
		}
		r.createRevisionFromCM(ctx, project, appliedHash, jobName, "succeeded", jobLogs)
		r.updateStatusWithCondition(ctx, project)
		sendNotification(ctx, project, "apply:success")
		return ctrl.Result{}, nil
	}
	if job.Status.Failed > 0 {
		if project.Spec.RetryPolicy != nil && project.Status.RetryCount < project.Spec.RetryPolicy.MaxRetries {
			project.Status.RetryCount++
			delay := parseRetryDelay(project.Spec.RetryPolicy.Delay)
			project.Status.Phase = "Retrying"
			project.Status.Message = fmt.Sprintf("Retry %d/%d after failure", project.Status.RetryCount, project.Spec.RetryPolicy.MaxRetries)
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		applyTotal.WithLabelValues(project.Namespace, project.Name, "failed").Inc()
		failLogs, logsErr := r.readJobLogs(ctx, job)
		if logsErr != nil {
			log.Error(logsErr, "failed to read failed job logs (non-fatal)")
		}
		if failLogs != "" && isStateLockError(failLogs) {
			project.Status.StateLocked = true
			project.Status.Phase = "Locked"
			project.Status.LastJobName = jobName
			project.Status.Message = "State is locked — use 'kubectl tofu force-unlock' to recover"
			r.createRevisionFromCM(ctx, project, appliedHash, jobName, "failed", failLogs)
			r.updateStatusWithCondition(ctx, project)
			sendNotification(ctx, project, "apply:error")
			return ctrl.Result{}, nil
		}
		project.Status.Phase = "Error"
		project.Status.LastJobName = jobName
		project.Status.Message = "Job failed"
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
	// Skip if this hash was already successfully applied
	if project.Status.LastAppliedHash == appliedHash {
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			return r.reconcileDriftDetection(ctx, project, program, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
		}
		return requeueAfter(syncInterval), nil
	}

	if err := r.invalidateStalePlan(ctx, project, appliedHash); err != nil {
		return ctrl.Result{}, err
	}

	// Check if approved
	approvedHash := ""
	if ann := project.GetAnnotations(); ann != nil {
		approvedHash = ann[approvedHashAnnotation]
	}
	if approvedHash == appliedHash {
		return r.handleApprovedPlan(ctx, project, program, appliedHash, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
	}

	if project.Status.Phase == "WaitingApproval" && project.Status.PendingPlanHash == appliedHash {
		return ctrl.Result{}, nil
	}

	planJobName := fmt.Sprintf("%s-plan-%s", project.Name, appliedHash[:8])
	planJob := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: planJobName, Namespace: project.Namespace}, planJob); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.ensurePlanJob(ctx, project, program, appliedHash, cmName, image, planJobName, cacheEnabled, cachePVCName, saName)
	}

	return r.handlePlanJobStatus(ctx, project, planJob, appliedHash)
}

// invalidateStalePlan clears plan state when the spec hash has changed since the last plan.
func (r *TofuProjectReconciler) invalidateStalePlan(ctx context.Context, project *tofuv1alpha1.TofuProject, appliedHash string) error {
	if project.Status.PendingPlanHash == "" || project.Status.PendingPlanHash == appliedHash {
		return nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.Info("spec changed, invalidating stale plan", "oldHash", project.Status.PendingPlanHash, "newHash", appliedHash)
	project.Status.PendingPlanHash = ""
	project.Status.PlanOutput = ""
	project.Status.PlanSummary = ""
	project.Status.BlastRadius = nil
	project.Status.LastPlanJobName = ""
	if ann := project.GetAnnotations(); ann != nil {
		if ann[approvedHashAnnotation] != appliedHash {
			delete(ann, approvedHashAnnotation)
			project.SetAnnotations(ann)
			if err := r.Update(ctx, project); err != nil {
				return err
			}
		}
	}
	r.updateStatusWithCondition(ctx, project)
	return nil
}

// handleApprovedPlan handles the approved state: checks apply schedule and creates the apply job.
func (r *TofuProjectReconciler) handleApprovedPlan(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image string, syncInterval time.Duration, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	if project.Spec.ApplySchedule != nil {
		inWindow, nextWindow := isWithinApplyWindow(project.Spec.ApplySchedule, time.Now())
		if !inWindow {
			project.Status.Phase = "ScheduledApply"
			project.Status.Message = fmt.Sprintf("Plan approved, waiting for apply window (next: %s)", nextWindow.UTC().Format(time.RFC3339))
			r.updateStatusWithCondition(ctx, project)
			wait := time.Until(nextWindow)
			if wait <= 0 {
				wait = 30 * time.Second
			}
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	return r.createApplyAfterApproval(ctx, project, program, appliedHash, cmName, image, syncInterval, cacheEnabled, cachePVCName, saName)
}

// ensurePlanJob creates a plan job for the given hash.
func (r *TofuProjectReconciler) ensurePlanJob(ctx context.Context, project *tofuv1alpha1.TofuProject, program *tofuv1alpha1.TofuProgram, appliedHash, cmName, image, planJobName string, cacheEnabled bool, cachePVCName string, saName string) (ctrl.Result, error) {
	newJob := buildPlanJob(*project, planJobName, cmName, image, program, saName)
	if err := r.configureAndCreateJob(ctx, newJob, project, program, image, cacheEnabled, cachePVCName); err != nil {
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
		return r.handlePlanSuccess(ctx, project, planJob, appliedHash)
	}
	if planJob.Status.Failed > 0 {
		planTotal.WithLabelValues(project.Namespace, project.Name, "failed").Inc()
		output, err := r.readJobLogs(ctx, planJob)
		if err != nil {
			output = fmt.Sprintf("(failed to read plan logs: %v)", err)
		}
		if len(output) > maxPlanOutputBytes {
			output = output[len(output)-maxPlanOutputBytes:]
		}
		if isStateLockError(output) {
			project.Status.StateLocked = true
			project.Status.Phase = "Locked"
			project.Status.LastPlanJobName = planJob.Name
			project.Status.PlanOutput = output
			project.Status.Message = "State is locked — use 'kubectl tofu force-unlock' to recover"
			r.updateStatusWithCondition(ctx, project)
			return ctrl.Result{}, nil
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

// handlePlanSuccess processes a succeeded plan job: reads logs, checks auto-approve threshold.
func (r *TofuProjectReconciler) handlePlanSuccess(ctx context.Context, project *tofuv1alpha1.TofuProject, planJob *batchv1.Job, appliedHash string) (ctrl.Result, error) {
	output, err := r.readJobLogs(ctx, planJob)
	if err != nil {
		output = fmt.Sprintf("(failed to read plan logs: %v)", err)
	}
	if len(output) > maxPlanOutputBytes {
		output = output[len(output)-maxPlanOutputBytes:]
	}

	planSummary := extractPlanSummary(output)
	blastRadius := parsePlanCounts(planSummary)

	planTotal.WithLabelValues(project.Namespace, project.Name, "succeeded").Inc()

	project.Status.PendingPlanHash = appliedHash
	project.Status.PlanOutput = output
	project.Status.PlanSummary = planSummary
	project.Status.BlastRadius = blastRadius
	project.Status.LastPlanJobName = planJob.Name
	recordBlastRadius(project)

	if autoApproved, result, err := r.checkAutoApproveThreshold(ctx, project, blastRadius, appliedHash); autoApproved {
		return result, err
	}

	project.Status.Phase = "WaitingApproval"
	project.Status.Message = "Plan complete. Approve to apply."
	r.updateStatusWithCondition(ctx, project)
	sendNotification(ctx, project, "plan:complete")
	return ctrl.Result{}, nil
}

// checkAutoApproveThreshold auto-approves a plan if blast radius is within threshold.
// Returns (autoApproved=true, result, err) if the plan was auto-approved.
func (r *TofuProjectReconciler) checkAutoApproveThreshold(ctx context.Context, project *tofuv1alpha1.TofuProject, blastRadius *tofuv1alpha1.BlastRadiusSummary, appliedHash string) (bool, ctrl.Result, error) {
	if project.Spec.AutoApprove || project.Spec.AutoApproveMaxBlastRadius == nil || blastRadius == nil {
		return false, ctrl.Result{}, nil
	}
	threshold := *project.Spec.AutoApproveMaxBlastRadius
	if blastRadius.Total > threshold {
		return false, ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.Info("auto-approving plan within blast radius threshold", "total", blastRadius.Total, "threshold", threshold)
	ann := project.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[approvedHashAnnotation] = appliedHash
	project.SetAnnotations(ann)
	if err := r.Update(ctx, project); err != nil {
		return true, ctrl.Result{}, err
	}

	if project.Spec.ApplySchedule != nil {
		inWindow, nextWindow := isWithinApplyWindow(project.Spec.ApplySchedule, time.Now())
		if !inWindow {
			project.Status.Phase = "ScheduledApply"
			project.Status.Message = fmt.Sprintf("Plan auto-approved (blast radius %d <= threshold %d), waiting for apply window (next: %s)",
				blastRadius.Total, threshold, nextWindow.UTC().Format(time.RFC3339))
			r.updateStatusWithCondition(ctx, project)
			sendNotification(ctx, project, "plan:auto-approved")
			wait := time.Until(nextWindow)
			if wait <= 0 {
				wait = 30 * time.Second
			}
			return true, ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	project.Status.Phase = "WaitingApproval"
	project.Status.Message = fmt.Sprintf("Plan auto-approved (blast radius %d <= threshold %d)", blastRadius.Total, threshold)
	r.updateStatusWithCondition(ctx, project)
	sendNotification(ctx, project, "plan:auto-approved")
	return true, ctrl.Result{Requeue: true}, nil
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
		if err := r.configureAndCreateJob(ctx, newJob, project, program, image, cacheEnabled, cachePVCName); err != nil {
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
		applyTotal.WithLabelValues(project.Namespace, project.Name, "succeeded").Inc()
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
		project.Status.StateLocked = false
		// Set drift check time so drift detection doesn't fire immediately after apply
		if project.Spec.DriftDetection != nil && project.Spec.DriftDetection.Enabled {
			now := metav1.Now()
			project.Status.LastDriftCheckTime = &now
		}
		// Clear plan fields after successful apply
		project.Status.PendingPlanHash = ""
		project.Status.PlanOutput = ""
		project.Status.PlanSummary = ""
		project.Status.BlastRadius = nil
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
		applyTotal.WithLabelValues(project.Namespace, project.Name, "failed").Inc()
		// Read job logs for failure audit
		failLogs, logsErr := r.readJobLogs(ctx, job)
		if logsErr != nil {
			log.Error(logsErr, "failed to read failed job logs (non-fatal)")
		}
		if failLogs != "" && isStateLockError(failLogs) {
			project.Status.StateLocked = true
			project.Status.Phase = "Locked"
			project.Status.LastJobName = jobName
			project.Status.Message = "State is locked — use 'kubectl tofu force-unlock' to recover"
			r.createRevisionFromCM(ctx, project, appliedHash, jobName, "failed", failLogs)
			r.updateStatusWithCondition(ctx, project)
			sendNotification(ctx, project, "apply:error")
			return ctrl.Result{}, nil
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

	if blocked, result := r.checkDeleteProtection(ctx, project); blocked {
		return result, nil
	}

	cmName, image, programPtr, saName, result, err := r.resolveDestroyContext(ctx, project)
	if err != nil {
		return result, err
	}
	if cmName == "" {
		// ConfigMap not found, finalizer removed
		return result, nil
	}

	jobName := fmt.Sprintf("%s-destroy", project.Name)
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: project.Namespace}, &job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err := r.buildAndCreateDestroyJob(ctx, project, cmName, image, programPtr, saName); err != nil {
			return ctrl.Result{}, err
		}
		project.Status.Phase = "Destroying"
		project.Status.LastJobName = jobName
		project.Status.Message = ""
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if job.Status.Succeeded > 0 {
		applyTotal.WithLabelValues(project.Namespace, project.Name, "succeeded").Inc()
		log.Info("destroy succeeded, removing finalizer", "name", project.Name)
		deleteProjectMetrics(project.Namespace, project.Name)
		controllerutil.RemoveFinalizer(project, finalizerName)
		return ctrl.Result{}, r.Update(ctx, project)
	}
	if job.Status.Failed > 0 {
		applyTotal.WithLabelValues(project.Namespace, project.Name, "failed").Inc()
		project.Status.Phase = "DestroyFailed"
		project.Status.Message = "Destroy job failed"
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// checkDeleteProtection checks if delete protection blocks destruction.
// Returns (blocked=true, result) if the caller should return early.
func (r *TofuProjectReconciler) checkDeleteProtection(ctx context.Context, project *tofuv1alpha1.TofuProject) (bool, ctrl.Result) {
	if !project.Spec.DeleteProtection {
		return false, ctrl.Result{}
	}
	approved := false
	if ann := project.GetAnnotations(); ann != nil {
		approved = ann[approvedDeleteAnnotation] == "true"
	}
	if approved {
		return false, ctrl.Result{}
	}
	if project.Status.Phase != "WaitingDeleteApproval" {
		project.Status.Phase = "WaitingDeleteApproval"
		project.Status.Message = "Delete protection enabled. Run 'kubectl tofu delete <name>' to approve."
		r.updateStatusWithCondition(ctx, project)
	}
	return true, ctrl.Result{}
}

// resolveDestroyContext resolves ConfigMap, program, image, and SA name for destruction.
// Returns cmName="" if the ConfigMap was not found and the finalizer was removed.
func (r *TofuProjectReconciler) resolveDestroyContext(ctx context.Context, project *tofuv1alpha1.TofuProject) (string, string, *tofuv1alpha1.TofuProgram, string, ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	cmName := fmt.Sprintf("%s-tf", project.Name)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: project.Namespace}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ConfigMap not found, cannot run destroy — removing finalizer", "configmap", cmName)
			controllerutil.RemoveFinalizer(project, finalizerName)
			return "", "", nil, "", ctrl.Result{}, r.Update(ctx, project)
		}
		return "", "", nil, "", ctrl.Result{}, err
	}

	progNs := project.Spec.ProgramRef.Namespace
	if progNs == "" {
		progNs = project.Namespace
	}
	var program tofuv1alpha1.TofuProgram
	var programPtr *tofuv1alpha1.TofuProgram
	if err := r.Get(ctx, types.NamespacedName{Name: project.Spec.ProgramRef.Name, Namespace: progNs}, &program); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", "", nil, "", ctrl.Result{}, err
		}
	} else {
		programPtr = &program
	}

	image := project.Spec.Image
	if image == "" {
		image = "ghcr.io/opentofu/opentofu:latest"
	}

	saName := "tofu-runner"
	if project.Spec.ServiceAccount != nil && project.Spec.ServiceAccount.Name != "" {
		saName = project.Spec.ServiceAccount.Name
	}

	return cmName, image, programPtr, saName, ctrl.Result{}, nil
}

// buildAndCreateDestroyJob creates the destroy job with cache, env, volumes, resources, and validation.
func (r *TofuProjectReconciler) buildAndCreateDestroyJob(ctx context.Context, project *tofuv1alpha1.TofuProject, cmName, image string, programPtr *tofuv1alpha1.TofuProgram, saName string) error {
	jobName := fmt.Sprintf("%s-destroy", project.Name)
	newJob := buildDestroyJob(project, jobName, cmName, image, programPtr, saName)
	setJobTimeout(newJob, project)
	cacheMode := r.cacheMode(project)
	if cacheMode != "" {
		pvcName, err := r.ensureCachePVC(ctx, project, cacheMode)
		if err != nil {
			return err
		}
		addCacheToJob(newJob, pvcName)
	}
	addEnvToJob(newJob, project)
	addExtraVolumesToJob(newJob, project)
	if err := addResourcesToJob(newJob, project); err != nil {
		return err
	}
	destroyGitMode := programPtr != nil && isGitSource(programPtr)
	var destroySource *tofuv1alpha1.GitSource
	if destroyGitMode {
		destroySource = programPtr.Spec.Source
	}
	if err := addValidationToJob(newJob, project, image, destroyGitMode, destroySource); err != nil {
		return err
	}
	return r.Create(ctx, newJob)
}
