package v1alpha1

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ObjectRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type KubernetesBackendSpec struct {
	// secretSuffix is appended to the secret name used by the kubernetes backend.
	SecretSuffix string `json:"secretSuffix,omitempty"`
	// namespace where the backend secret lives (defaults to the project's namespace)
	Namespace string `json:"namespace,omitempty"`
}

// BackendSpec configures the OpenTofu state backend.
// Type selects the backend: "kubernetes" (default) or "s3".
type BackendSpec struct {
	// type: "kubernetes" (default) or "s3"
	Type string `json:"type,omitempty"`

	// Inline kubernetes fields for backward compatibility
	KubernetesBackendSpec `json:",inline"`

	// s3 backend config (required when type is "s3")
	S3 *S3BackendSpec `json:"s3,omitempty"`
}

type S3BackendSpec struct {
	// bucket name (required)
	Bucket string `json:"bucket"`
	// region (required)
	Region string `json:"region"`
	// key override (default: <namespace>/<project-name>/terraform.tfstate)
	Key string `json:"key,omitempty"`
}

type ProjectDependency struct {
	ProjectRef ObjectRef         `json:"projectRef"`
	Outputs    map[string]string `json:"outputs"` // upstream output name → downstream param name
}

type ParamFromSource struct {
	ConfigMapRef *ObjectRef `json:"configMapRef,omitempty"`
	SecretRef    *ObjectRef `json:"secretRef,omitempty"`
}

type ValuesFromSource struct {
	// configMapRef is the name of a ConfigMap to import all keys from.
	ConfigMapRef string `json:"configMapRef,omitempty"`
	// secretRef is the name of a Secret to import all keys from.
	SecretRef string `json:"secretRef,omitempty"`
}

type ParamBinding struct {
	Name            string  `json:"name"`
	ConfigMapKeyRef *KeyRef `json:"configMapKeyRef,omitempty"`
	SecretKeyRef    *KeyRef `json:"secretKeyRef,omitempty"`
}

type KeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type CacheSpec struct {
	// mode: "shared" (namespace PVC, jobs serialized) or "dedicated" (per-project PVC)
	Mode         string `json:"mode,omitempty"`
	Size         string `json:"size,omitempty"` // default "1Gi"
	StorageClass string `json:"storageClass,omitempty"`
}

type ServiceAccountSpec struct {
	// name: use an existing ServiceAccount instead of auto-creating "tofu-runner"
	Name string `json:"name,omitempty"`
	// annotations added to the auto-created ServiceAccount (e.g. for IRSA/workload identity)
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

type RetryPolicy struct {
	MaxRetries int32  `json:"maxRetries"`
	Delay      string `json:"delay,omitempty"` // e.g. "30s", "1m"
}

type DriftDetectionSpec struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval,omitempty"` // e.g. "10m", "1h". Default: "15m"
}

type NotificationSpec struct {
	Webhooks []WebhookNotification `json:"webhooks,omitempty"`
}

type WebhookNotification struct {
	URL    string   `json:"url"`
	Events []string `json:"events"` // "apply:success", "apply:error", "drift:detected", "plan:complete"
}

type ValidationSpec struct {
	TofuValidate *bool            `json:"tofuValidate,omitempty"`
	Steps        []ValidationStep `json:"steps,omitempty"`
}

type ValidationStep struct {
	Name     string                `json:"name"`
	Standard string                `json:"standard,omitempty"`
	Custom   *CustomValidationStep `json:"custom,omitempty"`
}

type CustomValidationStep struct {
	Command string `json:"command"`
	Image   string `json:"image,omitempty"`
}

type BlastRadiusSummary struct {
	Add     int32 `json:"add"`
	Change  int32 `json:"change"`
	Destroy int32 `json:"destroy"`
	Total   int32 `json:"total"`
}

type ApplyScheduleSpec struct {
	// schedule is a cron expression defining when the apply window opens, e.g. "0 2 * * *"
	Schedule string `json:"schedule"`
	// window is how long the apply window stays open after the cron fires. Default: "1h"
	Window string `json:"window,omitempty"`
}

