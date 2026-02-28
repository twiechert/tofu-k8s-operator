package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type TofuProgram struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TofuProgramSpec   `json:"spec,omitempty"`
	Status TofuProgramStatus `json:"status,omitempty"`
}

type ProviderSpec struct {
	// Name is the provider name used in `provider "<name>" {}`.
	Name string `json:"name"`
	// Source is the registry source, e.g. "hashicorp/aws".
	Source string `json:"source,omitempty"`
	// Version constraint, e.g. "~> 5.0".
	Version string `json:"version,omitempty"`
	// configHCL is inserted verbatim into a provider block body.
	ConfigHCL string `json:"configHCL,omitempty"`
}

type SecretRef struct {
	Name string `json:"name"`
}

type GitSource struct {
	// URL is the git repository URL (HTTPS or SSH).
	URL string `json:"url"`
	// Ref is the branch, tag, or SHA to check out. Defaults to "main".
	Ref string `json:"ref,omitempty"`
	// Path is the subdirectory within the repo containing .tf files. Defaults to root.
	Path string `json:"path,omitempty"`
	// CredentialsSecretRef references a Secret with "username" and "password" keys for private repos.
	CredentialsSecretRef *SecretRef `json:"credentialsSecretRef,omitempty"`
}

type TofuProgramSpec struct {
	// programHCL is the main OpenTofu program written in HCL (stored as main.tf).
	// Mutually exclusive with Source.
	ProgramHCL string `json:"programHCL,omitempty"`
	// Source points to a git repository containing .tf files.
	// Mutually exclusive with ProgramHCL.
	Source *GitSource `json:"source,omitempty"`
	// providers become required_providers + provider blocks in providers.tf
	Providers []ProviderSpec `json:"providers,omitempty"`
}

type TofuProgramStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
type TofuProgramList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TofuProgram `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TofuProgram{}, &TofuProgramList{})
}

// DeepCopyObject implementations (handwritten minimal)
func (in *TofuProgram) DeepCopyObject() runtime.Object {
	if in == nil { return nil }
	out := new(TofuProgram)
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec = in.Spec
	out.Status = in.Status
	if in.Spec.Source != nil {
		src := *in.Spec.Source
		if src.CredentialsSecretRef != nil {
			ref := *src.CredentialsSecretRef
			src.CredentialsSecretRef = &ref
		}
		out.Spec.Source = &src
	}
	if in.Spec.Providers != nil {
		out.Spec.Providers = append([]ProviderSpec(nil), in.Spec.Providers...)
	}
	return out
}

func (in *TofuProgramList) DeepCopyObject() runtime.Object {
	if in == nil { return nil }
	out := new(TofuProgramList)
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]TofuProgram, len(in.Items))
		copy(out.Items, in.Items)
	}
	return out
}
