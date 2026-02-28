package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
	"github.com/twiechert/tofu-k8s-operator/controllers"
)

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log := ctrl.Log.WithName("setup")
	log.Info("starting tofu-k8s-operator")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tofuv1alpha1.AddToScheme(scheme))

	log.Info("scheme registered", "types", fmt.Sprintf("%v", scheme.AllKnownTypes()))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "tofu-k8s-operator.leader",
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}
	log.Info("manager created")

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Error(err, "unable to create kubernetes clientset")
		os.Exit(1)
	}

	if err := (&controllers.TofuProjectReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Clientset: clientset,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to setup controller")
		os.Exit(1)
	}
	log.Info("controller registered")

	_ = mgr.AddHealthzCheck("healthz", func(_ *http.Request) error { return nil })
	_ = mgr.AddReadyzCheck("readyz", func(_ *http.Request) error { return nil })

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
