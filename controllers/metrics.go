package controllers

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

var (
	projectPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tofu_project_phase",
		Help: "Current phase of a TofuProject (1 for active phase, 0 for others)",
	}, []string{"namespace", "name", "phase"})

	reconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tofu_project_reconcile_total",
		Help: "Total reconcile outcomes per TofuProject",
	}, []string{"namespace", "name", "result"})

	applyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tofu_project_apply_total",
		Help: "Total apply job completions per TofuProject",
	}, []string{"namespace", "name", "result"})

	planTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tofu_project_plan_total",
		Help: "Total plan job completions per TofuProject",
	}, []string{"namespace", "name", "result"})

	driftDetected = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tofu_project_drift_detected",
		Help: "Whether drift is detected for a TofuProject (1=drift, 0=no drift)",
	}, []string{"namespace", "name"})

	blastRadius = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tofu_project_blast_radius",
		Help: "Total affected resources from the last plan",
	}, []string{"namespace", "name"})

	projectInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tofu_project_info",
		Help: "Static info labels for a TofuProject (always 1)",
	}, []string{"namespace", "name", "auto_approve", "image"})
)

func init() {
	metrics.Registry.MustRegister(
		projectPhase,
		reconcileTotal,
		applyTotal,
		planTotal,
		driftDetected,
		blastRadius,
		projectInfo,
	)
}

// knownPhases lists every phase the controller can set.
var knownPhases = []string{
	"Succeeded", "Error", "Running", "Planning", "WaitingApproval",
	"WaitingDeleteApproval", "Suspended", "ScheduledApply",
	"DriftChecking", "Destroying", "DestroyFailed", "Retrying", "Queued",
}

// recordPhase sets 1 for the active phase and 0 for all others.
func recordPhase(project *tofuv1alpha1.TofuProject) {
	ns, name := project.Namespace, project.Name
	for _, p := range knownPhases {
		var v float64
		if p == project.Status.Phase {
			v = 1
		}
		projectPhase.WithLabelValues(ns, name, p).Set(v)
	}
}

// recordInfo emits the info metric with static labels.
func recordInfo(project *tofuv1alpha1.TofuProject) {
	image := project.Spec.ResolveImage()
	autoApprove := "false"
	if project.Spec.AutoApprove {
		autoApprove = "true"
	}
	projectInfo.WithLabelValues(project.Namespace, project.Name, autoApprove, image).Set(1)
}

// recordBlastRadius sets the blast radius gauge from the project status.
func recordBlastRadius(project *tofuv1alpha1.TofuProject) {
	var total float64
	if project.Status.BlastRadius != nil {
		total = float64(project.Status.BlastRadius.Total)
	}
	blastRadius.WithLabelValues(project.Namespace, project.Name).Set(total)
}

// recordDrift sets the drift detected gauge.
func recordDrift(project *tofuv1alpha1.TofuProject) {
	var v float64
	if project.Status.DriftDetected {
		v = 1
	}
	driftDetected.WithLabelValues(project.Namespace, project.Name).Set(v)
}

// deleteProjectMetrics removes all metric series for a deleted project.
func deleteProjectMetrics(namespace, name string) {
	labels := prometheus.Labels{"namespace": namespace, "name": name}
	for _, p := range knownPhases {
		projectPhase.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "name": name, "phase": p})
	}
	reconcileTotal.DeletePartialMatch(labels)
	applyTotal.DeletePartialMatch(labels)
	planTotal.DeletePartialMatch(labels)
	driftDetected.DeletePartialMatch(labels)
	blastRadius.DeletePartialMatch(labels)
	projectInfo.DeletePartialMatch(labels)
}
