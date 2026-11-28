package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
// team-a-dev and team-b-dev, with a watch on desktops a test can write through.
type desktopsSource struct {
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

func (f *desktopsSource) write(t *testing.T, s *Server, o *unstructured.Unstructured) {
	t.Helper()
	h := desktops.Held{Namespace: o.GetNamespace(), Resource: desktops.GVRInstance.Resource}
	gen := s.desktops.Gen(h)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		w := f.instances
		f.mu.Unlock()
		if w != nil {
			w.Modify(o)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no watch on desktops")
		}
		time.Sleep(time.Millisecond)
	}
	for s.desktops.Gen(h) == gen {
		if time.Now().After(deadline) {
			t.Fatal("the write never reached the snapshot")
		}
		time.Sleep(time.Millisecond)
	}
}

// tenantSeesTeams lets the tenant see team-a-dev and team-b-dev, whatever it
// may list there.
func tenantSeesTeams(token, ns string) bool {
	return token == tenantToken && (ns == "team-a-dev" || ns == "team-b-dev")
}

// desktopsServer runs the snapshot over src, with team-b as a second project,
// and waits until it synced.
func desktopsServer(t *testing.T, fc *fakeCluster, src *desktopsSource) *Server {
	t.Helper()
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team-b-dev", Labels: map[string]string{"dotvirt.io/project": "team-b"}, Annotations: map[string]string{"dotvirt.io/repo": "https://forge/team-b.git"},
	}})
	s := fc.server(nil, Config{})
	s.desktops = desktops.New(src, s.bus, func() bool { return true })
	s.desktops.Run(t.Context())
	for deadline := time.Now().Add(5 * time.Second); !s.desktops.Overview(nil, nil).Synced; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the snapshot never synced")
		}
	}
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

func getDesktops(t *testing.T, s *Server, token string) model.Desktops {
	t.Helper()
	rec := do(s, token, http.MethodGet, "/api/desktops", "")
	wantStatus(t, rec, http.StatusOK)
	var got model.Desktops
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
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

func TestDesktopsShowWhatTheCallerMayList(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == adminToken || token == tenantToken && a.Verb == "list" && a.Group == desktops.Group && a.Namespace == "team-a-dev"
	}
	s := desktopsServer(t, fc, &desktopsSource{})

	all := getDesktops(t, s, adminToken)
	if !all.Synced || len(all.Classes) != 1 || len(all.Pools) != 2 || len(all.Desktops) != 2 || len(all.Claims) != 2 {
		t.Fatalf("an admin reads %+v", all)
	}

	got := getDesktops(t, s, tenantToken)
	if nss := namespaces(got); len(nss) != 1 || nss[0] != "team-a-dev" {
		t.Fatalf("the tenant reads namespaces %v: %+v", nss, got)
	}
	if len(got.Pools) != 1 || len(got.Desktops) != 1 || len(got.Claims) != 1 {
		t.Fatalf("the tenant reads %+v", got)
	}
	if len(got.Classes) != 0 {
		t.Fatalf("a tenant without a cluster-wide list read classes: %+v", got.Classes)
	}
	// A project viewer sees a pool's counts: the desktops behind them are
	// said to be hidden, not read as none.
	hidden, err := json.Marshal(got.Hidden)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"resource":"vdiclasses"},` +
		`{"namespace":"team-b-dev","resource":"vdiclaims"},` +
		`{"namespace":"team-b-dev","resource":"vdiinstances"},` +
		`{"namespace":"team-b-dev","resource":"vdipools"}]`
	if string(hidden) != want {
		t.Fatalf("hidden = %s, want %s", hidden, want)
	}
	if len(all.Hidden) != 0 {
		t.Fatalf("an admin is told of hidden kinds: %+v", all.Hidden)
	}

	rec := do(s, viewerToken, http.MethodGet, "/api/desktops", "")
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "team-") {
		t.Fatalf("a caller seeing no namespace read %s", rec.Body)
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
			fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
				return token == tenantToken && c.lists && a.Verb == "list" && a.Group == desktops.Group && a.Namespace == "team-a-dev"
			}
			src := &desktopsSource{}
			s := desktopsServer(t, fc, src)

			before := desktopsVersion(t, s, tenantToken, "team-a-dev", "team-b-dev")
			src.write(t, s, deskInstance("team-b-dev", "solo", "Stopping"))
			if after := desktopsVersion(t, s, tenantToken, "team-a-dev", "team-b-dev"); after != before {
				t.Fatalf("another namespace's desktop moved the version: %d then %d", before, after)
			}
			src.write(t, s, deskInstance("team-a-dev", "solo", "Stopping"))
			if after := desktopsVersion(t, s, tenantToken, "team-a-dev", "team-b-dev"); (after != before) != c.moves {
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

// A kind held where the caller may not list it moves the version as it comes
// and goes, never as its objects move.
func TestDesktopsVersionSaysAHiddenKindByItsPresenceOnly(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = func(token string, _ *authzv1.ResourceAttributes) bool { return token == adminToken }
	src := &desktopsSource{}
	s := desktopsServer(t, fc, src)

	before := desktopsVersion(t, s, tenantToken, "team-a-dev", "team-c-dev")
	src.write(t, s, deskInstance("team-c-dev", "solo", "Ready"))
	appeared := desktopsVersion(t, s, tenantToken, "team-a-dev", "team-c-dev")
	if appeared == before {
		t.Fatal("a desktop the caller may not list appearing left the version")
	}
	src.write(t, s, deskInstance("team-c-dev", "solo", "Stopping"))
	if desktopsVersion(t, s, tenantToken, "team-a-dev", "team-c-dev") != appeared {
		t.Fatal("a hidden desktop's move moved the version")
	}
}
