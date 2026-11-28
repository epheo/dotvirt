package desktops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
)

const (
	pin         = "4821"
	adminSecret = "desk-admin-7f3a"
)

func obj(ns, name string, content map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: content}
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// desktop is a running VdiInstance with a pairing under way and Sunshine's
// credentials named, as DeskuBe leaves one.
func desktop(ns, name string, status map[string]any) *unstructured.Unstructured {
	st := map[string]any{
		"phase": "Ready", "address": "10.0.0.5:47989", "sessionState": "busy",
		"adminSecret": adminSecret,
		"lastActive":  "2026-11-28T10:00:00Z",
		"pairing":     map[string]any{"pin": pin, "phase": "Pending"},
		"conditions":  []any{map[string]any{"type": "Ready", "status": "True", "reason": "Aggregate", "lastTransitionTime": "2026-11-28T09:00:00Z"}},
	}
	for k, v := range status {
		st[k] = v
	}
	return obj(ns, name, map[string]any{
		"spec": map[string]any{
			"className": "workstation", "owner": "alice", "running": true,
			"pairing": map[string]any{"pin": pin, "clientName": "moonlight"},
		},
		"status": st,
	})
}

func controlled(u *unstructured.Unstructured, apiVersion, kind, name string, controller bool) *unstructured.Unstructured {
	u.SetOwnerReferences(append(u.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &controller,
	}))
	return u
}

func pool(ns, name string, status map[string]any) *unstructured.Unstructured {
	content := map[string]any{"spec": map[string]any{"className": "workstation", "seats": int64(4), "warm": int64(1), "assignment": "Personal"}}
	if status != nil {
		content["status"] = status
	}
	return obj(ns, name, content)
}

func claim(ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	return obj(ns, name, map[string]any{"spec": spec, "status": status})
}

func newSnapshot(t *testing.T) (*Snapshot, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	s := New(nil, bus, func() bool { return true })
	s.started.Store(true)
	return s, bus
}

func mustAdd(t *testing.T, s cache.Store, o any) {
	t.Helper()
	if err := s.Add(o); err != nil {
		t.Fatal(err)
	}
}

// everything is a view that reads every kind in namespaces, and the classes.
func everything(namespaces ...string) map[Kind]bool {
	out := map[Kind]bool{{Resource: GVRClass.Resource}: true}
	for _, ns := range namespaces {
		for _, gvr := range Namespaced {
			out[Kind{ns, gvr.Resource}] = true
		}
	}
	return out
}

func TestThePINAndTheAdminSecretAreNeverHeld(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.instances, desktop("team-a", "alice", nil))
	held := fmt.Sprintf("%+v", s.instances.Rows)
	body, err := json.Marshal(s.Overview(everything("team-a"), nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{pin, adminSecret, "2026-11-28T10:00:00Z"} {
		if strings.Contains(held, leak) {
			t.Errorf("the projection holds %q: %s", leak, held)
		}
		if strings.Contains(string(body), leak) {
			t.Errorf("the overview carries %q: %s", leak, body)
		}
	}
	got := s.Overview(everything("team-a"), nil).Desktops
	if len(got) != 1 || got[0].Address != "10.0.0.5:47989" || got[0].Session != "busy" || !got[0].Running {
		t.Fatalf("desktops = %+v", got)
	}
}

// DeskuBe rewrites lastActive about every 30s while a session runs.
func TestASessionsHeartbeatPublishesNothing(t *testing.T) {
	s, bus := newSnapshot(t)
	mustAdd(t, s.instances, desktop("team-a", "alice", nil))
	before, gen := bus.Version(eventbus.DesktopsChanged), s.Gen(Kind{"team-a", GVRInstance.Resource})
	beat := desktop("team-a", "alice", map[string]any{"lastActive": "2026-11-28T10:00:30Z"})
	beat.SetResourceVersion("2")
	if err := s.instances.Update(beat); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.DesktopsChanged) != before || s.Gen(Kind{"team-a", GVRInstance.Resource}) != gen {
		t.Fatal("a heartbeat published")
	}
	if err := s.instances.Update(desktop("team-a", "alice", map[string]any{"sessionState": "free"})); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.DesktopsChanged) == before {
		t.Fatal("a session ending did not publish")
	}
}

