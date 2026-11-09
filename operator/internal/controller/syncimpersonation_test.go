package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

const (
	argoSAUser   = "system:serviceaccount:argocd:"
	controllerSA = "argocd-application-controller"
)

// reviews answers SubjectAccessReviews with allow and counts them; every
// other Create reaches the fake.
func reviews(allow func(account string, a *authzv1.ResourceAttributes) bool, n *atomic.Int64) interceptor.Funcs {
	return interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		sar, ok := obj.(*authzv1.SubjectAccessReview)
		if !ok {
			return cl.Create(ctx, obj, opts...)
		}
		n.Add(1)
		if !slices.Contains(sar.Spec.Groups, "system:serviceaccounts:argocd") {
			return nil
		}
		sar.Status.Allowed = allow(strings.TrimPrefix(sar.Spec.User, argoSAUser), sar.Spec.ResourceAttributes)
		if sar.Status.Allowed {
			sar.Status.Reason = "allowed by the test's grant"
		}
		return nil
	}}
}

// granted is the cluster the platform repo's grants make: each project's sync
// account may write in its own namespaces only, the platform account holds its
// operand roles, the controller may impersonate them, nothing else.
func granted(projects map[string][]string) func(string, *authzv1.ResourceAttributes) bool {
	return func(account string, a *authzv1.ResourceAttributes) bool {
		switch account {
		case tenancy.PlatformSyncAccount:
			return true
		case controllerSA:
			return a.Verb == "impersonate"
		}
		return a.Namespace != "" && slices.Contains(projects[account], a.Namespace)
	}
}

func argoCDInstance(extra map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"server": map[string]any{"route": map[string]any{"enabled": true}}}}}
	u.SetGroupVersionKind(install.ArgoCDGVK)
	u.SetNamespace("argocd")
	u.SetName("argocd")
	if extra != nil {
		u.Object["spec"].(map[string]any)["extraConfig"] = extra
	}
	return u
}

func argoCM(on bool) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"}}
	if on {
		cm.Data = map[string]string{install.SyncImpersonationKey: "true"}
	}
	return cm
}

func extraConfig(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	inst := argoCDInstance(nil)
	if !exists(t, c, inst, "argocd", "argocd") {
		t.Fatal("ArgoCD instance gone")
	}
	if _, ok, _ := unstructured.NestedMap(inst.Object, "spec", "server"); !ok {
		t.Errorf("a field the operator does not own was dropped: %v", inst.Object["spec"])
	}
	extra, _, _ := unstructured.NestedMap(inst.Object, "spec", "extraConfig")
	return extra
}

func syncCond(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt) *metav1.Condition {
	t.Helper()
	got := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionSyncImpersonationReady)
	if got == nil {
		t.Fatal("no SyncImpersonationReady condition")
	}
	return got
}

func optIn(dv *dotvirtv1alpha1.Dotvirt, ignoreForeign bool) *dotvirtv1alpha1.Dotvirt {
	dv.Spec.ArgoCD.SyncImpersonation = dotvirtv1alpha1.SyncImpersonationSpec{Enabled: true, IgnoreForeignApplications: ignoreForeign}
	return dv
}

func foreignApp(ns, name, project, destNS string) *unstructured.Unstructured {
	app := argoObj("Application", ns, name, nil)
	app.Object["spec"] = map[string]any{"project": project, "destination": map[string]any{"server": install.InClusterServer, "namespace": destNS}}
	return app
}

// remote is an Application in project naming its cluster by name.
func remote(project string) *unstructured.Unstructured {
	app := foreignApp("argocd", "edge", project, "edge")
	app.Object["spec"].(map[string]any)["destination"] = map[string]any{"name": "edge-cluster", "namespace": "edge"}
	return app
}

// tenantApp is project's generated Application, reporting a kind it manages
// in ns.
func tenantApp(project, kind, ns string) *unstructured.Unstructured {
	app := application("dotvirt-"+project, tenancy.AppProjectPrefix+project)
	i := slices.IndexFunc(tenancy.TenantKinds, func(k tenancy.Kind) bool { return k.Kind == kind })
	app.Object["status"] = map[string]any{"resources": []any{
		map[string]any{"group": tenancy.TenantKinds[i].Group, "kind": kind, "namespace": ns, "name": "x"},
	}}
	return app
}

