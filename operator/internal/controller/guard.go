package controller

import (
	"context"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/guard"
)

// guardNextAction is what a cluster admin does about a guard not in place.
const guardNextAction = "a cluster admin installs it from the console (Install operator guard), or runs the command in status.operatorGuard"

// reconcileOperatorGuard reports whether the admission policies bounding the
// operator's own account are installed as rendered for this install: its
// Secrets only in the install and ArgoCD namespaces and never a token, its
// Deployments only in the install namespace, as dotvirt's own accounts, off
// the host. The operator reads them and never writes them: an operator that
// could install its guard could loosen it. Best-effort: never a halt, never
// Available.
func (r *DotvirtReconciler) reconcileOperatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.operatorGuard(ctx, dv, rc.argoNS)
	r.setCondition(dv, dotvirtv1alpha1.ConditionOperatorGuard, status, reason, msg)
	return nil, nil
}

func (r *DotvirtReconciler) operatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) (metav1.ConditionStatus, string, string) {
	p := guard.Params{Operator: r.OperatorUsername, Namespace: dv.Namespace, ArgoNamespace: argoNS}
	if !p.Valid() {
		dv.Status.OperatorGuard = ""
		return metav1.ConditionUnknown, "AccountUnknown",
			"the operator does not know the account it runs as (no mounted ServiceAccount token, as when it runs outside its pod), so no guard can pin it"
	}
	want := guard.Render(p)
	cmd, err := want.ApplyCommand()
	if err != nil {
		return metav1.ConditionUnknown, "RenderFailed", err.Error()
	}
	dv.Status.OperatorGuard = cmd
	policies := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	bindings := map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	for _, name := range guard.Names {
		pol, bind := &admissionregistrationv1.ValidatingAdmissionPolicy{}, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
		for _, o := range []client.Object{pol, bind} {
			err := r.reader().Get(ctx, client.ObjectKey{Name: name}, o)
			switch {
			case meta.IsNoMatchError(err):
				return metav1.ConditionUnknown, "Unsupported",
					"the cluster does not serve admissionregistration.k8s.io/v1 ValidatingAdmissionPolicy (Kubernetes 1.30+), so no guard can bound the operator"
			case apierrors.IsNotFound(err):
				continue
			case err != nil:
				return metav1.ConditionUnknown, "Unreadable", "could not read the operator guard: " + err.Error()
			}
			if o == client.Object(pol) {
				policies[name] = pol
			} else {
				bindings[name] = bind
			}
		}
	}
	switch state, detail := guard.Compare(want, policies, bindings); state {
	case guard.Installed:
		return metav1.ConditionTrue, string(state), "the admission policies bounding " + p.Operator + " are installed as rendered"
	default:
		return metav1.ConditionFalse, string(state), detail + "; " + guardNextAction
	}
}
