package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