func TestMissingCountsReadAsZero(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.pools, pool("team-a", "fresh", nil))
	mustAdd(t, s.pools, pool("team-a", "busy", map[string]any{
		"held": int64(2), "warm": int64(1), "free": int64(1), "inSession": int64(1),
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ClassFound"}},
	}))
	got := s.Overview(everything("team-a"), nil).Pools
	if len(got) != 2 || got[0].Name != "busy" || got[1].Name != "fresh" {
		t.Fatalf("pools = %+v", got)
	}
	busy, fresh := got[0], got[1]
	if busy.Counts.Held != 2 || busy.Counts.Warm != 1 || busy.Counts.Free != 1 || busy.Counts.InSession != 1 || busy.Counts.Waiting != 0 {
		t.Fatalf("counts = %+v", busy.Counts)
	}
	if len(busy.Conditions) != 1 || busy.Conditions[0].Reason != "ClassFound" {
		t.Fatalf("conditions = %+v", busy.Conditions)
	}
	if busy.KeepWarm != 1 || busy.Seats != 4 {
		t.Fatalf("pool = %+v", busy)
	}
	body, err := json.Marshal(fresh.Counts)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"held":0,"inSession":0,"ready":0,"stopped":0,"warm":0,"free":0,"waiting":0}`; string(body) != want {
		t.Fatalf("counts = %s, want %s", body, want)
	}
}

// A desktop is a pool's when the pool controls it, as DeskuBe's own policy
// reads it: an orphaned one keeps the pool's label and is the pool's no more.
func TestAPoolsDesktopsAreThoseItControls(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.pools, pool("team-a", "lab", nil))
	mustAdd(t, s.instances, controlled(desktop("team-a", "lab-alice", nil), "vdi.desku.be/v1alpha1", "VdiPool", "lab", true))
	orphan := desktop("team-a", "lab-bob", nil)
	orphan.SetLabels(map[string]string{"vdi.desku.be/pool": "lab", "vdi.desku.be/claim": "bob"})
	mustAdd(t, s.instances, orphan)
	mustAdd(t, s.instances, controlled(desktop("team-a", "not-controller", nil), "vdi.desku.be/v1alpha1", "VdiPool", "lab", false))
	mustAdd(t, s.instances, controlled(desktop("team-a", "other-group", nil), "other.example/v1", "VdiPool", "lab", true))
	mustAdd(t, s.instances, desktop("team-a", "solo", nil))

	got := s.Overview(everything("team-a"), nil)
	if len(got.Pools) != 1 || len(got.Pools[0].Desktops) != 1 || got.Pools[0].Desktops[0].Name != "lab-alice" || got.Pools[0].Desktops[0].Pool != "lab" {
		t.Fatalf("pools = %+v", got.Pools)
	}
	var alone []string
	for _, d := range got.Desktops {
		if d.Pool != "" {
			t.Errorf("%s reads as a pool's", d.Name)
		}
		alone = append(alone, d.Name)
	}
	if want := "lab-bob not-controller other-group solo"; strings.Join(alone, " ") != want {
		t.Fatalf("one by one = %v, want %s", alone, want)
	}
}

// Where the pool is not shown, its desktops are shown with the others.
func TestAPoolsDesktopNeverHidesWithItsPool(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.pools, pool("team-a", "lab", nil))
	mustAdd(t, s.instances, controlled(desktop("team-a", "lab-alice", nil), "vdi.desku.be/v1alpha1", "VdiPool", "lab", true))
	got := s.Overview(map[Kind]bool{{"team-a", GVRInstance.Resource}: true}, nil)
	if len(got.Pools) != 0 || len(got.Desktops) != 1 || got.Desktops[0].Pool != "lab" {
		t.Fatalf("overview = %+v", got)
	}
}

func TestClaimsJoinTheirDesktopByName(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.pools, pool("team-a", "lab", nil))
	mustAdd(t, s.instances, controlled(desktop("team-a", "lab-alice", nil), "vdi.desku.be/v1alpha1", "VdiPool", "lab", true))
	mustAdd(t, s.instances, desktop("team-a", "bench", nil))
	mustAdd(t, s.instances, desktop("team-a", "studio", nil))
	mustAdd(t, s.claims, claim("team-a", "alice", map[string]any{"poolName": "lab", "user": "alice"},
		map[string]any{"phase": "Bound", "desktopName": "lab-alice", "lastUsed": "2026-11-27T08:00:00Z"}))
	mustAdd(t, s.claims, claim("team-a", "carol", map[string]any{"poolName": "lab", "user": "carol"},
		map[string]any{"phase": "Waiting", "position": int64(1)}))
	mustAdd(t, s.claims, claim("team-a", "dave", map[string]any{"desktopName": "bench", "user": "dave", "running": false},
		map[string]any{"phase": "Waiting"}))
	mustAdd(t, s.claims, claim("team-a", "erin", map[string]any{"desktopName": "studio", "user": "erin"},
		map[string]any{"phase": "Bound", "desktopName": "studio"}))

	got := s.Overview(everything("team-a"), nil)
	if len(got.Claims) != 4 {
		t.Fatalf("claims = %+v", got.Claims)
	}
	alice, carol, dave, erin := got.Claims[0], got.Claims[1], got.Claims[2], got.Claims[3]
	if alice.Pool != "lab" || alice.Desktop != "lab-alice" || alice.LastUsed != "2026-11-27T08:00:00Z" || !alice.Running {
		t.Errorf("alice = %+v", alice)
	}
	if carol.Pool != "lab" || carol.Desktop != "" || carol.Position != 1 {
		t.Errorf("carol = %+v", carol)
	}
	if dave.Pool != "" || dave.Desktop != "bench" || dave.Running {
		t.Errorf("dave = %+v", dave)
	}
	if erin.Pool != "" || erin.Desktop != "studio" {
		t.Errorf("erin = %+v", erin)
	}
	if d := got.Pools[0].Desktops[0]; d.Claim != "alice" {
		t.Errorf("the pool's desktop names claim %q", d.Claim)
	}
	claims := map[string]string{}
	for _, d := range got.Desktops {
		claims[d.Name] = d.Claim
	}
	if claims["bench"] != "" || claims["studio"] != "erin" {
		t.Errorf("claims by desktop = %v, want only studio held, by erin", claims)
	}
}

// A desktop's claim is the one holding it: a stranger's claim naming it is
// refused, a waiting one holds nothing yet.
func TestOnlyABoundClaimIsTheDesktops(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.instances, desktop("team-a", "studio", nil))
	mustAdd(t, s.claims, claim("team-a", "mallory", map[string]any{"desktopName": "studio", "user": "mallory"},
		map[string]any{"phase": "Refused", "message": "studio is not yours"}))
	mustAdd(t, s.claims, claim("team-a", "aaron", map[string]any{"desktopName": "studio", "user": "aaron"},
		map[string]any{"phase": "Waiting"}))
	if got := s.Overview(everything("team-a"), nil).Desktops; len(got) != 1 || got[0].Claim != "" {
		t.Fatalf("a claim that holds nothing names the desktop: %+v", got)
	}
	mustAdd(t, s.claims, claim("team-a", "erin", map[string]any{"desktopName": "studio", "user": "erin"},
		map[string]any{"phase": "Bound", "desktopName": "studio"}))
	if got := s.Overview(everything("team-a"), nil).Desktops; got[0].Claim != "erin" {
		t.Fatalf("the desktop names claim %q, want erin", got[0].Claim)
	}
}

func TestClassesNeedTheirOwnRead(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.classes, obj("", "workstation", map[string]any{"spec": map[string]any{"image": "quay.io/deskube/desktop:1"}}))
	if got := s.Overview(map[Kind]bool{{"team-a", GVRPool.Resource}: true}, nil).Classes; len(got) != 0 {
		t.Fatalf("classes without their read = %+v", got)
	}
	got := s.Overview(everything(), nil).Classes
	if len(got) != 1 || got[0].Image != "quay.io/deskube/desktop:1" {
		t.Fatalf("classes = %+v", got)
	}
}

func TestANamespacesLevelMovesWithItsOwnObjectsOnly(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.instances, desktop("team-a", "alice", nil))
	mustAdd(t, s.instances, desktop("team-b", "bob", nil))
	mustAdd(t, s.pools, pool("team-a", "lab", nil))
	a, aPools := s.Gen(Kind{"team-a", GVRInstance.Resource}), s.Gen(Kind{"team-a", GVRPool.Resource})

	if err := s.instances.Update(desktop("team-b", "bob", map[string]any{"phase": "Stopping"})); err != nil {
		t.Fatal(err)
	}
	if s.Gen(Kind{"team-a", GVRInstance.Resource}) != a {
		t.Fatal("another namespace's desktop moved this one's level")
	}
	if err := s.instances.Update(desktop("team-a", "alice", map[string]any{"phase": "Stopping"})); err != nil {
		t.Fatal(err)
	}
	if s.Gen(Kind{"team-a", GVRInstance.Resource}) == a {
		t.Fatal("a desktop's move left its level")
	}
	if s.Gen(Kind{"team-a", GVRPool.Resource}) != aPools {
		t.Fatal("a desktop's move moved its namespace's pools")
	}
}

func TestUnlistedKindsAreSaidByNamespaceAndKind(t *testing.T) {
	s, _ := newSnapshot(t)
	got := s.Overview(map[Kind]bool{{"team-a", GVRInstance.Resource}: true, {"team-b", GVRPool.Resource}: true},
		[]Kind{{"team-b", GVRInstance.Resource}, {"team-a", GVRClaim.Resource}, {"team-a", GVRPool.Resource}})
	body, err := json.Marshal(got.Unlisted)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"namespace":"team-a","resource":"vdiclaims"},{"namespace":"team-a","resource":"vdipools"},{"namespace":"team-b","resource":"vdiinstances"}]`; string(body) != want {
		t.Fatalf("unlisted = %s, want %s", body, want)
	}
	if got := s.Overview(everything("team-a"), nil).Unlisted; got == nil {
		t.Fatal("unlisted answered null, want an empty list")
	}
}

