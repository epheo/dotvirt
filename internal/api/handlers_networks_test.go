package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
)

// A tenant sees only its own scope: project networks in its namespaces and
// shared networks published to one of them, never an unpublished or
// foreign-published one, and never another tenant's namespace on a shared one.
// A caller with platform networking authority sees every network, to fix it.
func TestScopeNetworks(t *testing.T) {
	visible := map[string]bool{"tenant-a": true}
	in := []model.Network{
		{Name: "vm-net", Scope: model.ScopeProject, Namespace: "tenant-a", Backing: model.KindUDN.Kind},
		{Name: "secret", Scope: model.ScopeProject, Namespace: "tenant-b", Backing: model.KindUDN.Kind},
		{Name: "prod-vlan", Kind: model.NetworkVLAN, Scope: model.ScopeShared, Namespaces: []string{"tenant-a", "tenant-b", "tenant-c"}},
		{Name: "mt-b", Scope: model.ScopeShared, Namespaces: []string{"tenant-b"}},
		{Name: "green-secondary", Scope: model.ScopeShared},
	}
	names := func(nets []model.Network) map[string]model.Network {
		m := map[string]model.Network{}
		for _, n := range nets {
			m[n.Name] = n
		}
		return m
	}

	tenant := names(scopeNetworks(in, wideView(visible), false))
	for _, hidden := range []string{"secret", "mt-b", "green-secondary"} {
		if _, ok := tenant[hidden]; ok {
			t.Errorf("a tenant sees %s", hidden)
		}
	}
	if _, ok := tenant["vm-net"]; !ok {
		t.Error("a project network in the caller's namespace should be kept")
	}
	shared, ok := tenant["prod-vlan"]
	if !ok {
		t.Fatal("a shared network published to the caller should be kept")
	}
	if len(shared.Namespaces) != 1 || shared.Namespaces[0] != "tenant-a" {
		t.Errorf("shared network namespaces = %v, want [tenant-a] (foreign namespaces redacted)", shared.Namespaces)
	}

	platform := names(scopeNetworks(in, wideView(visible), true))
	for _, want := range []string{"vm-net", "prod-vlan", "mt-b", "green-secondary"} {
		if _, ok := platform[want]; !ok {
			t.Errorf("a platform caller misses %s", want)
		}
	}
	if _, ok := platform["secret"]; ok {
		t.Error("a project network stays namespace-scoped, platform caller or not")
	}
	// Per-request scoping must not mutate the shared, cached catalog.
	if len(in[2].Namespaces) != 3 {
		t.Errorf("scopeNetworks mutated the cached input slice: %v", in[2].Namespaces)
	}
}

// A network key is the UI's route key: the attach ref for a project network
// (two projects may name networks alike), the name for a shared one.
func TestFindNetwork(t *testing.T) {
	nets := []model.Network{
		{Name: "vm-net", Scope: model.ScopeProject, Namespace: "a", AttachRef: "a/vm-net"},
		{Name: "vm-net", Scope: model.ScopeProject, Namespace: "b", AttachRef: "b/vm-net"},
		{Name: "lan-101", Scope: model.ScopeShared, AttachRef: "lan-101"},
	}
	for key, want := range map[string]string{"b/vm-net": "b", "a/vm-net": "a", "vm-net": "a", "lan-101": ""} {
		n, ok := findNetwork(nets, key)
		if !ok || n.Namespace != want {
			t.Errorf("findNetwork(%q) = %+v, %v; want namespace %q", key, n, ok, want)
		}
	}
	if _, ok := findNetwork(nets, "c/vm-net"); ok {
		t.Error("an unknown key must not resolve")
	}
}

// A network's bindings and the cluster rules naming its VMs govern the
// caller's own VMs: they stay, through the caller's redactor. A peer rule in
// a namespace the caller cannot see never shows.
func TestScopeNetworkRules(t *testing.T) {
	in := model.NetworkRules{
		Applies: []model.PolicyBinding{{Policy: model.Policy{Name: "deny-lateral", Kind: model.PolicyAdmin}}},
		Reaches: []model.PeerRule{
			{Policy: model.Policy{Name: "mine", Kind: model.PolicyProjectRule, Namespace: "a", Backing: "NetworkPolicy"}},
			{Policy: model.Policy{Name: "unlisted", Kind: model.PolicyProjectRule, Namespace: "a", Backing: "EgressFirewall"}},
			{Policy: model.Policy{Name: "foreign", Kind: model.PolicyProjectRule, Namespace: "b", Backing: "NetworkPolicy"}},
			{Policy: model.Policy{Name: "cluster", Kind: model.PolicyAdmin}},
		},
	}
	var redacted []string
	lists := viewReading(map[string]bool{"a": true}, map[model.Kind][]string{model.KindNetpol: {"a"}})
	got := scopeNetworkRules(in, lists, func(*model.Policy) {}, func(p *model.Policy, _ *model.PolicyRuleView) {
		redacted = append(redacted, p.Name)
	})
	var names []string
	for _, r := range got.Reaches {
		names = append(names, r.Policy.Name)
	}
	if strings.Join(names, ",") != "mine,cluster" {
		t.Errorf("reaches = %v, want mine and cluster", names)
	}
	if strings.Join(redacted, ",") != "deny-lateral,mine,cluster" {
		t.Errorf("redacted = %v, want every kept policy through the redactor", redacted)
	}
}

