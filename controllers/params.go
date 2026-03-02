package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

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
	resolved, err := r.resolveParamFromSources(ctx, project)
	if err != nil {
		return nil, "", err
	}

	if err := r.resolveParamBindingSources(ctx, project, resolved); err != nil {
		return nil, "", err
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

// resolveParamFromSources bulk-reads all keys from each ConfigMap/Secret referenced by paramFrom.
func (r *TofuProjectReconciler) resolveParamFromSources(ctx context.Context, project *tofuv1alpha1.TofuProject) (map[string]string, error) {
	resolved := map[string]string{}
	for _, pf := range project.Spec.ParamFrom {
		if pf.ConfigMapRef != nil {
			ns := pf.ConfigMapRef.Namespace
			if ns == "" {
				ns = project.Namespace
			}
			var cm corev1.ConfigMap
			if err := r.Get(ctx, types.NamespacedName{Name: pf.ConfigMapRef.Name, Namespace: ns}, &cm); err != nil {
				return nil, fmt.Errorf("paramFrom configMapRef %s/%s: %w", ns, pf.ConfigMapRef.Name, err)
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
				return nil, fmt.Errorf("paramFrom secretRef %s/%s: %w", ns, pf.SecretRef.Name, err)
			}
			for k, v := range secret.Data {
				resolved[k] = string(v)
			}
		}
	}
	return resolved, nil
}

// resolveParamBindingSources resolves individual key refs from paramBindings into the resolved map.
func (r *TofuProjectReconciler) resolveParamBindingSources(ctx context.Context, project *tofuv1alpha1.TofuProject, resolved map[string]string) error {
	for _, pb := range project.Spec.ParamBindings {
		if pb.ConfigMapKeyRef != nil {
			ns := project.Namespace
			var cm corev1.ConfigMap
			if err := r.Get(ctx, types.NamespacedName{Name: pb.ConfigMapKeyRef.Name, Namespace: ns}, &cm); err != nil {
				return fmt.Errorf("paramBindings configMapKeyRef %s/%s: %w", ns, pb.ConfigMapKeyRef.Name, err)
			}
			val, ok := cm.Data[pb.ConfigMapKeyRef.Key]
			if !ok {
				return fmt.Errorf("paramBindings configMapKeyRef %s/%s: key %q not found", ns, pb.ConfigMapKeyRef.Name, pb.ConfigMapKeyRef.Key)
			}
			resolved[pb.Name] = val
		}
		if pb.SecretKeyRef != nil {
			ns := project.Namespace
			var secret corev1.Secret
			if err := r.Get(ctx, types.NamespacedName{Name: pb.SecretKeyRef.Name, Namespace: ns}, &secret); err != nil {
				return fmt.Errorf("paramBindings secretKeyRef %s/%s: %w", ns, pb.SecretKeyRef.Name, err)
			}
			val, ok := secret.Data[pb.SecretKeyRef.Key]
			if !ok {
				return fmt.Errorf("paramBindings secretKeyRef %s/%s: key %q not found", ns, pb.SecretKeyRef.Name, pb.SecretKeyRef.Key)
			}
			resolved[pb.Name] = string(val)
		}
	}
	return nil
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