func appProject(name string, accounts ...map[string]any) *unstructured.Unstructured {
	ap := argoObj("AppProject", "argocd", name, nil)
	entries := make([]any, 0, len(accounts))
	for _, a := range accounts {
		entries = append(entries, a)
	}
	ap.Object["spec"] = map[string]any{"destinationServiceAccounts": entries}
	return ap
}

func setSpec(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt, edit func(*dotvirtv1alpha1.Dotvirt)) *dotvirtv1alpha1.Dotvirt {
	t.Helper()
	stored := getCR(t, c, dv)
	edit(stored)
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// Every tier names its sync account, by name only: each tenant AppProject its
// project's, the platform AppProject the platform's. No ServiceAccount object
// is made; the platform account holds the apply role and its own.
func TestSyncAccountsRendered(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, creds, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(nil), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)

	for appProject, account := range map[string]string{"dotvirt-p-team-a": "dotvirt-p-team-a", "dotvirt-platform": tenancy.PlatformSyncAccount} {
		ap := argoObj("AppProject", "", "", nil)
		if !exists(t, c, ap, "argocd", appProject) {
			t.Fatalf("AppProject %s missing", appProject)
		}
		entries, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinationServiceAccounts")
		if len(entries) != 1 || entries[0].(map[string]any)["defaultServiceAccount"] != "argocd:"+account || entries[0].(map[string]any)["namespace"] != "*" {
			t.Errorf("%s destinationServiceAccounts = %v, want argocd:%s", appProject, entries, account)
		}
	}
	var sas corev1.ServiceAccountList
	if err := c.List(context.Background(), &sas, client.InNamespace("argocd")); err != nil || len(sas.Items) != 0 {
		t.Errorf("ServiceAccounts in argocd = %d (%v), want none: the accounts are names", len(sas.Items), err)
	}
	var apply rbacv1.ClusterRoleBinding
	exists(t, c, &apply, "", "dotvirt-argocd-apply")
	var names []string
	for _, s := range apply.Subjects {
		names = append(names, s.Namespace+"/"+s.Name)
	}
	if !slices.Equal(names, []string{"argocd/" + controllerSA, "argocd/" + tenancy.PlatformSyncAccount}) {
		t.Errorf("dotvirt-argocd-apply subjects = %v, want the controller and the platform account", names)
	}
	var platformSync rbacv1.ClusterRoleBinding
	if !exists(t, c, &platformSync, "", tenancy.PlatformSyncAccount) || platformSync.RoleRef.Name != tenancy.PlatformSyncAccount {
		t.Errorf("platform sync binding = %+v", platformSync)
	}
	var dep appsv1.Deployment
	exists(t, c, &dep, dv.Namespace, install.AppName)
	if v, ok := envValue(dep.Spec.Template.Spec.Containers[0].Env, "DOTVIRT_ARGO_NAMESPACE"); !ok || v != "argocd" {
		t.Errorf("DOTVIRT_ARGO_NAMESPACE = %q, %v", v, ok)
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionUnknown || got.Reason != "Disabled" {
		t.Errorf("condition = %+v", got)
	}
}

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// The operand roles carry the names pkg/tenancy gives the app's grants.
func TestSyncOperandRoles(t *testing.T) {
	roles := map[string]*rbacv1.ClusterRole{}
	for _, r := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", "operand_roles.yaml")) {
		roles[r.Name] = r
	}
	egress := roles[tenancy.TenantEgressRole]
	if egress == nil || len(egress.Rules) != 1 || !slices.Equal(egress.Rules[0].Resources, []string{"egressfirewalls"}) {
		t.Errorf("%s = %+v, want EgressFirewall writes alone", tenancy.TenantEgressRole, egress)
	}
	platformSync := roles[tenancy.PlatformSyncAccount]
	if platformSync == nil {
		t.Fatalf("no %s role", tenancy.PlatformSyncAccount)
	}
	var bind []string
	for _, rule := range platformSync.Rules {
		if slices.Contains(rule.Verbs, "escalate") || slices.Contains(rule.Resources, "*") {
			t.Errorf("%s grants %+v", tenancy.PlatformSyncAccount, rule)
		}
		if slices.Contains(rule.Verbs, "bind") {
			bind = append(bind, rule.ResourceNames...)
		}
	}
	if !slices.Equal(bind, []string{"admin", tenancy.TenantEgressRole}) {
		t.Errorf("%s binds %v, want exactly the roles project grants name", tenancy.PlatformSyncAccount, bind)
	}
}