// A cluster rule's apply error names whatever its selectors reach: the drift
// attaches before the redactor runs, so a tenant reads only the generic line.
func TestScopeNetworkRulesRedactsDrift(t *testing.T) {
	in := model.NetworkRules{
		Applies: []model.PolicyBinding{{Policy: model.Policy{Name: "deny-lateral", Kind: model.PolicyAdmin}}},
		Reaches: []model.PeerRule{{Policy: model.Policy{Name: "into-b", Kind: model.PolicyBaseline}}},
	}
	drift := func(p *model.Policy) { p.SyncError = "namespace team-b: admission denied" }
	no := func(model.PolicyKind) bool { return false }
	tenant := policyRedactor{cluster: no, lists: listsAll}
	got := scopeNetworkRules(in, wideView(nil), drift, tenant.apply)
	for _, p := range []model.Policy{got.Applies[0].Policy, got.Reaches[0].Policy} {
		if p.SyncError != redactedSyncError {
			t.Errorf("%s: a tenant reads the apply error %q", p.Name, p.SyncError)
		}
	}
}

// A shared network's apply error can name every namespace its selector
// reaches: it reaches only callers who may author shared networks, a
// project network's own error stays with its tenant.
func TestRedactSharedErrors(t *testing.T) {
	nets := []model.Network{
		{Name: "dc-vlan", Scope: model.ScopeShared, SyncError: "NAD exists in namespace team-b"},
		{Name: "backend", Scope: model.ScopeProject, Namespace: "team-a", SyncError: "invalid subnet"},
		{Name: "quiet", Scope: model.ScopeShared},
	}
	redactSharedErrors(nets)
	if strings.Contains(nets[0].SyncError, "team-b") || nets[0].SyncError == "" {
		t.Errorf("shared error = %q, want a generic line", nets[0].SyncError)
	}
	if nets[1].SyncError != "invalid subnet" || nets[2].SyncError != "" {
		t.Errorf("redaction reached beyond shared errors: %+v", nets[1:])
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

// Through the route: a tenant's catalog lists only the shared network
// published to its namespace; a platform caller's lists all three, the
// unpublished and the foreign-published one included.
func TestNetworksTenantSeesOwnScope(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	// cudn-lister may only list shared networks cluster-wide: kubectl already
	// shows it every one, so its catalog does too.
	fc.ssar = func(token string, attrs *authzv1.ResourceAttributes) bool {
		return token == adminToken ||
			token == "cudn-lister" && attrs.Verb == "list" && attrs.Resource == "clusteruserdefinednetworks"
	}
	resources := func(gv string, names ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			list := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv}
			for _, n := range names {
				list.APIResources = append(list.APIResources, metav1.APIResource{Name: n, Verbs: []string{"list", "watch"}})
			}
			writeObj(w, list)
		}
	}
	cudn := func(name string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
			"metadata": map[string]any{"name": name, "uid": name, "resourceVersion": "1"},
			"spec":     map[string]any{"network": map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{"10.0.0.0/24"}}}},
		}
	}
	nad := func(ns, owner string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": owner, "namespace": ns, "resourceVersion": "1",
				"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": owner, "uid": owner}},
			},
		}
	}
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1", resources("k8s.ovn.org/v1", "clusteruserdefinednetworks", "userdefinednetworks"))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1", resources("k8s.cni.cncf.io/v1", "network-attachment-definitions"))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/clusteruserdefinednetworks", unstructuredList("k8s.ovn.org/v1", "ClusterUserDefinedNetwork",
		cudn("blue-secondary"), cudn("mt-b"), cudn("green-secondary")))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/userdefinednetworks", unstructuredList("k8s.ovn.org/v1", "UserDefinedNetwork"))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1/network-attachment-definitions", unstructuredList("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition",
		nad("team-a-dev", "blue-secondary"), nad("team-b-dev", "mt-b")))
	// No platform repo: the CUDN create authority alone is the platform signal.
	s := fc.server(nil, Config{})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Catalog().Networks) < 3 || len(ns.Catalog().Networks[0].Namespaces) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("netstate never synced the shared networks: %+v", ns.Catalog().Networks)
		}
	}

	names := func(token string) []string {
		t.Helper()
		rec := do(s, token, http.MethodGet, "/api/networks", "")
		wantStatus(t, rec, http.StatusOK)
		var inv model.NetworkInventory
		if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range inv.Networks {
			out = append(out, n.Name)
			for _, other := range n.Namespaces {
				if token == tenantToken && other != "team-a-dev" {
					t.Errorf("a tenant reads %s published to %s", n.Name, other)
				}
			}
		}
		return out
	}
	if got := names(tenantToken); strings.Join(got, ",") != "blue-secondary" {
		t.Errorf("tenant sees %v, want only blue-secondary", got)
	}
	if got := names(adminToken); strings.Join(got, ",") != "blue-secondary,green-secondary,mt-b" {
		t.Errorf("platform caller sees %v, want every shared network", got)
	}
	if got := names("cudn-lister"); strings.Join(got, ",") != "blue-secondary,green-secondary,mt-b" {
		t.Errorf("a cluster-wide CUDN lister sees %v, want the unpublished green-secondary too", got)
	}
	// A network the tenant cannot list is not found by key either.
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/networks/rules?network=mt-b", ""), http.StatusNotFound)
}