// A caller who may list no kind is told what a cluster without DeskuBe says,
// even of a pull not synced and stale.
func TestACallerReadingNoKindIsToldNothing(t *testing.T) {
	s, _ := newSnapshot(t)
	off := New(nil, nil, func() bool { return false }).Overview(everything("team-a"), nil)
	mustAdd(t, s.instances, desktop("team-a", "alice", nil))
	s.healthy[0].Store(false)
	if got := s.Overview(everything("team-a"), nil); got.Synced || !got.Stale || len(got.Desktops) != 1 {
		t.Fatalf("a reader reads %+v", got)
	}
	got, err := json.Marshal(s.Overview(map[Kind]bool{}, []Kind{{"team-a", GVRPool.Resource}}))
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(off)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("a caller reading no kind reads %s, a cluster without DeskuBe %s", got, want)
	}
}

type fakeSource struct {
	serves func(schema.GroupVersionResource) (bool, error)
	mu     sync.Mutex
	starts map[string]int
	ended  map[string]int
	lists  map[string][]unstructured.Unstructured
}

func newSource(serves func(schema.GroupVersionResource) (bool, error)) *fakeSource {
	return &fakeSource{serves: serves, starts: map[string]int{}, ended: map[string]int{}, lists: map[string][]unstructured.Unstructured{}}
}

