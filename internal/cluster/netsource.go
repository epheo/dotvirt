package cluster

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// The raw access the netstate and drs snapshots build their reflectors on, kept
// here so the dynamic client and discovery stay behind cluster.Client (no snapshot
// touches a clientset directly).

// DynamicListWatch is a List+Watch over gvr across all namespaces - cluster-scoped
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

// HasAPIResource reports whether gvr is served by the cluster - the discovery gate a
// snapshot uses to stay a slow probe where a CRD is absent (OVN-K UDN or nmstate not
// installed), never a reflector error loop.
func (c *Client) HasAPIResource(gvr schema.GroupVersionResource) bool {
	ok, _ := c.ServesAPIResource(gvr)
	return ok
}

// ServesAPIResource is HasAPIResource that tells a definite absence (the group
// version or the resource is not served) from a failed discovery read, which
// returns its error.
func (c *Client) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	rls, err := c.kube.Discovery().ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, r := range rls.APIResources {
		if r.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}

// NodeLabels is a node's name + labels - enough for netstate to compute which nodes
// an uplink (an NNCP nodeSelector, or all of them for br-ex) covers - and the pod
// subnets OVN-Kubernetes gave it, the cluster default network's address range.
// Distinct from model.NodeInfo (the UI's node detail) and model.Node (the inventory row).
type NodeLabels struct {
	Name   string
	Labels map[string]string
	// PodSubnets is the node's OVN-Kubernetes node-subnets annotation: JSON, per
	// network name.
	PodSubnets string
}

// nodeSubnetsAnnotation is where OVN-Kubernetes records each node's pod subnets.
const nodeSubnetsAnnotation = "k8s.ovn.org/node-subnets"

// ListNodeLabels lists every node's name+labels under this client (nodes:list - no
// watch). netstate refreshes a cache from this off the request path, so uplink node
// membership needs neither a per-request node LIST nor a nodes:watch grant.
func (c *Client) ListNodeLabels(ctx context.Context) ([]NodeLabels, error) {
	list, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NodeLabels, 0, len(list.Items))
	for i := range list.Items {
		n := &list.Items[i]
		out = append(out, NodeLabels{Name: n.Name, Labels: n.Labels, PodSubnets: n.Annotations[nodeSubnetsAnnotation]})
	}
	return out, nil
}
