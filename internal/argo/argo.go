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
		ListFunc: func(o metav1.ListOptions) (runtime.Object, error) {
			return c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).List(context.Background(), o)
		},
		WatchFunc: func(o metav1.ListOptions) (watch.Interface, error) {
			return c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).Watch(context.Background(), o)
		},
	}
}

// driftFromApps builds per-VM drift keyed "namespace/name" from a set of ArgoCD
// Application objects (the reflector store's *unstructured.Unstructured). VMs absent
// from the result are managed by no Application (caller reports NotTracked). Only
// scalar fields are read out of each object, so nothing escapes the store to be
// mutated. Always returns a non-nil map.
func driftFromApps(objs []any) map[string]Drift {
	out := map[string]Drift{}
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
				if asString(res, "group") != "kubevirt.io" || asString(res, "kind") != "VirtualMachine" {
					continue
				}
				ns, name := asString(res, "namespace"), asString(res, "name")
				if name == "" {
					continue
				}
				out[ns+"/"+name] = Drift{
					Sync:   syncStatus(asString(res, "status")),
					Health: nestedString(res, "health", "status"),
				}
			}
		}
		mergeSyncMessages(out, app.Object)
	}
	return out
}

// mergeSyncMessages attaches per-VM apply errors onto the drift map. ArgoCD keeps
// the live tree in status.resources[] (sync/health, no error text) but the actual
// apply failure for each object in status.operationState.syncResult.resources[].
// We surface the latter so the UI can show *why* a VM is OutOfSync. Synced rows
// carry a benign "unchanged" message, so only non-Synced ones are kept.
func mergeSyncMessages(out map[string]Drift, app map[string]any) {
	results, found, err := unstructured.NestedSlice(app, "status", "operationState", "syncResult", "resources")
	if err != nil || !found {
		return
	}
	for _, raw := range results {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if asString(res, "group") != "kubevirt.io" || asString(res, "kind") != "VirtualMachine" {
			continue
		}
		msg := asString(res, "message")
		if msg == "" || asString(res, "status") == "Synced" {
			continue
		}
		key := asString(res, "namespace") + "/" + asString(res, "name")
		// Only annotate a VM the live tree (status.resources[]) already reported. A VM
		// present ONLY here — a failed first apply that never entered the live tree —
		// must NOT be synthesized with a zero Sync ("") : that empty status crashes the
		// frontend SyncBadge (it indexes its style table by sync). Such a VM stays
		// absent so the caller reports NotTracked.
		d, ok := out[key]
		if !ok {
			continue
		}
		d.Message = msg
		out[key] = d
	}
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
