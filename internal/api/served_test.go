package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/tenancy"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A kind the cluster does not serve takes no new or changed object, whatever
// RBAC allows: it would merge into git and never sync. Removing one stays
// open, and the capabilities answer as the routes do.
func TestUnservedKindsTakeNoNewOrChangedObject(t *testing.T) {
	fc := newFakeCluster(t)
	fc.serveKinds(model.KindNetpol)
	d := &gatedDraft{planeDraft: &planeDraft{declared: map[model.ObjectRef]string{}}}
	s := fc.server(d, Config{BaseBranch: "main"})

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/egressfirewalls", `{"namespace":"team-a-dev"}`},
		{http.MethodPut, "/api/objects/egressfirewall/team-a-dev/default", `{"yaml":"x"}`},
		{http.MethodPost, "/api/objects/egressfirewall/team-a-dev/default/restore", `{"hash":"` + hash40 + `"}`},
	} {
		rec := do(s, adminToken, c.method, c.path, c.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "does not serve egressfirewalls") {
			t.Errorf("%s %s on a cluster without EgressFirewalls = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(s, adminToken, http.MethodDelete, "/api/objects/egressfirewall/team-a-dev/default", ""); rec.Code == http.StatusConflict {
		t.Errorf("removing an unserved kind's object is refused: %s", rec.Body.String())
	}
	if rec := do(s, adminToken, http.MethodPost, "/api/networkpolicies", `{"namespace":"team-a-dev"}`); rec.Code == http.StatusConflict {
		t.Errorf("a served kind is refused: %s", rec.Body.String())
	}

	caps := decodeBody[model.Capabilities](t, do(s, adminToken, http.MethodGet, "/api/capabilities?namespace=team-a-dev", "")).Namespace
	if caps == nil || caps.EgressFirewall != (model.ObjectCaps{Delete: true}) || !caps.NetworkPolicy.Create || caps.Network.Create {
		t.Errorf("capabilities = %+v, want egress firewalls and project networks delete-only, network policies open", caps)
	}
}

// A VM Network is a UserDefinedNetwork: on a cluster that serves none, New
// Project and New Namespace refuse one and its capability reads false; the
// project without it stays open, and a cluster that serves them takes one.
func TestVMNetworkNeedsServedUserDefinedNetworks(t *testing.T) {
	rbac := "rbac.authorization.k8s.io"
	grants := []grant{
		{verb: verbGet, resource: "namespaces"},
		{verb: verbCreate, resource: "namespaces"},
		{verb: verbCreate, group: rbac, resource: "rolebindings"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: "admin"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: tenancy.TenantEgressRole},
		kind(verbCreate, model.KindUDN, ""),
	}
	withNet := `{"name":"team-c","vmNetwork":{"name":"v","subnet":"10.0.0.0/24"}}`
	for _, served := range []bool{false, true} {
		g := newCoordinatorRig(t)
		if served {
			g.fc.serveKinds(model.KindNS, model.KindUDN)
		} else {
			g.fc.serveKinds(model.KindNS)
		}
		tok := g.token(grants)
		cl, _ := g.caps(tok, "team-a-dev")
		if cl.VMNetwork != served || !cl.NamespaceCreate {
			t.Errorf("served=%v: vmNetwork = %v, namespaceCreate = %v", served, cl.VMNetwork, cl.NamespaceCreate)
		}
		if rec := do(g.s, tok, http.MethodPost, "/api/projects", withNet); served != (rec.Code == http.StatusOK) {
			t.Errorf("served=%v: new project with a VM Network = %d %s", served, rec.Code, rec.Body.String())
		}
	}

	g := newCoordinatorRig(t)
	g.fc.serveKinds(model.KindNS)
	tok := g.token(grants)
	for _, c := range []struct{ path, body string }{
		{"/api/projects", withNet},
		{"/api/namespaces", `{"project":"team-a","name":"newns","vmNetwork":{"name":"v","subnet":"10.0.0.0/24"}}`},
	} {
		rec := do(g.s, tok, http.MethodPost, c.path, c.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "does not serve userdefinednetworks") {
			t.Errorf("POST %s with a VM Network on a cluster without UserDefinedNetworks = %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/projects", `{"name":"team-c"}`); rec.Code != http.StatusOK {
		t.Errorf("a new project without a VM Network = %d %s", rec.Code, rec.Body.String())
	}
}

// Rebalancing installs its operator through OLM, and PSI adds a MachineConfig:
// a cluster that serves neither is offered neither and the route refuses them,
// while turning rebalancing off, a removal, stays open.
func TestDRSNeedsOLMAndMachineConfigServed(t *testing.T) {
	for _, c := range []struct {
		name        string
		serves      []model.Kind
		enable, psi bool
		refused     string // the kind a refused enable names
	}{
		{"no OLM", []model.Kind{model.KindNS}, false, false, "subscriptions"},
		{"OLM, no machine config", []model.Kind{model.KindNS, model.KindSub, model.KindOpGroup}, true, false, "machineconfigs"},
		{"OLM and machine config", []model.Kind{model.KindNS, model.KindSub, model.KindOpGroup, model.KindMachCfg}, true, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.serveKinds(c.serves...)
			s := fc.server(&planeDraft{}, Config{PlatformRepo: platformRepo})

			caps := decodeBody[model.Capabilities](t, do(s, adminToken, http.MethodGet, "/api/capabilities", "")).Cluster
			if caps == nil || caps.DRSEnable != c.enable || caps.DRSPSI != c.psi {
				t.Errorf("caps = %+v, want enable %v, psi %v", caps, c.enable, c.psi)
			}
			for _, b := range []struct {
				body string
				want bool
			}{{`{"mode":"Predictive"}`, c.enable}, {`{"mode":"Predictive","installPSI":true}`, c.psi}} {
				rec := do(s, adminToken, http.MethodPost, "/api/drs", b.body)
				switch {
				case b.want && rec.Code != http.StatusOK:
					t.Errorf("POST /api/drs %s = %d %s, want 200", b.body, rec.Code, rec.Body.String())
				case !b.want && (rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "does not serve")):
					t.Errorf("POST /api/drs %s = %d %s, want 409", b.body, rec.Code, rec.Body.String())
				}
			}
			if c.refused != "" {
				rec := do(s, adminToken, http.MethodPost, "/api/drs", `{"mode":"Predictive","installPSI":true}`)
				if !strings.Contains(rec.Body.String(), c.refused) {
					t.Errorf("refusal %q does not name %s", rec.Body.String(), c.refused)
				}
			}
			if rec := do(s, adminToken, http.MethodDelete, "/api/drs", ""); rec.Code == http.StatusConflict {
				t.Errorf("turning rebalancing off is refused: %s", rec.Body.String())
			}
		})
	}
}

// What a frame flags in a tenant repo follows the install's policy, with the
// scope the served-kinds index holds and without reading discovery for it: a
// kind the index has not seen reads as namespaced.
func TestTenantKindRuleReadsTheIndexOnly(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{TenantKinds: tenancy.AnyPolicy(tenancy.GroupKind{Group: "capsule.clastix.io", Kind: "*"})})
	prio := schema.GroupKind{Group: "scheduling.k8s.io", Kind: "PriorityClass"}
	rule := s.tenantKindRule()
	if !rule.Declarable("apps", "Deployment") || !rule.Declarable(prio.Group, prio.Kind) ||
		rule.Declarable("rbac.authorization.k8s.io", "Role") || rule.Declarable("capsule.clastix.io", "Tenant") {
		t.Error("before discovery: a namespaced reading of unknown kinds, the deny list kept")
	}
	if calls := fc.seen(); len(calls) != 0 {
		t.Errorf("the rule read the cluster: %v", calls)
	}
	s.kinds.Store(&kindIndex{asked: map[schema.GroupKind]bool{prio: true}, cluster: map[schema.GroupKind]bool{prio: true}})
	if s.tenantKindRule().Declarable(prio.Group, prio.Kind) {
		t.Error("a kind discovery serves without a namespace is declarable")
	}
	if listed := (&Server{}).tenantKindRule(); listed.Declarable("apps", "Deployment") {
		t.Error("the listed kinds admit a Deployment")
	}
}

