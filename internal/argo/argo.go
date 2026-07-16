// Package argo is dotvirt's drift-read plane: it reads ArgoCD Application CRs and
// reports each VM's sync/health straight from Argo's own status, so dotvirt never
// re-implements diffing. Like the cluster plane, identity is per-token: a Factory
// mints one Client per bearer token (drift is read as the user); the SA client
// drives background watches + resync, which have no user context.
package argo

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/pkg/forge"
)

// applicationsGVR is the ArgoCD Application resource.
var applicationsGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// Drift is one VM's sync/health as reported by the managing Application.
type Drift struct {
	Sync   model.SyncStatus
	Health string
	// Message is the apply/sync error from the Application's last operation, when
	// this VM failed to sync (e.g. a KubeVirt webhook rejection). Empty when synced.
	Message string
}

// Client reads Application status via the dynamic client — no heavy argo-cd
// module, just the handful of status fields dotvirt needs. One identity.
type Client struct {
	dyn dynamic.Interface
}

// Factory mints per-token Clients, reusing the shared restfactory for the
// identity machinery (see cluster.Factory). The SA client drives background
// watches + resync.
type Factory struct {
	*restfactory.Factory[*Client]
}

// NewFactory builds a Factory. kubeconfig empty means in-cluster config.
func NewFactory(kubeconfig string) (*Factory, error) {
	base, err := restfactory.New(kubeconfig, clientFor)
	if err != nil {
		return nil, err
	}
	return &Factory{base}, nil
}

// clientFor is the restfactory build hook: a token-bearing config → a drift Client.
func clientFor(cfg *rest.Config) (*Client, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return &Client{dyn: dyn}, nil
}

// ApplicationsListWatch is the List+Watch source for the cluster-wide ArgoCD
// Application reflector that backs Snapshot — the drift plane's equivalent of
// cluster.VMListWatch.
func (c *Client) ApplicationsListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// resKey identifies one ArgoCD-managed object across kinds — the key of the general
// per-object drift map that VMs, segments, and any future rendered object share.
// namespace is "" for cluster-scoped kinds (a CUDN, an EgressIP).
type resKey struct{ group, kind, namespace, name string }

// resourceDriftFromApps builds per-object drift keyed by identity from a set of ArgoCD
// Application objects (the reflector store's *unstructured.Unstructured). Un-scoped:
// EVERY kind an Application manages is kept, so a non-VM object (a segment, a policy)
// carries the same sync/health VMs always had — the per-VM view (vmView) and the
// per-segment enrichment both read this one map. Objects absent from the result are
// managed by no Application. Only scalar fields are read, so nothing escapes the store
// to be mutated. Always returns a non-nil map.
func resourceDriftFromApps(objs []any) map[resKey]Drift {
	out := map[resKey]Drift{}
	for _, obj := range objs {
		app, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		resources, found, err := unstructured.NestedSlice(app.Object, "status", "resources")
		if err == nil && found {
			for _, raw := range resources {
				res, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if asString(res, "name") == "" {
					continue
				}
				out[keyOf(res)] = Drift{
					Sync:   syncStatus(asString(res, "status")),
					Health: nestedString(res, "health", "status"),
				}
			}
		}
		mergeSyncMessages(out, app.Object)
	}
	return out
}

// vmView is the VM slice of the general drift map, re-keyed "namespace/name" — the
// shape the inventory VM enrichment consumes.
func vmView(all map[resKey]Drift) map[string]Drift {
	out := make(map[string]Drift, len(all))
	for k, d := range all {
		if k.group == "kubevirt.io" && k.kind == "VirtualMachine" {
			out[k.namespace+"/"+k.name] = d
		}
	}
	return out
}

// driftFromApps returns the per-VM drift keyed "namespace/name" — the VM view over the
// general map. VMs absent from the result are managed by no Application (caller reports
// NotTracked). Always non-nil.
func driftFromApps(objs []any) map[string]Drift { return vmView(resourceDriftFromApps(objs)) }

// keyOf reads an object's identity out of an ArgoCD status.resources[] /
// syncResult.resources[] entry.
func keyOf(res map[string]any) resKey {
	return resKey{asString(res, "group"), asString(res, "kind"), asString(res, "namespace"), asString(res, "name")}
}

