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

type ProjectDependency struct {
	ProjectRef ObjectRef         `json:"projectRef"`
	Outputs    map[string]string `json:"outputs"` // upstream output name → downstream param name
}

type ParamFromSource struct {
	ConfigMapRef *ObjectRef `json:"configMapRef,omitempty"`
	SecretRef    *ObjectRef `json:"secretRef,omitempty"`
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
	Size         string `json:"size,omitempty"`         // default "1Gi"
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

type TofuProjectSpec struct {
	ProgramRef ObjectRef `json:"programRef"`

	// params are arbitrary key/value variables passed to the program as terraform.tfvars.json
	Params map[string]string `json:"params,omitempty"`

	// paramFrom bulk-imports all keys from ConfigMaps/Secrets as params (lowest precedence)
	ParamFrom []ParamFromSource `json:"paramFrom,omitempty"`

	// paramBindings maps individual ConfigMap/Secret keys to named params (medium precedence)
	ParamBindings []ParamBinding `json:"paramBindings,omitempty"`

	// workspace name (optional)
	Workspace string `json:"workspace,omitempty"`

	// container image running `tofu` (default: ghcr.io/opentofu/opentofu:latest)
	Image string `json:"image,omitempty"`

	// serviceAccount configures the ServiceAccount used by tofu Jobs
	ServiceAccount *ServiceAccountSpec `json:"serviceAccount,omitempty"`

	// backend config for kubernetes state backend
	Backend KubernetesBackendSpec `json:"backend,omitempty"`

	// autoApprove passes -auto-approve to apply
	AutoApprove bool `json:"autoApprove,omitempty"`

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

	// driftDetection configures periodic drift checking.
	DriftDetection *DriftDetectionSpec `json:"driftDetection,omitempty"`

	// notifications configures webhook notifications for lifecycle events.
	Notifications *NotificationSpec `json:"notifications,omitempty"`
}

type TofuProjectStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastJobName        string `json:"lastJobName,omitempty"`
	LastAppliedHash    string `json:"lastAppliedHash,omitempty"`
	Phase              string `json:"phase,omitempty"`
	Message            string `json:"message,omitempty"`
	SyncStatus         string `json:"syncStatus,omitempty"` // 'sync' or 'not in sync'
	PendingPlanHash    string `json:"pendingPlanHash,omitempty"`
	PlanOutput         string `json:"planOutput,omitempty"`
	PlanSummary        string `json:"planSummary,omitempty"`
	LastPlanJobName    string              `json:"lastPlanJobName,omitempty"`
	Outputs            map[string]string   `json:"outputs,omitempty"`
	Conditions         []metav1.Condition  `json:"conditions,omitempty"`
	RetryCount         int32               `json:"retryCount,omitempty"`
	LastDriftCheckTime *metav1.Time        `json:"lastDriftCheckTime,omitempty"`
	DriftDetected      bool                `json:"driftDetected,omitempty"`
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
	if in.Spec.Params != nil {
		out.Spec.Params = map[string]string{}
		for k, v := range in.Spec.Params {
			out.Spec.Params[k] = v
		}
	}
	if in.Spec.ParamFrom != nil {
		out.Spec.ParamFrom = make([]ParamFromSource, len(in.Spec.ParamFrom))
		for i, pf := range in.Spec.ParamFrom {
			out.Spec.ParamFrom[i] = ParamFromSource{}
			if pf.ConfigMapRef != nil {
				ref := *pf.ConfigMapRef
				out.Spec.ParamFrom[i].ConfigMapRef = &ref
			}
			if pf.SecretRef != nil {
				ref := *pf.SecretRef
				out.Spec.ParamFrom[i].SecretRef = &ref
			}
		}
	}
	if in.Spec.ParamBindings != nil {
		out.Spec.ParamBindings = make([]ParamBinding, len(in.Spec.ParamBindings))
		for i, pb := range in.Spec.ParamBindings {
			out.Spec.ParamBindings[i] = ParamBinding{Name: pb.Name}
			if pb.ConfigMapKeyRef != nil {
				ref := *pb.ConfigMapKeyRef
				out.Spec.ParamBindings[i].ConfigMapKeyRef = &ref
			}
			if pb.SecretKeyRef != nil {
				ref := *pb.SecretKeyRef
				out.Spec.ParamBindings[i].SecretKeyRef = &ref
			}
		}
	}
	if in.Spec.Dependencies != nil {
		out.Spec.Dependencies = make([]ProjectDependency, len(in.Spec.Dependencies))
		for i, dep := range in.Spec.Dependencies {
			out.Spec.Dependencies[i] = ProjectDependency{
				ProjectRef: dep.ProjectRef,
			}
			if dep.Outputs != nil {
				out.Spec.Dependencies[i].Outputs = map[string]string{}
				for k, v := range dep.Outputs {
					out.Spec.Dependencies[i].Outputs[k] = v
				}
			}
		}
	}
	if in.Spec.ApplyImmediately != nil {
		val := *in.Spec.ApplyImmediately
		out.Spec.ApplyImmediately = &val
	}
	if in.Spec.ServiceAccount != nil {
		saCopy := *in.Spec.ServiceAccount
		if in.Spec.ServiceAccount.Annotations != nil {
			saCopy.Annotations = map[string]string{}
			for k, v := range in.Spec.ServiceAccount.Annotations {
				saCopy.Annotations[k] = v
			}
		}
		out.Spec.ServiceAccount = &saCopy
	}
	if in.Spec.Cache != nil {
		cacheCopy := *in.Spec.Cache
		out.Spec.Cache = &cacheCopy
	}
	if in.Spec.Env != nil {
		data, _ := json.Marshal(in.Spec.Env)
		var envCopy []corev1.EnvVar
		_ = json.Unmarshal(data, &envCopy)
		out.Spec.Env = envCopy
	}
	if in.Spec.EnvFrom != nil {
		data, _ := json.Marshal(in.Spec.EnvFrom)
		var envFromCopy []corev1.EnvFromSource
		_ = json.Unmarshal(data, &envFromCopy)
		out.Spec.EnvFrom = envFromCopy
	}
	if in.Spec.Resources != nil {
		resCopy := ResourceRequirements{}
		if in.Spec.Resources.Limits != nil {
			resCopy.Limits = map[string]string{}
			for k, v := range in.Spec.Resources.Limits {
				resCopy.Limits[k] = v
			}
		}
		if in.Spec.Resources.Requests != nil {
			resCopy.Requests = map[string]string{}
			for k, v := range in.Spec.Resources.Requests {
				resCopy.Requests[k] = v
			}
		}
		out.Spec.Resources = &resCopy
	}
	if in.Spec.RetryPolicy != nil {
		rpCopy := *in.Spec.RetryPolicy
		out.Spec.RetryPolicy = &rpCopy
	}
	if in.Spec.DriftDetection != nil {
		ddCopy := *in.Spec.DriftDetection
		out.Spec.DriftDetection = &ddCopy
	}
	if in.Spec.Notifications != nil {
		nCopy := NotificationSpec{}
		if in.Spec.Notifications.Webhooks != nil {
			nCopy.Webhooks = make([]WebhookNotification, len(in.Spec.Notifications.Webhooks))
			for i, wh := range in.Spec.Notifications.Webhooks {
				nCopy.Webhooks[i] = WebhookNotification{URL: wh.URL}
				if wh.Events != nil {
					nCopy.Webhooks[i].Events = make([]string, len(wh.Events))
					copy(nCopy.Webhooks[i].Events, wh.Events)
				}
			}
		}
		out.Spec.Notifications = &nCopy
	}
	if in.Status.LastDriftCheckTime != nil {
		t := *in.Status.LastDriftCheckTime
		out.Status.LastDriftCheckTime = &t
	}
	if in.Status.Outputs != nil {
		out.Status.Outputs = map[string]string{}
		for k, v := range in.Status.Outputs {
			out.Status.Outputs[k] = v
		}
	}
	if in.Status.Conditions != nil {
		out.Status.Conditions = make([]metav1.Condition, len(in.Status.Conditions))
		copy(out.Status.Conditions, in.Status.Conditions)
	}
	return out
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
