package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The platform tier rides the frame as its Application's rollup: absent while
// Argo is off or pre-sync, the app's own state when it exists, and a standing
// error when nothing applies the platform repo.
func TestPlatformRollup(t *testing.T) {
	const repo = "https://forge.example/dotvirt/platform.git"
	key := forge.NormalizeRepoURL(repo)
	if got := platformRollup(nil, repo); got != nil {
		t.Fatalf("argo off or pre-sync must yield nil, got %+v", got)
	}
	degraded := model.ProjectSync{Health: "Degraded", Unhealthy: []model.ObjectHealth{{Kind: "NodeNetworkConfigurationPolicy", Name: "dc-vlan-bridge", Health: "Degraded"}}}
	got := platformRollup(map[string]model.ProjectSync{key: degraded}, repo)
	if got == nil || got.Health != "Degraded" || len(got.Unhealthy) != 1 || got.Unhealthy[0].Name != "dc-vlan-bridge" {
		t.Fatalf("want the app's own rollup with its unhealthy objects, got %+v", got)
	}
	missing := platformRollup(map[string]model.ProjectSync{}, repo)
	if missing == nil || missing.Operation != "Error" || !strings.Contains(missing.SyncError, "dotvirt-platform Application is missing") {
		t.Fatalf("an absent Application must read as a standing error, got %+v", missing)
	}
}

// A caller without platform authority gets the rows about objects in its own
// namespaces and no verdict: that moves with other tenants' changes.
func TestTenantPlatformViewCarriesNoVerdict(t *testing.T) {
	np := func(ns, name string) model.ObjectHealth {
		return model.ObjectHealth{Group: model.KindNetpol.Group, Kind: model.KindNetpol.Kind, Namespace: ns, Name: name, Health: "Degraded"}
	}
	full := &model.ProjectSync{
		Sync: model.SyncOutOfSync, Health: "Degraded", Operation: "Failed", Revision: "abc1234",
		SyncError: `namespaces "team-b" is forbidden`,
		Unhealthy: []model.ObjectHealth{
			np("team-a-dev", "listed"),
			np("team-b", "theirs"),
			{Kind: "Namespace", Name: "team-a-dev", Health: "Missing"},
			{Kind: "Namespace", Name: "team-b", Health: "Missing"},
			{Kind: "NodeNetworkConfigurationPolicy", Name: "dc-vlan-bridge", Health: "Degraded"},
		},
	}
	fc := newFakeCluster(t)
	fc.serveKinds(model.KindNetpol)
	s := fc.server(&planeDraft{}, Config{BaseBranch: "main"})
	lists := func(a *authzv1.ResourceAttributes) bool { return a.Verb == verbList && a.Namespace == "team-a-dev" }
	visible := map[string]bool{"team-a-dev": true}
	id := auth.Identity{Token: "t", Username: "u"}

	if s.tenantPlatformView(context.Background(), id, newAsking(lists).c, visible, nil) != nil {
		t.Fatal("no rollup stays no rollup")
	}
	got := s.tenantPlatformView(context.Background(), id, newAsking(lists).c, visible, full)
	if got.Sync != "" || got.Health != "" || got.Operation != "" || got.SyncError != "" || got.Revision != "" {
		t.Errorf("a verdict reached a tenant: %+v", got)
	}
	if len(got.Unhealthy) != 2 || got.Unhealthy[0] != full.Unhealthy[2] || got.Unhealthy[1] != full.Unhealthy[0] {
		t.Errorf("unhealthy = %+v, want its own Namespace and what it lists", got.Unhealthy)
	}
	if len(full.Unhealthy) != 5 {
		t.Error("the shared rollup was mutated")
	}
	id.Token = "t2"
	none := func(*authzv1.ResourceAttributes) bool { return false }
	if got := s.tenantPlatformView(context.Background(), id, newAsking(none).c, map[string]bool{"elsewhere": true}, full); got != nil {
		t.Errorf("nothing of theirs still sent %+v", got)
	}
}

