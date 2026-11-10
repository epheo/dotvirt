package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

func projectNS(name, project, repo string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{install.ProjectLabel: project}}}
	if repo != "" {
		ns.Annotations = map[string]string{install.RepoAnnotation: repo}
	}
	return ns
}

// application is a generated Application: the ApplicationSet labels each with
// its project.
func application(name, project string) *unstructured.Unstructured {
	app := argoObj("Application", "argocd", name, map[string]string{install.ProjectLabel: strings.TrimPrefix(name, "dotvirt-")})
	app.Object["spec"] = map[string]any{"project": project}
	return app
}

func appProjectSpec(t *testing.T, c client.Client, name string) (destinations, sources []string) {
	t.Helper()
	ap := argoObj("AppProject", "", "", nil)
	if !exists(t, c, ap, "argocd", name) {
		t.Fatalf("AppProject %s missing", name)
	}
	dests, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinations")
	for _, d := range dests {
		destinations = append(destinations, d.(map[string]any)["namespace"].(string))
	}
	sources, _, _ = unstructured.NestedStringSlice(ap.Object, "spec", "sourceRepos")
	return destinations, sources
}

// Each project gets an AppProject admitting exactly its namespaces and its
// repo, resolved as the app resolves it. A repo that does not resolve admits
// no source; a project no AppProject of this install's can confine is kept out
// of the generator, and ArgoReady turns Degraded saying why.
func TestTenantAppProjectsFromNamespaceLabels(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	forge := forgeDown.URL
	// Same instance name, another install's namespace: OwnedBy must refuse it.
	foreign := argoObj("AppProject", "argocd", "dotvirt-p-taken",
		map[string]string{install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere"})
	c := testBuilder(t).WithObjects(dv, creds, foreign,
		projectNS("a-prod", "team-a", "dotvirt/team-a.git"),
		projectNS("a-dev", "team-a", "dotvirt/team-a.git"),
		projectNS("b", "team-b", "https://other.example/x/team-b"),
		projectNS("c1", "team-c", "dotvirt/c1.git"),
		projectNS("c2", "team-c", "dotvirt/c2.git"),
		projectNS("g", "team-g", "dotvirt/*"),
		projectNS("plain", "plain", ""),
		projectNS("kube-x", "team-k", "dotvirt/k.git"),
		projectNS("argocd", "team-k", "dotvirt/k.git"),
		projectNS("k", "team-k", "dotvirt/k.git"),
		projectNS("p", "platform", "dotvirt/p.git"),
		projectNS("bad", "Team_Bad", "dotvirt/bad.git"),
		projectNS("t", "taken", "dotvirt/taken.git"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unlabeled"}},
	).Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)

	for name, want := range map[string]struct{ dests, sources []string }{
		"team-a": {[]string{"a-dev", "a-prod"}, []string{forge + "/dotvirt/team-a.git"}},
		"team-b": {[]string{"b"}, []string{"https://other.example/x/team-b"}},
		"team-c": {[]string{"c1", "c2"}, nil},
		"team-g": {[]string{"g"}, nil},
		"plain":  {[]string{"plain"}, nil},
		"team-k": {[]string{"k"}, []string{forge + "/dotvirt/k.git"}},
	} {
		dests, sources := appProjectSpec(t, c, "dotvirt-p-"+name)
		if !slices.Equal(dests, want.dests) || !slices.Equal(sources, want.sources) {
			t.Errorf("%s: destinations %v sources %v, want %v %v", name, dests, sources, want.dests, want.sources)
		}
	}
	for _, name := range []string{"dotvirt-p-platform", "dotvirt-p-Team_Bad", install.TenantsProjectName} {
		if exists(t, c, argoObj("AppProject", "", "", nil), "argocd", name) {
			t.Errorf("AppProject %s rendered", name)
		}
	}
	if !exists(t, c, foreign, "argocd", "dotvirt-p-taken") || install.OwnedBy(foreign.GetLabels(), dv) {
		t.Errorf("a foreign AppProject was taken over: %v", foreign.GetLabels())
	}
	appset := argoObj("ApplicationSet", "", "", nil)
	exists(t, c, appset, "argocd", "dotvirt-projects")
	gen, _, _ := unstructured.NestedSlice(appset.Object, "spec", "generators")
	values, _, _ := unstructured.NestedStringSlice(gen[0].(map[string]any)["selector"].(map[string]any)["matchExpressions"].([]any)[0].(map[string]any), "values")
	if !slices.Equal(values, []string{"Team_Bad", "taken"}) {
		t.Errorf("generator excludes %v, want the unconfined projects", values)
	}
	cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "Degraded" {
		t.Fatalf("ArgoReady = %+v", cond)
	}
	for _, said := range []string{"team-c", "team-g", "kube-x", "argocd is platform space", "Team_Bad", "dotvirt-p-taken"} {
		if !strings.Contains(cond.Message, said) {
			t.Errorf("ArgoReady does not mention %q: %s", said, cond.Message)
		}
	}
	if strings.Contains(cond.Message, "plain") {
		t.Errorf("a repoless project has no Application to warn about: %s", cond.Message)
	}
}

// A BYO forge resolves relative refs against the URL in its credentials
// secret, which is what the app reads.
func TestTenantAppProjectResolvesAgainstBYOForge(t *testing.T) {
	dv, creds := byoForgeCR("https://forge.byo.example")
	dv.Spec.Forge.URL = "https://spec.example"
	c := testBuilder(t).WithObjects(dv, creds, projectNS("a", "team-a", "dotvirt/team-a.git")).Build()
	r := newReconciler(c, depsOK)
	tier, err := r.tenantAppProjects(context.Background(), dv, &reconcileCtx{argoNS: "argocd"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tier.objs) != 1 {
		t.Fatalf("rendered %d AppProjects", len(tier.objs))
	}
	sources, _, _ := unstructured.NestedStringSlice(tier.objs[0].(*unstructured.Unstructured).Object, "spec", "sourceRepos")
	if !slices.Equal(sources, []string{"https://forge.byo.example/dotvirt/team-a.git"}) {
		t.Errorf("sourceRepos = %v", sources)
	}
}

// Existing Applications move off the shared tier without a failed sync: the
// per-project AppProjects are applied before the ApplicationSet names them, a
// failed AppProject apply leaves the template alone, and the shared tier stays
// until no Application references it.
func TestTenantMigrationOrder(t *testing.T) {
	dv := testCR()
	shared := argoObj("AppProject", "argocd", install.TenantsProjectName, install.Labels(dv))
	app := application("dotvirt-team-a", install.TenantsProjectName)
	// Not generated: no project label, so it holds nothing in place.
	handMade := argoObj("Application", "argocd", "hand-made", nil)
	handMade.Object["spec"] = map[string]any{"project": install.TenantsProjectName}
	var applied []string
	failProject := true
	c := testBuilder(t).WithObjects(dv, shared, app, handMade, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				kind := obj.GetObjectKind().GroupVersionKind().Kind
				if kind == "AppProject" && obj.GetName() == "dotvirt-p-team-a" && failProject {
					return errors.New("apiserver unavailable")
				}
				applied = append(applied, kind+"/"+obj.GetName())
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := newReconciler(c, depsOK)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dv)}); err == nil {
		t.Fatal("a failed AppProject apply must fail the pass")
	}
	if slices.Contains(applied, "ApplicationSet/dotvirt-projects") {
		t.Fatal("the ApplicationSet switched ahead of the AppProject it names")
	}

	failProject, applied = false, nil
	reconcileOnce(t, r, dv)
	at := func(key string) int {
		i := slices.Index(applied, key)
		if i < 0 {
			t.Fatalf("%s not applied: %v", key, applied)
		}
		return i
	}
	if at("AppProject/dotvirt-p-team-a") > at("ApplicationSet/dotvirt-projects") {
		t.Errorf("apply order %v: the AppProject must precede the ApplicationSet", applied)
	}
	appset := argoObj("ApplicationSet", "", "", nil)
	exists(t, c, appset, "argocd", "dotvirt-projects")
	spec, _, _ := unstructured.NestedMap(appset.Object, "spec", "template", "spec")
	if spec["project"] != "dotvirt-p-{{.project}}" || spec["destination"].(map[string]any)["namespace"] != "{{.namespace}}" {
		t.Errorf("template project %v destination %v", spec["project"], spec["destination"])
	}
	if !exists(t, c, argoObj("AppProject", "", "", nil), "argocd", install.TenantsProjectName) {
		t.Fatal("the shared tier was removed while an Application references it")
	}
	if cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady); cond == nil ||
		!strings.Contains(cond.Message, install.TenantsProjectName+" kept while 1 Applications") {
		t.Errorf("ArgoReady = %+v", cond)
	}

	// The ApplicationSet controller moves the Application.
	app.Object["spec"] = map[string]any{"project": "dotvirt-p-team-a"}
	if err := c.Update(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, dv)
	if exists(t, c, argoObj("AppProject", "", "", nil), "argocd", install.TenantsProjectName) {
		t.Error("the shared tier outlived its last reference")
	}
}

