package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Snapshot is the SA-maintained, watch-fed in-memory view of every ArgoCD
// Application - the drift plane's equivalent of clusterstate.State. A single
// reflector keeps the store current and derives the drift view on every store
// move; reads are lock-free (an atomic view load, or a store scan), never a
// cluster LIST, so drift is never staler than the watch delivers.
type Snapshot struct {
	sa      *Client
	apps    cache.Indexer
	synced  reflect.Ready
	healthy atomic.Bool // false while the Applications watch errors (ArgoCD unreachable)
	bus     *eventbus.Bus

	// view is derived once per store move and shared by every identity's frame.
	view atomic.Pointer[driftView]
}

// driftView is one derivation of the Application store: the per-object map, its
// per-VM slice, and the per-project rollup, all from the same sorted scan.
// Immutable once stored; readers share its maps.
type driftView struct {
	resources map[resKey]Drift
	vms       map[string]Drift
	projects  map[string]model.ProjectSync
	// objGen counts content changes to the non-VM entries only: VM drift rides the
	// frame itself, and objGen feeds the catalog watermark, which must not move
	// (and refetch the catalog) on a VM badge change.
	objGen uint64
}

// NewSnapshot builds the Application snapshot over the SA argo client. bus may be
// nil (signalling disabled, e.g. in tests).
func NewSnapshot(sa *Client, bus *eventbus.Bus) *Snapshot {
	return &Snapshot{sa: sa, apps: reflect.NewIndexer(), bus: bus}
}

// Run starts the Applications reflector; it owns its own relist/backoff and stops
// when ctx is cancelled. Returns immediately - call WaitForSync to block until the
// initial LIST has populated the snapshot.
func (s *Snapshot) Run(ctx context.Context) {
	reflect.Run(ctx, reflect.TrackHealth(s.sa.applicationsListWatch(), &s.healthy, s.publish), s.store())
}

// store is the reflector's sink: every mutation re-derives the view.
func (s *Snapshot) store() cache.Store {
	return reflect.NewStore(s.apps, s.derive, s.synced.Mark)
}

// derive rebuilds the view from the store and publishes DriftChanged only when
// its content moved: Argo rewrites every Application on each reconcile
// (status.reconciledAt), and publishing those rebuilt every connected frame to
// find identical bytes. The scan is sorted so two apps claiming one repo or one
// resource resolve the same way every time and never flap. Runs on the reflector
// goroutine only, so prev cannot move underneath it.
func (s *Snapshot) derive() {
	objs := reflect.List(s.apps)
	sort.Slice(objs, func(i, j int) bool { return appKey(objs[i]) < appKey(objs[j]) })
	next := &driftView{resources: resourceDriftFromApps(objs), projects: appSyncFromApps(objs)}
	prev := s.view.Load()
	var prevRes map[resKey]Drift
	if prev != nil {
		if maps.Equal(prev.resources, next.resources) && equality.Semantic.DeepEqual(prev.projects, next.projects) {
			return
		}
		prevRes, next.objGen = prev.resources, prev.objGen
	}
	if !nonVMEqual(prevRes, next.resources) {
		next.objGen++
	}
	next.vms = vmView(next.resources)
	s.view.Store(next)
	s.publish()
}

// current is the drift view, nil until the initial LIST has landed and been
// derived (the store marks synced just before it derives).
func (s *Snapshot) current() *driftView {
	if !s.synced.Done() {
		return nil
	}
	return s.view.Load()
}

func (s *Snapshot) publish() { s.bus.Publish(eventbus.DriftChanged) }

// Healthy reports whether the Applications watch is currently established
// (reflect.TrackHealth). Drift keeps serving its last-good store while
// unhealthy; the inventory surfaces a "may be stale" warning so a permanent
// outage isn't silent.
func (s *Snapshot) Healthy() bool { return s.healthy.Load() }

// WaitForSync blocks until the initial Applications LIST has landed or ctx is done.
func (s *Snapshot) WaitForSync(ctx context.Context) error { return s.synced.Wait(ctx) }

// Drift returns per-VM drift keyed "namespace/name". It returns nil UNTIL the
// initial LIST has landed: inventory treats a non-nil (even empty) Drift map as
// "Argo is configured, so a VM absent from it is NotTracked" - returning an empty
// map pre-sync would flash every VM to NotTracked. nil instead leaves Sync unset
// (the benign not-yet-known state), the same as when Argo is disabled. Always
// non-nil once synced. Shared and read-only.
func (s *Snapshot) Drift() map[string]Drift {
	if v := s.current(); v != nil {
		return v.vms
	}
	return nil
}

