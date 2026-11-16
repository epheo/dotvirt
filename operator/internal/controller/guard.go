package controller

import (
	"context"
	"fmt"
	"os"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
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
func (r *DotvirtReconciler) guardParams(dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) adminobjects.Params {
	return adminobjects.Params{
		Operator: r.OperatorUsername, Namespace: dv.Namespace, ArgoNamespace: rc.argoNS, ArgoController: rc.argoSA,
		SpecImage: dv.Spec.Image, DotvirtImage: os.Getenv("RELATED_IMAGE_DOTVIRT"), ForgejoImage: os.Getenv("RELATED_IMAGE_FORGEJO"),
	}
}

func (r *DotvirtReconciler) operatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (metav1.ConditionStatus, string, string) {
	isGuard := func(e dotvirtv1alpha1.AdminObject) bool {
		return (e.Kind == "ValidatingAdmissionPolicy" || e.Kind == "ValidatingAdmissionPolicyBinding") && slices.Contains(adminobjects.GuardNames, e.Name)
	}
	p := r.guardParams(dv, rc)
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
			return metav1.ConditionFalse, string(s), details[i] + "; " + guardNextAction(p)
		}
	}
	return metav1.ConditionTrue, string(adminobjects.Installed), "the admission policies bounding " + p.Operator + " are installed as rendered"
}

// guardNextAction is what a cluster admin does about a guard not in place,
// with this install's values for the release template.
func guardNextAction(p adminobjects.Params) string {
	return fmt.Sprintf("a cluster admin installs it from the console (Install), or fills the release's install/guard.yaml with "+
		"OPERATOR_USERNAME=%s INSTALL_NAMESPACE=%s ARGO_NAMESPACE=%s ARGO_CONTROLLER=%s SPEC_IMAGE=%q RELATED_IMAGE_DOTVIRT=%q RELATED_IMAGE_FORGEJO=%q and applies it",
		p.Operator, p.Namespace, p.ArgoNamespace, p.ArgoController, p.SpecImage, p.DotvirtImage, p.ForgejoImage)
}

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