// A project whose namespaces are gone keeps an AppProject admitting nothing
// while its Application is still being removed, then it is pruned.
func TestEmptiedProjectAppProject(t *testing.T) {
	dv := testCR()
	gone := install.TenantAppProject(dv, "argocd", "dotvirt-p-gone", "gone", []string{"gone-ns"}, []string{"https://forge/dotvirt/gone.git"}, nil)
	app := application("dotvirt-gone", gone.GetName())
	c := testBuilder(t).WithObjects(dv, gone, app).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, dv)
	dests, sources := appProjectSpec(t, c, gone.GetName())
	if len(dests) != 0 || len(sources) != 0 {
		t.Errorf("emptied project admits destinations %v sources %v", dests, sources)
	}

	if err := c.Delete(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, dv)
	if exists(t, c, argoObj("AppProject", "", "", nil), "argocd", gone.GetName()) {
		t.Error("an unreferenced emptied project's AppProject survived")
	}
}

// A project namespace event re-derives the AppProjects only when it moves the
// project or the repo.
func TestTenancyChanged(t *testing.T) {
	ns := projectNS("a", "team-a", "dotvirt/a.git")
	for _, c := range []struct {
		name   string
		mutate func(*corev1.Namespace)
		want   bool
	}{
		{"unrelated label", func(n *corev1.Namespace) { n.Labels["x"] = "y" }, false},
		{"project", func(n *corev1.Namespace) { n.Labels[install.ProjectLabel] = "team-b" }, true},
		{"repo", func(n *corev1.Namespace) { n.Annotations[install.RepoAnnotation] = "dotvirt/b.git" }, true},
	} {
		next := ns.DeepCopy()
		c.mutate(next)
		if got := tenancyChanged(event.UpdateEvent{ObjectOld: ns, ObjectNew: next}); got != c.want {
			t.Errorf("%s: tenancyChanged = %v, want %v", c.name, got, c.want)
		}
	}
}

