package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// reconcileWorkload renders + server-side-applies the namespaced workload,
// owner-referenced to this CR for automatic GC (unlike the cluster-scoped
// resources reconcileArgo applies, which a namespaced CR can't own — those rely
// on the finalizer).
func (r *DotvirtReconciler) reconcileWorkload(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	// Exposure first: on OpenShift an empty ingress.host yields a hostless Route the
	// router names. Read that host back and fill it in-memory so the Deployment's
	// DOTVIRT_PUBLIC_URL (OAuth callback + webhook self-registration) is set this same
	// pass. Not yet assigned: the Deployment renders without it and Owns(Route) re-triggers
	// once the host lands.
	base := []client.Object{
		install.ServiceAccount(dv),
		install.DraftsPVC(dv),
		install.Service(dv),
	}
	if exposure := r.exposure(dv); exposure != nil {
		base = append(base, exposure)
	}
	for _, obj := range base {
		if err := controllerutil.SetControllerReference(dv, obj, r.Scheme); err != nil {
			return nil, err
		}
		if err := install.Apply(ctx, r.Client, obj, r.DryRun); err != nil {
			return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionWorkloadReady, "ApplyFailed", err)
		}
	}
	if dv.Spec.Ingress.Host == "" && !r.DryRun && r.Platform == platform.OpenShift {
		if host := r.routeHost(ctx, dv.Namespace, install.AppName); host != "" {
			dv.Spec.Ingress.Host = host // in-memory only (never persisted; drives DOTVIRT_PUBLIC_URL)
		}
	}
	if dv.Spec.Ingress.Host != "" {
		dv.Status.ConsoleURL = "https://" + dv.Spec.Ingress.Host
	}
	deployment := install.Deployment(dv)
	if err := controllerutil.SetControllerReference(dv, deployment, r.Scheme); err != nil {
		return nil, err
	}
	if err := install.Apply(ctx, r.Client, deployment, r.DryRun); err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionWorkloadReady, "ApplyFailed", err)
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionWorkloadReady, metav1.ConditionTrue, "Ready", "workload applied")
	return nil, nil
}

// resolveExposureType picks the exposure kind for the configured/detected ingress type:
// the explicit spec value, or Route on OpenShift / Ingress on vanilla when "auto"/unset.
func (r *DotvirtReconciler) resolveExposureType(dv *dotvirtv1alpha1.Dotvirt) string {
	if t := string(dv.Spec.Ingress.Type); t != "" && t != "auto" {
		return t
	}
	if r.Platform == platform.OpenShift {
		return "route"
	}
	return "ingress"
}

// exposureFor builds the external exposure of the named Service for the resolved
// type: a Route on OpenShift (host may be empty — the router then assigns one), an
// Ingress on vanilla Kubernetes (host required), nil for the not-yet-implemented
// Gateway type.
func (r *DotvirtReconciler) exposureFor(dv *dotvirtv1alpha1.Dotvirt, name string, port int32, host string) client.Object {
	switch r.resolveExposureType(dv) {
	case "route":
		return install.Route(dv, name, host)
	case "ingress":
		if host != "" {
			return install.Ingress(dv, name, port, host)
		}
	}
	return nil
}

// exposure builds the UI ingress object on spec.ingress.host.
func (r *DotvirtReconciler) exposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	return r.exposureFor(dv, install.AppName, install.HTTPPort, dv.Spec.Ingress.Host)
}
