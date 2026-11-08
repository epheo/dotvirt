package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
)

// scopePolicies must keep namespace-tier rows only in visible namespaces and
// cluster-tier rows only for callers with authority over that specific kind -
// an admin policy's rules name namespaces across tenants.
func TestScopePolicies(t *testing.T) {
	all := []model.Policy{
		{Name: "mine", Kind: model.PolicyProjectRule, Namespace: "team-a"},
		{Name: "theirs", Kind: model.PolicyProjectRule, Namespace: "team-b"},
		{Name: "block-dev", Kind: model.PolicyAdmin},
		{Name: "prod-egress", Kind: model.PolicyEgressIP},
	}
	visible := map[string]bool{"team-a": true}

	got := scopePolicies(all, visible, func(k model.PolicyKind) bool { return k == model.PolicyEgressIP })
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["mine"] || !names["prod-egress"] {
		t.Errorf("visible rows missing: %v", names)
	}
	if names["theirs"] {
		t.Error("a foreign namespace's policy leaked")
	}
	if names["block-dev"] {
		t.Error("a cluster-tier kind leaked without authority over it")
	}
}

// TestPolicyRedactor pins the effective/trace disclosure rule: a cluster-tier
// policy's subject namespaces and apply error are authoring-tier information,
// stripped without authority over the kind (its text goes through
// netstate.Redact, pinned there) - while the binding itself, a namespace-tier
// policy, and a caller with authority are untouched. Its identity follows
// list on the kind alone.
func TestPolicyRedactor(t *testing.T) {
	yes := func(model.PolicyKind) bool { return true }
	no := func(model.PolicyKind) bool { return false }
	lister := policyRedactor{can: no, named: yes}

	anp := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a", "tenant-b"}, SyncError: "namespace tenant-b: denied"}
	lister.apply(&anp, nil)
	if anp.Namespaces != nil || strings.Contains(anp.SyncError, "tenant-b") || anp.Name != "lockdown" || anp.Withheld {
		t.Errorf("admin policy without authority = %+v", anp)
	}

	none := policyRedactor{can: no, named: no}
	dfw := model.Policy{Name: "allow-web", Kind: model.PolicyProjectRule, Namespace: "team-a", Namespaces: []string{"team-a"}, SourceFile: "team-a/allow-web.yaml"}
	none.apply(&dfw, nil)
	if dfw.Namespaces == nil || dfw.Name != "allow-web" || dfw.SourceFile == "" {
		t.Error("namespace-tier policies are not admin-tier: must keep their namespaces and identity")
	}

	all := policyRedactor{can: yes, named: yes}
	allowed := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a"}, SyncError: "x", SourceFile: "cluster/lockdown.yaml"}
	all.apply(&allowed, nil)
	if allowed.Namespaces == nil || allowed.SyncError != "x" || allowed.Name != "lockdown" || allowed.SourceFile == "" {
		t.Error("authority over the kind keeps the enumeration, the error and the identity")
	}

	// Creating the kind without listing it reads the text, never the name:
	// Argo's apply error words the object's name.
	creator := policyRedactor{can: yes, named: no}
	held := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Priority: 10, SyncError: `AdminNetworkPolicy "lockdown" is invalid`, SourceFile: "cluster/lockdown.yaml"}
	creator.apply(&held, nil)
	if held.Name != "cluster rule, priority 10" || !held.Withheld || held.SourceFile != "" || held.SyncError != redactedSyncError {
		t.Errorf("a cluster policy the caller may not list = %+v", held)
	}

	// One response labels each withheld policy once, distinct from the others.
	pols := []model.Policy{
		{Name: "a", Kind: model.PolicyAdmin, Backing: "AdminNetworkPolicy", Priority: 10},
		{Name: "b", Kind: model.PolicyAdmin, Backing: "AdminNetworkPolicy", Priority: 10},
		{Name: "a", Kind: model.PolicyAdmin, Backing: "AdminNetworkPolicy", Priority: 10},
		{Name: "default", Kind: model.PolicyBaseline, Backing: "BaselineAdminNetworkPolicy"},
		{Name: "pool", Kind: model.PolicyEgressIP, Backing: "EgressIP"},
		{Name: "hop", Kind: model.PolicyRoute, Backing: "AdminPolicyBasedExternalRoute"},
	}
	var got []string
	for i := range pols {
		none.apply(&pols[i], nil)
		got = append(got, pols[i].Name)
	}
	want := []string{"cluster rule, priority 10", "cluster rule, priority 10 (2)", "cluster rule, priority 10", "baseline cluster rule", "fixed outbound IP", "external route"}
	if !slices.Equal(got, want) {
		t.Errorf("withheld labels = %q, want %q", got, want)
	}
}