// Only instance-labelled objects and project-labelled namespaces enter the
// cache, and the Route kind is registered only where the route API exists.
func TestCacheByObjectScope(t *testing.T) {
	for _, plat := range []platform.Platform{platform.OpenShift, platform.Kubernetes} {
		routes := 0
		for obj, opts := range CacheByObject(plat) {
			if obj.GetObjectKind().GroupVersionKind() == install.RouteGVK {
				routes++
			}
			key := install.InstanceLabel
			if _, ns := obj.(*corev1.Namespace); ns {
				key = install.ProjectLabel
			}
			if !opts.Label.Matches(labels.Set{key: "x"}) || opts.Label.Matches(labels.Set{"app": "other"}) {
				t.Errorf("%s: %T selector %q does not select by %s", plat, obj, opts.Label, key)
			}
		}
		if want := map[platform.Platform]int{platform.OpenShift: 1, platform.Kubernetes: 0}[plat]; routes != want {
			t.Errorf("%s: Route entries = %d, want %d", plat, routes, want)
		}
	}
}

// An ApplicationSet that reports an error freezes every tenant Application
// where it is (an app image without the namespace parameter, say): ArgoReady
// says so instead of reading Ready, and since nothing watches the
// ApplicationSet, the pass polls it on settleRetry until it clears.
func TestArgoReadyCarriesApplicationSetError(t *testing.T) {
	dv := optOut(testCR())
	appset := erroredAppSet(dv, `map has no entry for key "namespace"`)
	c := testBuilder(t).WithObjects(dv, appset).Build()
	r := newReconciler(c, depsOK)
	res := reconcileOnce(t, r, dv)
	cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || !strings.Contains(cond.Message, `no entry for key "namespace"`) {
		t.Errorf("ArgoReady = %+v", cond)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > settleRetry {
		t.Errorf("RequeueAfter = %v, want at most %v while ArgoReady is not True", res.RequeueAfter, settleRetry)
	}

	if err := c.Get(context.Background(), client.ObjectKeyFromObject(appset), appset); err != nil {
		t.Fatal(err)
	}
	delete(appset.Object, "status")
	if err := c.Update(context.Background(), appset); err != nil {
		t.Fatal(err)
	}
	res = reconcileOnce(t, r, dv)
	if cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("ArgoReady once the error clears = %+v, want True", cond)
	}
	if res.RequeueAfter != driftResync {
		t.Errorf("RequeueAfter = %v, want the drift resync %v once Argo is ready", res.RequeueAfter, driftResync)
	}
}

