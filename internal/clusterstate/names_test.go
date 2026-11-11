package clusterstate

import (
	"maps"
	stdreflect "reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/epheo/dotvirt/internal/reflect"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

const projectKey = "dotvirt.io/project"

func namespace(name string, lbls ...string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	for _, l := range lbls {
		ns.Labels[l] = name
	}
	return ns
}

// sinkState is a State whose namespace sink feeds its project store as New
// wires it, minus the bus, recording what it forgets.
func sinkState(t *testing.T) (*State, *[]string) {
	t.Helper()
	s := stateWithIndexers()
	sel, err := labels.Parse(projectKey)
	if err != nil {
		t.Fatal(err)
	}
	forgot := &[]string{}
	store := reflect.NewStore(s.nss, func() { s.nssGen.Add(1) }, nil)
	s.names = newNamespaceSink(sel, store, func(ns string) { *forgot = append(*forgot, ns) }, nil)
	return s, forgot
}

func projects(s *State) []string {
	keys := s.nss.ListKeys()
	slices.Sort(keys)
	return keys
}

// One watch of every namespace holds what a watch filtered by the project
// label would: a namespace losing the label leaves the project store, and
// every name stays in AllNamespaces.
func TestNamespaceSinkSelectsProjectsInMemory(t *testing.T) {
	s, _ := sinkState(t)
	_ = s.names.Replace([]any{namespace("a", projectKey), namespace("default")}, "1")
	if got := projects(s); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("projects after the list: %v", got)
	}
	_ = s.names.Add(namespace("b", projectKey))
	_ = s.names.Update(namespace("a"))
	_ = s.names.Update(namespace("default"))
	if got := projects(s); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("projects after a label came and went: %v", got)
	}
	_ = s.names.Delete(namespace("b", projectKey))
	if got := projects(s); len(got) != 0 {
		t.Fatalf("projects after a delete: %v", got)
	}
	if got := s.AllNamespaces(); !maps.Equal(got, map[string]bool{"a": true, "default": true}) {
		t.Errorf("AllNamespaces = %v", got)
	}
}

// AllNamespaces is every namespace the watch reports plus the project ones;
// one shared map until either moves.
func TestAllNamespaces(t *testing.T) {
	s, _ := sinkState(t)
	_ = s.names.Replace([]any{namespace("proj", projectKey), namespace("default"), namespace("openshift-cnv")}, "1")
	want := map[string]bool{"proj": true, "default": true, "openshift-cnv": true}
	first := s.AllNamespaces()
	if !maps.Equal(first, want) {
		t.Fatalf("got %v, want %v", first, want)
	}
	if stdreflect.ValueOf(s.AllNamespaces()).Pointer() != stdreflect.ValueOf(first).Pointer() {
		t.Error("an unmoved read rebuilt the map")
	}
	_ = s.names.Add(namespace("ci-1"))
	_ = s.names.Delete(namespace("default"))
	if got := s.AllNamespaces(); got["default"] || !got["ci-1"] || !got["proj"] {
		t.Errorf("after an add and a delete: %v", got)
	}
}

// A deleted namespace's grant levels are forgotten once ForgetAfter passed,
// at the next sweep; one recreated in between keeps them.
func TestDeletedNamespacesAreForgotten(t *testing.T) {
	s, forgot := sinkState(t)
	now := time.Unix(1000, 0)
	s.names.now = func() time.Time { return now }
	_ = s.names.Replace([]any{namespace("gone"), namespace("back"), namespace("ci")}, "1")
	_ = s.names.Delete(namespace("gone"))
	_ = s.names.Delete(namespace("back"))
	now = now.Add(time.Minute)
	_ = s.names.Add(namespace("back"))
	now = now.Add(ForgetAfter)
	_ = s.names.Delete(namespace("ci"))
	if !slices.Equal(*forgot, []string{"gone"}) {
		t.Fatalf("forgot %v, want [gone]", *forgot)
	}
}

// A move in a namespace is recent for that namespace alone, a cluster-wide
// one for every namespace; moves older than the horizon are dropped. Only a
// move a frame can read counts for LastGrantMove.
func TestGrantMovesByNamespace(t *testing.T) {
	var g grantMoves
	t0 := time.Unix(1000, 0)
	g.record("x", t0, true)
	if g.last("x") != t0.UnixNano() || g.last("y") != 0 || g.last("") != 0 || g.framed.Load() != t0.UnixNano() {
		t.Fatalf("after a move in x: x=%d y=%d cluster=%d", g.last("x"), g.last("y"), g.last(""))
	}
	g.record("ci", t0.Add(time.Second), false)
	if g.framed.Load() != t0.UnixNano() || g.last("ci") != t0.Add(time.Second).UnixNano() {
		t.Fatal("a move no frame reads moved LastGrantMove, or was not recorded for its namespace")
	}
	t1 := t0.Add(2 * time.Second)
	g.record("", t1, true)
	if g.last("x") != t1.UnixNano() || g.last("y") != t1.UnixNano() {
		t.Fatal("a cluster-wide move is not the latest for every namespace")
	}
	g.record("y", t0.Add(2*grantMoveHorizon), true)
	g.mu.Lock()
	_, kept := g.ns["x"]
	g.mu.Unlock()
	if kept {
		t.Error("a move older than the horizon was kept")
	}
}

