package api

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

// scopePolicies must keep namespace-tier rows only in visible namespaces and
// cluster-tier rows only for callers with authority over that specific kind -
// an admin policy's rules name namespaces across tenants.
func TestScopePolicies(t *testing.T) {
	all := []model.Policy{
		{Name: "mine", Kind: model.PolicyDFW, Namespace: "team-a"},
		{Name: "theirs", Kind: model.PolicyDFW, Namespace: "team-b"},
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
// policy, and a caller with authority are untouched.
func TestPolicyRedactor(t *testing.T) {
	none := policyRedactor{can: func(model.PolicyKind) bool { return false }}

	anp := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a", "tenant-b"}, SyncError: "namespace tenant-b: denied"}
	none.apply(&anp, nil)
	if anp.Namespaces != nil || strings.Contains(anp.SyncError, "tenant-b") || anp.Name != "lockdown" {
		t.Errorf("admin policy without authority = %+v", anp)
	}

	dfw := model.Policy{Name: "allow-web", Kind: model.PolicyDFW, Namespace: "team-a", Namespaces: []string{"team-a"}}
	none.apply(&dfw, nil)
	if dfw.Namespaces == nil {
		t.Error("namespace-tier policies are not admin-tier: must keep their namespaces")
	}

	all := policyRedactor{can: func(model.PolicyKind) bool { return true }}
	allowed := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a"}, SyncError: "x"}
	all.apply(&allowed, nil)
	if allowed.Namespaces == nil || allowed.SyncError != "x" {
		t.Error("authority over the kind keeps the enumeration and the error")
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