func (f *fakeSource) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	return f.serves(gvr)
}

func (f *fakeSource) Can(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func (f *fakeSource) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}, Items: f.lists[gvr.Resource]}, nil
		},
		WatchFuncWithContext: func(ctx context.Context, _ metav1.ListOptions) (watch.Interface, error) {
			f.mu.Lock()
			f.starts[gvr.Resource]++
			f.mu.Unlock()
			w := watch.NewFake()
			go func() {
				<-ctx.Done()
				f.mu.Lock()
				f.ended[gvr.Resource]++
				f.mu.Unlock()
				w.Stop()
			}()
			return w, nil
		},
	}
}

func (f *fakeSource) watching(n int) func() bool {
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.starts) == n
	}
}

func (f *fakeSource) stopped(n int) func() bool {
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.ended) == n
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func fastPace(t *testing.T) {
	saved := pace
	pace = time.Millisecond
	t.Cleanup(func() { pace = saved })
}

func TestTheModuleGoingOffStopsTheWatchesAndForgets(t *testing.T) {
	fastPace(t)
	src := newSource(func(schema.GroupVersionResource) (bool, error) { return true, nil })
	src.lists[GVRInstance.Resource] = []unstructured.Unstructured{*desktop("team-a", "alice", nil)}
	var on atomic.Bool
	on.Store(true)
	s := New(src, eventbus.New(), on.Load)
	s.Run(t.Context())
	eventually(t, "every watch started", src.watching(4))
	eventually(t, "synced", func() bool { return s.Overview(everything(), nil).Synced })
	if len(s.Overview(everything("team-a"), nil).Desktops) != 1 {
		t.Fatal("the desktop never landed")
	}

	on.Store(false)
	eventually(t, "every watch ended", src.stopped(4))
	eventually(t, "not started", func() bool { return !s.Started() })
	got := s.Overview(everything("team-a"), nil)
	if got.Synced || len(got.Desktops) != 0 {
		t.Fatalf("a stopped module still reads %+v", got)
	}
}

