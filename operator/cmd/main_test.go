package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// Only instance-labelled objects enter the cache, and the Route kind is
// registered only where the route API exists.
func TestCacheByObjectScope(t *testing.T) {
	for _, plat := range []platform.Platform{platform.OpenShift, platform.Kubernetes} {
		byObject := cacheByObject(plat)
		routes := 0
		for obj, opts := range byObject {
			if obj.GetObjectKind().GroupVersionKind() == install.RouteGVK {
				routes++
			}
			if !opts.Label.Matches(labels.Set{install.InstanceLabel: "x"}) || opts.Label.Matches(labels.Set{"app": "other"}) {
				t.Errorf("%s: %T selector %q does not select by the instance label", plat, obj, opts.Label)
			}
		}
		if want := map[platform.Platform]int{platform.OpenShift: 1, platform.Kubernetes: 0}[plat]; routes != want {
			t.Errorf("%s: Route entries = %d, want %d", plat, routes, want)
		}
	}
}