// Asked, and every account holds its grants and no more: the operator sets
// the one key on the instance, leaving the rest of its spec alone, and says
// Pending until argocd-cm carries it. Asked no longer: the key goes.
func TestSyncImpersonationFlipAndRollback(t *testing.T) {
	dv := optIn(testCR(), false)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil),
		projectNS("a-prod", "team-a", "dotvirt/team-a.git"), projectNS("a-dev", "team-a", "dotvirt/team-a.git"),
		projectNS("b", "team-b", "dotvirt/team-b.git"),
	).WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"a-dev", "a-prod"}, "dotvirt-p-team-b": {"b"}}), &n)).
		WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	now := time.Now()
	r.now = func() time.Time { return now }
	res := reconcileOnce(t, r, dv)

	if got := extraConfig(t, c)[install.SyncImpersonationKey]; got != "true" {
		t.Fatalf("%s = %v, want true", install.SyncImpersonationKey, got)
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionFalse || got.Reason != "Pending" {
		t.Errorf("condition = %+v, want Pending until argocd-cm carries it", got)
	}
	if got := getCR(t, c, dv).Status.SyncImpersonationInstance; got != "argocd/argocd" {
		t.Errorf("status instance = %q", got)
	}
	if res.RequeueAfter != time.Minute {
		t.Errorf("requeue = %v, want a recheck while Pending", res.RequeueAfter)
	}
	// 2 per project namespace (no repo declares anything yet), 2 per
	// project, the controller's clone and impersonation rights, and every
	// account's clone and tenant-kind rights.
	if got := n.Load(); got != 2*3+2*2+8 {
		t.Errorf("%d reviews, want 18", got)
	}
	// The argocd-operator copies the key into argocd-cm: on.
	if err := c.Create(context.Background(), argoCM(true)); err != nil {
		t.Fatal(err)
	}
	res = reconcileOnce(t, r, dv)
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionTrue || got.Reason != "Enabled" {
		t.Errorf("condition = %+v, want Enabled", got)
	}
	if res.RequeueAfter != driftResync {
		t.Errorf("requeue = %v once on", res.RequeueAfter)
	}
	// A clean verdict serves the passes that follow until its inputs change.
	if got := n.Load(); got != 18 {
		t.Errorf("%d reviews after a second pass, want the cached 18", got)
	}
	// RBAC moves are not watched: an aged verdict is asked again.
	now = now.Add(driftResync / 2)
	reconcileOnce(t, r, dv)
	if got := n.Load(); got != 36 {
		t.Errorf("%d reviews once the verdict aged out, want 36", got)
	}

	stored := setSpec(t, c, dv, func(d *dotvirtv1alpha1.Dotvirt) { d.Spec.ArgoCD.SyncImpersonation.Enabled = false })
	reconcileOnce(t, r, stored)
	if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
		t.Errorf("opted out, extraConfig = %v", extra)
	}
	if got := getCR(t, c, dv); got.Status.SyncImpersonationInstance != "" {
		t.Errorf("opted out, status instance = %q", got.Status.SyncImpersonationInstance)
	}
}