// mergeSyncMessages attaches per-object apply errors onto the drift map. ArgoCD keeps
// the live tree in status.resources[] (sync/health, no error text) but the actual
// apply failure for each object in status.operationState.syncResult.resources[]. We
// surface the latter so the UI can show *why* an object is OutOfSync. Synced rows carry
// a benign "unchanged" message, so only non-Synced ones are kept.
func mergeSyncMessages(out map[resKey]Drift, app map[string]any) {
	results, found, err := unstructured.NestedSlice(app, "status", "operationState", "syncResult", "resources")
	if err != nil || !found {
		return
	}
	for _, raw := range results {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		msg := asString(res, "message")
		if msg == "" || asString(res, "status") == "Synced" {
			continue
		}
		// Only annotate an object the live tree (status.resources[]) already reported. An
		// object present ONLY here — a failed first apply that never entered the live tree
		// — must NOT be synthesized with a zero Sync (""): that empty status crashes the
		// frontend SyncBadge (it indexes its style table by sync). Such an object stays
		// absent so the caller reports NotTracked.
		d, ok := out[keyOf(res)]
		if !ok {
			continue
		}
		d.Message = msg
		out[keyOf(res)] = d
	}
}

// appSyncFromApps builds each project's overall sync/health keyed by canonical
// repoURL, read straight from the managing Application's own rollup
// (status.sync/health/operationState). Unlike driftFromApps, which keeps only VMs,
// this rollup is exactly what covers every kind the Application manages — segments,
// network policies, tenancy — so a merged PR that fails to apply for a non-VM object
// still surfaces. One repo maps to one Application in dotvirt's model; on the rare
// collision the more severe status wins, so a rollup never hides a degraded app behind
// a healthy one. Always returns a non-nil map.
func appSyncFromApps(objs []any) map[string]model.ProjectSync {
	out := map[string]model.ProjectSync{}
	for _, obj := range objs {
		app, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		repo := appRepo(app.Object)
		if repo == "" {
			continue
		}
		ps := model.ProjectSync{
			Sync:      syncStatus(nestedString(app.Object, "status", "sync", "status")),
			Health:    nestedString(app.Object, "status", "health", "status"),
			Operation: nestedString(app.Object, "status", "operationState", "phase"),
			Revision:  shortRev(nestedString(app.Object, "status", "sync", "revision")),
		}
		// Surface the apply error only when the last operation didn't succeed — a clean
		// sync leaves a benign "successfully synced" message that isn't an error.
		if ps.Operation != "" && ps.Operation != "Succeeded" {
			ps.SyncError = nestedString(app.Object, "status", "operationState", "message")
		}
		if prev, exists := out[repo]; !exists || rollupSeverity(ps) >= rollupSeverity(prev) {
			out[repo] = ps
		}
	}
	return out
}

// appRepo is the Application's canonical primary repoURL — spec.source.repoURL, or the
// first spec.sources[] entry for a multi-source app — the key tying an Application to
// its dotvirt project (matched against a normalized project.Repo).
func appRepo(app map[string]any) string {
	if u := nestedString(app, "spec", "source", "repoURL"); u != "" {
		return forge.NormalizeRepoURL(u)
	}
	sources, found, _ := unstructured.NestedSlice(app, "spec", "sources")
	if found {
		for _, raw := range sources {
			if src, ok := raw.(map[string]any); ok {
				if u := asString(src, "repoURL"); u != "" {
					return forge.NormalizeRepoURL(u)
				}
			}
		}
	}
	return ""
}

// rollupSeverity ranks a project's sync state so a collision (two apps, one repo)
// keeps the worst — and so the frontend can pick the single most-alarming signal.
func rollupSeverity(ps model.ProjectSync) int {
	return max(syncSeverity(ps.Sync), healthSeverity(ps.Health), opSeverity(ps.Operation))
}

func syncSeverity(s model.SyncStatus) int {
	switch s {
	case model.SyncOutOfSync:
		return 3
	case model.SyncUnknown:
		return 2
	case model.SyncSynced:
		return 1
	default:
		return 0
	}
}

func healthSeverity(h string) int {
	switch h {
	case "Degraded", "Missing":
		return 3
	case "Unknown", "Progressing":
		return 2
	case "Healthy", "Suspended":
		return 1
	default:
		return 0
	}
}

func opSeverity(phase string) int {
	switch phase {
	case "Failed", "Error":
		return 3
	case "Running", "Terminating":
		return 2
	case "Succeeded":
		return 1
	default:
		return 0
	}
}

// shortRev abbreviates a git revision for display; short or empty values pass through.
func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

func syncStatus(s string) model.SyncStatus {
	switch s {
	case "Synced":
		return model.SyncSynced
	case "OutOfSync":
		return model.SyncOutOfSync
	default:
		return model.SyncUnknown
	}
}

func asString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func nestedString(m map[string]any, keys ...string) string {
	cur := m
	for i, k := range keys {
		if i == len(keys)-1 {
			return asString(cur, k)
		}
		next, ok := cur[k].(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}
