package controller

import (
	"context"
	"errors"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

const guardAccount = "dotvirt-operator-controller"

// guardPass runs the guard phase once over objs. Any write but a create fails
// the test: the operator holds no patch, update or delete on the guard.
func guardPass(t *testing.T, account string, create func(client.Object) error, objs ...client.Object) (metav1.Condition, client.Client, int) {
	t.Helper()
	creates := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			if create != nil {
				if err := create(obj); err != nil {
					return err
				}
			}
			return cl.Create(ctx, obj, opts...)
		},
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			t.Error("the guard is never patched")
			return errors.New("patch")
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			t.Error("the guard is never updated")
			return errors.New("update")
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			t.Error("the guard is never deleted")
			return errors.New("delete")
		},
	}).Build()
	r := &DotvirtReconciler{Client: c, Scheme: c.Scheme(), OperatorServiceAccount: account}
	dv := &dotvirtv1alpha1.Dotvirt{}
	if res, err := r.reconcileOperatorGuard(t.Context(), dv, nil); res != nil || err != nil {
		t.Fatalf("the guard phase never halts: %v %v", res, err)
	}
	cond := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionOperatorGuard)
	if cond == nil {
		t.Fatal("no OperatorGuard condition")
	}
	return *cond, c, creates
}

func TestOperatorGuardIsCreatedOnce(t *testing.T) {
	cond, c, creates := guardPass(t, guardAccount, nil)
	if cond.Status != metav1.ConditionTrue || creates != 2 {
		t.Fatalf("first pass: %s %s, %d creates; want True after 2", cond.Status, cond.Message, creates)
	}
	var p admissionregistrationv1.ValidatingAdmissionPolicy
	if err := c.Get(t.Context(), client.ObjectKey{Name: install.OperatorGuardName}, &p); err != nil {
		t.Fatal(err)
	}
	var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if err := c.Get(t.Context(), client.ObjectKey{Name: install.OperatorGuardName}, &b); err != nil {
		t.Fatal(err)
	}
	cond, _, creates = guardPass(t, guardAccount, nil, &p, &b)
	if cond.Status != metav1.ConditionTrue || creates != 0 {
		t.Errorf("second pass: %s %s, %d creates; want True and no write", cond.Status, cond.Message, creates)
	}
}

// A changed guard is reported, never rewritten: the operator cannot loosen
// what bounds it, so it cannot repair it either.
func TestOperatorGuardReportsAChangedPolicy(t *testing.T) {
	weakened := install.OperatorGuardPolicy(guardAccount)
	weakened.Spec.Validations = weakened.Spec.Validations[:1]
	other := install.OperatorGuardPolicy("someone-else")
	warnOnly := install.OperatorGuardBinding()
	warnOnly.Spec.ValidationActions = []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn}
	narrowed := install.OperatorGuardBinding()
	narrowed.Spec.MatchResources = &admissionregistrationv1.MatchResources{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"nowhere": "true"}},
	}
	for name, objs := range map[string][]client.Object{
		"a validation dropped": {weakened, install.OperatorGuardBinding()},
		"another account":      {other, install.OperatorGuardBinding()},
		"a warn-only binding":  {install.OperatorGuardPolicy(guardAccount), warnOnly},
		"a narrowed binding":   {install.OperatorGuardPolicy(guardAccount), narrowed},
	} {
		t.Run(name, func(t *testing.T) {
			cond, _, creates := guardPass(t, guardAccount, nil, objs...)
			if cond.Status != metav1.ConditionFalse || cond.Reason != "Modified" || creates != 0 {
				t.Errorf("%s %s %s, %d creates; want False Modified and no write", cond.Status, cond.Reason, cond.Message, creates)
			}
		})
	}
}

func TestOperatorGuardNeedsItsAccountAndItsRights(t *testing.T) {
	cond, _, creates := guardPass(t, "", nil)
	if cond.Status != metav1.ConditionUnknown || cond.Reason != "AccountUnknown" || creates != 0 {
		t.Errorf("no account: %s %s, %d creates", cond.Status, cond.Reason, creates)
	}
	forbidden := func(client.Object) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"}, install.OperatorGuardName, errors.New("no"))
	}
	cond, _, _ = guardPass(t, guardAccount, forbidden)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "Forbidden" {
		t.Errorf("forbidden: %s %s %s", cond.Status, cond.Reason, cond.Message)
	}
}
