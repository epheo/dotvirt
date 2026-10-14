// Command manager runs the dotvirt installer operator (see api/v1alpha1 for
// what an install comprises).
package main

import (
	"flag"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/controller"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

var scheme = runtime.NewScheme()

// cacheByObject caches the watched kinds only where they carry the instance
// label, so the operator never holds every Secret in the cluster in memory.
func cacheByObject(plat platform.Platform) map[client.Object]cache.ByObject {
	hasInstance, _ := labels.NewRequirement(install.InstanceLabel, selection.Exists, nil)
	instanceOnly := cache.ByObject{Label: labels.NewSelector().Add(*hasInstance)}
	byObject := map[client.Object]cache.ByObject{
		&corev1.Secret{}:     instanceOnly,
		&appsv1.Deployment{}: instanceOnly,
	}
	// Registering the Route kind on vanilla Kubernetes fails the cache's REST lookup.
	if plat == platform.OpenShift {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(install.RouteGVK)
		byObject[route] = instanceOnly
	}
	return byObject
}

// operatorNamespace is the namespace the manager pod runs in, "" out of cluster.
func operatorNamespace() string {
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dotvirtv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var enableLeaderElection, dryRun bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "metrics endpoint (0 disables)")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe endpoint")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "enable leader election for HA")
	flag.BoolVar(&dryRun, "dry-run", false, "validate rendered resources via server-side dry-run apply; persist nothing")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	cfg := ctrl.GetConfigOrDie()
	// Detection FAILS startup rather than defaulting: a wrong platform silently
	// mis-renders every platform-gated resource (most damagingly fsGroup, which an
	// OpenShift SCC then rejects, bricking Forgejo). Failing loud turns a transient
	// discovery-API blip at boot into a quick pod restart that retries, instead of
	// a permanent mis-render from an empty or guessed platform.
	plat, err := platform.Detect(cfg)
	if err != nil {
		setupLog.Error(err, "unable to detect platform")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "dotvirt-operator.dotvirt.io",
		Cache:                  cache.Options{ByObject: cacheByObject(plat)},
		// ConfigMaps are read point-wise, never watched: the cached client would
		// spin a CLUSTER-WIDE informer on first Get, exceeding the narrow RBAC
		// (get, no list/watch) and blocking that Get forever, wedging reconcile
		// and deletion alike. Secrets are read direct too: the label-filtered
		// cache cannot see the unlabelled ones the operator reads (a BYO forge
		// credential, a pre-existing generated secret).
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}}}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.DotvirtReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Config:   mgr.GetConfig(),
		Platform: plat,
		DryRun:   dryRun,

		APIReader:         mgr.GetAPIReader(),
		OperatorNamespace: operatorNamespace(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Dotvirt")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting dotvirt operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