// Held back: a missing grant, a grant reaching past a project's namespaces,
// another team's Application with no account mapped, Applications still in
// the shared tier, a controller that cannot impersonate. Each is said, the
// pass comes back within a minute, and nothing is set.
func TestSyncImpersonationRefusals(t *testing.T) {
	own := map[string][]string{"dotvirt-p-team-a": {"team-a"}}
	or := func(extra func(string, *authzv1.ResourceAttributes) bool) func(string, *authzv1.ResourceAttributes) bool {
		return func(account string, a *authzv1.ResourceAttributes) bool {
			return granted(own)(account, a) || extra(account, a)
		}
	}
	for _, tc := range []struct {
		name  string
		allow func(string, *authzv1.ResourceAttributes) bool
		objs  []client.Object
		inst  *unstructured.Unstructured
		want  string
	}{
		{"missing grant", granted(nil), nil, nil, "project team-a's sync account holds no grant in team-a: stage its grants (POST /api/projects/team-a/sync-access)"},
		{"declared kind beyond the grant", func(account string, a *authzv1.ResourceAttributes) bool {
			return granted(own)(account, a) && a.Resource != "egressfirewalls"
		}, []client.Object{tenantApp("team-a", "EgressFirewall", "team-a")}, nil,
			"project team-a's repo declares egressfirewalls in team-a, which its grant there does not let it write: an admin grant there carries them"},
		{"every account writes a tenant kind cluster-wide", or(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == syncProbeAccount && a.Resource == "networkpolicies" && a.Namespace == ""
		}), nil, nil, "every ServiceAccount of argocd may create networkpolicies in every namespace, every project's sync account included (allowed by the test's grant)"},
		{"name-addressed Application, nothing mapped", granted(own), []client.Object{remote("teamx"), appProject("teamx")}, nil, "argocd/edge"},
		{"cluster-wide grant", or(func(account string, _ *authzv1.ResourceAttributes) bool { return account == "dotvirt-p-team-a" }),
			nil, nil, "may create VirtualMachines in every namespace"},
		{"namespace open to every account", or(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == syncProbeAccount && a.Resource == "virtualmachines"
		}), nil, nil, "namespace team-a lets every ServiceAccount of argocd create VirtualMachines"},
		{"project account clones anywhere", or(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == "dotvirt-p-team-a" && a.Subresource == "source"
		}), nil, nil, "may clone disks out of any namespace"},
		{"every account clones anywhere", or(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == syncProbeAccount && a.Subresource == "source"
		}), nil, nil, "every ServiceAccount of argocd may clone disks"},
		{"controller cannot impersonate", func(account string, a *authzv1.ResourceAttributes) bool {
			return account != controllerSA && granted(own)(account, a)
		}, nil, nil, "cannot impersonate ServiceAccounts in argocd"},
		{"unmapped foreign Application", granted(own),
			[]client.Object{foreignApp("argocd", "billing", "default", "billing"), appProject("default")}, nil, "argocd/billing"},
		{"foreign Application in a source namespace", granted(own),
			[]client.Object{foreignApp("team-apps", "web", "default", "web")},
			func() *unstructured.Unstructured {
				u := argoCDInstance(nil)
				u.Object["spec"].(map[string]any)["sourceNamespaces"] = []any{"/^team-/"}
				return u
			}(), "team-apps/web"},
		{"shared tier in use", granted(own),
			[]client.Object{application("dotvirt-team-a", install.TenantsProjectName), argoObj("AppProject", "argocd", install.TenantsProjectName, install.Labels(testCR()))},
			nil, "shared dotvirt-tenants AppProject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dv := optIn(testCR(), false)
			inst := tc.inst
			if inst == nil {
				inst = argoCDInstance(nil)
			}
			var n atomic.Int64
			c := testBuilder(t).WithObjects(append(tc.objs, dv, inst, projectNS("team-a", "team-a", "dotvirt/team-a.git"))...).
				WithInterceptorFuncs(reviews(tc.allow, &n)).Build()
			res := reconcileOnce(t, newReconciler(c, depsOK), dv)
			if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
				t.Errorf("set while held back: %v", extra)
			}
			got := syncCond(t, c, dv)
			if got.Status != metav1.ConditionFalse || got.Reason != "NotReady" || !strings.Contains(got.Message, tc.want) {
				t.Errorf("condition = %+v, want NotReady saying %q", got, tc.want)
			}
			// Held back on what a person fixes: back as the verdict ages out.
			if res.RequeueAfter != driftResync/2 {
				t.Errorf("requeue = %v, want %v", res.RequeueAfter, driftResync/2)
			}
		})
	}

	// Not held back: an Application whose AppProject maps an account, another
	// instance's, one naming its cluster by name (said, not counted), and a
	// project whose grant is narrower than admin while its repo declares
	// nothing it may not write.
	noEgress := func(account string, a *authzv1.ResourceAttributes) bool {
		return granted(own)(account, a) && a.Resource != "egressfirewalls"
	}
	for name, tc := range map[string]struct {
		objs  []client.Object
		allow func(string, *authzv1.ResourceAttributes) bool
		say   string
	}{
		"mapped AppProject": {[]client.Object{foreignApp("argocd", "billing", "default", "billing"),
			appProject("default", map[string]any{"server": "https://kubernetes.*", "namespace": "{billing,payroll}", "defaultServiceAccount": "argocd:" + controllerSA})}, granted(own), ""},
		"another instance": {[]client.Object{foreignApp("team-argo", "web", "default", "web")}, granted(own), ""},
		"cluster named by name, mapped on a named server": {[]client.Object{remote("default"),
			appProject("default", map[string]any{"server": "https://edge.example", "namespace": "*", "defaultServiceAccount": "argocd:edge-sync"})},
			granted(own), "unchecked: Applications naming their cluster by name (argocd/edge)"},
		"cluster named by name, mapped on every server": {[]client.Object{remote("default"),
			appProject("default", map[string]any{"server": "*", "namespace": "edge", "defaultServiceAccount": "argocd:edge-sync"})},
			granted(own), ""},
		"edit grant, nothing beyond": {[]client.Object{tenantApp("team-a", "VirtualMachine", "team-a")}, noEgress, ""},
	} {
		dv := optIn(testCR(), false)
		var n atomic.Int64
		c := testBuilder(t).WithObjects(append(tc.objs, dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git"))...).
			WithInterceptorFuncs(reviews(tc.allow, &n)).Build()
		reconcileOnce(t, newReconciler(c, depsOK), dv)
		got := syncCond(t, c, dv)
		if extraConfig(t, c)[install.SyncImpersonationKey] != "true" || !strings.Contains(got.Message, tc.say) {
			t.Errorf("%s: condition %+v, want set, saying %q", name, got, tc.say)
		}
	}
	dv := optIn(testCR(), true)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, foreignApp("argocd", "billing", "default", "billing"), argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(own), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Error("ignoreForeignApplications did not proceed")
	}
}