// The redactor's view is the caller's own: their visible namespaces and the
// projects those belong to, under the configured project label. A failed
// visibility read hides every name.
func TestPolicyRedactorView(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	var down atomic.Bool
	fc.ssarFail = func(*authzv1.ResourceAttributes) int {
		if down.Load() {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		t.Fatal(err)
	}
	v := s.policyRedactor(context.Background(), id, c).view
	if v.ProjectLabel != "dotvirt.io/project" || !v.Namespace("team-a-dev") || v.Namespace("orphans-ns") ||
		!v.Project("team-a") || v.Project("orphans") {
		t.Errorf("view does not match the caller's visibility")
	}

	down.Store(true)
	s.bus.Publish(eventbus.RBACChanged)
	v = s.policyRedactor(context.Background(), id, c).view
	if v.Namespace("team-a-dev") || v.Project("team-a") {
		t.Error("a failed visibility read must hide every name")
	}
}

// Through the routes: a tenant reads the cluster rule governing its VM on the
// effective chain, its network's rules and a trace, and never a selector value
// that reaches past its own namespace; a value confined to it stays in clear.
// The policy plane holds no cluster row for it. A caller with authority over
// the kind reads every value.
func TestTenantReadsOwnScopeOnEveryPolicySurface(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	fc.namespaces[0].Labels["env"] = "prod"
	fc.namespaces[1].Labels["env"], fc.namespaces[1].Labels["tier"] = "prod", "gold"
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.ObjectMeta.Labels = map[string]string{"app": "web"}
	tmpl.Spec.Networks = []kubevirtcorev1.Network{{Name: "default", NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}}}
	fc.vms = []kubevirtcorev1.VirtualMachine{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: "web"},
		Spec:       kubevirtcorev1.VirtualMachineSpec{Template: tmpl},
	}}
	resources := func(gv string, names ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			list := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv}
			for _, n := range names {
				list.APIResources = append(list.APIResources, metav1.APIResource{Name: n, Verbs: []string{"list", "watch"}})
			}
			writeObj(w, list)
		}
	}
	sel := func(k, v string) map[string]any { return map[string]any{"matchLabels": map[string]any{k: v}} }
	anpGV := "policy.networking.k8s.io/v1alpha1"
	fc.on(http.MethodGet, "/apis/"+anpGV, resources(anpGV, "adminnetworkpolicies"))
	fc.on(http.MethodGet, "/apis/"+anpGV+"/adminnetworkpolicies", unstructuredList(anpGV, "AdminNetworkPolicy", map[string]any{
		"apiVersion": anpGV, "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "platform", "resourceVersion": "1"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": sel("env", "prod")},
			"ingress": []any{map[string]any{"action": "Allow", "from": []any{map[string]any{"pods": map[string]any{
				"namespaceSelector": sel("tier", "gold"), "podSelector": sel("app", "billing"),
			}}}}},
			"egress": []any{
				map[string]any{"action": "Allow", "to": []any{map[string]any{"namespaces": sel("dotvirt.io/project", "team-a")}}},
				map[string]any{"action": "Allow", "to": []any{map[string]any{"networks": []any{"198.51.100.0/24"}}}},
			},
		},
	}))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1", resources("k8s.ovn.org/v1", "userdefinednetworks"))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/userdefinednetworks", unstructuredList("k8s.ovn.org/v1", "UserDefinedNetwork", map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "vm-net", "namespace": "team-a-dev", "resourceVersion": "1"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{"10.0.0.0/24"}}},
	}))
	s := fc.server(nil, Config{})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Policies()) == 0 || len(ns.Catalog().Networks) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("netstate never synced the ANP and the network")
		}
	}

	trace := `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"ip":"198.51.100.7"},"port":443}`
	surfaces := []struct{ method, path, body string }{
		{http.MethodGet, vmPath + "/policy", ""},
		{http.MethodGet, "/api/networks/rules?network=team-a-dev/vm-net", ""},
		{http.MethodPost, "/api/networking/trace", trace},
	}
	read := func(token, method, path, body string) string {
		t.Helper()
		rec := do(s, token, method, path, body)
		wantStatus(t, rec, http.StatusOK)
		return rec.Body.String()
	}
	for _, sf := range surfaces {
		tenant := read(tenantToken, sf.method, sf.path, sf.body)
		for _, secret := range []string{"orphans", "kube-system", "gold", "tier", "billing", "env=prod"} {
			if strings.Contains(tenant, secret) {
				t.Errorf("%s: a tenant reads %q: %s", sf.path, secret, tenant)
			}
		}
		if !strings.Contains(tenant, "cluster rule, priority 10") || !strings.Contains(tenant, "other namespaces") {
			t.Errorf("%s: the rule governing the tenant's VM must stay, redacted: %s", sf.path, tenant)
		}
		if sf.method == http.MethodGet && !strings.Contains(tenant, "dotvirt.io/project=team-a") {
			t.Errorf("%s: a selector confined to the tenant's namespace was hidden: %s", sf.path, tenant)
		}
		admin := read(adminToken, sf.method, sf.path, sf.body)
		if !strings.Contains(admin, "env=prod") {
			t.Errorf("%s: authority over the kind must read every value: %s", sf.path, admin)
		}
	}
	if pols := read(tenantToken, http.MethodGet, "/api/policies", ""); strings.Contains(pols, "platform") {
		t.Errorf("the policy plane lists a cluster rule to a tenant: %s", pols)
	}
	if pols := read(adminToken, http.MethodGet, "/api/policies", ""); !strings.Contains(pols, "tier=gold") || !strings.Contains(pols, "app=billing") {
		t.Errorf("the policy plane hides values from authority over the kind: %s", pols)
	}
}

