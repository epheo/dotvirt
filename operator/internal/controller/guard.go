package controller

import (
	"context"
	"os"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// reconcileOperatorGuard reports whether the admission policies bounding the
// operator's own account are installed as rendered for this install, and
// lists them in status. The operator renders them only to compare: it never
// writes them (an operator that could install its guard could loosen it),
// and the console renders what it installs on its own. Best-effort: never a
// halt, never Available.
func (r *DotvirtReconciler) reconcileOperatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.operatorGuard(ctx, dv, rc)
	r.setCondition(dv, dotvirtv1alpha1.ConditionOperatorGuard, status, reason, msg)
	return nil, nil
}

// guardParams are the guard's pins for this install, as the console takes
// them: the images from the CR and from the operator's own environment, which
// its Deployment sets.
func (r *DotvirtReconciler) guardParams(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) adminobjects.Params {
	p := adminobjects.Params{
		Operator: r.OperatorUsername, Namespace: dv.Namespace, ArgoNamespace: rc.argoNS, ArgoController: rc.argoSA,
		SpecImage: dv.Spec.Image, DotvirtImage: os.Getenv("RELATED_IMAGE_DOTVIRT"), ForgejoImage: os.Getenv("RELATED_IMAGE_FORGEJO"),
		InsecureTLS:       dv.Spec.Forge.InsecureTLS,
		Metrics:           adminobjects.MetricsFor(dv.Spec.Metrics.URL, dv.Spec.Metrics.TenancyURL, dv.Spec.Metrics.CA != nil),
		BrandingConfigMap: dv.Spec.Branding.ConfigMap,
		ForgeSecret:       adminobjects.ForgeSecret(dv.Spec.Forge.CredentialsSecret),
	}
	if dv.Spec.Metrics.CA != nil {
		p.MetricsCAConfigMap = dv.Spec.Metrics.CA.Name
	}
	if o := dv.Spec.Auth.OIDC; o != nil {
		p.OIDCIssuer = o.IssuerURL
		if o.ClientSecret != nil {
			p.OIDCSecret, p.OIDCSecretKey = o.ClientSecret.Name, o.ClientSecret.Key
		}
	}
	p.ForgeManaged = dv.Spec.Forge.Managed
	domain := r.appsDomain(ctx, dv)
	router := ""
	if dv.Spec.Ingress.Host == "" && domain == "" && r.Platform == platform.OpenShift {
		router = r.routerHost(ctx, dv)
	}
	p.ConsoleHost = adminobjects.ConsoleHost(dv.Spec.Ingress.Host, dv.Namespace, domain, router)
	return p
}

// routerHost is the host routers report serving the console's Route under,
// read as the console reads it where no cluster ingress domain is served.
func (r *DotvirtReconciler) routerHost(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) string {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(install.RouteGVK)
	if err := r.reader().Get(ctx, client.ObjectKey{Namespace: dv.Namespace, Name: install.AppName}, u); err != nil {
		return ""
	}
	return adminobjects.RouterHost(u.Object)
}

func (r *DotvirtReconciler) operatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (metav1.ConditionStatus, string, string) {
	isGuard := func(e dotvirtv1alpha1.AdminObject) bool {
		return (e.Kind == "ValidatingAdmissionPolicy" || e.Kind == "ValidatingAdmissionPolicyBinding") && slices.Contains(adminobjects.GuardNames, e.Name)
	}
	p := r.guardParams(ctx, dv, rc)
	if !p.Valid() {
		publishAdminObjects(dv, isGuard, nil, nil)
		return metav1.ConditionUnknown, "AccountUnknown",
			"the operator does not know the account it runs as (its SelfSubjectReview failed at startup), so no guard can pin it"
	}
	want := adminobjects.Render(p)
	states, details, err := r.adminObjectStates(ctx, want)
	switch {
	case meta.IsNoMatchError(err):
		publishAdminObjects(dv, isGuard, nil, nil)
		return metav1.ConditionUnknown, "Unsupported",
			"the cluster does not serve admissionregistration.k8s.io/v1 ValidatingAdmissionPolicy (Kubernetes 1.30+), so no guard can bound the operator"
	case err != nil:
		return metav1.ConditionUnknown, "Unreadable", "could not read the operator guard: " + err.Error()
	}
	publishAdminObjects(dv, isGuard, want, states)
	for _, s := range []adminobjects.State{adminobjects.NotInstalled, adminobjects.Outdated} {
		if i := slices.Index(states, s); i >= 0 {
			return metav1.ConditionFalse, string(s), details[i] + "; " + guardNextAction
		}
	}
	return metav1.ConditionTrue, string(adminobjects.Installed), "the admission policies bounding " + p.Operator + " are installed as rendered"
}

// appsDomain is the cluster's ingress domain, read as the console reads it
// (config.openshift.io Ingress cluster), when the console's host is the
// router's; "" without spec.ingress.host where nothing serves it.
func (r *DotvirtReconciler) appsDomain(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Ingress.Host != "" || r.Platform != platform.OpenShift {
		return ""
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "Ingress"})
	if err := r.reader().Get(ctx, client.ObjectKey{Name: "cluster"}, u); err != nil {
		return ""
	}
	domain, _, _ := unstructured.NestedString(u.Object, "spec", "domain")
	return domain
}

// guardNextAction is what a cluster admin does about a guard not in place.
// It names no value: the operator writes this message, so an admin derives
// each pin from the cluster, as the template's header shows.
const guardNextAction = "a cluster admin installs it from the console (Install), or fills the release's install/guard.yaml with values derived as its header shows, and applies it"

// adminObjectStates reads each rendered admin object back, by name, and
// weighs it against its render.
func (r *DotvirtReconciler) adminObjectStates(ctx context.Context, want []adminobjects.Object) ([]adminobjects.State, []string, error) {
	states, details := make([]adminobjects.State, len(want)), make([]string, len(want))
	for i, w := range want {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(w.Object.GroupVersionKind())
		err := r.reader().Get(ctx, client.ObjectKeyFromObject(w.Object), live)
		if apierrors.IsNotFound(err) {
			live, err = nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		states[i], details[i] = adminobjects.Compare(w.Object, live)
	}
	return states, details, nil
}

// publishAdminObjects replaces the status entries owned matches with want at
// their states. A phase that renders admin objects publishes its own, so
// phases never drop each other's.
func publishAdminObjects(dv *dotvirtv1alpha1.Dotvirt, owned func(dotvirtv1alpha1.AdminObject) bool, want []adminobjects.Object, states []adminobjects.State) {
	var kept []dotvirtv1alpha1.AdminObject
	for _, e := range dv.Status.AdminObjects {
		if !owned(e) {
			kept = append(kept, e)
		}
	}
	for i, w := range want {
		kept = append(kept, dotvirtv1alpha1.AdminObject{
			Purpose: w.Purpose, Kind: w.Object.GetKind(), Name: w.Object.GetName(), Namespace: w.Object.GetNamespace(), State: string(states[i]),
		})
	}
	dv.Status.AdminObjects = kept
}