// Concurrent callers each enrich their own copy of the shared catalog: the
// uplinks the route stamps with source files and drift are never the
// snapshot's (fails under -race otherwise).
func TestNetworksConcurrentCallersOwnTheirUplinks(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{PlatformRepo: platformRepo})
	s.netstate = netstate.New(fc.sa, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 5 {
				rec := do(s, adminToken, http.MethodGet, "/api/networks", "")
				if rec.Code != http.StatusOK {
					t.Errorf("status %d: %s", rec.Code, rec.Body)
					return
				}
				var inv model.NetworkInventory
				if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil || len(inv.Uplinks) == 0 {
					t.Errorf("uplinks = %+v (%v), want the builtin one", inv.Uplinks, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// A project network shows only to who may list its backing kind there.
func TestScopeNetworksAsksTheBackingKind(t *testing.T) {
	in := []model.Network{
		{Name: "udn", Scope: model.ScopeProject, Namespace: "a", Backing: model.KindUDN.Kind},
		{Name: "nad", Scope: model.ScopeProject, Namespace: "a", Backing: model.KindNAD.Kind},
	}
	onlyUDN := viewReading(map[string]bool{"a": true}, map[model.Kind][]string{model.KindUDN: {"a"}})
	got := scopeNetworks(in, onlyUDN, false)
	if len(got) != 1 || got[0].Name != "udn" {
		t.Errorf("networks = %+v, want the udn alone", got)
	}
}

// What a tenant reads of a shared network is its NAD: published to a namespace
// they see but may not list NADs in, it is not theirs.
func TestSharedNetworksFollowTheNADRight(t *testing.T) {
	shared := []model.Network{{Name: "s", Scope: model.ScopeShared, Namespaces: []string{"a", "b"}}}
	visible := map[string]bool{"a": true, "b": true}
	listsInA := viewReading(visible, map[model.Kind][]string{model.KindNAD: {"a"}})
	got := scopeNetworks(shared, listsInA, false)
	if len(got) != 1 || !slices.Equal(got[0].Namespaces, []string{"a"}) {
		t.Errorf("got %+v, want the row narrowed to a", got)
	}
	if got := scopeNetworks(shared, viewReading(visible, nil), false); len(got) != 0 {
		t.Errorf("no NAD right still shows %+v", got)
	}
}

// Through the routes: the Networking and Security views list namespaced
// objects in project namespaces only, to an admin too: networks, rules, and a
// network's peer rules. Cluster-scoped ones stay, and a shared network names
// only the project namespaces it reaches.
func TestViewsListProjectNamespacesOnly(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd"}})
	resources := func(gv string, names ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			list := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv}
			for _, n := range names {
				list.APIResources = append(list.APIResources, metav1.APIResource{Name: n, Verbs: []string{"list", "watch"}})
			}
			writeObj(w, list)
		}
	}
	udn := func(ns, name string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
			"metadata": map[string]any{"name": name, "namespace": ns, "uid": name, "resourceVersion": "1"},
			"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{"10.0.0.0/24"}}},
		}
	}
	nad := func(ns string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "blue-secondary", "namespace": ns, "resourceVersion": "1",
				"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "blue-secondary", "uid": "blue-secondary"}},
			},
		}
	}
	netpol := func(ns string) map[string]any {
		return map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": "deny-" + ns, "namespace": ns, "resourceVersion": "1"},
			"spec":     map[string]any{"podSelector": map[string]any{}},
		}
	}
	// A rule elsewhere whose other end is team-a-dev: a peer rule on vm-net.
	reach := func(ns string) map[string]any {
		return map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": "reach-" + ns, "namespace": ns, "resourceVersion": "1"},
			"spec": map[string]any{
				"podSelector": map[string]any{},
				"policyTypes": []any{"Egress"},
				"egress": []any{map[string]any{"to": []any{map[string]any{
					"namespaceSelector": map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "team-a"}},
				}}}},
			},
		}
	}
	anpGV := "policy.networking.k8s.io/v1alpha1"
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1", resources("k8s.ovn.org/v1", "clusteruserdefinednetworks", "userdefinednetworks"))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1", resources("k8s.cni.cncf.io/v1", "network-attachment-definitions"))
	fc.on(http.MethodGet, "/apis/"+anpGV, resources(anpGV, "adminnetworkpolicies"))
	fc.on(http.MethodGet, "/apis/networking.k8s.io/v1", resources("networking.k8s.io/v1", "networkpolicies"))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/clusteruserdefinednetworks", unstructuredList("k8s.ovn.org/v1", "ClusterUserDefinedNetwork", map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
		"metadata": map[string]any{"name": "blue-secondary", "uid": "blue-secondary", "resourceVersion": "1"},
		"spec":     map[string]any{"network": map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{"10.1.0.0/24"}}}},
	}))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/userdefinednetworks", unstructuredList("k8s.ovn.org/v1", "UserDefinedNetwork",
		udn("team-a-dev", "vm-net"), udn("argocd", "r-drbd-l2")))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1/network-attachment-definitions", unstructuredList("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition",
		nad("team-a-dev"), nad("argocd")))
	fc.on(http.MethodGet, "/apis/networking.k8s.io/v1/networkpolicies", unstructuredList("networking.k8s.io/v1", "NetworkPolicy",
		netpol("team-a-dev"), netpol("argocd"), reach("orphans-ns"), reach("argocd")))
	fc.on(http.MethodGet, "/apis/"+anpGV+"/adminnetworkpolicies", unstructuredList(anpGV, "AdminNetworkPolicy", map[string]any{
		"apiVersion": anpGV, "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "lockdown", "resourceVersion": "1"},
		"spec":     map[string]any{"priority": int64(5), "subject": map[string]any{"namespaces": map[string]any{}}},
	}))
	s := fc.server(nil, Config{})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Catalog().Networks) < 3 || len(ns.Policies()) < 5; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("netstate never synced: networks %+v, policies %+v", ns.Catalog().Networks, ns.Policies())
		}
	}

	rec := do(s, adminToken, http.MethodGet, "/api/networks", "")
	wantStatus(t, rec, http.StatusOK)
	got := map[string][]string{}
	for _, n := range decodeBody[model.NetworkInventory](t, rec).Networks {
		got[n.Name] = n.Namespaces
	}
	if _, ok := got["r-drbd-l2"]; ok || len(got) != 2 {
		t.Errorf("networks = %v, want vm-net and blue-secondary, nothing in argocd", got)
	}
	if ns := got["blue-secondary"]; len(ns) != 1 || ns[0] != "team-a-dev" {
		t.Errorf("blue-secondary reaches %v, want [team-a-dev]", ns)
	}
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/networks/rules?network=argocd/r-drbd-l2", ""), http.StatusNotFound)

	rec = do(s, adminToken, http.MethodGet, "/api/networks/rules?network=team-a-dev/vm-net", "")
	wantStatus(t, rec, http.StatusOK)
	var peers []string
	for _, pr := range decodeBody[model.NetworkRules](t, rec).Reaches {
		peers = append(peers, pr.Policy.Name)
	}
	if strings.Join(peers, ",") != "reach-orphans-ns" {
		t.Errorf("vm-net's peer rules = %v, want reach-orphans-ns alone", peers)
	}

	rec = do(s, adminToken, http.MethodGet, "/api/policies", "")
	wantStatus(t, rec, http.StatusOK)
	var pols []string
	for _, p := range decodeBody[model.PolicyInventory](t, rec).Policies {
		pols = append(pols, p.Name)
	}
	slices.Sort(pols)
	if strings.Join(pols, ",") != "deny-team-a-dev,lockdown,reach-orphans-ns" {
		t.Errorf("policies = %v, want deny-team-a-dev, lockdown and reach-orphans-ns", pols)
	}
}