// A platform repo RoleBinding binding a role the platform account may not
// bind fails once Argo applies it as that account: it holds the flip back.
func TestSyncImpersonationPlatformRoles(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	optIn(dv, false)
	platformApp := argoObj("Application", "argocd", install.PlatformApplicationName, nil)
	platformApp.Object["status"] = map[string]any{"resources": []any{
		map[string]any{"group": "rbac.authorization.k8s.io", "kind": "RoleBinding", "namespace": "team-a", "name": "team-a-admins"},
		map[string]any{"group": "rbac.authorization.k8s.io", "kind": "RoleBinding", "namespace": "team-a", "name": "team-a-viewers"},
	}}
	rb := func(name, role string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"},
			RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}}
	}
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, creds, platformApp, rb("team-a-admins", "admin"), rb("team-a-viewers", "view"),
		argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(func(account string, a *authzv1.ResourceAttributes) bool {
			if account == tenancy.PlatformSyncAccount && a.Verb == "bind" {
				return a.Name != "view"
			}
			return granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}})(account, a)
		}, &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Reason != "NotReady" || !strings.Contains(got.Message, "cannot bind ClusterRole view, which RoleBinding team-a/team-a-viewers") {
		t.Errorf("condition = %+v, want the view binding named", got)
	}

	// Once on, a newly merged binding the account cannot apply never exists:
	// the absence is what says so.
	platformApp.Object["status"] = map[string]any{"resources": []any{
		map[string]any{"group": "rbac.authorization.k8s.io", "kind": "RoleBinding", "namespace": "team-a", "name": "team-a-auditors"},
	}}
	c = testBuilder(t).WithObjects(dv, creds, platformApp, argoCM(true), argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Reason != "Degraded" || !strings.Contains(got.Message, "RoleBinding team-a/team-a-auditors the platform repo declares is not on the cluster") {
		t.Errorf("condition = %+v, want the absent binding named", got)
	}
}

