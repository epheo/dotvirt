package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// The raw access the netstate snapshot builds its reflectors on, kept here so the
// dynamic client and discovery stay behind cluster.Client (netstate never touches a
// clientset directly). Mirrors the typed VMListWatch/HasKubeDeschedulerAPI helpers.

// DynamicListWatch is a List+Watch over gvr across all namespaces — cluster-scoped
// kinds included (NamespaceAll addresses the whole cluster for them). The source for a
// netstate reflector over an arbitrary CRD.
func (c *Client) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// HasAPIResource reports whether gvr is served by the cluster — the discovery gate a
// snapshot uses to stay a slow probe where a CRD is absent (OVN-K UDN or nmstate not
// installed), never a reflector error loop. Mirrors HasKubeDeschedulerAPI.
func (c *Client) HasAPIResource(gvr schema.GroupVersionResource) bool {
	rls, err := c.kube.Discovery().ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return false
	}
	for _, r := range rls.APIResources {
		if r.Name == gvr.Resource {
			return true
		}
	}
	return false
}

// NodeInfo is a node's name + labels — enough for netstate to compute which nodes an
// uplink (an NNCP nodeSelector, or all of them for br-ex) covers.
type NodeInfo struct {
	Name   string
	Labels map[string]string
}

// NodeInfos lists every node's name+labels under this client (nodes:list — no watch).
// netstate refreshes a cache from this off the request path, so uplink node membership
// needs neither a per-request node LIST nor a nodes:watch grant.
func (c *Client) NodeInfos(ctx context.Context) ([]NodeInfo, error) {
	list, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NodeInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, NodeInfo{Name: list.Items[i].Name, Labels: list.Items[i].Labels})
	}
	return out, nil
}