// A project's publish namespace is found from the one Namespace watch, though
// no project selects it: it goes once its label changes, it joins a project,
// or it is deleted, a user's own project labeled like one is none, and a
// relist rebuilds what the events built.
func TestNamespaceSinkKeepsPublishNamespaces(t *testing.T) {
	s, _ := sinkState(t)
	publishNS := func(name, project string, more ...string) *corev1.Namespace {
		ns := namespace(name, more...)
		ns.Labels[tenancy.PublishLabel] = project
		return ns
	}
	found := func(project string) string {
		ns, _ := s.PublishNamespace(project)
		return ns
	}
	_ = s.names.Replace([]any{publishNS("shop-publish", "shop"), publishNS("web", "web"), namespace("default")}, "1")
	if found("shop") != "shop-publish" || found("web") != "" {
		t.Fatalf("after the list: shop %q, web %q; want shop-publish and none", found("shop"), found("web"))
	}
	if got := projects(s); len(got) != 0 {
		t.Errorf("a publish namespace is in no project, yet the project store holds %v", got)
	}
	_ = s.names.Update(publishNS("shop-publish", "other"))
	if found("shop") != "" {
		t.Errorf("relabeled for another project, still shop's")
	}
	_ = s.names.Update(publishNS("shop-publish", "shop"))
	_ = s.names.Update(publishNS("shop-publish", "shop", projectKey))
	if found("shop") != "" {
		t.Errorf("joined a project, still a publish namespace")
	}
	squatted := publishNS("shop-publish", "shop")
	squatted.Annotations = map[string]string{tenancy.RequesterAnnotation: "mallory"}
	_ = s.names.Update(squatted)
	if found("shop") != "" {
		t.Errorf("a user's own project, labeled by the platform sync, taken for shop's")
	}
	_ = s.names.Add(publishNS("db-publish", "db"))
	_ = s.names.Delete(publishNS("db-publish", "db"))
	if found("db") != "" {
		t.Errorf("deleted, still found")
	}
	_ = s.names.Replace([]any{publishNS("db-publish", "db")}, "2")
	if found("db") != "db-publish" || found("shop") != "" {
		t.Errorf("after a relist: db %q, shop %q", found("db"), found("shop"))
	}
}

// A publish namespace's name held by another namespace is told apart from a
// free one, and a user's own project from the rest: the events and a relist
// say the same.
func TestNamespaceSinkTellsAHeldPublishName(t *testing.T) {
	s, _ := sinkState(t)
	requested := func(name string) *corev1.Namespace {
		ns := namespace(name)
		ns.Annotations = map[string]string{tenancy.RequesterAnnotation: "mallory"}
		return ns
	}
	held := func(project string) [2]bool {
		h, r := s.PublishNameHeld(project)
		return [2]bool{h, r}
	}
	_ = s.names.Replace([]any{requested("shop-publish"), namespace("db-publish"), namespace("web")}, "1")
	if held("shop") != [2]bool{true, true} || held("db") != [2]bool{true, false} || held("web") != [2]bool{} {
		t.Fatalf("after the list: shop %v, db %v, web %v", held("shop"), held("db"), held("web"))
	}
	ours := namespace("db-publish")
	ours.Labels[tenancy.PublishLabel] = "db"
	_ = s.names.Update(ours)
	_ = s.names.Delete(requested("shop-publish"))
	if held("db") != [2]bool{} || held("shop") != [2]bool{} {
		t.Errorf("db's own publish namespace, and shop's freed name: db %v, shop %v", held("db"), held("shop"))
	}
	_ = s.names.Add(requested("shop-publish"))
	if held("shop") != [2]bool{true, true} {
		t.Errorf("requested again: %v", held("shop"))
	}
}

// The namespaces labeled for a project's VM Network are known from the one
// watch, a released one, in no project, among them.
func TestNamespaceSinkKnowsNetworkMembers(t *testing.T) {
	s, _ := sinkState(t)
	on := func(name, net string, more ...string) *corev1.Namespace {
		ns := namespace(name, more...)
		ns.Labels[tenancy.NetworkLabel] = net
		return ns
	}
	_ = s.names.Replace([]any{on("web", "shop", projectKey), on("old", "shop"), on("db", "other", projectKey), namespace("plain")}, "1")
	if got := s.NetworkMembers("shop"); !slices.Equal(got, []string{"old", "web"}) {
		t.Errorf("after the list: %v", got)
	}
	_ = s.names.Delete(on("old", "shop"))
	_ = s.names.Update(namespace("web", projectKey))
	if got := s.NetworkMembers("shop"); len(got) != 0 {
		t.Errorf("after a delete and a relabel: %v", got)
	}
}
