package cluster

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The platform tier's capture: cluster-scoped kinds, no namespace, landing at the
// paths the platform generators write so a later edit hits the same file.
func TestClusterAdoptableObjects(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds(),
		liveObj("k8s.ovn.org/v1", "ClusterUserDefinedNetwork", "", "shared", nil),
		liveObj("policy.networking.k8s.io/v1alpha1", "BaselineAdminNetworkPolicy", "", "default", nil),
		liveObj("k8s.ovn.org/v1", "AdminPolicyBasedExternalRoute", "", "gw", nil),
		// Claimed by another live app: leave it alone.
		liveObj("k8s.ovn.org/v1", "EgressIP", "", "claimed", map[string]any{
			"annotations": map[string]any{trackingIDAnnotation: "other:k8s.ovn.org/EgressIP:/claimed"},
		}),
		// A tenant object must not surface in the cluster sweep.
		liveObj("k8s.ovn.org/v1", "UserDefinedNetwork", "team-a", "db-net", nil),
	)

	got, unreadable, err := NewClient(nil, nil, dc).ClusterAdoptableObjects(context.Background(), map[string]bool{"other": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Errorf("unreadable = %v", unreadable)
	}
	paths := map[string]string{}
	for _, o := range got {
		if o.Namespace != "" {
			t.Errorf("%s %s captured with namespace %q", o.Kind, o.Name, o.Namespace)
		}
		paths[o.Kind+"/"+o.Name] = o.Path
		if strings.Contains(string(o.Manifest), "status:") {
			t.Errorf("%s manifest keeps status:\n%s", o.Name, o.Manifest)
		}
	}
	want := map[string]string{
		"ClusterUserDefinedNetwork/shared":   "networks/shared.yaml",
		"BaselineAdminNetworkPolicy/default": "baselineadminnetworkpolicies/default.yaml",
		"AdminPolicyBasedExternalRoute/gw":   "externalroutes/gw.yaml",
	}
	for k, p := range want {
		if paths[k] != p {
			t.Errorf("%s path = %q, want %q", k, paths[k], p)
		}
	}
	for _, absent := range []string{"EgressIP/claimed", "UserDefinedNetwork/db-net"} {
		if _, ok := paths[absent]; ok {
			t.Errorf("%s must not be captured", absent)
		}
	}
}
