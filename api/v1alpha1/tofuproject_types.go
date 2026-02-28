package v1alpha1

import (
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

type TofuProjectSpec struct {
	ProgramRef ObjectRef `json:"programRef"`

	// params are arbitrary key/value variables passed to the program as terraform.tfvars.json
	Params map[string]string `json:"params,omitempty"`

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
