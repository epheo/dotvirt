package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/adminobjects"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

const guardOperator = "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller"

var guardRC = &reconcileCtx{argoNS: "argocd", argoSA: "argocd-application-controller"}

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
	if res, err := r.reconcileOperatorGuard(t.Context(), dv, guardRC); res != nil || err != nil {
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

var guardHere = adminobjects.Params{Operator: guardOperator, Namespace: "dotvirt", ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
	ForgeSecret: adminobjects.DefaultForgeSecret}

func TestOperatorGuardStates(t *testing.T) {
	elsewhere := guardHere
	elsewhere.ArgoNamespace = "openshift-gitops"
	edited := adminobjects.Render(guardHere)
	v, _, _ := unstructured.NestedSlice(edited[1].Object.Object, "spec", "validations")
	_ = unstructured.SetNestedSlice(edited[1].Object.Object, v[:1], "spec", "validations")
	for name, c := range map[string]struct {
		objs   []client.Object
		status metav1.ConditionStatus
		state  adminobjects.State
	}{
		"none installed":                {nil, metav1.ConditionFalse, adminobjects.NotInstalled},
		"installed as rendered":         {live(adminobjects.Render(guardHere)), metav1.ConditionTrue, adminobjects.Installed},
		"rendered for another ArgoCD":   {live(adminobjects.Render(elsewhere)), metav1.ConditionFalse, adminobjects.Outdated},
		"a Deployment rule edited away": {live(edited), metav1.ConditionFalse, adminobjects.Outdated},
	} {
		t.Run(name, func(t *testing.T) {
			cond, dv := guardPass(t, guardOperator, nil, c.objs...)
			if cond.Status != c.status || cond.Reason != string(c.state) {
				t.Errorf("%s %s: %s", cond.Status, cond.Reason, cond.Message)
			}
			if c.status != metav1.ConditionTrue && (!strings.Contains(cond.Message, "a cluster admin installs it from the console") ||
				!strings.Contains(cond.Message, "install/guard.yaml") || strings.Contains(cond.Message, guardOperator)) {
				t.Errorf("message %q: want the next action, and no value an admin would copy from the operator", cond.Message)
			}
			want := adminobjects.Render(guardHere)
			if len(dv.Status.AdminObjects) != len(want) {
				t.Fatalf("status lists %d admin objects, want %d", len(dv.Status.AdminObjects), len(want))
			}
			for i, e := range dv.Status.AdminObjects {
				if e.Purpose == "" || e.Kind != want[i].Object.GetKind() || e.Name != want[i].Object.GetName() {
					t.Errorf("entry %d = %+v, want %s %s", i, e, want[i].Object.GetKind(), want[i].Object.GetName())
				}
				if c.state == adminobjects.Installed && e.State != string(adminobjects.Installed) {
					t.Errorf("entry %d is %s", i, e.State)
				}
			}
		})
	}
}

// The guard's pins follow what an admin chose: an image set in spec reads
// Outdated until the guard is installed for it.
func TestOperatorGuardFollowsTheAdminImage(t *testing.T) {
	t.Setenv("RELATED_IMAGE_DOTVIRT", "quay.io/epheo/dotvirt@sha256:"+strings.Repeat("a", 64))
	domain := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "config.openshift.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": "cluster"},
		"spec": map[string]any{"domain": "apps.example"},
	}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(domain).Build()
	r := &DotvirtReconciler{Client: c, OperatorUsername: guardOperator, Platform: platform.OpenShift}
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	dv.Spec.Image = "registry.example/team/dotvirt:custom"
	p := r.guardParams(t.Context(), dv, guardRC)
	if p.SpecImage != dv.Spec.Image || p.DotvirtImage != "quay.io/epheo/dotvirt@sha256:"+strings.Repeat("a", 64) || p.ForgejoImage != "" ||
		p.ConsoleHost != "dotvirt-dotvirt.apps.example" {
		t.Errorf("params = %+v", p)
	}
	dv.Spec.Ingress.Host = "console.example"
	if p := r.guardParams(t.Context(), dv, guardRC); p.ConsoleHost != "console.example" {
		t.Errorf("an admin's host: %q", p.ConsoleHost)
	}

	// No ingress domain served (MicroShift): the host routers report for the
	// console's Route.
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "route.openshift.io/v1", "kind": "Route", "metadata": map[string]any{"name": "dotvirt", "namespace": "dotvirt"},
		"status": map[string]any{"ingress": []any{map[string]any{"host": "dotvirt-dotvirt.apps.micro"}, map[string]any{"host": "dotvirt-dotvirt.apps.micro"}}},
	}}
	r.Client = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(route).Build()
	dv.Spec.Ingress.Host = ""
	if p := r.guardParams(t.Context(), dv, guardRC); p.ConsoleHost != "dotvirt-dotvirt.apps.micro" {
		t.Errorf("the router's host: %q", p.ConsoleHost)
	}
	r.Platform = platform.Kubernetes
	if p := r.guardParams(t.Context(), dv, guardRC); p.ConsoleHost != "" {
		t.Errorf("no router: %q", p.ConsoleHost)
	}
}

// The guard's entries replace its own, never another phase's.
func TestOperatorGuardKeepsOtherAdminObjects(t *testing.T) {
	other := dotvirtv1alpha1.AdminObject{Purpose: "another phase's", Kind: "Role", Name: "r", Namespace: "dotvirt", State: "NotInstalled"}
	stale := dotvirtv1alpha1.AdminObject{Purpose: "old", Kind: "ValidatingAdmissionPolicy", Name: adminobjects.GuardSecretsName, State: "Installed"}
	_, dv := guardPass(t, guardOperator, []dotvirtv1alpha1.AdminObject{other, stale})
	if len(dv.Status.AdminObjects) != 1+2*len(adminobjects.GuardNames) || dv.Status.AdminObjects[0].Purpose != "another phase's" {
		t.Errorf("entries = %+v, want the other phase's kept and the guard's own", dv.Status.AdminObjects)
	}
	_, dv = guardPass(t, "", []dotvirtv1alpha1.AdminObject{other, stale})
	if len(dv.Status.AdminObjects) != 1 {
		t.Errorf("no account: entries = %+v, want only the other phase's", dv.Status.AdminObjects)
	}
}

func TestOperatorGuardNeedsItsAccount(t *testing.T) {
	cond, dv := guardPass(t, "", nil)
	if cond.Status != metav1.ConditionUnknown || cond.Reason != "AccountUnknown" || len(dv.Status.AdminObjects) != 0 {
		t.Errorf("%s %s, status %+v", cond.Status, cond.Reason, dv.Status.AdminObjects)
	}
}

// What the guard admits by name must stay what the operator renders: its
// Deployments' accounts, the platform sync account, and every pin the
// console needs.
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
	if adminobjects.PlatformSyncAccount != tenancy.PlatformSyncAccount {
		t.Errorf("guard names %s, tenancy %s", adminobjects.PlatformSyncAccount, tenancy.PlatformSyncAccount)
	}
	env := map[string]string{}
	for _, e := range install.Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	// Where the install is, for the console to read it under an admin's token;
	// no pin rides the env, which the operator writes.
	if env["DOTVIRT_INSTALL_NAMESPACE"] != "dotvirt" || env["DOTVIRT_INSTALL_NAME"] != "x" ||
		env["DOTVIRT_OPERATOR_USERNAME"] != "" || env["DOTVIRT_ARGO_CONTROLLER"] != "" {
		t.Errorf("console env = %v", env)
	}
}
