// Command manager runs the dotvirt installer operator (see api/v1alpha1 for
// what an install comprises).
package main

import (
	"context"
	"flag"
	"os"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/controller"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var scheme = runtime.NewScheme()

// operatorNamespace is the namespace the manager pod runs in, "" out of cluster.
func operatorNamespace() string {
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// operatorUsername is the username the manager authenticates as, asked of
// the apiserver once (every authenticated user may: system:basic-user); ""
// when the answer is not a ServiceAccount's.
func operatorUsername(cfg *rest.Config) string {
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	review, err := kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil || !adminobjects.ValidUsername(review.Status.UserInfo.Username) {
		return ""
	}
	return review.Status.UserInfo.Username
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
	clusterVersioned, err := platform.ServesClusterVersion(cfg)
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
		Cache:                  cache.Options{ByObject: controller.CacheByObject(plat)},
		// ConfigMaps, Secrets and RoleBindings are read point-wise: the cached
		// client would spin a CLUSTER-WIDE informer on first Get, exceeding the
		// RBAC (get by name, no list or watch) and blocking that Get forever,
		// wedging reconcile and deletion alike. The ones watched by name
		// (argocd-cm, the credential grant) feed events only.
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}, &rbacv1.RoleBinding{}, &rbacv1.Role{}}}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.DotvirtReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Config:           mgr.GetConfig(),
		Platform:         plat,
		ClusterVersioned: clusterVersioned,
		DryRun:           dryRun,

		APIReader:         mgr.GetAPIReader(),
		OperatorNamespace: operatorNamespace(),
		OperatorUsername:  operatorUsername(cfg),
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