// An ApplicationSet error that outlasts driftResync is not Argo recovering on
// its own: the pass falls back to the drift timer instead of polling forever.
func TestArgoNotReadyPollsOnlyWhileRecovering(t *testing.T) {
	dv := optOut(testCR())
	c := testBuilder(t).WithObjects(dv, erroredAppSet(dv, "connection refused")).Build()
	r := newReconciler(c, depsOK)
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != settleRetry {
		t.Fatalf("RequeueAfter = %v, want %v as ArgoReady turns False", res.RequeueAfter, settleRetry)
	}
	cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("ArgoReady = %+v, want False", cond)
	}
	since := cond.LastTransitionTime.Time

	r.now = func() time.Time { return since.Add(driftResync - time.Second) }
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != settleRetry {
		t.Errorf("RequeueAfter = %v, want %v inside the recovery window", res.RequeueAfter, settleRetry)
	}
	r.now = func() time.Time { return since.Add(driftResync) }
	if res := reconcileOnce(t, r, dv); res.RequeueAfter != driftResync {
		t.Errorf("RequeueAfter = %v, want the drift resync %v past the recovery window", res.RequeueAfter, driftResync)
	}
}

func erroredAppSet(dv *dotvirtv1alpha1.Dotvirt, msg string) *unstructured.Unstructured {
	appset := argoObj("ApplicationSet", "argocd", "dotvirt-projects", install.Labels(dv))
	appset.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "ErrorOccurred", "status": "True", "message": msg},
	}}
	return appset
}

// With tenantKinds Any, the Argo phase decides whether the instance
// impersonates where Argo reads it, whatever SyncImpersonationReady says: the
// argocd-cm key, and an AppProject this install applied keeping
// destinationServiceAccounts (an Argo CD older than 2.13 prunes it and ignores
// the key). Only both carry the deny list; otherwise the listed kinds stay and
// ArgoReady says why.
func TestTenantKindsFollowTheImpersonationSetting(t *testing.T) {
	for _, c := range []struct {
		name               string
		key, field, pinned bool
	}{
		{"key unset", false, true, false},
		{"key set, old Argo pruned the field", true, false, false},
		{"key set, field kept, the ArgoCD resource pins its version", true, true, true},
		{"key set, field kept", true, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dv, creds := byoForgeCR("https://forge.byo.example")
			dv.Spec.ArgoCD.TenantKinds = "Any"
			meta.SetStatusCondition(&dv.Status.Conditions, metav1.Condition{Type: dotvirtv1alpha1.ConditionSyncImpersonationReady, Status: metav1.ConditionFalse, Reason: "Degraded"})
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"}, Data: map[string]string{}}
			if c.key {
				cm.Data[install.SyncImpersonationKey] = "true"
			}
			applied := install.TenantAppProject(dv, "argocd", "dotvirt-p-team-a", "team-a", []string{"a"}, nil, nil)
			if !c.field {
				unstructured.RemoveNestedField(applied.Object, "spec", "destinationServiceAccounts")
			}
			objs := []client.Object{dv, creds, cm, applied, projectNS("a", "team-a", "dotvirt/team-a.git")}
			if c.pinned {
				inst := argoCDInstance(nil)
				_ = unstructured.SetNestedField(inst.Object, "v2.12.0", "spec", "version")
				objs = append(objs, inst)
			}
			c2 := testBuilder(t).WithObjects(objs...).Build()
			r := newReconciler(c2, depsOK)
			tier, err := r.tenantAppProjects(context.Background(), dv, &reconcileCtx{argoNS: "argocd"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := c.key && c.field && !c.pinned
			spec := tier.objs[0].(*unstructured.Unstructured).Object["spec"].(map[string]any)
			if _, black := spec["namespaceResourceBlacklist"]; black != want {
				t.Errorf("blacklist = %v, want %v", black, want)
			}
			noted := slices.ContainsFunc(tier.notes, func(n string) bool { return strings.Contains(n, "keep the listed kinds") })
			if noted == want {
				t.Errorf("notes = %v", tier.notes)
			}
		})
	}
}
