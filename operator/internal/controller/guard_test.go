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
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	dv.Status.AdminObjects = prior
	return guardPassOn(t, operator, dv, objs...)
}

// guardPassOn is guardPass over dv as it is.
func guardPassOn(t *testing.T, operator string, dv *dotvirtv1alpha1.Dotvirt, objs ...client.Object) (metav1.Condition, *dotvirtv1alpha1.Dotvirt) {
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
	rc := *guardRC
	if res, err := r.reconcileOperatorGuard(t.Context(), dv, &rc); res != nil || err != nil {
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

// An upgrade moves the operator's RELATED_IMAGE_* digests inside the
// official repositories; the guard installed for the previous ones stays
// Installed, so upgrades stay automatic.
func TestOperatorGuardStaysInstalledAcrossReleases(t *testing.T) {
	previous := guardHere
	previous.DotvirtImage = adminobjects.Repositories[0] + "@sha256:" + strings.Repeat("a", 64)
	previous.ForgejoImage = adminobjects.Repositories[1] + ":11-rootless@sha256:" + strings.Repeat("a", 64)
	t.Setenv("RELATED_IMAGE_DOTVIRT", adminobjects.Repositories[0]+"@sha256:"+strings.Repeat("b", 64))
	t.Setenv("RELATED_IMAGE_FORGEJO", adminobjects.Repositories[1]+":12-rootless@sha256:"+strings.Repeat("b", 64))
	if cond, _ := guardPass(t, guardOperator, nil, live(adminobjects.Render(previous))...); cond.Reason != string(adminobjects.Installed) {
		t.Errorf("after an upgrade: %s %s", cond.Reason, cond.Message)
	}
}

// The pass records whether the guard's Argo policy and its binding are in
// place as rendered, whatever the rest of the guard is: desktop pools wait on
// them alone.
func TestArgoGuardedFollowsItsPolicy(t *testing.T) {
	rendered := adminobjects.Render(guardHere)
	argo := slices.DeleteFunc(slices.Clone(rendered), func(o adminobjects.Object) bool { return o.Object.GetName() != adminobjects.GuardArgoName })
	elsewhere := guardHere
	elsewhere.ArgoController = "other-argocd-application-controller"
	for name, c := range map[string]struct {
		objs []client.Object
		want bool
	}{
		"none":                           {nil, false},
		"the Argo policy alone":          {live(argo), true},
		"the whole guard":                {live(rendered), true},
		"for another controller":         {live(adminobjects.Render(elsewhere)), false},
		"the binding without its policy": {live(argo[1:]), false},
	} {
		cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(c.objs...).Build()
		r := &DotvirtReconciler{Client: cl, Scheme: cl.Scheme(), OperatorUsername: guardOperator}
		rc := *guardRC
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		if _, err := r.reconcileOperatorGuard(t.Context(), dv, &rc); err != nil {
			t.Fatal(err)
		}
		if rc.argoGuarded != c.want {
			t.Errorf("%s: argoGuarded = %v, want %v", name, rc.argoGuarded, c.want)
		}
	}
}

func TestOperatorGuardStates(t *testing.T) {
	elsewhere := guardHere
	elsewhere.ArgoNamespace = "openshift-gitops"
	edited := adminobjects.Render(guardHere)
	deployments := edited[slices.IndexFunc(edited, func(o adminobjects.Object) bool {
		return o.Object.GetKind() == "ValidatingAdmissionPolicy" && o.Object.GetName() == adminobjects.GuardDeploymentsName
	})].Object.Object
	v, _, _ := unstructured.NestedSlice(deployments, "spec", "validations")
	_ = unstructured.SetNestedSlice(deployments, v[:1], "spec", "validations")
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

	// Delegated: the forge application the spec names.
	delegated := dv.DeepCopy()
	delegated.Spec.Forge.PullRequests, delegated.Spec.Forge.URL = "Delegated", "https://forge.example"
	delegated.Spec.Forge.UserAuth = &dotvirtv1alpha1.ForgeUserAuthSpec{ClientID: "dotvirt", AuthorizeURL: "https://forge.example/login/oauth/authorize",
		TokenURL: "https://forge.internal/token", ClientSecret: dotvirtv1alpha1.SecretKeyRef{Name: "forge-app", Key: "client-secret"}}
	if p := r.guardParams(t.Context(), delegated, guardRC); !p.ForgeDelegated || p.ForgeURL != "https://forge.example" || p.ForgeClientID != "dotvirt" ||
		p.ForgeAuthorizeURL != "https://forge.example/login/oauth/authorize" || p.ForgeTokenURL != "https://forge.internal/token" ||
		p.ForgeClientSecret != "forge-app" || p.ForgeClientSecretKey != "client-secret" {
		t.Errorf("Delegated params = %+v", p)
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

// A pin the spec gets wrong is named, never its value, with where to fix it.
func TestOperatorGuardNamesAMalformedPin(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	dv.Spec.Branding.ConfigMap = "Not_A_Name"
	cond, _ := guardPassOn(t, guardOperator, dv)
	if cond.Reason != "PinMalformed" || !strings.Contains(cond.Message, "branding ConfigMap") || strings.Contains(cond.Message, "Not_A_Name") {
		t.Errorf("%s: %s", cond.Reason, cond.Message)
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

// credentialInstall is an install whose forge credential is an admin's
// Secret, and the guard as rendered for it, installed.
func credentialInstall() (*dotvirtv1alpha1.Dotvirt, adminobjects.Params) {
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt", UID: "uid-x"}}
	dv.Spec.Forge.CredentialsSecret = "creds"
	p := guardHere
	p.ForgeSecret, p.InstallName, p.InstallUID = "creds", "x", "uid-x"
	return dv, p
}

// The forge credential's grant is listed with the guard, but only the guard
// decides OperatorGuard: a missing grant is ForgeCredentialReady's to report.
func TestOperatorGuardIgnoresTheCredentialGrant(t *testing.T) {
	dv, p := credentialInstall()
	cond, dv := guardPassOn(t, guardOperator, dv, live(adminobjects.Guard(p))...)
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("OperatorGuard = %s %s (%s), want True: the guard is installed", cond.Status, cond.Reason, cond.Message)
	}
	var grant []string
	for _, e := range dv.Status.AdminObjects {
		if e.Name == adminobjects.CredentialGrantName {
			grant = append(grant, e.Kind+" "+e.State)
		}
	}
	if !slices.Equal(grant, []string{"Role NotInstalled", "RoleBinding NotInstalled"}) {
		t.Errorf("status lists the grant as %v, want both NotInstalled", grant)
	}
}

// Each pass replaces its own listing, the grant included: status lists every
// admin object once, however many passes ran.
func TestOperatorGuardListsEachAdminObjectOnce(t *testing.T) {
	dv, p := credentialInstall()
	objs := live(adminobjects.Guard(p))
	for range 3 {
		_, dv = guardPassOn(t, guardOperator, dv, objs...)
	}
	if want := len(adminobjects.Render(p)); len(dv.Status.AdminObjects) != want {
		t.Errorf("after three passes status lists %d admin objects, want %d", len(dv.Status.AdminObjects), want)
	}
}

// A grant left after the credential went back to the operator's own keeps
// reaching the old Secret: status lists it Stale, to remove.
func TestOperatorGuardListsAStaleCredentialGrant(t *testing.T) {
	dv, p := credentialInstall()
	role, binding := adminobjects.CredentialGrant(p)
	dv.Spec.Forge.CredentialsSecret = ""
	_, dv = guardPassOn(t, guardOperator, dv, append(live(adminobjects.Render(guardHere)), role, binding)...)
	var stale []string
	for _, e := range dv.Status.AdminObjects {
		if e.State == string(adminobjects.Stale) {
			stale = append(stale, e.Kind+" "+e.Namespace+"/"+e.Name)
		}
	}
	if !slices.Equal(stale, []string{"Role dotvirt/" + adminobjects.CredentialGrantName, "RoleBinding dotvirt/" + adminobjects.CredentialGrantName}) {
		t.Errorf("stale entries %v, want the grant's Role and RoleBinding", stale)
	}
}
