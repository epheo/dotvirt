package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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

func application(name, project string) *unstructured.Unstructured {
	app := argoObj("Application", "argocd", name, nil)
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
// repo, resolved as the app resolves it; what cannot be confined as derived
// falls back narrowly and is said in ArgoReady.
func TestTenantAppProjectsFromNamespaceLabels(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	forge := forgeDown.URL
	foreign := argoObj("AppProject", "argocd", "dotvirt-p-taken", map[string]string{"team": "theirs"})
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

	fallback := forge + "/dotvirt/*"
	for name, want := range map[string]struct{ dests, sources []string }{
		"team-a": {[]string{"a-dev", "a-prod"}, []string{forge + "/dotvirt/team-a.git"}},
		"team-b": {[]string{"b"}, []string{"https://other.example/x/team-b"}},
		"team-c": {[]string{"c1", "c2"}, []string{fallback}},
		"team-g": {[]string{"g"}, []string{fallback}},
		"plain":  {[]string{"plain"}, []string{fallback}},
		"team-k": {[]string{"k"}, []string{forge + "/dotvirt/k.git"}},
	} {
		dests, sources := appProjectSpec(t, c, install.TenantProjectName(name))
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
	cond := meta.FindStatusCondition(getCR(t, c, dv).Status.Conditions, dotvirtv1alpha1.ConditionArgoReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
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
	rc := phaseCtx(r, dv)
	objs, _, err := r.tenantAppProjects(context.Background(), dv, rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("rendered %d AppProjects", len(objs))
	}
	sources, _, _ := unstructured.NestedStringSlice(objs[0].(*unstructured.Unstructured).Object, "spec", "sourceRepos")
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
	var applied []string
	failProject := true
	c := testBuilder(t).WithObjects(dv, shared, app, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
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
	gone := install.TenantAppProject(dv, "argocd", "gone", []string{"gone-ns"}, []string{"https://forge/dotvirt/gone.git"})
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
