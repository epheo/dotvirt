package argo

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Snapshot is the SA-maintained, watch-fed in-memory view of every ArgoCD
// Application — the drift plane's equivalent of clusterstate.State. A single
// reflector keeps the store current and publishes DriftChanged on the shared bus so
// the hub rebroadcasts; reads (Drift, ManagingApp) are lock-free store scans, never
// a cluster LIST. The store is always current post-sync, so drift is never staler
// than the watch delivers.
type Snapshot struct {
	sa       *Client
	apps     cache.Indexer
	synced   atomic.Bool
	syncedCh chan struct{} // closed on the initial LIST, so WaitForSync doesn't poll
	healthy  atomic.Bool   // false while the Applications watch errors (ArgoCD unreachable)
	bus      *eventbus.Bus

	// Memoized drift: rebuilt lazily only when the Application store moves
	// (driftDirty), so one reconcile across N identities parses the apps once, not N
	// times — the N:1 sharing the read path needs on the hot inventory-build path.
	driftMu    sync.Mutex
	driftCache map[string]Drift
	driftDirty atomic.Bool
}

// NewSnapshot builds the Application snapshot over the SA argo client. bus may be
// nil (signalling disabled, e.g. in tests).
func NewSnapshot(sa *Client, bus *eventbus.Bus) *Snapshot {
	s := &Snapshot{
		sa:       sa,
		apps:     cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}),
		syncedCh: make(chan struct{}),
		bus:      bus,
	}
	s.healthy.Store(true) // optimistic until a list/watch actually errors
	return s
}

// Run starts the Applications reflector; it owns its own relist/backoff and stops
// when ctx is cancelled. Returns immediately — call WaitForSync to block until the
// initial LIST has populated the snapshot.
func (s *Snapshot) Run(ctx context.Context) {
	store := reflect.NewStore(s.apps,
		func() { s.driftDirty.Store(true); s.bus.Publish(eventbus.DriftChanged) },
		func() { s.synced.Store(true); close(s.syncedCh) }, // onSynced fires once → close is safe
	)
	r := cache.NewReflector(s.healthTracking(s.sa.ApplicationsListWatch()), &unstructured.Unstructured{}, store, 0)
	go r.Run(ctx.Done())
}

// Healthy reports whether the Applications watch is currently established. It goes
// false when the reflector's list/watch errors (ArgoCD unreachable) and true again
// when a watch re-establishes — a deterministic, error-driven staleness signal, not
// a TTL. Drift keeps serving its last-good store while unhealthy; the inventory
// surfaces a "may be stale" warning so a permanent outage isn't silent.
func (s *Snapshot) Healthy() bool { return s.healthy.Load() }

// healthTracking wraps lw so a failed List or Watch flips healthy false and a
// successful Watch establish flips it true. The reflector re-lists+re-watches on a
// drop, so a transient blip that immediately recovers stays healthy; a sustained
// outage (repeated errors) reads as unhealthy.
func (s *Snapshot) healthTracking(lw *cache.ListWatch) *cache.ListWatch {
	list, watchFn := lw.ListFunc, lw.WatchFunc
	return &cache.ListWatch{
		ListFunc: func(o metav1.ListOptions) (runtime.Object, error) {
			obj, err := list(o)
			if err != nil {
				s.healthy.Store(false)
			}
			return obj, err
		},
		WatchFunc: func(o metav1.ListOptions) (watch.Interface, error) {
			w, err := watchFn(o)
			s.healthy.Store(err == nil)
			return w, err
		},
	}
}

