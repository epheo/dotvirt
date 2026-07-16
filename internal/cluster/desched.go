package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// kubedeschedulersGVR is the Kube Descheduler Operator's configuration CR — the
// object behind dotvirt's DRS status plane.
var kubedeschedulersGVR = schema.GroupVersionResource{
	Group:    "operator.openshift.io",
	Version:  "v1",
	Resource: "kubedeschedulers",
}

// KubeDeschedulerListWatch is the List+Watch source for the DRS status
// reflector. The CR is namespaced (to the operator's install namespace), but
// watched across all namespaces so a nonstandard install still surfaces.
func (c *Client) KubeDeschedulerListWatch() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return c.dyn.Resource(kubedeschedulersGVR).Namespace(metav1.NamespaceAll).List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return c.dyn.Resource(kubedeschedulersGVR).Namespace(metav1.NamespaceAll).Watch(ctx, o)
		},
	}
}

// HasKubeDeschedulerAPI reports whether the KubeDescheduler API is served. The
// operator may be legitimately absent — installing it is exactly what the DRS
// panel proposes — so the status reflector gates on this probe instead of
// error-looping on a missing CRD.
func (c *Client) HasKubeDeschedulerAPI() bool {
	rls, err := c.kube.Discovery().ServerResourcesForGroupVersion(kubedeschedulersGVR.GroupVersion().String())
	if err != nil {
		return false
	}
	for _, r := range rls.APIResources {
		if r.Name == kubedeschedulersGVR.Resource {
			return true
		}
	}
	return false
}