type TofuProjectSpec struct {
	ProgramRef ObjectRef `json:"programRef"`

	// params are arbitrary key/value variables passed to the program as terraform.tfvars.json
	Params map[string]string `json:"params,omitempty"`

	// paramFrom bulk-imports all keys from ConfigMaps/Secrets as params (lowest precedence)
	ParamFrom []ParamFromSource `json:"paramFrom,omitempty"`

	// paramBindings maps individual ConfigMap/Secret keys to named params (medium precedence)
	ParamBindings []ParamBinding `json:"paramBindings,omitempty"`

	// valuesFrom is an ordered list of ConfigMaps/Secrets to bulk-import as params.
	// Later entries override earlier ones. Lowest precedence (paramFrom, paramBindings, params all override).
	ValuesFrom []ValuesFromSource `json:"valuesFrom,omitempty"`

	// workspace name (optional)
	Workspace string `json:"workspace,omitempty"`

	// container image running `tofu` (default: ghcr.io/opentofu/opentofu:latest)
	Image string `json:"image,omitempty"`

	// serviceAccount configures the ServiceAccount used by tofu Jobs
	ServiceAccount *ServiceAccountSpec `json:"serviceAccount,omitempty"`

	// backend config for state backend (kubernetes or s3)
	Backend BackendSpec `json:"backend,omitempty"`

	// autoApprove passes -auto-approve to apply
	AutoApprove bool `json:"autoApprove,omitempty"`

	// autoApproveMaxBlastRadius auto-approves plans when the total affected resources
	// (add + change + destroy) is at or below this threshold. Only meaningful when autoApprove is false.
	// nil = manual approval always required; 0 = auto-approve only "No changes" plans.
	AutoApproveMaxBlastRadius *int32 `json:"autoApproveMaxBlastRadius,omitempty"`

	// applyImmediately controls whether the resource reports Ready only after apply completes.
	// Default: true — the Ready condition stays False until the apply succeeds or errors,
	// causing tools like ArgoCD to wait for completion.
	// Set to false for async/fire-and-forget: Ready is set True immediately when a job is created.
	ApplyImmediately *bool `json:"applyImmediately,omitempty"`

	// suspend pauses reconciliation entirely when true
	Suspend bool `json:"suspend,omitempty"`

	// deleteProtection blocks deletion until explicitly approved
	DeleteProtection bool `json:"deleteProtection,omitempty"`

	// keepInSync: if true, the controller will run 'tofu apply' instead of 'tofu plan' in the sync loop.
	KeepInSync bool `json:"keepInSync,omitempty"`

	// syncInterval is how often to re-reconcile after a successful sync (e.g. "5m", "1h").
	// Default: no periodic requeue (only reconciles on spec/status changes).
	SyncInterval string `json:"syncInterval,omitempty"`

	// dependencies declares upstream TofuProjects whose outputs are consumed as input params.
	Dependencies []ProjectDependency `json:"dependencies,omitempty"`

	// cache configures provider plugin caching via PVC.
	Cache *CacheSpec `json:"cache,omitempty"`

	// env are extra environment variables injected into tofu Job containers.
	Env []corev1.EnvVar `json:"env,omitempty"`

	// envFrom bulk-imports env variables from ConfigMaps/Secrets.
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// resources configures resource requests/limits for tofu Job containers.
	Resources *ResourceRequirements `json:"resources,omitempty"`

	// retryPolicy configures retry behavior for failed Jobs.
	RetryPolicy *RetryPolicy `json:"retryPolicy,omitempty"`

	// jobTimeout is the maximum duration a Job is allowed to run before being killed.
	// Uses Go duration format (e.g. "30m", "1h"). Default: "30m".
	JobTimeout string `json:"jobTimeout,omitempty"`

	// resourceTimeout kills a running Job if any single resource has been in progress
	// for longer than this duration (detected via "Still creating/destroying..." log lines).
	// Uses Go duration format (e.g. "20m", "1h"). Empty = disabled.
	ResourceTimeout string `json:"resourceTimeout,omitempty"`

	// driftDetection configures periodic drift checking.
	DriftDetection *DriftDetectionSpec `json:"driftDetection,omitempty"`

	// notifications configures webhook notifications for lifecycle events.
	Notifications *NotificationSpec `json:"notifications,omitempty"`

	// validation configures pre-apply validation (tofu validate + optional tool steps).
	Validation *ValidationSpec `json:"validation,omitempty"`

	// ignoreProviders strips all provider blocks from source .tf files.
	IgnoreProviders bool `json:"ignoreProviders,omitempty"`

	// additionalProvidersHCL is raw HCL written as additional-providers.tf for custom provider config.
	AdditionalProvidersHCL string `json:"additionalProvidersHCL,omitempty"`

	// extraVolumes are additional volumes added to tofu Job pods.
	ExtraVolumes []corev1.Volume `json:"extraVolumes,omitempty"`

	// extraVolumeMounts are additional volume mounts added to the tofu container.
	ExtraVolumeMounts []corev1.VolumeMount `json:"extraVolumeMounts,omitempty"`

	// revisionHistoryLimit is the maximum number of revision ConfigMaps to retain. Default: 10. 0 = keep all.
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`

	// pinnedRevision pins the project to a stored revision for rollback. 0 = normal flow.
	PinnedRevision int32 `json:"pinnedRevision,omitempty"`

	// applySchedule gates when approved plans are actually applied.
	// Plans auto-apply only during the configured maintenance window (cron + duration).
	// Only meaningful when autoApprove is false.
	ApplySchedule *ApplyScheduleSpec `json:"applySchedule,omitempty"`

	// ttl specifies how long the project lives after creation before auto-deletion.
	// Uses Go duration format (e.g. "24h", "168h"). Empty = no TTL.
	TTL string `json:"ttl,omitempty"`
}

type TofuProjectStatus struct {
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	LastJobName        string              `json:"lastJobName,omitempty"`
	LastAppliedHash    string              `json:"lastAppliedHash,omitempty"`
	Revision           int32               `json:"revision,omitempty"`
	Phase              string              `json:"phase,omitempty"`
	Message            string              `json:"message,omitempty"`
	SyncStatus         string              `json:"syncStatus,omitempty"` // 'sync' or 'not in sync'
	PendingPlanHash    string              `json:"pendingPlanHash,omitempty"`
	PlanOutput         string              `json:"planOutput,omitempty"`
	PlanSummary        string              `json:"planSummary,omitempty"`
	LastPlanJobName    string              `json:"lastPlanJobName,omitempty"`
	Outputs            map[string]string   `json:"outputs,omitempty"`
	Conditions         []metav1.Condition  `json:"conditions,omitempty"`
	RetryCount         int32               `json:"retryCount,omitempty"`
	LastDriftCheckTime *metav1.Time        `json:"lastDriftCheckTime,omitempty"`
	DriftDetected      bool                `json:"driftDetected,omitempty"`
	BlastRadius        *BlastRadiusSummary `json:"blastRadius,omitempty"`
	// StateLocked is true when the last job failure was due to a state lock error.
	StateLocked bool `json:"stateLocked,omitempty"`
	// ExpiresAt is when this project will be auto-deleted due to TTL.
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type TofuProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TofuProjectSpec   `json:"spec,omitempty"`
	Status TofuProjectStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TofuProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TofuProject `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TofuProject{}, &TofuProjectList{})
}