// A DeskuBe release without claims must not hold the pull back.
func TestSyncedWaitsForTheServedKindsOnly(t *testing.T) {
	fastPace(t)
	src := newSource(func(gvr schema.GroupVersionResource) (bool, error) { return gvr != GVRClaim, nil })
	s := New(src, eventbus.New(), func() bool { return true })
	s.Run(t.Context())
	eventually(t, "the served kinds watched", src.watching(3))
	eventually(t, "synced", func() bool { return s.Overview(everything(), nil).Synced })
	if got := s.Overview(everything("team-a"), nil); got.Stale {
		t.Fatal("an absent kind reads as stale")
	}
}

// A failed discovery read is unknown, not absent: the pull stays unsynced
// until the kind answers.
func TestADiscoveryErrorKeepsThePullUnsynced(t *testing.T) {
	fastPace(t)
	var down atomic.Bool
	down.Store(true)
	src := newSource(func(gvr schema.GroupVersionResource) (bool, error) {
		if gvr != GVRClaim {
			return true, nil
		}
		if down.Load() {
			return false, errors.New("discovery unavailable")
		}
		return false, nil
	})
	bus := eventbus.New()
	s := New(src, bus, func() bool { return true })
	s.Run(t.Context())
	eventually(t, "the served kinds watched", src.watching(3))
	eventually(t, "the served kinds listed", func() bool {
		return s.classes.Synced() && s.pools.Synced() && s.instances.Synced()
	})
	if s.Overview(everything(), nil).Synced {
		t.Fatal("a kind discovery could not read reads as absent")
	}

	down.Store(false)
	before := bus.Version(eventbus.DesktopsChanged)
	claims := slices.IndexFunc(s.kinds(), func(k watched) bool { return k.gvr == GVRClaim })
	// The probe the next discovery tick makes.
	s.probe(claims)(GVRClaim)
	if !s.Overview(everything(), nil).Synced {
		t.Fatal("a kind discovery says is absent still holds the pull back")
	}
	if bus.Version(eventbus.DesktopsChanged) == before {
		t.Fatal("the answer published nothing")
	}
}

// A write a stopped module's reflector lands after clear is no row of a
// module that is off.
func TestAStoppedModuleShowsNothing(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.instances, desktop("team-a", "alice", nil))
	s.clear()
	mustAdd(t, s.instances, desktop("team-a", "bob", nil))
	got := s.Overview(everything("team-a"), []Kind{{"team-a", GVRPool.Resource}})
	if got.Synced || len(got.Desktops) != 0 || len(got.Unlisted) != 0 {
		t.Fatalf("a stopped module reads %+v", got)
	}
}