// Through the route: a tenant on a primary UDN tracing to an address in
// another tenant's network range reads exactly what it reads for an outside
// address, certain once the cluster's pod and service networks are known, so
// the trace is no oracle for that range; a caller who sees that namespace
// reads the overlay answer.
func TestTraceViewIsTheCallers(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.Spec.Networks = []kubevirtcorev1.Network{{Name: "default", NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}}}
	// web sits on team-a-dev's primary UDN; legacy and db on the default
	// network of a namespace without one.
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain-ns", Labels: map[string]string{"dotvirt.io/project": "plain"}}})
	running := func(ns, name, ip string) (kubevirtcorev1.VirtualMachine, kubevirtcorev1.VirtualMachineInstance) {
		return kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: kubevirtcorev1.VirtualMachineSpec{Template: tmpl}},
			kubevirtcorev1.VirtualMachineInstance{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec:       kubevirtcorev1.VirtualMachineInstanceSpec{Networks: tmpl.Spec.Networks},
				Status: kubevirtcorev1.VirtualMachineInstanceStatus{Phase: kubevirtcorev1.Running, Interfaces: []kubevirtcorev1.VirtualMachineInstanceNetworkInterface{
					{Name: "default", IP: ip, IPs: []string{ip}},
				}},
			}
	}
	for _, v := range [][3]string{{"team-a-dev", "web", "10.100.0.5"}, {"plain-ns", "legacy", "10.128.0.9"}, {"plain-ns", "db", "10.128.0.7"}} {
		vm, vmi := running(v[0], v[1], v[2])
		fc.vms, fc.vmis = append(fc.vms, vm), append(fc.vmis, vmi)
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
	udn := func(ns, name, cidr string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
			"metadata": map[string]any{"name": name, "namespace": ns, "uid": name, "resourceVersion": "1"},
			"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{cidr}}},
		}
	}
	nad := func(ns, name string) map[string]any {
		return map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{"name": name, "namespace": ns, "resourceVersion": "1",
				"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork", "name": name, "uid": name}}},
		}
	}
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1", resources("k8s.ovn.org/v1", "userdefinednetworks"))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1", resources("k8s.cni.cncf.io/v1", "network-attachment-definitions"))
	fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/userdefinednetworks", unstructuredList("k8s.ovn.org/v1", "UserDefinedNetwork",
		udn("team-a-dev", "net-a", "10.100.0.0/16"), udn("orphans-ns", "net-o", "10.200.0.0/16")))
	fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1/network-attachment-definitions", unstructuredList("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition",
		nad("team-a-dev", "net-a"), nad("orphans-ns", "net-o")))
	fc.on(http.MethodGet, "/api/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeObj(w, corev1.NodeList{TypeMeta: metav1.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "w1", Annotations: map[string]string{"k8s.ovn.org/node-subnets": `{"default":["10.128.0.0/23"]}`}},
		}}})
	})
	fc.on(http.MethodGet, "/apis/config.openshift.io/v1/networks/cluster", func(w http.ResponseWriter, _ *http.Request) {
		writeObj(w, map[string]any{
			"apiVersion": "config.openshift.io/v1", "kind": "Network", "metadata": map[string]any{"name": "cluster"},
			"status": map[string]any{"clusterNetwork": []any{map[string]any{"cidr": "10.128.0.0/14"}}, "serviceNetwork": []any{"172.30.0.0/16"}},
		})
	})
	s := fc.server(nil, Config{})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Catalog().Networks) < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("netstate never synced the networks")
		}
	}

	trace := func(token, ip string) model.TraceResult {
		t.Helper()
		rec := do(s, token, http.MethodPost, "/api/networking/trace", `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"ip":"`+ip+`"},"port":443}`)
		wantStatus(t, rec, http.StatusOK)
		return decodeBody[model.TraceResult](t, rec)
	}
	note := func(res model.TraceResult, ip string) string {
		for _, st := range res.Steps {
			if st.Stage == "connectivity" {
				return strings.ReplaceAll(st.Note, ip, "<ip>")
			}
		}
		return ""
	}
	raw := func(token, ip string) string {
		t.Helper()
		rec := do(s, token, http.MethodPost, "/api/networking/trace", `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"ip":"`+ip+`"},"port":443}`)
		wantStatus(t, rec, http.StatusOK)
		return strings.ReplaceAll(rec.Body.String(), ip, "<ip>")
	}
	for deadline := time.Now().Add(10 * time.Second); trace(tenantToken, "203.0.113.9").Verdict != "Allow"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the cluster's pod and service networks never placed an outside address")
		}
	}
	if inRange, outside := raw(tenantToken, "10.200.0.7"), raw(tenantToken, "203.0.113.9"); inRange != outside {
		t.Errorf("a tenant tells another tenant's range apart:\n%s\nwant\n%s", inRange, outside)
	}
	if admin := trace(adminToken, "10.200.0.7"); !strings.Contains(note(admin, "10.200.0.7"), "no in-cluster path") {
		t.Errorf("a caller who sees the namespace reads the overlay answer: %+v", admin)
	}

	// Beside primary UDNs, a default-network pod address is certain only as a
	// default-network VM the caller sees.
	fromLegacy := func(ip string) model.TraceResult {
		t.Helper()
		rec := do(s, adminToken, http.MethodPost, "/api/networking/trace", `{"source":{"namespace":"plain-ns","vm":"legacy"},"destination":{"ip":"`+ip+`"},"port":443}`)
		wantStatus(t, rec, http.StatusOK)
		return decodeBody[model.TraceResult](t, rec)
	}
	if res := fromLegacy("10.128.0.7"); res.Verdict != "Allow" {
		t.Errorf("a default-network VM the caller sees: %+v", res)
	}
	if res := fromLegacy("10.128.0.8"); res.Verdict != "Conditional" {
		t.Errorf("a default-network address no VM holds: %+v", res)
	}
}