func (in *TofuProject) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(TofuProject)
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec = in.Spec
	out.Status = in.Status
	deepCopySpecPointers(&in.Spec, &out.Spec)
	deepCopySpecSlices(&in.Spec, &out.Spec)
	deepCopySpecNested(&in.Spec, &out.Spec)
	deepCopyStatus(&in.Status, &out.Status)
	return out
}

func deepCopySpecPointers(in, out *TofuProjectSpec) {
	if in.Backend.S3 != nil {
		s3Copy := *in.Backend.S3
		out.Backend.S3 = &s3Copy
	}
	if in.ApplyImmediately != nil {
		val := *in.ApplyImmediately
		out.ApplyImmediately = &val
	}
	if in.AutoApproveMaxBlastRadius != nil {
		val := *in.AutoApproveMaxBlastRadius
		out.AutoApproveMaxBlastRadius = &val
	}
	if in.ServiceAccount != nil {
		saCopy := *in.ServiceAccount
		if in.ServiceAccount.Annotations != nil {
			saCopy.Annotations = map[string]string{}
			for k, v := range in.ServiceAccount.Annotations {
				saCopy.Annotations[k] = v
			}
		}
		out.ServiceAccount = &saCopy
	}
	if in.Cache != nil {
		cacheCopy := *in.Cache
		out.Cache = &cacheCopy
	}
	if in.RetryPolicy != nil {
		rpCopy := *in.RetryPolicy
		out.RetryPolicy = &rpCopy
	}
	if in.DriftDetection != nil {
		ddCopy := *in.DriftDetection
		out.DriftDetection = &ddCopy
	}
	if in.RevisionHistoryLimit != nil {
		val := *in.RevisionHistoryLimit
		out.RevisionHistoryLimit = &val
	}
	if in.ApplySchedule != nil {
		asCopy := *in.ApplySchedule
		out.ApplySchedule = &asCopy
	}
}

