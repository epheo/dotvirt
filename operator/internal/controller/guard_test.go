package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/guard"
)

const guardOperator = "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller"

// guardPass runs the guard phase once over objs, failing the test on any
// write: the operator never writes its guard.
func guardPass(t *testing.T, operator string, objs ...client.Object) (metav1.Condition, *dotvirtv1alpha1.Dotvirt) {
	t.Helper()
	wrote := func(verb string) error {
		t.Errorf("the guard phase must write nothing, but it called %s", verb)
		return nil
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return wrote("create")
		},
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return wrote("patch")
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return wrote("update")
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return wrote("delete")
		},
	}).Build()
	r := &DotvirtReconciler{Client: c, Scheme: c.Scheme(), OperatorUsername: operator}
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	if res, err := r.reconcileOperatorGuard(t.Context(), dv, &reconcileCtx{argoNS: "argocd"}); res != nil || err != nil {
		t.Fatalf("the guard phase never halts: %v %v", res, err)
	}
	cond := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionOperatorGuard)
	if cond == nil {
		t.Fatal("no OperatorGuard condition")
	}
	return *cond, dv
}

func installed(s guard.Set) []client.Object {
	var out []client.Object
	for _, o := range s.Policies {
		out = append(out, o)
	}
	for _, o := range s.Bindings {
		out = append(out, o)
	}
	return out
}

func TestOperatorGuardStates(t *testing.T) {
	here := guard.Params{Operator: guardOperator, Namespace: "dotvirt", ArgoNamespace: "argocd"}
	elsewhere := here
	elsewhere.ArgoNamespace = "openshift-gitops"
	edited := guard.Render(here)
	edited.Policies[1].Spec.Validations = edited.Policies[1].Spec.Validations[:1]
	for name, c := range map[string]struct {
		objs   []client.Object
		status metav1.ConditionStatus
		reason guard.State
	}{
		"none installed":                {nil, metav1.ConditionFalse, guard.NotInstalled},
		"installed as rendered":         {installed(guard.Render(here)), metav1.ConditionTrue, guard.Installed},
		"rendered for another ArgoCD":   {installed(guard.Render(elsewhere)), metav1.ConditionFalse, guard.Outdated},
		"a Deployment rule edited away": {installed(edited), metav1.ConditionFalse, guard.Modified},
	} {
		t.Run(name, func(t *testing.T) {
			cond, dv := guardPass(t, guardOperator, c.objs...)
			if cond.Status != c.status || cond.Reason != string(c.reason) {
				t.Errorf("%s %s: %s", cond.Status, cond.Reason, cond.Message)
			}
			if c.status != metav1.ConditionTrue && !strings.Contains(cond.Message, "a cluster admin installs it from the console") {
				t.Errorf("message %q gives no next action", cond.Message)
			}
			if want, _ := guard.Render(here).ApplyCommand(); dv.Status.OperatorGuard != want {
				t.Errorf("status.operatorGuard = %q, want the render's command", dv.Status.OperatorGuard)
			}
		})
	}
}

func TestOperatorGuardNeedsItsAccount(t *testing.T) {
	cond, dv := guardPass(t, "")
	if cond.Status != metav1.ConditionUnknown || cond.Reason != "AccountUnknown" || dv.Status.OperatorGuard != "" {
		t.Errorf("%s %s, status %q", cond.Status, cond.Reason, dv.Status.OperatorGuard)
	}
}

// The guard admits dotvirt's own accounts by name; they must stay the ones
// its Deployments run as, and the console must learn every pin.
func TestOperatorGuardMatchesTheOperand(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	for _, sa := range []string{
		install.Deployment(dv, "argocd", nil).Spec.Template.Spec.ServiceAccountName,
		install.ForgejoDeployment(dv, false, "", "").Spec.Template.Spec.ServiceAccountName,
	} {
		if !slices.Contains(guard.OperandAccounts, sa) {
			t.Errorf("Deployment account %q is not one the guard admits (%v)", sa, guard.OperandAccounts)
		}
	}
	dep := install.WithOperatorGuard(install.Deployment(dv, "argocd", nil), guardOperator)
	env := map[string]string{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["DOTVIRT_OPERATOR_USERNAME"] != guardOperator || env["DOTVIRT_INSTALL_NAMESPACE"] != "dotvirt" || env["DOTVIRT_ARGO_NAMESPACE"] != "argocd" {
		t.Errorf("the console misses a guard pin: %v", env)
	}
}