// Once on, a project that loses its grants, or an Application newly synced
// with no account mapped, is reported, never a reason to turn impersonation
// off. A key set by hand is not the operator's to remove.
func TestSyncImpersonationStaysOn(t *testing.T) {
	dv := optIn(testCR(), true)
	byHand := argoCDInstance(map[string]any{install.SyncImpersonationKey: "true"})
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, byHand, argoCM(true), projectNS("team-a", "team-a", "dotvirt/team-a.git"),
		foreignApp("argocd", "billing", "default", "billing"), appProject("default")).
		WithInterceptorFuncs(reviews(granted(nil), &n)).WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	res := reconcileOnce(t, r, dv)
	got := syncCond(t, c, dv)
	if got.Status != metav1.ConditionFalse || got.Reason != "Degraded" || !strings.Contains(got.Message, "project team-a") || !strings.Contains(got.Message, "argocd/billing") {
		t.Errorf("condition = %+v, want Degraded naming team-a and argocd/billing", got)
	}
	if res.RequeueAfter != driftResync {
		t.Errorf("requeue = %v; Degraded waits for the drift timer", res.RequeueAfter)
	}

	stored := setSpec(t, c, dv, func(d *dotvirtv1alpha1.Dotvirt) { d.Spec.ArgoCD.SyncImpersonation.Enabled = false })
	reconcileOnce(t, r, stored)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Error("removed a key the operator did not set")
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionTrue || !strings.Contains(got.Message, "outside dotvirt") {
		t.Errorf("condition = %+v, want Enabled outside dotvirt", got)
	}
}

// A key another manager holds is that manager's: the operator reports the
// conflict and never takes it.
func TestSyncImpersonationConflict(t *testing.T) {
	dv := optIn(testCR(), false)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)).WithReturnManagedFields().Build()
	held := install.ArgoCDSyncImpersonation("argocd", "argocd", true)
	unstructured.SetNestedField(held.Object, "false", "spec", "extraConfig", install.SyncImpersonationKey)                               //nolint:errcheck // a literal path
	if err := c.Patch(context.Background(), held, client.Apply, client.FieldOwner("platform-team"), client.ForceOwnership); err != nil { //nolint:staticcheck // SA1019, as install.Apply
		t.Fatal(err)
	}
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Reason != "Conflict" || !strings.Contains(got.Message, "platform-team") {
		t.Errorf("condition = %+v, want Conflict naming platform-team", got)
	}
	if got := extraConfig(t, c)[install.SyncImpersonationKey]; got != "false" {
		t.Errorf("%s = %v, want the other manager's false kept", install.SyncImpersonationKey, got)
	}
}

// Until argocd-cm carries the key, the controller still syncs: Pending keeps
// the clone warning visible.
func TestSyncImpersonationPendingKeepsCloneWarning(t *testing.T) {
	dv := optIn(testCR(), false)
	own := map[string][]string{"dotvirt-p-team-a": {"team-a"}}
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(func(account string, a *authzv1.ResourceAttributes) bool {
			return granted(own)(account, a) || (account == controllerSA && a.Subresource == "source")
		}, &n)).WithReturnManagedFields().Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Reason != "Pending" || !strings.Contains(got.Message, "datavolumes/source") {
		t.Errorf("condition = %+v, want Pending with the clone warning", got)
	}
}

// Uninstalling takes the key off the instance before prune takes dotvirt's
// AppProjects: left on, every Application there naming no other account would
// stop syncing.
func TestSyncImpersonationReleasedOnUninstall(t *testing.T) {
	dv := optIn(testCR(), false)
	var n atomic.Int64
	var ops []string
	funcs := reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)
	funcs.Patch = func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if obj.GetObjectKind().GroupVersionKind() == install.ArgoCDGVK {
			ops = append(ops, "argocd")
		}
		return cl.Patch(ctx, obj, patch, opts...)
	}
	funcs.Delete = func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if obj.GetObjectKind().GroupVersionKind() == install.ArgoGVK("AppProject") {
			ops = append(ops, "appproject")
		}
		return cl.Delete(ctx, obj, opts...)
	}
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(funcs).WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Fatal("not set")
	}
	if err := c.Delete(context.Background(), getCR(t, c, dv)); err != nil {
		t.Fatal(err)
	}
	ops = nil
	reconcileOnce(t, r, dv)
	if exists(t, c, &dotvirtv1alpha1.Dotvirt{}, dv.Namespace, dv.Name) {
		t.Error("CR still present")
	}
	if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
		t.Errorf("uninstalled, extraConfig = %v", extra)
	}
	if len(ops) < 2 || ops[0] != "argocd" || !slices.Contains(ops[1:], "appproject") {
		t.Errorf("uninstall did %v, want the key released before any AppProject goes", ops)
	}
}

