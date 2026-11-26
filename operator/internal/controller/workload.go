package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// reconcileWorkload renders + server-side-applies the namespaced workload,
// owner-referenced to this CR for automatic GC (unlike the cluster-scoped
// resources reconcileArgo applies, which a namespaced CR can't own - those rely
// on the finalizer).
func (r *DotvirtReconciler) reconcileWorkload(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
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
	if r.Platform == platform.OpenShift {
		base = append(base, install.OperandNetworkPolicy(dv, rc.argoNS, r.OperatorNamespace))
	}
	if err := r.applyOwned(ctx, dv, base...); err != nil {
		return nil, applyFailed(err)
	}
	if dv.Spec.Ingress.Host == "" && !r.DryRun && r.Platform == platform.OpenShift {
		host, err := r.routeHost(ctx, dv.Namespace, install.AppName)
		if err != nil {
			return nil, fmt.Errorf("read the console Route: %w", err)
		}
		dv.Spec.Ingress.Host = host // in-memory only (never persisted; drives DOTVIRT_PUBLIC_URL)
	}
	if dv.Spec.Ingress.Host != "" {
		dv.Status.ConsoleURL = "https://" + dv.Spec.Ingress.Host
	}
	if dv.Spec.Auth.SSOEnabled() {
		if dv.Spec.Ingress.Host != "" {
			dv.Status.SSOOAuthClient = ssoApplyCommand(dv.Namespace, dv.Spec.Ingress.Host)
		}
	} else {
		// Derived status: SSO turned or resolved off must retire the stale apply
		// command, not leave it inviting a needless OAuthClient forever.
		dv.Status.SSOOAuthClient = ""
	}
	if err := r.applyOwned(ctx, dv, install.Deployment(dv, rc.argoNS, r.tenantKinds(ctx, dv, rc, nil))); err != nil {
		return nil, applyFailed(err)
	}
	if r.DryRun {
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionWorkloadReady, "the rollout check")
		return nil, nil
	}
	ready, err := r.operandReady(ctx, dv)
	if err != nil {
		return nil, err
	}
	// Not a halt: the later phases do not need the pod, and a crashlooping pod
	// must not stop the Argo wiring from converging. Available reads this
	// condition at the end of the pass; the Deployment watch re-triggers.
	if !ready {
		r.setCondition(dv, dotvirtv1alpha1.ConditionWorkloadReady, metav1.ConditionFalse, "Progressing",
			"workload applied; waiting for the dotvirt pod to become ready")
		return nil, nil
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionWorkloadReady, metav1.ConditionTrue, "Ready", "dotvirt pod ready")
	return nil, nil
}

// operandReady reads the Deployment back; not-found is not-ready, since the
// cache can lag the apply this same pass.
func (r *DotvirtReconciler) operandReady(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (bool, error) {
	var d appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.AppName}, &d); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return d.Status.ReadyReplicas >= 1, nil
}

// resolveExposureType picks the exposure kind (adminobjects.ExposureType): a
// named Gateway wins over the platform, as MicroShift serves the Route API with
// no router behind it once its ingress is Removed.
func (r *DotvirtReconciler) resolveExposureType(dv *dotvirtv1alpha1.Dotvirt) dotvirtv1alpha1.IngressType {
	return dotvirtv1alpha1.IngressType(adminobjects.ExposureType(string(dv.Spec.Ingress.Type), dv.Spec.Ingress.Gateway != nil, r.Platform == platform.OpenShift))
}

// exposureFor builds the external exposure of the named Service as kind: a
// Route on OpenShift (host may be empty - the router then assigns one), an
// Ingress on vanilla Kubernetes or an HTTPRoute on gateway (host required).
func (r *DotvirtReconciler) exposureFor(dv *dotvirtv1alpha1.Dotvirt, name string, port int32, host string, gateway *dotvirtv1alpha1.GatewayRef, kind dotvirtv1alpha1.IngressType) client.Object {
	switch kind {
	case dotvirtv1alpha1.IngressRoute:
		return install.Route(dv, name, host)
	case dotvirtv1alpha1.IngressIngress:
		if host != "" {
			return install.Ingress(dv, name, port, host)
		}
	case dotvirtv1alpha1.IngressGateway:
		if host != "" {
			return install.HTTPRoute(dv, gateway, name, port, host)
		}
	}
	return nil
}

// exposure is the console's: on a Gateway with no host it takes every name the
// listener does, for a machine reached by its address. Nothing else goes hostless.
func (r *DotvirtReconciler) exposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	if r.resolveExposureType(dv) == dotvirtv1alpha1.IngressGateway {
		return install.HTTPRoute(dv, dv.Spec.Ingress.Gateway, install.AppName, install.HTTPPort, dv.Spec.Ingress.Host)
	}
	return r.exposureFor(dv, install.AppName, install.HTTPPort, dv.Spec.Ingress.Host, dv.Spec.Ingress.Gateway, r.resolveExposureType(dv))
}

// ssoApplyCommand is the one command an admin runs to register the cluster-scoped
// OAuthClient SSO needs: the redirect URI is filled from the assigned console host, and
// the client secret is read from the operator-generated Secret at apply time, so it
// never lands in status. One line by design: the console status view flattens
// newlines, so a heredoc dies on copy-paste. The operator deliberately doesn't
// create the OAuthClient itself.
func ssoApplyCommand(ns, host string) string {
	secret := fmt.Sprintf(`$(oc -n %s get secret %s -o jsonpath='{.data.clientSecret}' | base64 -d)`,
		ns, install.OAuthSecretName)
	return fmt.Sprintf(`echo "{\"apiVersion\":\"oauth.openshift.io/v1\",\"kind\":\"OAuthClient\",\"metadata\":{\"name\":\"%s\"},\"secret\":\"%s\",\"redirectURIs\":[\"https://%s/api/auth/callback\"],\"grantMethod\":\"auto\"}" | oc apply -f -`,
		install.OAuthClientName(ns), secret, host)
}