// ProjectDrift returns each project's overall Application sync/health keyed by
// canonical repoURL - the rollup covering every object kind the repo declares, not
// just VMs. Same nil-before-sync contract as Drift, so the inventory build can
// distinguish "Argo disabled/not-yet-synced" (nil) from "tracked" (non-nil, a repo
// absent from it simply has no managing Application). Shared and read-only.
func (s *Snapshot) ProjectDrift() map[string]model.ProjectSync {
	if v := s.current(); v != nil {
		return v.projects
	}
	return nil
}

// ForeignApps: live apps except those sourcing ownRepo, under every tracking-id
// identity (name, ns_name, ns/name). Only a FOREIGN app is a claim; the own app
// survives a lost repo while git declares nothing, and counting it blocked the
// recovery capture (git is the authority for the own tier). A deleted app's
// annotation is residue, not a claim. nil before the initial LIST so callers
// refuse rather than misread.
func (s *Snapshot) ForeignApps(ownRepo string) map[string]bool {
	if !s.synced.Done() {
		return nil
	}
	own := forge.NormalizeRepoURL(ownRepo)
	out := map[string]bool{}
	for _, u := range reflect.List(s.apps) {
		if own != "" && slices.Contains(appRepos(u.Object), own) {
			continue
		}
		out[u.GetName()] = true
		out[u.GetNamespace()+"_"+u.GetName()] = true
		out[u.GetNamespace()+"/"+u.GetName()] = true
	}
	return out
}

// PrunePending: what Argo itself would prune for the apps sourcing repo, within
// namespaces (status.resources[].requiresPruning). Argo's comparison is the one
// authority on what a sync deletes; re-deriving from git could drift from it.
// nil before the initial LIST.
func (s *Snapshot) PrunePending(repo string, namespaces []string) []model.ObjectRef {
	want := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		want[ns] = true
	}
	own := forge.NormalizeRepoURL(repo)
	if !s.synced.Done() || own == "" {
		return nil
	}
	var out []model.ObjectRef
	for _, u := range reflect.List(s.apps) {
		if !slices.Contains(appRepos(u.Object), own) {
			continue
		}
		for _, raw := range nestedSlice(u.Object, "status", "resources") {
			res, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if prune, _ := res["requiresPruning"].(bool); !prune {
				continue
			}
			ref := model.ObjectRef{
				Kind:      asString(res, "kind"),
				Namespace: asString(res, "namespace"),
				Name:      asString(res, "name"),
			}
			if ref.Kind == "" || ref.Name == "" || !want[ref.Namespace] {
				continue
			}
			out = append(out, ref)
		}
	}
	// Sorted: the derived warning string must not flap on store-scan order.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out
}

// ResourceDrift returns one Argo-managed object's sync/health by identity; ok=false if
// no Application manages it or the snapshot hasn't synced. General across kinds - the
// per-object surface VMs and segments (and any future rendered object) share. group/kind
// are the ArgoCD resource's own (e.g. "k8s.ovn.org","UserDefinedNetwork"); namespace is
// "" for cluster-scoped kinds.
func (s *Snapshot) ResourceDrift(group, kind, namespace, name string) (Drift, bool) {
	v := s.current()
	if v == nil {
		return Drift{}, false
	}
	d, ok := v.resources[resKey{group, kind, namespace, name}]
	return d, ok
}

// ObjectDriftGen returns the generation of the non-VM drift content - a monotonic
// counter that moves only when a non-VM object's sync/health/error actually changed,
// not on every Application object churn.
func (s *Snapshot) ObjectDriftGen() uint64 {
	if v := s.current(); v != nil {
		return v.objGen
	}
	return 0
}

// appKey is an Application's stable sort key for the deterministic rebuild scan.
func appKey(u *unstructured.Unstructured) string {
	return u.GetNamespace() + "/" + u.GetName()
}

// nonVMEqual reports whether the non-VM drift entries of two builds match. VM entries
// are excluded because their changes already repaint the frame (per-VM badges), so only
// other kinds should advance the catalog watermark.
func nonVMEqual(a, b map[resKey]Drift) bool {
	isVM := func(k resKey) bool { return k.group == "kubevirt.io" && k.kind == "VirtualMachine" }
	count := func(m map[resKey]Drift) int {
		n := 0
		for k := range m {
			if !isVM(k) {
				n++
			}
		}
		return n
	}
	if count(a) != count(b) {
		return false
	}
	for k, d := range a {
		if isVM(k) {
			continue
		}
		if bd, ok := b[k]; !ok || bd != d {
			return false
		}
	}
	return true
}