// Through the frame: the platform rollup names a cluster-scoped object only
// to a caller who may list every kind the platform repo declares. Listing
// namespaces cluster-wide alone, in a repo holding cluster rules, reads the
// verdict and the generic line.
func TestPlatformRollupNamesWhatTheCallerMayList(t *testing.T) {
	fc := newFakeCluster(t)
	const reader, lagging, platform = "reader-token", "lagging-token", "platform-token"
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		switch token {
		case reader, lagging:
			return a.Resource == "namespaces" && a.Verb == verbList
		case platform:
			return a.Verb == verbList && a.Namespace == "" && a.Resource != "virtualmachines" // no project: the frame reads no repo
		}
		return false
	}
	app := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "dotvirt-platform", "namespace": "openshift-gitops", "resourceVersion": "1"},
		"spec":     map[string]any{"source": map[string]any{"repoURL": platformRepo}},
		"status": map[string]any{
			"sync":   map[string]any{"status": "OutOfSync"},
			"health": map[string]any{"status": "Degraded"},
			"operationState": map[string]any{"phase": "Failed",
				"message": `one or more objects failed to apply: AdminNetworkPolicy "acme-lockdown" is invalid`},
			"resources": []any{map[string]any{
				"group": "policy.networking.k8s.io", "kind": "AdminNetworkPolicy", "name": "acme-lockdown",
				"health": map[string]any{"status": "Degraded", "message": `AdminNetworkPolicy "acme-lockdown" degraded`},
			}},
		},
	}
	fc.on(http.MethodGet, "/apis/argoproj.io/v1alpha1/applications", unstructuredList("argoproj.io/v1alpha1", "Application", app))
	fc.serveKinds(model.KindNS, model.KindANP)
	declares := []schema.GroupKind{{Kind: model.KindNS.Kind}, {Group: model.KindANP.Group, Kind: model.KindANP.Kind}}
	d := &planeDraft{kinds: declares}
	s := fc.server(d, Config{PlatformRepo: platformRepo})
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: t\n  cluster: {server: %s, insecure-skip-tls-verify: true}\ncontexts:\n- name: t\n  context: {cluster: t, user: t}\ncurrent-context: t\nusers:\n- name: t\n  user: {token: sa-token}\n", fc.srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	ac, err := argo.NewSAClient(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	snap := argo.NewSnapshot(ac, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	snap.Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); len(snap.ProjectDrift()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the platform Application never synced")
		}
	}
	s.drift = snap

	frame := func(token string) string {
		t.Helper()
		rec := do(s, token, http.MethodGet, "/api/inventory", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s frame = %d %s", token, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := frame(reader); strings.Contains(body, "acme-lockdown") || strings.Contains(body, platformErrorForTenants) || strings.Contains(body, `"platform"`) {
		t.Errorf("a caller who is no platform reader got its rollup: %s", body)
	}
	if body := frame(platform); !strings.Contains(body, "acme-lockdown") {
		t.Errorf("a caller listing every platform kind must read the rollup: %s", body)
	}

	// Git's declared kinds and Argo's live tree part (a mirror behind the
	// merge): a reader of a repo declaring namespaces alone still never reads
	// the cluster rule Argo reports.
	d.kinds = declares[:1]
	if cl := decodeBody[model.Capabilities](t, do(s, lagging, http.MethodGet, "/api/capabilities", "")).Cluster; cl == nil || !cl.PlatformRead {
		t.Fatalf("a namespace lister of a namespaces-only repo must read the platform tier: %+v", cl)
	}
	if body := frame(lagging); strings.Contains(body, "acme-lockdown") || !strings.Contains(body, platformErrorForTenants) {
		t.Errorf("a platform reader without list on cluster rules reads one: %s", body)
	}
	if body := frame(platform); !strings.Contains(body, "acme-lockdown") {
		t.Errorf("a caller listing cluster rules must still read the row: %s", body)
	}
}