func deepCopySpecSlices(in, out *TofuProjectSpec) {
	if in.Params != nil {
		out.Params = map[string]string{}
		for k, v := range in.Params {
			out.Params[k] = v
		}
	}
	if in.ParamFrom != nil {
		out.ParamFrom = make([]ParamFromSource, len(in.ParamFrom))
		for i, pf := range in.ParamFrom {
			out.ParamFrom[i] = ParamFromSource{}
			if pf.ConfigMapRef != nil {
				ref := *pf.ConfigMapRef
				out.ParamFrom[i].ConfigMapRef = &ref
			}
			if pf.SecretRef != nil {
				ref := *pf.SecretRef
				out.ParamFrom[i].SecretRef = &ref
			}
		}
	}
	if in.ParamBindings != nil {
		out.ParamBindings = make([]ParamBinding, len(in.ParamBindings))
		for i, pb := range in.ParamBindings {
			out.ParamBindings[i] = ParamBinding{Name: pb.Name}
			if pb.ConfigMapKeyRef != nil {
				ref := *pb.ConfigMapKeyRef
				out.ParamBindings[i].ConfigMapKeyRef = &ref
			}
			if pb.SecretKeyRef != nil {
				ref := *pb.SecretKeyRef
				out.ParamBindings[i].SecretKeyRef = &ref
			}
		}
	}
	if in.ValuesFrom != nil {
		out.ValuesFrom = make([]ValuesFromSource, len(in.ValuesFrom))
		copy(out.ValuesFrom, in.ValuesFrom)
	}
	if in.Dependencies != nil {
		out.Dependencies = make([]ProjectDependency, len(in.Dependencies))
		for i, dep := range in.Dependencies {
			out.Dependencies[i] = ProjectDependency{
				ProjectRef: dep.ProjectRef,
			}
			if dep.Outputs != nil {
				out.Dependencies[i].Outputs = map[string]string{}
				for k, v := range dep.Outputs {
					out.Dependencies[i].Outputs[k] = v
				}
			}
		}
	}
	deepCopySpecSlicesViaJSON(in, out)
}

func deepCopySpecSlicesViaJSON(in, out *TofuProjectSpec) {
	if in.Env != nil {
		data, _ := json.Marshal(in.Env)
		var envCopy []corev1.EnvVar
		_ = json.Unmarshal(data, &envCopy)
		out.Env = envCopy
	}
	if in.EnvFrom != nil {
		data, _ := json.Marshal(in.EnvFrom)
		var envFromCopy []corev1.EnvFromSource
		_ = json.Unmarshal(data, &envFromCopy)
		out.EnvFrom = envFromCopy
	}
	if in.ExtraVolumes != nil {
		data, _ := json.Marshal(in.ExtraVolumes)
		var volCopy []corev1.Volume
		_ = json.Unmarshal(data, &volCopy)
		out.ExtraVolumes = volCopy
	}
	if in.ExtraVolumeMounts != nil {
		data, _ := json.Marshal(in.ExtraVolumeMounts)
		var mountCopy []corev1.VolumeMount
		_ = json.Unmarshal(data, &mountCopy)
		out.ExtraVolumeMounts = mountCopy
	}
}

func deepCopySpecNested(in, out *TofuProjectSpec) {
	if in.Resources != nil {
		resCopy := ResourceRequirements{}
		if in.Resources.Limits != nil {
			resCopy.Limits = map[string]string{}
			for k, v := range in.Resources.Limits {
				resCopy.Limits[k] = v
			}
		}
		if in.Resources.Requests != nil {
			resCopy.Requests = map[string]string{}
			for k, v := range in.Resources.Requests {
				resCopy.Requests[k] = v
			}
		}
		out.Resources = &resCopy
	}
	if in.Validation != nil {
		vCopy := ValidationSpec{}
		if in.Validation.TofuValidate != nil {
			val := *in.Validation.TofuValidate
			vCopy.TofuValidate = &val
		}
		if in.Validation.Steps != nil {
			vCopy.Steps = make([]ValidationStep, len(in.Validation.Steps))
			for i, s := range in.Validation.Steps {
				vCopy.Steps[i] = ValidationStep{Name: s.Name, Standard: s.Standard}
				if s.Custom != nil {
					cCopy := *s.Custom
					vCopy.Steps[i].Custom = &cCopy
				}
			}
		}
		out.Validation = &vCopy
	}
	if in.Notifications != nil {
		nCopy := NotificationSpec{}
		if in.Notifications.Webhooks != nil {
			nCopy.Webhooks = make([]WebhookNotification, len(in.Notifications.Webhooks))
			for i, wh := range in.Notifications.Webhooks {
				nCopy.Webhooks[i] = WebhookNotification{URL: wh.URL}
				if wh.Events != nil {
					nCopy.Webhooks[i].Events = make([]string, len(wh.Events))
					copy(nCopy.Webhooks[i].Events, wh.Events)
				}
			}
		}
		out.Notifications = &nCopy
	}
}

func deepCopyStatus(in, out *TofuProjectStatus) {
	if in.LastDriftCheckTime != nil {
		t := *in.LastDriftCheckTime
		out.LastDriftCheckTime = &t
	}
	if in.Outputs != nil {
		out.Outputs = map[string]string{}
		for k, v := range in.Outputs {
			out.Outputs[k] = v
		}
	}
	if in.BlastRadius != nil {
		brCopy := *in.BlastRadius
		out.BlastRadius = &brCopy
	}
	if in.ExpiresAt != nil {
		t := *in.ExpiresAt
		out.ExpiresAt = &t
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		copy(out.Conditions, in.Conditions)
	}
}

func (in *TofuProjectList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(TofuProjectList)
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]TofuProject, len(in.Items))
		copy(out.Items, in.Items)
	}
	return out
}
