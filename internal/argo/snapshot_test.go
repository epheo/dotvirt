package argo

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/epheo/dotvirt/internal/model"
)

// appWithSource builds an Application with a single-source repoURL + targetRevision
// and the given status.resources.
func appWithSource(ns, name, repoURL string, resources []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"spec":       map[string]any{"source": map[string]any{"repoURL": repoURL, "targetRevision": "main"}},
		"status":     map[string]any{"resources": resources},
	}}
}

func TestSnapshotDriftNilUntilSynced(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	if s.Drift() != nil {
		t.Fatal("Drift() must be nil before the initial LIST has landed (else every VM flashes NotTracked)")
	}
	_ = s.apps.Add(app("argocd", "managed", []any{vmResource("prod", "vm-a", "Synced", "Healthy")}))
	s.synced.Store(true)

	d := s.Drift()
	if d == nil {
		t.Fatal("Drift() must be non-nil once synced")
	}
	if d["prod/vm-a"].Sync != model.SyncSynced {
		t.Errorf("vm-a drift wrong: %+v", d["prod/vm-a"])
	}
}

func TestSnapshotManagingApp(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	_ = s.apps.Add(appWithSource("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")}))
	s.synced.Store(true)

	ref, ok := s.ManagingApp("prod", "vm-a")
	if !ok || ref.Name != "team-a" || ref.Namespace != "argocd" || ref.TargetRevision != "main" {
		t.Fatalf("ManagingApp wrong: %+v ok=%v", ref, ok)
	}
	if _, ok := s.ManagingApp("prod", "ghost"); ok {
		t.Error("ManagingApp should miss an unmanaged VM")
	}
}

func TestSnapshotRefreshForRepoMatchesByCanonicalURL(t *testing.T) {
	a := appWithSource("argocd", "team-a", "https://Forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	other := appWithSource("argocd", "team-b", "https://forge/team-b.git", []any{})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a, other)

	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	_ = s.apps.Add(other)
	s.synced.Store(true)

	// Pushed as the html_url form (no .git, lowercase host) — must still match the
	// annotated clone_url form (.git, mixed case) via canonical normalization.
	s.RefreshForRepo(context.Background(), "https://forge/team-a")

	got, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()["argocd.argoproj.io/refresh"] != "hard" {
		t.Errorf("team-a should be hard-refreshed, annotations=%v", got.GetAnnotations())
	}
	gotB, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-b", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := gotB.GetAnnotations()["argocd.argoproj.io/refresh"]; has {
		t.Error("team-b (unrelated repo) must not be refreshed")
	}
}

func TestSnapshotRefreshForRepoNoopUntilSynced(t *testing.T) {
	a := appWithSource("argocd", "team-a", "https://forge/team-a.git", nil)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a)
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	// synced is false — RefreshForRepo must be a no-op (no patch).
	s.RefreshForRepo(context.Background(), "https://forge/team-a")

	got, _ := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-a", metav1.GetOptions{})
	if _, has := got.GetAnnotations()["argocd.argoproj.io/refresh"]; has {
		t.Error("RefreshForRepo must not patch before the snapshot has synced")
	}
}

// TestSnapshotDriftMemoizedUntilStoreMoves proves Drift() serves one memoized map
// across calls (the N:1 sharing the hot broadcast path needs) and rebuilds only when
// the reflector signals a store move via driftDirty.
func TestSnapshotDriftMemoizedUntilStoreMoves(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	_ = s.apps.Add(app("argocd", "managed", []any{vmResource("prod", "vm-a", "Synced", "Healthy")}))
	s.synced.Store(true)

	_ = s.Drift() // populate the memoized cache

	// Add an app WITHOUT signalling a move: the memoized map must not yet reflect it.
	_ = s.apps.Add(app("argocd", "managed2", []any{vmResource("prod", "vm-b", "OutOfSync", "Degraded")}))
	if _, ok := s.Drift()["prod/vm-b"]; ok {
		t.Error("Drift() rebuilt without a store-move signal — memoization not in effect")
	}
	// Signal the move (what the reflector's onChange does): the rebuild includes it.
	s.driftDirty.Store(true)
	if _, ok := s.Drift()["prod/vm-b"]; !ok {
		t.Error("Drift() did not rebuild after the store-move signal")
	}
}

// ObjectDriftGen must advance only when a NON-VM object's drift content changes:
// Application churn with identical drift (reconciledAt ticks) and VM-only drift moves
// must not bump it — it feeds the catalog watermark, and a false bump would send
// otherwise-suppressed frames and refetch the catalog for nothing.
func TestObjectDriftGen(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	a := app("argocd", "net", []any{
		vmResource("prod", "vm-a", "Synced", "Healthy"),
		udnResource("prod", "db-net", "Synced", ""),
	})
	_ = s.apps.Add(a)
	s.synced.Store(true)

	gen := s.ObjectDriftGen()

	// Store churn, same content (the reflector marks dirty on ANY object update).
	s.driftDirty.Store(true)
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on contentless churn: %d -> %d", gen, got)
	}

	// A VM-only drift move: rides the frame itself, must not bump the catalog gen.
	_ = s.apps.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "Synced", ""),
	}))
	s.driftDirty.Store(true)
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on a VM-only drift change: %d -> %d", gen, got)
	}

	// The segment's drift moves: gen must bump.
	_ = s.apps.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "OutOfSync", "Progressing"),
	}))
	s.driftDirty.Store(true)
	if got := s.ObjectDriftGen(); got != gen+1 {
		t.Errorf("gen did not bump on a segment drift change: %d -> %d", gen, got)
	}
}
