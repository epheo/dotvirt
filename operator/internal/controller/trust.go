package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// ensureTrustAnchors: the CA ConfigMaps that let a zero-config install VERIFY
// every TLS hop. dotvirt-ingress-ca is a COPY (pods cannot mount across
// namespaces) and CONVERGED (the ingress CA rotates); it signs every
// router-served Route. dotvirt-service-ca is injector-filled and verifies
// in-cluster serving certs (thanos). Best-effort: mounts are optional and every
// CA load is tolerant, so a missing anchor degrades legibly, never wedges.
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
		cm := &corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{Name: install.IngressCAConfigMap, Namespace: dv.Namespace, Labels: install.Labels(dv.Name)},
			Data:       map[string]string{install.IngressCAKey: ca},
		}
		if err := r.applyOwned(ctx, dv, cm); err != nil {
			log.Info("ingress CA copy failed; router-served TLS stays on the system pool", "error", err.Error())
		}
	}

	// The injector owns Data; only the annotation is ours to assert (SSA leaves
	// unsent fields to their owner).
	sc := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name: install.ServiceCAConfigMap, Namespace: dv.Namespace,
			Labels:      install.Labels(dv.Name),
			Annotations: map[string]string{"service.beta.openshift.io/inject-cabundle": "true"},
		},
	}
	if err := r.applyOwned(ctx, dv, sc); err != nil {
		log.Info("service CA ConfigMap failed; in-cluster metrics TLS stays on the system pool", "error", err.Error())
	}
}
