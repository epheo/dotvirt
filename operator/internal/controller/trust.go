package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// ensureTrustAnchors materializes the CA ConfigMaps the workload mounts, so a
// zero-config install VERIFIES every TLS hop instead of shipping insecureTLS:
//
//   - dotvirt-ingress-ca: a converged COPY of the cluster's default ingress CA
//     (openshift-config-managed/default-ingress-cert), which signs every
//     router-served Route: the managed forge, the oauth endpoints, ArgoCD.
//     Copied because pods cannot mount ConfigMaps across namespaces, and
//     converged because the ingress CA rotates.
//   - dotvirt-service-ca: an empty ConfigMap the service-ca operator fills via
//     the inject-cabundle annotation, verifying in-cluster serving certs (the
//     sample's thanos-querier metrics endpoint).
//
// OpenShift-only and best-effort: the mounts are optional and every consumer's CA
// load is tolerant, so a missing anchor degrades to a legible TLS error in the
// affected feature, never a wedged install. Both are owner-referenced (same
// namespace), so uninstall garbage-collects them.
func (r *DotvirtReconciler) ensureTrustAnchors(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) {
	if r.Platform != platform.OpenShift || r.DryRun {
		return
	}
	log := logf.FromContext(ctx)

	var src corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: "openshift-config-managed", Name: "default-ingress-cert"}, &src); err != nil {
		log.Info("default ingress CA unreadable; router-served TLS stays on the system pool", "error", err.Error())
	} else if ca := src.Data[install.IngressCAKey]; ca == "" {
		log.Info("default ingress CA has no ca-bundle.crt; router-served TLS stays on the system pool")
	} else {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.IngressCAConfigMap, Namespace: dv.Namespace}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
			cm.Labels = install.Labels(dv.Name)
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[install.IngressCAKey] = ca
			return controllerutil.SetControllerReference(dv, cm, r.Scheme)
		}); err != nil {
			log.Info("ingress CA copy failed; router-served TLS stays on the system pool", "error", err.Error())
		}
	}

	sc := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.ServiceCAConfigMap, Namespace: dv.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sc, func() error {
		sc.Labels = install.Labels(dv.Name)
		if sc.Annotations == nil {
			sc.Annotations = map[string]string{}
		}
		// The injector owns Data; only the annotation is ours to assert.
		sc.Annotations["service.beta.openshift.io/inject-cabundle"] = "true"
		return controllerutil.SetControllerReference(dv, sc, r.Scheme)
	}); err != nil {
		log.Info("service CA ConfigMap failed; in-cluster metrics TLS stays on the system pool", "error", err.Error())
	}
}