// A cleanup that fails keeps the CR, which says why.
func TestFinalizeFailureSaysWhy(t *testing.T) {
	dv := optIn(testCR(), false)
	var n atomic.Int64
	refuse := false
	funcs := reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)
	funcs.Patch = func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if refuse && obj.GetObjectKind().GroupVersionKind() == install.ArgoCDGVK {
			return errors.New("instance unreachable")
		}
		return cl.Patch(ctx, obj, patch, opts...)
	}
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(funcs).WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)
	if err := c.Delete(context.Background(), getCR(t, c, dv)); err != nil {
		t.Fatal(err)
	}
	refuse = true
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dv)}); err == nil {
		t.Fatal("a failed cleanup reported success")
	}
	got := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionAvailable)
	if got == nil || got.Reason != "CleanupFailed" || !strings.Contains(got.Message, "instance unreachable") {
		t.Errorf("Available = %+v, want CleanupFailed saying why", got)
	}
}

// The key stays with the instance it was set on: pointing spec.argocd at
// another ArgoCD takes it off the first.
func TestSyncImpersonationFollowsInstance(t *testing.T) {
	dv := optIn(testCR(), false)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)).WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Fatal("not set")
	}
	stored := setSpec(t, c, dv, func(d *dotvirtv1alpha1.Dotvirt) { d.Spec.ArgoCD.Namespace = "argo-two" })
	reconcileOnce(t, r, stored)
	if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
		t.Errorf("moved, the first instance keeps %v", extra)
	}
	if got := getCR(t, c, dv).Status.SyncImpersonationInstance; got != "" {
		t.Errorf("status instance = %q, want none: argo-two has no ArgoCD resource", got)
	}
}

// Off, with a controller that may create datavolumes/source cluster-wide: a
// tenant repo's DataVolume could clone a disk out of any namespace, so the
// condition says so.
func TestSyncImpersonationWarnsCloneAnywhere(t *testing.T) {
	dv := testCR()
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == controllerSA && a.Resource == "datavolumes" && a.Subresource == "source" && a.Namespace == ""
		}, &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionFalse || got.Reason != "CloneAnywhere" {
		t.Errorf("condition = %+v, want CloneAnywhere", got)
	}
	if n.Load() != 1 {
		t.Errorf("%d reviews while off, want the one clone check", n.Load())
	}
}

// Ready, but no ArgoCD resource to set it on: the condition hands the admin
// the setting instead, and argocd-cmd-params-cm says which namespaces'
// Applications the instance reads.
func TestSyncImpersonationManualStep(t *testing.T) {
	own := map[string][]string{"dotvirt-p-team-a": {"team-a"}}
	params := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cmd-params-cm", Namespace: "argocd"},
		Data: map[string]string{"application.namespaces": "apps-*, other"}}
	for name, tc := range map[string]struct {
		objs []client.Object
		want string
	}{
		"ready":                         {nil, "ManualStep"},
		"Applications in any namespace": {[]client.Object{params, foreignApp("apps-x", "web", "default", "web")}, "NotReady"},
	} {
		dv := optIn(testCR(), false)
		var n atomic.Int64
		c := testBuilder(t).WithObjects(append(tc.objs, dv, projectNS("team-a", "team-a", "dotvirt/team-a.git"))...).
			WithInterceptorFuncs(reviews(granted(own), &n)).Build()
		reconcileOnce(t, newReconciler(c, depsOK), dv)
		if got := syncCond(t, c, dv); got.Reason != tc.want || (tc.want == "ManualStep" && !strings.Contains(got.Message, "argocd/argocd-cm")) {
			t.Errorf("%s: condition = %+v, want %s", name, got, tc.want)
		}
	}
}

func TestArgoMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s  string
		regex, want bool
	}{
		{"*", "", false, true},
		{"https://kubernetes.*", "https://kubernetes.default.svc", false, true},
		{"team-?", "team-a", false, true},
		{"team-[ab]", "team-c", false, false},
		{"team-[!ab]", "team-c", false, true},
		{"{billing,payroll}", "payroll", false, true},
		{"{billing,payroll}", "pay", false, false},
		{"a.b", "axb", false, false},
		{"/^team-/", "team-x", true, true},
		{"/^team-/", "team-x", false, false},
		{"/[/", "x", true, false},
	} {
		if got := argoMatch(c.pattern, c.s, c.regex); got != c.want {
			t.Errorf("argoMatch(%q, %q, %v) = %v", c.pattern, c.s, c.regex, got)
		}
	}
}