// OVN-Kubernetes takes a wildcard dnsName only with its DNS name resolver,
// which serves DNSNameResolver: elsewhere the create form refuses one, and
// a named host stays open.
func TestWildcardEgressNeedsTheDNSNameResolver(t *testing.T) {
	wildcard := `{"namespace":"team-a-dev","rules":[{"action":"Allow","dnsName":"api.example.com"},{"action":"Allow","dnsName":"*.example.com"}]}`
	named := `{"namespace":"team-a-dev","rules":[{"action":"Allow","dnsName":"api.example.com"}]}`
	for _, resolver := range []bool{false, true} {
		fc := newFakeCluster(t)
		if resolver {
			fc.serveKinds(model.KindEgressFW, kindDNSNameResolver)
		} else {
			fc.serveKinds(model.KindEgressFW)
		}
		d := &gatedDraft{planeDraft: &planeDraft{declared: map[model.ObjectRef]string{}}}
		s := fc.server(d, Config{BaseBranch: "main"})
		rec := do(s, adminToken, http.MethodPost, "/api/egressfirewalls", wildcard)
		refused := rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "rule 2: this cluster resolves no wildcard DNS names")
		if refused == resolver || resolver && rec.Code != http.StatusOK {
			t.Errorf("resolver=%v: a wildcard rule = %d %s", resolver, rec.Code, rec.Body.String())
		}
		if rec := do(s, adminToken, http.MethodPost, "/api/egressfirewalls", named); rec.Code != http.StatusOK {
			t.Errorf("resolver=%v: a named host = %d %s", resolver, rec.Code, rec.Body.String())
		}
		// The manifest editor alike: an edit OVN-K refuses keeps the old rules.
		edit := func(dns string) *httptest.ResponseRecorder {
			manifest := "kind: EgressFirewall\nspec:\n  egress:\n  - type: Allow\n    to:\n      cidrSelector: 10.0.0.0/8\n  - type: Allow\n    to:\n      dnsName: '" + dns + "'\n"
			body, _ := json.Marshal(model.UpdateManifestRequest{YAML: manifest})
			return do(s, adminToken, http.MethodPut, "/api/objects/egressfirewall/team-a-dev/default", string(body))
		}
		rec = edit("*.example.com")
		refused = rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "rule 2: this cluster resolves no wildcard DNS names")
		if refused == resolver || resolver && rec.Code != http.StatusOK {
			t.Errorf("resolver=%v: an edit to a wildcard = %d %s", resolver, rec.Code, rec.Body.String())
		}
		if rec := edit("api.example.com"); rec.Code != http.StatusOK {
			t.Errorf("resolver=%v: an edit to a named host = %d %s", resolver, rec.Code, rec.Body.String())
		}
	}
}

