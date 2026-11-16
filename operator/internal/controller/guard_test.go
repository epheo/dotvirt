package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

const guardOperator = "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller"

// guardPass runs the guard phase once over objs, after an earlier pass left
// prior in status, failing the test on any write: the operator never writes
// its guard.
func guardPass(t *testing.T, operator string, prior []dotvirtv1alpha1.AdminObject, objs ...client.Object) (metav1.Condition, *dotvirtv1alpha1.Dotvirt) {
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
	dv.Status.AdminObjects = prior
	if res, err := r.reconcileOperatorGuard(t.Context(), dv, &reconcileCtx{argoNS: "argocd"}); res != nil || err != nil {
		t.Fatalf("the guard phase never halts: %v %v", res, err)
	}
	cond := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionOperatorGuard)
	if cond == nil {
		t.Fatal("no OperatorGuard condition")
	}
	return *cond, dv
}

// live is objs as the apiserver would hold them.
func live(objs []adminobjects.Object) []client.Object {
	var out []client.Object
	for _, o := range objs {
		out = append(out, o.Object.DeepCopy())
	}
	return out
}

func TestOperatorGuardStates(t *testing.T) {
	here := adminobjects.GuardParams{Operator: guardOperator, Namespace: "dotvirt", ArgoNamespace: "argocd"}
	elsewhere := here
	elsewhere.ArgoNamespace = "openshift-gitops"
	edited := adminobjects.Guard(here)
	v, _, _ := unstructured.NestedSlice(edited[1].Object.Object, "spec", "validations")
	_ = unstructured.SetNestedSlice(edited[1].Object.Object, v[:1], "spec", "validations")
	for name, c := range map[string]struct {
		objs   []client.Object
		status metav1.ConditionStatus
		state  adminobjects.State
	}{
		"none installed":                {nil, metav1.ConditionFalse, adminobjects.NotInstalled},
		"installed as rendered":         {live(adminobjects.Guard(here)), metav1.ConditionTrue, adminobjects.Installed},
		"rendered for another ArgoCD":   {live(adminobjects.Guard(elsewhere)), metav1.ConditionFalse, adminobjects.Outdated},
		"a Deployment rule edited away": {live(edited), metav1.ConditionFalse, adminobjects.Outdated},
	} {
		t.Run(name, func(t *testing.T) {
			cond, dv := guardPass(t, guardOperator, nil, c.objs...)
			if cond.Status != c.status || cond.Reason != string(c.state) {
				t.Errorf("%s %s: %s", cond.Status, cond.Reason, cond.Message)
			}
			if c.status != metav1.ConditionTrue && !strings.Contains(cond.Message, "a cluster admin installs it from the console") {
				t.Errorf("message %q gives no next action", cond.Message)
			}
			want := adminobjects.Guard(here)
			if m, _ := adminobjects.Manifest(want); len(dv.Status.AdminObjects) != len(want) || dv.Status.AdminManifest != string(m) {
				t.Fatalf("status lists %d admin objects (manifest %q), want %d and their manifest", len(dv.Status.AdminObjects), dv.Status.AdminManifest, len(want))
			}
			for i, e := range dv.Status.AdminObjects {
				var got map[string]any
				if err := json.Unmarshal(e.Object.Raw, &got); err != nil {
					t.Fatal(err)
				}
				if e.Purpose == "" || !equalJSON(t, got, want[i].Object.Object) {
					t.Errorf("entry %d is not the render: %q %v", i, e.Purpose, got["metadata"])
				}
			}
			if c.state == adminobjects.Installed && slices.ContainsFunc(dv.Status.AdminObjects, func(e dotvirtv1alpha1.AdminObject) bool {
				return e.State != string(adminobjects.Installed)
			}) {
				t.Errorf("an entry is not Installed: %+v", dv.Status.AdminObjects)
			}
		})
	}
}

func equalJSON(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// The guard's entries replace its own, never another phase's.
func TestOperatorGuardKeepsOtherAdminObjects(t *testing.T) {
	other := dotvirtv1alpha1.AdminObject{Purpose: "another phase's", State: "NotInstalled",
		Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"r","namespace":"dotvirt"}}`)}}
	stale := dotvirtv1alpha1.AdminObject{Purpose: "old", State: "Installed",
		Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"admissionregistration.k8s.io/v1","kind":"ValidatingAdmissionPolicy","metadata":{"name":"` + adminobjects.GuardSecretsName + `"}}`)}}
	_, dv := guardPass(t, guardOperator, []dotvirtv1alpha1.AdminObject{other, stale})
	if len(dv.Status.AdminObjects) != 5 || dv.Status.AdminObjects[0].Purpose != "another phase's" {
		t.Errorf("entries = %+v, want the other phase's kept and the guard's four replaced", dv.Status.AdminObjects)
	}
	_, dv = guardPass(t, "", []dotvirtv1alpha1.AdminObject{other, stale})
	if len(dv.Status.AdminObjects) != 1 {
		t.Errorf("no account: entries = %+v, want only the other phase's", dv.Status.AdminObjects)
	}
}

func TestOperatorGuardNeedsItsAccount(t *testing.T) {
	cond, dv := guardPass(t, "", nil)
	if cond.Status != metav1.ConditionUnknown || cond.Reason != "AccountUnknown" || len(dv.Status.AdminObjects) != 0 || dv.Status.AdminManifest != "" {
		t.Errorf("%s %s, status %+v", cond.Status, cond.Reason, dv.Status.AdminObjects)
	}
}

// The guard admits dotvirt's own accounts by name; they must stay the ones
// its Deployments run as, and the console must learn where the install is.
func TestOperatorGuardMatchesTheOperand(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	for _, sa := range []string{
		install.Deployment(dv, "argocd", nil).Spec.Template.Spec.ServiceAccountName,
		install.ForgejoDeployment(dv, false, "", "").Spec.Template.Spec.ServiceAccountName,
	} {
		if !slices.Contains(adminobjects.OperandAccounts, sa) {
			t.Errorf("Deployment account %q is not one the guard admits (%v)", sa, adminobjects.OperandAccounts)
		}
	}
	env := map[string]string{}
	for _, e := range install.Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["DOTVIRT_INSTALL_NAMESPACE"] != "dotvirt" || env["DOTVIRT_INSTALL_NAME"] != "x" {
		t.Errorf("the console cannot find its install: %v", env)
	}
}
