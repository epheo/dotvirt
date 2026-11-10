package cluster

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// HasAny reports whether any object of gvr exists; false when it is not served.
func (c *Client) HasAny(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

// ServesAPIResource is HasAPIResource that tells a definite absence (the group
// version or the resource is not served) from a failed discovery read, which
// returns its error.
func (c *Client) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	served, err := c.ServedResources(gvr.GroupVersion())
	return served[gvr.Resource], err
}

// ServedResources is the set of resources one discovery read finds gv
// serving: empty when gv is not served, the error of a failed read.
func (c *Client) ServedResources(gv schema.GroupVersion) (map[string]bool, error) {
	rls, err := c.kube.Discovery().ServerResourcesForGroupVersion(gv.String())
	if apierrors.IsNotFound(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rls.APIResources))
	for _, r := range rls.APIResources {
		out[r.Name] = true
	}
	return out, nil
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

// gvrClusterNetwork is OpenShift's cluster network configuration, one object
// named cluster.
var gvrClusterNetwork = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "networks"}

// ClusterNetworks reads the pod network CIDRs (status.clusterNetwork) and the
// service network CIDRs (status.serviceNetwork) the cluster runs, from
// OpenShift's cluster Network config. A cluster that does not serve it
// answers NotFound.
func (c *Client) ClusterNetworks(ctx context.Context) (pod, service []string, err error) {
	u, err := c.dyn.Resource(gvrClusterNetwork).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	service, _, _ = unstructured.NestedStringSlice(u.Object, "status", "serviceNetwork")
	entries, _, _ := unstructured.NestedSlice(u.Object, "status", "clusterNetwork")
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			if cidr, ok := m["cidr"].(string); ok {
				pod = append(pod, cidr)
			}
		}
	}
	return pod, service, nil
}

// KindResources is the resource serving each of kinds in any version, by
// discovery: the group list, then the group's versions until the kind turns
// up. A kind missing from served is not served; clusterScoped holds the served
// ones without a namespace; failed names the groups whose resources discovery
// could not read, whose kinds are unknown.
func (c *Client) KindResources(kinds []schema.GroupKind) (served map[schema.GroupKind]string, clusterScoped map[schema.GroupKind]bool, failed map[string]bool, err error) {
	groups, err := c.kube.Discovery().ServerGroups()
	if err != nil {
		return nil, nil, nil, err
	}
	versions := map[string][]string{}
	for _, g := range groups.Groups {
		for _, v := range g.Versions {
			versions[g.Name] = append(versions[g.Name], v.GroupVersion)
		}
	}
	served, clusterScoped, failed = map[schema.GroupKind]string{}, map[schema.GroupKind]bool{}, map[string]bool{}
	read := map[string]*metav1.APIResourceList{}
	for _, gk := range kinds {
		for _, gv := range versions[gk.Group] {
			rl, ok := read[gv]
			if !ok {
				rl, err = c.kube.Discovery().ServerResourcesForGroupVersion(gv)
				if err != nil && !apierrors.IsNotFound(err) {
					failed[gk.Group] = true
					continue
				}
				read[gv] = rl
			}
			if res, namespaced := kindResource(rl, gk.Kind); res != "" {
				served[gk] = res
				if !namespaced {
					clusterScoped[gk] = true
				}
				break
			}
		}
	}
	return served, clusterScoped, failed, nil
}

// kindResource is the resource of rl serving kind, subresources aside.
func kindResource(rl *metav1.APIResourceList, kind string) (resource string, namespaced bool) {
	if rl == nil {
		return "", false
	}
	for _, r := range rl.APIResources {
		if r.Kind == kind && !strings.Contains(r.Name, "/") {
			return r.Name, r.Namespaced
		}
	}
	return "", false
}