// AppRef locates the Application managing a VM: enough to read its sync revision and
// to address it for a patch.
type AppRef struct {
	Namespace      string
	Name           string
	TargetRevision string
}

// managingApp returns the Application whose status.resources[] includes the
// VirtualMachine namespace/name, read from the in-memory store (no cluster call).
// ok=false if the snapshot hasn't synced or no Application manages the VM - the
// caller (Resync) then falls back to a live lookup.
func (s *Snapshot) managingApp(namespace, name string) (AppRef, bool) {
	if !s.synced.Done() {
		return AppRef{}, false
	}
	for _, app := range reflect.List(s.apps) {
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
// the Application's operation field - the k8s API dotvirt already has, no separate
// Argo API token. (Distinct from RefreshForRepo: this is the user's explicit
// per-VM "sync now", which must work even when auto-sync is off.) username is
// the caller, recorded as the operation's initiator so Argo's history names the
// person, not dotvirt's SA.
func (s *Snapshot) Resync(ctx context.Context, username, namespace, name string) (model.ResyncResult, error) {
	ref, ok := s.managingApp(namespace, name)
	if !ok {
		ref, ok = s.sa.findmanagingAppLive(ctx, namespace, name)
		if !ok {
			return model.ResyncResult{}, fmt.Errorf("%w: no ArgoCD Application manages %s/%s", model.ErrNotFound, namespace, name)
		}
	}
	revision := ref.TargetRevision
	if revision == "" {
		revision = "HEAD"
	}
	patch, err := resyncPatch(username, revision)
	if err != nil {
		return model.ResyncResult{}, err
	}
	if _, err := s.sa.patchApp(ctx, ref.Namespace, ref.Name, patch); err != nil {
		return model.ResyncResult{}, fmt.Errorf("trigger sync of %s: %w", ref.Name, err)
	}
	return model.ResyncResult{Application: ref.Name, Revision: revision}, nil
}

// resyncPatch is the Application operation requesting a sync to revision. It
// is marshalled, not formatted: username is caller-controlled text.
func resyncPatch(username, revision string) ([]byte, error) {
	type kv struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	var op struct {
		Operation struct {
			InitiatedBy struct {
				Username string `json:"username"`
			} `json:"initiatedBy"`
			Sync struct {
				Revision string `json:"revision"`
			} `json:"sync"`
			Info []kv `json:"info"`
		} `json:"operation"`
	}
	op.Operation.InitiatedBy.Username = username
	op.Operation.Sync.Revision = revision
	op.Operation.Info = []kv{{"reason", "dotvirt re-sync from git"}}
	return json.Marshal(op)
}

// RefreshForRepo asks ArgoCD to re-pull git and re-sync every Application sourcing
// any of repoURLs - the deterministic "pick up on push" path, driven by the forge
// webhook dotvirt already receives and HMAC-verifies. It matches spec.source.repoURL
// (and any spec.sources[]) against the pushed URLs by canonical form, then sets the
// argocd.argoproj.io/refresh=hard annotation: a hard refresh re-pulls git, and with
// auto-sync (every dotvirt-generated app) the resulting OutOfSync syncs itself.
// Best-effort: errors are logged, never returned - ArgoCD's own webhook + poll
// remain as backstops. No-op until the snapshot has synced.
func (s *Snapshot) RefreshForRepo(ctx context.Context, repoURLs ...string) {
	if !s.synced.Done() {
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
	for _, app := range reflect.List(s.apps) {
		if !appSourcesAny(app.Object, want) {
			continue
		}
		patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
		if _, err := s.sa.patchApp(ctx, app.GetNamespace(), app.GetName(), patch); err != nil {
			log.Printf("argo: refresh %s/%s: %v", app.GetNamespace(), app.GetName(), err)
		}
	}
}

// patchApp merge-patches the named Application - the single write the drift plane
// makes (operation for Resync, refresh annotation for RefreshForRepo).
func (c *Client) patchApp(ctx context.Context, namespace, name string, patch []byte) (*unstructured.Unstructured, error) {
	return c.dyn.Resource(applicationsGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
}

// findmanagingAppLive is managingApp's cluster fallback: a one-shot LIST used only
// when the snapshot hasn't synced yet, so an early resync still resolves its app.
func (c *Client) findmanagingAppLive(ctx context.Context, namespace, name string) (AppRef, bool) {
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
	for _, raw := range nestedSlice(app, "status", "resources") {
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

// appSourcesAny reports whether the app sources any repo in want (canonical URLs).
func appSourcesAny(app map[string]any, want map[string]bool) bool {
	for _, r := range appRepos(app) {
		if want[r] {
			return true
		}
	}
	return false
}
