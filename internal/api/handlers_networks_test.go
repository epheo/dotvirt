package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
)

// scopeNetworks must keep shared (cluster) networks discoverable to every tenant
// but never reveal a namespace the caller can't already see - neither a project
// network in a foreign namespace nor, on a shared network, the foreign entries of
// its own Namespaces list (a CUDN publishes cluster-wide, so that list can name
// other tenants' namespaces).
func TestScopeNetworks(t *testing.T) {
	visible := map[string]bool{"tenant-a": true}
	in := []model.Network{
		{Name: "vm-net", Scope: model.ScopeProject, Namespace: "tenant-a"},
		{Name: "secret", Scope: model.ScopeProject, Namespace: "tenant-b"},
		{Name: "prod-vlan", Scope: model.ScopeShared, Namespaces: []string{"tenant-a", "tenant-b", "tenant-c"}},
	}
	got := scopeNetworks(in, visible)

	byName := map[string]model.Network{}
	for _, n := range got {
		byName[n.Name] = n
	}
	if _, ok := byName["secret"]; ok {
		t.Error("a project network in a non-visible namespace leaked")
	}
	if _, ok := byName["vm-net"]; !ok {
		t.Error("a project network in the caller's namespace should be kept")
	}
	shared, ok := byName["prod-vlan"]
	if !ok {
		t.Fatal("a shared network should stay discoverable to every tenant")
	}
	if len(shared.Namespaces) != 1 || shared.Namespaces[0] != "tenant-a" {
		t.Errorf("shared network namespaces = %v, want [tenant-a] (foreign namespaces redacted)", shared.Namespaces)
	}
	// Per-request scoping must not mutate the shared, cached catalog.
	if len(in[2].Namespaces) != 3 {
		t.Errorf("scopeNetworks mutated the cached input slice: %v", in[2].Namespaces)
	}
}

// A second AdminNetworkPolicy at a priority a live one holds would be ordered
// arbitrarily by OVN-K: the create is refused before anything is staged.
func TestCreateAdminNetworkPolicyRefusesTakenPriority(t *testing.T) {
	fc := newFakeCluster(t)
	gv := "policy.networking.k8s.io/v1alpha1"
	fc.on(http.MethodGet, "/apis/"+gv, func(w http.ResponseWriter, _ *http.Request) {
		writeObj(w, metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
			GroupVersion: gv,
			APIResources: []metav1.APIResource{{Name: "adminnetworkpolicies", Kind: "AdminNetworkPolicy", Verbs: []string{"list", "watch"}}},
		})
	})
	fc.on(http.MethodGet, "/apis/"+gv+"/adminnetworkpolicies", unstructuredList(gv, "AdminNetworkPolicy", map[string]any{
		"apiVersion": gv, "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "lockdown", "resourceVersion": "1"},
		"spec":     map[string]any{"priority": int64(5), "subject": map[string]any{"namespaces": map[string]any{}}},
	}))
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Policies()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("netstate never synced the live ANP")
		}
	}

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/adminnetworkpolicies", `{"name":"other","priority":5}`), http.StatusConflict)
	d.none(t)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/adminnetworkpolicies", `{"name":"other","priority":6}`), http.StatusOK)
	d.only(t, "StageCreate "+platformProjectName+" adminnetworkpolicy")
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/adminnetworkpolicies", `{"baseline":true}`), http.StatusOK)
	d.only(t, "StageCreate "+platformProjectName+" adminnetworkpolicy")

	// A dry run refuses a held priority as the stage does, and renders a free one.
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/adminnetworkpolicies?dryRun=true", `{"name":"other","priority":5}`), http.StatusConflict)
	d.none(t)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/adminnetworkpolicies?dryRun=true", `{"name":"other","priority":6}`), http.StatusOK)
	d.only(t, "PreviewCreate "+platformProjectName+" adminnetworkpolicy")
}