// WaitForSync blocks until the initial Applications LIST has landed or ctx is done.
// Deterministic — waits on the channel closed by the initial Replace, not a poll.
func (s *Snapshot) WaitForSync(ctx context.Context) error {
	select {
	case <-s.syncedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Synced reports whether the initial Applications LIST has landed.
func (s *Snapshot) Synced() bool { return s.synced.Load() }

// Drift returns per-VM drift keyed "namespace/name", computed from the in-memory
// Application store. It returns nil UNTIL the initial LIST has landed: inventory
// treats a non-nil (even empty) Drift map as "Argo is configured, so a VM absent
// from it is NotTracked" — returning an empty map pre-sync would flash every VM to
// NotTracked. nil instead leaves Sync unset (the benign not-yet-known state), which
// the caller distinguishes from "Argo disabled" via Synced(). Always non-nil once
// synced.
func (s *Snapshot) Drift() map[string]Drift {
	if !s.synced.Load() {
		return nil
	}
	s.driftMu.Lock()
	defer s.driftMu.Unlock()
	// Rebuild only when the store has moved since the last build, so one reconcile
	// across N identities shares one parse instead of re-scanning every Application
	// per identity. The result is read-only to callers; a rebuild swaps in a fresh map
	// (driftFromApps allocates anew), never mutating one a caller still holds.
	if s.driftCache == nil || s.driftDirty.Swap(false) {
		s.driftCache = driftFromApps(s.apps.List())
	}
	return s.driftCache
}

// AppRef locates the Application managing a VM: enough to read its sync revision and
// to address it for a patch.
type AppRef struct {
	Namespace      string
	Name           string
	TargetRevision string
}

// ManagingApp returns the Application whose status.resources[] includes the
// VirtualMachine namespace/name, read from the in-memory store (no cluster call).
// ok=false if the snapshot hasn't synced or no Application manages the VM — the
// caller (Resync) then falls back to a live lookup.
func (s *Snapshot) ManagingApp(namespace, name string) (AppRef, bool) {
	if !s.synced.Load() {
		return AppRef{}, false
	}
	for _, obj := range s.apps.List() {
		app, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if appManagesVM(app.Object, namespace, name) {
			return appRefOf(app), true
		}
	}
	return AppRef{}, false
}

// Resync triggers an ArgoCD sync of the Application managing the given VM, so the
// cluster is reconciled back to git. It finds the managing Application from the
// in-memory snapshot (falling back to a live LIST when the snapshot hasn't synced,
// so a resync right after startup doesn't spuriously 404), then requests a sync via
// the Application's operation field — the k8s API dotvirt already has, no separate
// Argo API token. (Distinct from RefreshForRepo: this is the user's explicit
// per-VM "sync now", which must work even when auto-sync is off.)
func (s *Snapshot) Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error) {
	ref, ok := s.ManagingApp(namespace, name)
	if !ok {
		ref, ok = s.sa.findManagingAppLive(ctx, namespace, name)
		if !ok {
			return model.ResyncResult{}, fmt.Errorf("no ArgoCD Application manages %s/%s", namespace, name)
		}
	}
	revision := ref.TargetRevision
	if revision == "" {
		revision = "HEAD"
	}
	patch := fmt.Appendf(nil,
		`{"operation":{"initiatedBy":{"username":"dotvirt"},"sync":{"revision":%q},"info":[{"name":"reason","value":"dotvirt re-sync from git"}]}}`,
		revision,
	)
	if _, err := s.sa.patchApp(ctx, ref.Namespace, ref.Name, patch); err != nil {
		return model.ResyncResult{}, fmt.Errorf("trigger sync of %s: %w", ref.Name, err)
	}
	return model.ResyncResult{Application: ref.Name, Revision: revision}, nil
}

// RefreshForRepo asks ArgoCD to re-pull git and re-sync every Application sourcing
// any of repoURLs — the deterministic "pick up on push" path, driven by the forge
// webhook dotvirt already receives and HMAC-verifies. It matches spec.source.repoURL
// (and any spec.sources[]) against the pushed URLs by canonical form, then sets the
// argocd.argoproj.io/refresh=hard annotation: a hard refresh re-pulls git, and with
// auto-sync (every dotvirt-generated app) the resulting OutOfSync syncs itself.
// Best-effort: errors are logged, never returned — ArgoCD's own webhook + poll
// remain as backstops. No-op until the snapshot has synced.
func (s *Snapshot) RefreshForRepo(ctx context.Context, repoURLs ...string) {
	if !s.synced.Load() {
		return
	}
	want := make(map[string]bool, len(repoURLs))
	for _, u := range repoURLs {
		if n := forge.NormalizeRepoURL(u); n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return
	}
	for _, obj := range s.apps.List() {
		app, ok := obj.(*unstructured.Unstructured)
		if !ok || !appSourcesAny(app.Object, want) {
			continue
		}
		patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
		if _, err := s.sa.patchApp(ctx, app.GetNamespace(), app.GetName(), patch); err != nil {
			log.Printf("argo: refresh %s/%s: %v", app.GetNamespace(), app.GetName(), err)
		}
	}
}

// patchApp merge-patches the named Application — the single write the drift plane
// makes (operation for Resync, refresh annotation for RefreshForRepo).
func (c *Client) patchApp(ctx context.Context, namespace, name string, patch []byte) (*unstructured.Unstructured, error) {
	return c.dyn.Resource(applicationsGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
}

// findManagingAppLive is ManagingApp's cluster fallback: a one-shot LIST used only
// when the snapshot hasn't synced yet, so an early resync still resolves its app.
func (c *Client) findManagingAppLive(ctx context.Context, namespace, name string) (AppRef, bool) {
	apps, err := c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return AppRef{}, false
	}
	for i := range apps.Items {
		if appManagesVM(apps.Items[i].Object, namespace, name) {
			return appRefOf(&apps.Items[i]), true
		}
	}
	return AppRef{}, false
}

// appRefOf reads the AppRef (name/namespace/sync revision) from an Application.
func appRefOf(app *unstructured.Unstructured) AppRef {
	rev, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	return AppRef{Namespace: app.GetNamespace(), Name: app.GetName(), TargetRevision: rev}
}

// appManagesVM reports whether app's status.resources[] includes the VirtualMachine
// namespace/name.
func appManagesVM(app map[string]any, namespace, name string) bool {
	resources, found, err := unstructured.NestedSlice(app, "status", "resources")
	if err != nil || !found {
		return false
	}
	for _, raw := range resources {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if asString(res, "kind") == "VirtualMachine" &&
			asString(res, "namespace") == namespace &&
			asString(res, "name") == name {
			return true
		}
	}
	return false
}

// appSourcesAny reports whether app's source(s) reference any repo in want (keyed by
// canonical URL). Handles both single-source (spec.source) and multi-source
// (spec.sources[]) Applications.
func appSourcesAny(app map[string]any, want map[string]bool) bool {
	if u, found, _ := unstructured.NestedString(app, "spec", "source", "repoURL"); found && want[forge.NormalizeRepoURL(u)] {
		return true
	}
	sources, found, _ := unstructured.NestedSlice(app, "spec", "sources")
	if !found {
		return false
	}
	for _, raw := range sources {
		if src, ok := raw.(map[string]any); ok && want[forge.NormalizeRepoURL(asString(src, "repoURL"))] {
			return true
		}
	}
	return false
}