// Through the routes: a cluster-scoped policy's identity follows the caller's
// own list right on its kind. A tenant, who may not list them, reads every
// cluster rule governing its VMs by a neutral label on every surface, never
// a name or a source file; a caller who may list them reads them as before;
// a review that fails withholds them. The policy plane lists a kind's rows
// only to a caller who may create and list it.
func TestClusterRuleIdentityFollowsTheCallersRBAC(t *testing.T) {
	const (
		listerToken  = "lister-token"
		creatorToken = "creator-token"
		anpGroup     = "policy.networking.k8s.io"
	)
	clusterTier := func(a *authzv1.ResourceAttributes) bool {
		return slices.Contains([]string{"adminnetworkpolicies", "baselineadminnetworkpolicies", "egressips", "adminpolicybasedexternalroutes"}, a.Resource)
	}
	fc := newFakeCluster(t)
	fc.readsVMs = func(token, ns string) bool { return token != adminToken && ns == "team-a-dev" }
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		switch token {
		case adminToken:
			return true
		case listerToken:
			return clusterTier(a) && a.Verb == verbList
		case creatorToken:
			return clusterTier(a) && a.Verb == verbCreate
		}
		return false
	}
	var down atomic.Bool
	fc.ssarFail = func(a *authzv1.ResourceAttributes) int {
		if down.Load() && clusterTier(a) && a.Verb == verbList {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.Spec.Networks = []kubevirtcorev1.Network{{Name: "default", NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}}}
	for _, name := range []string{"web", "db"} {
		fc.vms = append(fc.vms, kubevirtcorev1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: name},
			Spec:       kubevirtcorev1.VirtualMachineSpec{Template: tmpl},
		})
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
	all := map[string]any{}
	team := map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "team-a"}}
	admin := func(kind, name string, priority int64, action string) map[string]any {
		spec := map[string]any{
			"subject": map[string]any{"namespaces": all},
			"ingress": []any{map[string]any{"action": action, "from": []any{map[string]any{"namespaces": all}}}},
			"egress":  []any{map[string]any{"action": action, "to": []any{map[string]any{"networks": []any{"198.51.100.0/24"}}}}},
		}
		if kind == "AdminNetworkPolicy" {
			spec["priority"] = priority
		}
		return map[string]any{
			"apiVersion": anpGroup + "/v1alpha1", "kind": kind,
			"metadata": map[string]any{"name": name, "resourceVersion": "1"}, "spec": spec,
		}
	}
	anpGV, ovnGV := anpGroup+"/v1alpha1", "k8s.ovn.org/v1"
	fc.on(http.MethodGet, "/apis/"+anpGV, resources(anpGV, "adminnetworkpolicies", "baselineadminnetworkpolicies"))
	fc.on(http.MethodGet, "/apis/"+anpGV+"/adminnetworkpolicies", unstructuredList(anpGV, "AdminNetworkPolicy",
		admin("AdminNetworkPolicy", "acme-lockdown", 10, "Allow"), admin("AdminNetworkPolicy", "acme-quarantine", 10, "Deny")))
	fc.on(http.MethodGet, "/apis/"+anpGV+"/baselineadminnetworkpolicies", unstructuredList(anpGV, "BaselineAdminNetworkPolicy",
		admin("BaselineAdminNetworkPolicy", "acme-baseline", 0, "Deny")))
	fc.on(http.MethodGet, "/apis/"+ovnGV, resources(ovnGV, "userdefinednetworks", "egressips", "adminpolicybasedexternalroutes"))
	fc.on(http.MethodGet, "/apis/"+ovnGV+"/userdefinednetworks", unstructuredList(ovnGV, "UserDefinedNetwork", map[string]any{
		"apiVersion": ovnGV, "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "vm-net", "namespace": "team-a-dev", "resourceVersion": "1"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{"10.0.0.0/24"}}},
	}))
	fc.on(http.MethodGet, "/apis/"+ovnGV+"/egressips", unstructuredList(ovnGV, "EgressIP", map[string]any{
		"apiVersion": ovnGV, "kind": "EgressIP",
		"metadata": map[string]any{"name": "acme-pool", "resourceVersion": "1"},
		"spec":     map[string]any{"egressIPs": []any{"192.0.2.10"}, "namespaceSelector": team},
	}))
	fc.on(http.MethodGet, "/apis/"+ovnGV+"/adminpolicybasedexternalroutes", unstructuredList(ovnGV, "AdminPolicyBasedExternalRoute", map[string]any{
		"apiVersion": ovnGV, "kind": "AdminPolicyBasedExternalRoute",
		"metadata": map[string]any{"name": "acme-hop", "resourceVersion": "1"},
		"spec": map[string]any{"from": map[string]any{"namespaceSelector": team},
			"nextHops": map[string]any{"static": []any{map[string]any{"ip": "192.0.2.1"}}}},
	}))
	d := &planeDraft{declared: map[model.ObjectRef]string{
		{Kind: "AdminNetworkPolicy", Name: "acme-lockdown"}:         "cluster/acme-lockdown.yaml",
		{Kind: "BaselineAdminNetworkPolicy", Name: "acme-baseline"}: "cluster/acme-baseline.yaml",
	}}
	s := fc.server(d, Config{PlatformRepo: platformRepo})
	ns := netstate.New(fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); len(ns.Policies()) < 5 || len(ns.Catalog().Networks) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("netstate never synced the cluster policies and the network")
		}
	}

	surfaces := []struct{ method, path, body string }{
		{http.MethodGet, vmPath + "/policy", ""},
		{http.MethodGet, "/api/namespaces/team-a-dev/policy", ""},
		{http.MethodGet, "/api/networks/rules?network=team-a-dev/vm-net", ""},
		{http.MethodPost, "/api/networking/trace", `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"ip":"198.51.100.7"},"port":443}`},
		{http.MethodPost, "/api/networking/trace", `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"namespace":"team-a-dev","vm":"db"},"port":443}`},
	}
	read := func(token, method, path, body string) string {
		t.Helper()
		rec := do(s, token, method, path, body)
		wantStatus(t, rec, http.StatusOK)
		return rec.Body.String()
	}
	names := []string{"acme-lockdown", "acme-quarantine", "acme-baseline", "acme-pool", "acme-hop"}
	withheld := func(who, token string) {
		t.Helper()
		for _, sf := range surfaces {
			body := read(token, sf.method, sf.path, sf.body)
			for _, secret := range names {
				if strings.Contains(body, secret) {
					t.Errorf("%s %s %s: reads %q: %s", who, sf.method, sf.path, secret, body)
				}
			}
			if !strings.Contains(body, `"withheld":true`) || !strings.Contains(body, "cluster rule, priority 10") {
				t.Errorf("%s %s %s: the rules governing the VMs must stay, labelled: %s", who, sf.method, sf.path, body)
			}
		}
		eff := decodeBody[model.EffectivePolicy](t, do(s, token, http.MethodGet, vmPath+"/policy", ""))
		var got []string
		for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.SNAT, eff.Routes} {
			for _, b := range bs {
				got = append(got, b.Policy.Name)
			}
		}
		want := []string{"cluster rule, priority 10", "cluster rule, priority 10 (2)", "baseline cluster rule", "fixed outbound IP", "external route"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: labels = %q, want %q", who, got, want)
		}
	}
	named := func(who, token string) {
		t.Helper()
		for _, sf := range surfaces[:3] {
			body := read(token, sf.method, sf.path, sf.body)
			for _, name := range []string{"acme-lockdown", "acme-quarantine", "acme-baseline"} {
				if !strings.Contains(body, name) {
					t.Errorf("%s %s: does not read %q: %s", who, sf.path, name, body)
				}
			}
			if strings.Contains(body, "withheld") {
				t.Errorf("%s %s: withholds a rule it may list: %s", who, sf.path, body)
			}
		}
	}
	plane := func(token string) string { return read(token, http.MethodGet, "/api/policies", "") }

	withheld("tenant", tenantToken)
	withheld("creator", creatorToken)
	named("lister", listerToken)
	named("admin", adminToken)
	for _, token := range []string{tenantToken, listerToken, creatorToken} {
		if body := plane(token); strings.Contains(body, "acme-") || strings.Contains(body, "cluster/") {
			t.Errorf("%s: the policy plane lists a cluster rule: %s", token, body)
		}
	}
	if body := plane(adminToken); !strings.Contains(body, "cluster/acme-lockdown.yaml") || !strings.Contains(body, "acme-quarantine") {
		t.Errorf("admin: the policy plane must list every cluster rule and its file: %s", body)
	}

	down.Store(true)
	s.bus.Publish(eventbus.RBACChanged) // the cached verdicts are re-asked
	withheld("admin, review failing", adminToken)
	if body := plane(adminToken); strings.Contains(body, "acme-") {
		t.Errorf("a failed review lists a cluster rule: %s", body)
	}
}
