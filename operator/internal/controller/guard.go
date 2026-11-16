package controller

import (
	"context"
	"encoding/json"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// adminNextAction is what a cluster admin does about an admin object not in
// place.
const adminNextAction = "a cluster admin installs it from the console (Install), or saves status.adminManifest to a file and applies it with `" +
	adminobjects.ApplyCommand + " <file>`"

// reconcileOperatorGuard reports whether the admission policies bounding the
// operator's own account are installed as rendered for this install, and
// lists them in status for a cluster admin to install. The operator reads
// them and never writes them: an operator that could install its guard could
// loosen it. Best-effort: never a halt, never Available.
func (r *DotvirtReconciler) reconcileOperatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.operatorGuard(ctx, dv, rc.argoNS)
	r.setCondition(dv, dotvirtv1alpha1.ConditionOperatorGuard, status, reason, msg)
	return nil, nil
}

func (r *DotvirtReconciler) operatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) (metav1.ConditionStatus, string, string) {
	isGuard := func(o *unstructured.Unstructured) bool {
		return o.GroupVersionKind().Group == "admissionregistration.k8s.io" && slices.Contains(adminobjects.GuardNames, o.GetName())
	}
	p := adminobjects.GuardParams{Operator: r.OperatorUsername, Namespace: dv.Namespace, ArgoNamespace: argoNS}
	if !p.Valid() {
		publishAdminObjects(dv, isGuard, nil, nil)
		return metav1.ConditionUnknown, "AccountUnknown",
			"the operator does not know the account it runs as (no mounted ServiceAccount token, as when it runs outside its pod), so no guard can pin it"
	}
	want := adminobjects.Guard(p)
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
			return metav1.ConditionFalse, string(s), details[i] + "; " + adminNextAction
		}
	}
	return metav1.ConditionTrue, string(adminobjects.Installed), "the admission policies bounding " + p.Operator + " are installed as rendered"
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

// publishAdminObjects replaces the status entries owned matches with want,
// each at its state, and the apply command with one for every entry. A phase
// that renders admin objects publishes its own, so phases never drop each
// other's.
func publishAdminObjects(dv *dotvirtv1alpha1.Dotvirt, owned func(*unstructured.Unstructured) bool, want []adminobjects.Object, states []adminobjects.State) {
	var kept []dotvirtv1alpha1.AdminObject
	for _, e := range dv.Status.AdminObjects {
		var o unstructured.Unstructured
		if json.Unmarshal(e.Object.Raw, &o.Object) == nil && !owned(&o) {
			kept = append(kept, e)
		}
	}
	for i, w := range want {
		raw, err := json.Marshal(w.Object.Object)
		if err != nil {
			continue
		}
		kept = append(kept, dotvirtv1alpha1.AdminObject{Purpose: w.Purpose, State: string(states[i]), Object: runtime.RawExtension{Raw: raw}})
	}
	dv.Status.AdminObjects = kept
	dv.Status.AdminManifest = ""
	var all []adminobjects.Object
	for _, e := range kept {
		o := &unstructured.Unstructured{}
		if err := json.Unmarshal(e.Object.Raw, &o.Object); err != nil {
			continue
		}
		all = append(all, adminobjects.Object{Purpose: e.Purpose, Object: o})
	}
	if len(all) > 0 {
		if b, err := adminobjects.Manifest(all); err == nil {
			dv.Status.AdminManifest = string(b)
		}
	}
}