// The gate's project-namespace fact is the snapshot's: a labelled namespace
// is a project's, any other is not.
func TestProjectNamespaceReadsTheSnapshot(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openshift-cnv"}})
	s := fc.server(nil, Config{BaseBranch: "main"})
	if !s.ProjectNamespace("team-a-dev") || s.ProjectNamespace("openshift-cnv") || s.ProjectNamespace("nowhere") {
		t.Errorf("team-a-dev %v, openshift-cnv %v, nowhere %v; want only team-a-dev",
			s.ProjectNamespace("team-a-dev"), s.ProjectNamespace("openshift-cnv"), s.ProjectNamespace("nowhere"))
	}
}

// Rescheduling moves VMs alone: on a cluster known to run no KubeVirt it is
// not offered and the route says why, while turning it off stays open. An
// unknown answer takes nothing away.
func TestDRSNeedsVMs(t *testing.T) {
	olm := []model.Kind{model.KindNS, model.KindSub, model.KindOpGroup}
	for _, c := range []struct {
		name   string
		vms    *bool
		enable bool
	}{
		{"no KubeVirt", new(false), false},
		{"KubeVirt", new(true), true},
		{"unknown", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.serveKinds(olm...)
			s := fc.server(&planeDraft{}, Config{PlatformRepo: platformRepo})
			if c.vms != nil {
				s.modules = knownModules(t, *c.vms)
			}
			caps := decodeBody[model.Capabilities](t, do(s, adminToken, http.MethodGet, "/api/capabilities", "")).Cluster
			if caps == nil || caps.DRSEnable != c.enable {
				t.Errorf("caps = %+v, want enable %v", caps, c.enable)
			}
			rec := do(s, adminToken, http.MethodPost, "/api/drs", `{"mode":"Predictive"}`)
			if refused := rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "nothing to rebalance"); refused == c.enable {
				t.Errorf("POST /api/drs = %d %s, want refused %v", rec.Code, rec.Body.String(), !c.enable)
			}
			if rec := do(s, adminToken, http.MethodDelete, "/api/drs", ""); rec.Code == http.StatusConflict {
				t.Errorf("turning rebalancing off is refused: %s", rec.Body.String())
			}
		})
	}
}
