package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
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
