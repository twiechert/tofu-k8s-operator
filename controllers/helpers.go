package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

// --- Status management ---

// updateStatusWithCondition updates the status subresource and automatically sets the Ready condition.
func (r *TofuProjectReconciler) updateStatusWithCondition(ctx context.Context, project *tofuv1alpha1.TofuProject) {
	updateReadyCondition(project)
	recordPhase(project)
	_ = r.Status().Update(ctx, project)
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
	case "Error", "DestroyFailed", "Locked":
		status = metav1.ConditionFalse
		reason = "Error"
		message = project.Status.Message
	default:
		// Running, Planning, WaitingApproval, WaitingDeleteApproval, Suspended, ScheduledApply, etc.
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

// applyImmediately returns the effective value of spec.applyImmediately (default true).
func applyImmediately(project *tofuv1alpha1.TofuProject) bool {
	if project.Spec.ApplyImmediately == nil {
		return true
	}
	return *project.Spec.ApplyImmediately
}

// --- Log/output parsing ---

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

var planCountsRe = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)

// parsePlanCounts parses the plan summary line into structured blast radius counts.
// Returns {0,0,0,0} for "No changes." summaries, nil if the summary can't be parsed.
func parsePlanCounts(summary string) *tofuv1alpha1.BlastRadiusSummary {
	if summary == "" {
		return nil
	}
	if summary == "No changes." {
		return &tofuv1alpha1.BlastRadiusSummary{}
	}
	m := planCountsRe.FindStringSubmatch(summary)
	if m == nil {
		return nil
	}
	add, _ := strconv.Atoi(m[1])
	change, _ := strconv.Atoi(m[2])
	destroy, _ := strconv.Atoi(m[3])
	return &tofuv1alpha1.BlastRadiusSummary{
		Add:     int32(add),
		Change:  int32(change),
		Destroy: int32(destroy),
		Total:   int32(add + change + destroy),
	}
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

// --- Cache management ---

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

// --- Notifications ---

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

// --- Utilities ---

func mergeLabels(existing, add map[string]string) map[string]string {
	if existing == nil {
		existing = map[string]string{}
	}
	for k, v := range add {
		existing[k] = v
	}
	return existing
}

// parseTTL parses a TTL duration string.
// Returns 0 on empty string, parse error, or non-positive duration (meaning no TTL).
func parseTTL(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// parseJobTimeout parses a job timeout duration string.
// Returns 30m on empty string, parse error, or non-positive duration.
func parseJobTimeout(s string) time.Duration {
	if s == "" {
		return 30 * time.Minute
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

// parseIdleTimeout parses an idle timeout duration string.
// Returns 0 on empty string, parse error, or non-positive duration (meaning disabled).
func parseIdleTimeout(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// checkJobIdle checks if the job's tofu container has produced any log output within the idle window.
// Returns true if the job is idle (no recent output).
func (r *TofuProjectReconciler) checkJobIdle(ctx context.Context, job *batchv1.Job, idleTimeout time.Duration) bool {
	// Don't check if job hasn't been running long enough
	if job.Status.StartTime == nil || time.Since(job.Status.StartTime.Time) < idleTimeout {
		return false
	}

	if r.Clientset == nil {
		return false
	}

	pods, err := r.Clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
	})
	if err != nil || len(pods.Items) == 0 {
		return false
	}

	podName := pods.Items[0].Name
	sinceSeconds := int64(idleTimeout.Seconds())
	var limitBytes int64 = 1
	req := r.Clientset.CoreV1().Pods(job.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container:    "tofu",
		SinceSeconds: &sinceSeconds,
		LimitBytes:   &limitBytes,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return false
	}
	defer stream.Close()

	buf := make([]byte, 1)
	n, _ := stream.Read(buf)
	return n == 0 // idle = no recent output
}

// isStateLockError checks if the logs contain an OpenTofu state lock error.
func isStateLockError(logs string) bool {
	return strings.Contains(logs, "Error acquiring the state lock") ||
		strings.Contains(logs, "Error locking state")
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

// isWithinApplyWindow checks whether the given time falls within the apply schedule window.
// Returns (inWindow, nextWindowStart). On parse errors returns (false, zero time).
func isWithinApplyWindow(spec *tofuv1alpha1.ApplyScheduleSpec, now time.Time) (bool, time.Time) {
	if spec == nil {
		return false, time.Time{}
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(spec.Schedule)
	if err != nil {
		return false, time.Time{}
	}

	windowDur := time.Hour // default 1h
	if spec.Window != "" {
		d, err := time.ParseDuration(spec.Window)
		if err == nil && d > 0 {
			windowDur = d
		}
	}

	// Find the most recent fire time before now by stepping back from now.
	// The cron library only provides Next(), so we search backwards by computing
	// Next() from progressively earlier times.
	// Start by checking a range of 2x the window + 25h (covers daily crons).
	searchStart := now.Add(-(windowDur + 25*time.Hour))
	candidate := sched.Next(searchStart)

	// Walk forward to find the last fire time <= now
	var lastFire time.Time
	for !candidate.After(now) {
		lastFire = candidate
		candidate = sched.Next(candidate)
	}
	nextWindowStart := candidate

	if !lastFire.IsZero() && now.Before(lastFire.Add(windowDur)) {
		return true, nextWindowStart
	}

	return false, nextWindowStart
}
