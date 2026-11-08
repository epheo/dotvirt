package clusterstate

import (
	"maps"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// AllNamespaces is every namespace the all-namespaces watch reports plus the
// project ones, which may land first; one shared map until either moves.
func TestAllNamespaces(t *testing.T) {
	s := stateWithIndexers()
	s.names = newNameSet(nil)
	if err := s.nss.Add(namespace("proj")); err != nil {
		t.Fatal(err)
	}
	s.nssGen.Add(1)
	if got := s.AllNamespaces(); !maps.Equal(got, map[string]bool{"proj": true}) {
		t.Fatalf("before the watch listed: %v, want the project namespaces", got)
	}
	_ = s.names.Replace([]any{namespace("default"), namespace("openshift-cnv")}, "1")
	want := map[string]bool{"proj": true, "default": true, "openshift-cnv": true}
	first := s.AllNamespaces()
	if !maps.Equal(first, want) {
		t.Fatalf("got %v, want %v", first, want)
	}
	if reflect.ValueOf(s.AllNamespaces()).Pointer() != reflect.ValueOf(first).Pointer() {
		t.Error("an unmoved read rebuilt the map")
	}
	_ = s.names.Add(namespace("ci-1"))
	_ = s.names.Delete(cache.DeletedFinalStateUnknown{Key: "default", Obj: namespace("default")})
	if got := s.AllNamespaces(); got["default"] || !got["ci-1"] || !got["proj"] {
		t.Errorf("after an add and a delete: %v", got)
	}
}

// A move in a namespace is recent for that namespace alone, a cluster-wide
// one for every namespace; moves older than the horizon are dropped.
func TestGrantMovesByNamespace(t *testing.T) {
	var g grantMoves
	t0 := time.Unix(1000, 0)
	g.record("x", t0)
	if g.last("x") != t0.UnixNano() || g.last("y") != 0 || g.last("") != 0 || g.any.Load() != t0.UnixNano() {
		t.Fatalf("after a move in x: x=%d y=%d cluster=%d", g.last("x"), g.last("y"), g.last(""))
	}
	t1 := t0.Add(time.Second)
	g.record("", t1)
	if g.last("x") != t1.UnixNano() || g.last("y") != t1.UnixNano() {
		t.Fatal("a cluster-wide move is not the latest for every namespace")
	}
	g.record("y", t0.Add(2*grantMoveHorizon))
	g.mu.Lock()
	_, kept := g.ns["x"]
	g.mu.Unlock()
	if kept {
		t.Error("a move older than the horizon was kept")
	}
}
