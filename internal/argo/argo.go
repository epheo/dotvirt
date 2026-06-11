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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

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

// VMDrift returns per-VM drift keyed by "namespace/name", built from every
// Application's status.resources[] that references a VirtualMachine. VMs absent
// from the map are managed by no Application (caller reports NotTracked).
func (c *Client) VMDrift(ctx context.Context) (map[string]Drift, error) {
	apps, err := c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list ArgoCD applications: %w", err)
	}

	out := map[string]Drift{}
	for i := range apps.Items {
		resources, found, err := unstructured.NestedSlice(apps.Items[i].Object, "status", "resources")
		if err != nil || !found {
			continue
		}
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
	return out, nil
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
