package controller

import (
	"context"
	"testing"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A plain-http managed forge (in-cluster service URL) must not demand the
// ingress CA: there is no TLS hop to trust, and platforms without
// default-ingress-cert (MicroShift) would otherwise wedge the reconcile.
func TestForgeTLSTrustSkipsPlainHTTP(t *testing.T) {
	r := &DotvirtReconciler{Platform: platform.OpenShift}
	dv := &dotvirtv1alpha1.Dotvirt{
		ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", Namespace: "dotvirt"},
		Spec: dotvirtv1alpha1.DotvirtSpec{
			Forge: dotvirtv1alpha1.ForgeSpec{Managed: true, URL: "http://forge.dotvirt.svc.cluster.local"},
		},
	}
	// No CA in the pass and no client wired: reaching the trust step would fail
	// on the missing CA, so a clean return proves the http skip fires first.
	if err := r.ensureForgeTLSTrust(context.Background(), dv, &reconcileCtx{argoNS: "argocd"}); err != nil {
		t.Fatalf("plain-http forge demanded TLS trust: %v", err)
	}
}
