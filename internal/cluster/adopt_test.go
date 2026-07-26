package cluster

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// adoptableListKinds registers every kind the sweep visits. The fake panics on an
// unregistered one, where a real cluster answers NoMatch and the sweep skips it.
func adoptableListKinds() map[schema.GroupVersionResource]string {
	kinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range adoptableKinds {
		singular := strings.TrimSuffix(gvr.Resource, "s")
		kinds[gvr] = strings.ToUpper(singular[:1]) + singular[1:] + "List"
	}
	return kinds
}

func liveObj(apiVersion, kind, ns, name string, meta map[string]any) *unstructured.Unstructured {
	m := map[string]any{"namespace": ns, "name": name}
	for k, v := range meta {
		m[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   m,
		"spec":       map[string]any{},
		"status":     map[string]any{"phase": "Running"},
	}}
}

// Adoption takes what git does not describe, which is exactly the objects no FOREIGN
// Application claims and no controller owns. A foreign-claimed object is declared by
// another repo, and re-capturing it here would give it two declaring sources. Owned
// objects are derived, so declaring them gives one object two sources. (The project's
// own app never appears in foreignApps: the caller excludes it, so a lost-repo
// recovery can re-capture the objects it still tracks.)
func TestAdoptableObjectsSkipsClaimedAndOwned(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds(),
		liveObj("kubevirt.io/v1", "VirtualMachine", "team-a", "web", nil),
		liveObj("networking.k8s.io/v1", "NetworkPolicy", "team-a", "deny", nil),
		// Declared by another live app's repo: leave it alone.
		liveObj("kubevirt.io/v1", "VirtualMachine", "team-a", "claimed", map[string]any{
			"annotations": map[string]any{trackingIDAnnotation: "dotvirt-platform:kubevirt.io/VirtualMachine:team-a/claimed"},
		}),
		// Created by a VM's dataVolumeTemplates, not declared beside it.
		liveObj("cdi.kubevirt.io/v1beta1", "DataVolume", "team-a", "web-disk", map[string]any{
			"ownerReferences": []any{map[string]any{"kind": "VirtualMachine", "name": "web"}},
		}),
	)

	got, _, err := NewClient(nil, nil, dc).AdoptableObjects(context.Background(), []string{"team-a"},
		map[string]bool{"dotvirt-platform": true})
	if err != nil {
		t.Fatalf("AdoptableObjects: %v", err)
	}
	paths := map[string]bool{}
	for _, o := range got {
		paths[o.Path] = true
	}
	if len(got) != 2 {
		t.Fatalf("want the unclaimed VM and policy only, got %d: %v", len(got), paths)
	}
	if !paths["team-a/web.yaml"] || !paths["team-a/networkpolicies/deny.yaml"] {
		t.Errorf("unexpected capture set: %v", paths)
	}
}

// A captured object must land where dotvirt's own generators write it, or adopting and
// then editing it through the UI leaves two files declaring one object in the same
// Application source.
func TestAdoptPathMatchesGeneratorLayout(t *testing.T) {
	for _, tc := range []struct{ apiVersion, kind, want string }{
		{"kubevirt.io/v1", "VirtualMachine", "team-a/web.yaml"},
		{"k8s.ovn.org/v1", "UserDefinedNetwork", "team-a/networks/web.yaml"},
		{"networking.k8s.io/v1", "NetworkPolicy", "team-a/networkpolicies/web.yaml"},
		{"k8s.ovn.org/v1", "EgressFirewall", "team-a/egressfirewalls/default.yaml"},
	} {
		if got := adoptPath(liveObj(tc.apiVersion, tc.kind, "team-a", "web", nil)); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.kind, got, tc.want)
		}
	}
}

// A terminating object still LISTs while its finalizers run, and adoptManifest strips
// exactly the fields that mark it as going away. Capturing one would turn a delete into
// a PR that re-creates it.
func TestAdoptableObjectsSkipsTerminating(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds(),
		liveObj("k8s.ovn.org/v1", "UserDefinedNetwork", "team-a", "dying", map[string]any{
			"deletionTimestamp": "2026-07-25T00:00:00Z",
			"finalizers":        []any{"k8s.ovn.org/user-defined-network-protection"},
		}),
	)
	got, _, err := NewClient(nil, nil, dc).AdoptableObjects(context.Background(), []string{"team-a"}, nil)
	if err != nil {
		t.Fatalf("AdoptableObjects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("adopting a terminating object would resurrect it; got %v", got)
	}
}

// ArgoCD leaves the tracking-id behind when an Application is deleted. Nothing declares
// such an object, and the inventory already reports it NotTracked and offers adoption,
// so skipping on the bare annotation would strand it outside git with a button that
// silently does nothing for it.
func TestAdoptableObjectsTakesResidueOfDeletedApp(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds(),
		liveObj("kubevirt.io/v1", "VirtualMachine", "team-a", "orphan", map[string]any{
			"annotations": map[string]any{trackingIDAnnotation: "deleted-app:kubevirt.io/VirtualMachine:team-a/orphan"},
		}),
	)
	got, _, err := NewClient(nil, nil, dc).AdoptableObjects(context.Background(), []string{"team-a"},
		map[string]bool{"some-other-app": true})
	if err != nil {
		t.Fatalf("AdoptableObjects: %v", err)
	}
	if len(got) != 1 || got[0].Name != "orphan" {
		t.Fatalf("a tracking-id naming a deleted Application is residue, not a claim; got %v", got)
	}
}

// Adoption runs under the caller's own token, and the standard OpenShift admin role
// grants no egressfirewalls. Failing hard on an unreadable kind would put adoption out
// of reach of every non-cluster-admin; the kind is skipped and named instead, so the
// caller can be told the capture stopped short of the whole namespace.
func TestAdoptableObjectsReportsUnreadableKind(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds(),
		liveObj("kubevirt.io/v1", "VirtualMachine", "team-a", "web", nil),
	)
	dc.PrependReactor("list", "egressfirewalls", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "egressfirewalls"}, "", nil)
	})
	got, unreadable, err := NewClient(nil, nil, dc).AdoptableObjects(context.Background(), []string{"team-a"}, nil)
	if err != nil {
		t.Fatalf("an unreadable kind must not fail the adoption: %v", err)
	}
	if len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("readable kinds must still be captured, got %v", got)
	}
	if len(unreadable) != 1 || unreadable[0] != "egressfirewalls" {
		t.Fatalf("the skipped kind must be reported, got %v", unreadable)
	}
}

// A kind whose CRD is not installed is absent, not withheld, so it is neither captured
// nor reported: nothing of it can exist to adopt.
func TestAdoptableObjectsSkipsAbsentCRDSilently(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), adoptableListKinds())
	dc.PrependReactor("list", "userdefinednetworks", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &meta.NoKindMatchError{}
	})
	_, unreadable, err := NewClient(nil, nil, dc).AdoptableObjects(context.Background(), []string{"team-a"}, nil)
	if err != nil {
		t.Fatalf("AdoptableObjects: %v", err)
	}
	if len(unreadable) != 0 {
		t.Fatalf("an absent CRD is not a permission gap, got %v", unreadable)
	}
}
