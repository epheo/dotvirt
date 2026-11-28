package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/model"
)

// desktopsSource serves a class, and a pool, a desktop and a claim in each of
// team-a-dev and team-b-dev, with a watch on desktops a test can write
// through. failClaims fails every claims list, so that watch stays unsynced
// and stale.
type desktopsSource struct {
	failClaims bool

	mu        sync.Mutex
	instances *watch.FakeWatcher
}

func (*desktopsSource) ServesAPIResource(schema.GroupVersionResource) (bool, error) {
	return true, nil
}

func (*desktopsSource) Can(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func deskObject(kind, ns, name string, spec, status map[string]any) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]any{"apiVersion": "vdi.desku.be/v1alpha1", "kind": kind, "spec": spec, "status": status}}
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func deskInstance(ns, name, phase string) *unstructured.Unstructured {
	u := deskObject("VdiInstance", ns, name, map[string]any{"className": "workstation", "owner": name}, map[string]any{"phase": phase})
	return &u
}

func (f *desktopsSource) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	var items []unstructured.Unstructured
	for _, ns := range []string{"team-a-dev", "team-b-dev"} {
		switch gvr {
		case desktops.GVRPool:
			items = append(items, deskObject("VdiPool", ns, "lab", map[string]any{"className": "workstation", "seats": int64(2)}, nil))
		case desktops.GVRInstance:
			items = append(items, *deskInstance(ns, "solo", "Ready"))
		case desktops.GVRClaim:
			items = append(items, deskObject("VdiClaim", ns, "someone", map[string]any{"poolName": "lab", "user": "someone"}, map[string]any{"phase": "Waiting"}))
		}
	}
	if gvr == desktops.GVRClass {
		items = append(items, deskObject("VdiClass", "", "workstation", map[string]any{"image": "quay.io/deskube/desktop:1"}, nil))
	}
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			if f.failClaims && gvr == desktops.GVRClaim {
				return nil, errors.New("claims unavailable")
			}
			return &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}, Items: items}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			w := watch.NewFake()
			if gvr == desktops.GVRInstance {
				f.mu.Lock()
				f.instances = w
				f.mu.Unlock()
			}
			return w, nil
		},
	}
}

// write sends one event on the desktops watch and waits until it landed.
func (f *desktopsSource) write(t *testing.T, s *Server, event watch.EventType, o *unstructured.Unstructured) {
	t.Helper()
	k := desktops.Kind{Namespace: o.GetNamespace(), Resource: desktops.GVRInstance.Resource}
	gen := s.desktops.Gen(k)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		w := f.instances
		f.mu.Unlock()
		if w != nil {
			w.Action(event, o)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no watch on desktops")
		}
		time.Sleep(time.Millisecond)
	}
	for s.desktops.Gen(k) == gen {
		if time.Now().After(deadline) {
			t.Fatal("the write never reached the snapshot")
		}
		time.Sleep(time.Millisecond)
	}
}

// tenantSeesTeams lets the tenant see team-a-dev, team-b-dev and team-d-dev,
// whatever it may list there.
func tenantSeesTeams(token, ns string) bool {
	return token == tenantToken && (ns == "team-a-dev" || ns == "team-b-dev" || ns == "team-d-dev")
}

var teams = []string{"team-a-dev", "team-b-dev", "team-d-dev"}

// desktopsServer runs the snapshot over src, with team-b and team-d as more
// projects, team-d holding nothing, and waits until it synced or a watch
// failed.
func desktopsServer(t *testing.T, fc *fakeCluster, src *desktopsSource) *Server {
	t.Helper()
	for _, p := range []string{"team-b", "team-d"} {
		fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: p + "-dev", Labels: map[string]string{"dotvirt.io/project": p}, Annotations: map[string]string{"dotvirt.io/repo": "https://forge/" + p + ".git"},
		}})
	}
	s := fc.server(nil, Config{})
	s.desktops = desktops.New(src, s.bus, func() bool { return true })
	s.desktops.Run(t.Context())
	every := map[desktops.Kind]bool{{Resource: desktops.GVRClass.Resource}: true}
	for _, gvr := range desktops.Namespaced {
		every[desktops.Kind{Namespace: "team-a-dev", Resource: gvr.Resource}] = true
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if o := s.desktops.Overview(every, nil); o.Synced || o.Stale {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("the snapshot never synced")
		}
	}
}

// offServer is a cluster without DeskuBe.
func offServer(t *testing.T) *Server {
	t.Helper()
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	s := fc.server(nil, Config{})
	s.desktops = desktops.New(nil, s.bus, func() bool { return false })
	return s
}

func desktopsVersion(t *testing.T, s *Server, token string, namespaces ...string) uint64 {
	t.Helper()
	r := asUser(httptest.NewRequest(http.MethodGet, "/", nil), token)
	id, c, err := s.userCluster(r)
	if err != nil {
		t.Fatal(err)
	}
	v := callerView{s: s, ctx: r.Context(), id: id, c: c}
	return s.desktopsWatermark(v, namespaces)
}

func desktopsBody(t *testing.T, s *Server, token, target string) string {
	t.Helper()
	rec := do(s, token, http.MethodGet, target, "")
	wantStatus(t, rec, http.StatusOK)
	return rec.Body.String()
}

func getDesktops(t *testing.T, s *Server, token, target string) model.Desktops {
	t.Helper()
	var got model.Desktops
	if err := json.Unmarshal([]byte(desktopsBody(t, s, token, target)), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// namespaces is every namespace the answer names.
func namespaces(d model.Desktops) []string {
	seen := map[string]bool{}
	for _, p := range d.Pools {
		seen[p.Namespace] = true
	}
	for _, x := range d.Desktops {
		seen[x.Namespace] = true
	}
	for _, c := range d.Claims {
		seen[c.Namespace] = true
	}
	var out []string
	for ns := range seen {
		out = append(out, ns)
	}
	return out
}

// listsKinds lets the tenant list the given vdi kinds in the given namespaces.
func listsKinds(resources []string, nss ...string) func(string, *authzv1.ResourceAttributes) bool {
	return func(token string, a *authzv1.ResourceAttributes) bool {
		if token == adminToken {
			return true
		}
		return token == tenantToken && a.Verb == "list" && a.Group == desktops.Group &&
			slices.Contains(resources, a.Resource) && slices.Contains(nss, a.Namespace)
	}
}

var allKinds = []string{"vdipools", "vdiinstances", "vdiclaims"}

// andClasses also lets the tenant list the classes cluster-wide.
func andClasses(f func(string, *authzv1.ResourceAttributes) bool) func(string, *authzv1.ResourceAttributes) bool {
	return func(token string, a *authzv1.ResourceAttributes) bool {
		return f(token, a) || token == tenantToken && a.Verb == "list" && a.Group == desktops.Group &&
			a.Resource == "vdiclasses" && a.Namespace == ""
	}
}

func unlistedJSON(t *testing.T, d model.Desktops) string {
	t.Helper()
	b, err := json.Marshal(d.Unlisted)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDesktopsShowWhatTheCallerMayList(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = listsKinds(allKinds, "team-a-dev")
	s := desktopsServer(t, fc, &desktopsSource{})

	all := getDesktops(t, s, adminToken, "/api/desktops")
	if !all.Synced || len(all.Classes) != 1 || len(all.Pools) != 2 || len(all.Desktops) != 2 || len(all.Claims) != 2 || len(all.Unlisted) != 0 {
		t.Fatalf("an admin reads %+v", all)
	}

	got := getDesktops(t, s, tenantToken, "/api/desktops")
	if nss := namespaces(got); len(nss) != 1 || nss[0] != "team-a-dev" {
		t.Fatalf("the tenant reads namespaces %v: %+v", nss, got)
	}
	if len(got.Pools) != 1 || len(got.Desktops) != 1 || len(got.Claims) != 1 {
		t.Fatalf("the tenant reads %+v", got)
	}
	if len(got.Classes) != 0 {
		t.Fatalf("a tenant without a cluster-wide list read classes: %+v", got.Classes)
	}
	// Each namespace of the scope says what the tenant may not list there,
	// those it lists nothing in too.
	want := `[{"namespace":"","resource":"vdiclasses"},` +
		`{"namespace":"team-b-dev","resource":"vdiclaims"},{"namespace":"team-b-dev","resource":"vdiinstances"},{"namespace":"team-b-dev","resource":"vdipools"},` +
		`{"namespace":"team-d-dev","resource":"vdiclaims"},{"namespace":"team-d-dev","resource":"vdiinstances"},{"namespace":"team-d-dev","resource":"vdipools"}]`
	if got := unlistedJSON(t, got); got != want {
		t.Fatalf("unlisted = %s, want %s", got, want)
	}
}

// The classes are cluster-scoped: a project persona never lists them, and is
// told so beside what it does list.
func TestClassesAreUnlistedBesideOtherReads(t *testing.T) {
	for _, c := range []struct {
		name     string
		classes  bool
		unlisted string
	}{
		{"pools only", false, `[{"namespace":"","resource":"vdiclasses"},{"namespace":"team-b-dev","resource":"vdiclaims"},{"namespace":"team-b-dev","resource":"vdiinstances"}]`},
		{"pools and classes", true, `[{"namespace":"team-b-dev","resource":"vdiclaims"},{"namespace":"team-b-dev","resource":"vdiinstances"}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.readsVMs = tenantSeesTeams
			fc.ssar = listsKinds([]string{"vdipools"}, "team-b-dev")
			if c.classes {
				fc.ssar = andClasses(fc.ssar)
			}
			s := desktopsServer(t, fc, &desktopsSource{})
			got := getDesktops(t, s, tenantToken, "/api/desktops?namespace=team-b-dev")
			if u := unlistedJSON(t, got); u != c.unlisted {
				t.Fatalf("unlisted = %s, want %s", u, c.unlisted)
			}
			if c.classes != (len(got.Classes) == 1) {
				t.Fatalf("classes = %+v", got.Classes)
			}
		})
	}
}

// A page narrowed to a namespace the caller lists nothing in still says what
// it may not list there, when it lists something elsewhere.
func TestUnlistedNamesANamespaceTheCallerListsNothingIn(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = listsKinds([]string{"vdipools"}, "team-b-dev")
	s := desktopsServer(t, fc, &desktopsSource{})

	got := getDesktops(t, s, tenantToken, "/api/desktops?namespace=team-d-dev")
	want := `[{"namespace":"","resource":"vdiclasses"},` +
		`{"namespace":"team-d-dev","resource":"vdiclaims"},{"namespace":"team-d-dev","resource":"vdiinstances"},{"namespace":"team-d-dev","resource":"vdipools"}]`
	if u := unlistedJSON(t, got); u != want {
		t.Fatalf("unlisted = %s, want %s", u, want)
	}
	if !got.Synced || len(got.Pools) != 0 {
		t.Fatalf("the tenant reads %+v", got)
	}
}

// A project viewer sees a pool's counts: the desktops behind them are said to
// be unlisted, not read as none. It is the caller's own rights, said where no
// claim and no desktop exist too, and nothing else moves with the rows.
func TestUnlistedIsTheCallersOwnRights(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = listsKinds([]string{"vdipools"}, "team-b-dev", "team-d-dev")
	src := &desktopsSource{}
	s := desktopsServer(t, fc, src)

	empty := getDesktops(t, s, tenantToken, "/api/desktops?namespace=team-d-dev")
	want := `[{"namespace":"","resource":"vdiclasses"},{"namespace":"team-d-dev","resource":"vdiclaims"},{"namespace":"team-d-dev","resource":"vdiinstances"}]`
	if u := unlistedJSON(t, empty); u != want {
		t.Fatalf("unlisted = %s, want %s", u, want)
	}

	body := desktopsBody(t, s, tenantToken, "/api/desktops")
	version := desktopsVersion(t, s, tenantToken, teams...)
	if got := getDesktops(t, s, tenantToken, "/api/desktops"); len(got.Pools) != 1 || len(got.Desktops) != 0 || len(got.Unlisted) != 8 {
		t.Fatalf("the tenant reads %+v", got)
	}
	for _, c := range []struct {
		what  string
		event watch.EventType
		obj   *unstructured.Unstructured
	}{
		{"a desktop made where none was", watch.Added, deskInstance("team-d-dev", "fresh", "Ready")},
		{"a desktop deleted", watch.Deleted, deskInstance("team-b-dev", "solo", "Ready")},
	} {
		src.write(t, s, c.event, c.obj)
		if got := desktopsBody(t, s, tenantToken, "/api/desktops"); got != body {
			t.Fatalf("%s moved the answer: %s then %s", c.what, body, got)
		}
		if got := desktopsVersion(t, s, tenantToken, teams...); got != version {
			t.Fatalf("%s moved the version: %d then %d", c.what, version, got)
		}
	}
}

// A caller who may list no vdi kind is told what a cluster without DeskuBe
// tells it, even while a watch is unsynced and stale.
func TestACallerWhoListsNoDesktopKindIsToldNothing(t *testing.T) {
	off := offServer(t)
	offBody := desktopsBody(t, off, tenantToken, "/api/desktops")
	if v := desktopsVersion(t, off, tenantToken, teams...); v != 0 {
		t.Fatalf("a cluster without DeskuBe gives version %d", v)
	}
	for _, c := range []struct {
		name       string
		failClaims bool
	}{
		{"synced", false},
		{"a watch unsynced and stale", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.readsVMs = tenantSeesTeams
			fc.ssar = listsKinds(nil)
			s := desktopsServer(t, fc, &desktopsSource{failClaims: c.failClaims})

			if all := getDesktops(t, s, adminToken, "/api/desktops"); all.Stale != c.failClaims || all.Synced == c.failClaims {
				t.Fatalf("an admin reads %+v", all)
			}
			if got := desktopsBody(t, s, tenantToken, "/api/desktops"); got != offBody {
				t.Fatalf("the tenant reads %s, a cluster without DeskuBe %s", got, offBody)
			}
			if v := desktopsVersion(t, s, tenantToken, teams...); v != 0 {
				t.Fatalf("version = %d, want 0", v)
			}
		})
	}
}

// Synced and stale speak of the kinds the caller reads only: a pools-only
// caller is not told the claims watch fails.
func TestAKindsHealthIsToldOnlyToItsReaders(t *testing.T) {
	read := func(failClaims bool) (model.Desktops, uint64) {
		fc := newFakeCluster(t)
		fc.readsVMs = tenantSeesTeams
		fc.ssar = listsKinds([]string{"vdipools"}, "team-b-dev")
		s := desktopsServer(t, fc, &desktopsSource{failClaims: failClaims})
		pools := map[desktops.Kind]bool{{Namespace: "team-b-dev", Resource: desktops.GVRPool.Resource}: true}
		for deadline := time.Now().Add(5 * time.Second); !s.desktops.Overview(pools, nil).Synced; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("a pools-only pull never synced (claims failing: %v)", failClaims)
			}
		}
		if failClaims {
			if all := getDesktops(t, s, adminToken, "/api/desktops"); !all.Stale || all.Synced {
				t.Fatalf("an admin reads %+v while the claims watch fails", all)
			}
		}
		return getDesktops(t, s, tenantToken, "/api/desktops"), desktopsVersion(t, s, tenantToken, teams...)
	}
	healthy, healthyVersion := read(false)
	failing, failingVersion := read(true)
	if !failing.Synced || failing.Stale {
		t.Fatalf("a pools-only caller reads synced=%v stale=%v while only the claims watch fails", failing.Synced, failing.Stale)
	}
	if failingVersion != healthyVersion {
		t.Fatalf("the claims watch failing moved a pools-only caller's version: %d then %d", healthyVersion, failingVersion)
	}
	if len(healthy.Pools) != 1 || len(failing.Pools) != 1 {
		t.Fatalf("pools read %+v and %+v", healthy.Pools, failing.Pools)
	}
}

func TestDesktopsVersionMovesWithWhatTheCallerReads(t *testing.T) {
	for _, c := range []struct {
		name  string
		lists bool
		moves bool
	}{
		{"lists desktops", true, true},
		{"lists none", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.readsVMs = tenantSeesTeams
			fc.ssar = listsKinds(nil)
			if c.lists {
				fc.ssar = listsKinds(allKinds, "team-a-dev")
			}
			src := &desktopsSource{}
			s := desktopsServer(t, fc, src)

			before := desktopsVersion(t, s, tenantToken, teams...)
			src.write(t, s, watch.Modified, deskInstance("team-b-dev", "solo", "Stopping"))
			if after := desktopsVersion(t, s, tenantToken, teams...); after != before {
				t.Fatalf("another namespace's desktop moved the version: %d then %d", before, after)
			}
			src.write(t, s, watch.Modified, deskInstance("team-a-dev", "solo", "Stopping"))
			if after := desktopsVersion(t, s, tenantToken, teams...); (after != before) != c.moves {
				t.Fatalf("version %d then %d, want moved=%v", before, after, c.moves)
			}
		})
	}
}

func TestDesktopsVersionIsAbsentWithoutTheModule(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	s.desktops = desktops.New(nil, s.bus, func() bool { return false })
	if v := desktopsVersion(t, s, adminToken, "team-a-dev"); v != 0 {
		t.Fatalf("version = %d, want 0", v)
	}
}
