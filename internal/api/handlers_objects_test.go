package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// declaredDraft serves one platform uplink's file; the embedded interface
// panics on anything else.
type declaredDraft struct {
	Reader
	Draft
}

func (d *declaredDraft) DeclaredFiles(project.ProjectInfo) (map[model.ObjectRef]string, error) {
	return map[model.ObjectRef]string{{Kind: "NodeNetworkConfigurationPolicy", Name: "uplink-1"}: "uplinks/uplink-1.yaml"}, nil
}

// A cluster-scoped object resolves against the platform repo, and only where
// the caller asked for the platform tier.
func TestSourceFilesPlatformObject(t *testing.T) {
	bus := eventbus.New()
	sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
	d := &declaredDraft{}
	s := NewServer(Deps{
		State:    clusterstate.New(sa, "dotvirt.io/project", bus),
		Bus:      bus,
		Resolver: project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
		Reader:   d,
		Draft:    d,
		Config:   Config{PlatformRepo: "https://forge/platform.git"},
	})
	id := auth.Identity{Token: "admin-token", Username: "admin"}
	// A seeded visible set keeps projectsFor off the cluster; the platform repo
	// needs no tenant namespace to resolve.
	seedVisible(s, id.Token, map[string]bool{})
	lookup := func(withPlatform bool) string {
		return s.sourceFiles(context.Background(), id, nil, withPlatform)("NodeNetworkConfigurationPolicy", "", "uplink-1")
	}

	if got := lookup(true); got != "uplinks/uplink-1.yaml" {
		t.Fatalf("sourceFile = %q", got)
	}
	if got := lookup(false); got != "" {
		t.Fatalf("sourceFile without the platform tier = %q, want none", got)
	}
}

// A refused object route names the kind in the UI's words, never by the name
// drafts persist it under.
func TestObjectRouteRefusalsSpeakTheUIsWords(t *testing.T) {
	s := tenantServer(t, &reachedDraft{}, []string{"web"}, []string{"web"})
	for path, want := range map[string]string{
		"/api/objects/drs/cluster/kubedescheduler": "object routes do not take rebalancing",
		"/api/objects/networkpolicy/cluster/p1":    "cluster scope does not hold a project rule",
		"/api/objects/uplink/web/u1":               "a namespace does not hold an uplink",
	} {
		rec := serve(s, http.MethodDelete, path, "", editorToken)
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest || !strings.Contains(body, want) {
			t.Errorf("%s: %d %q, want 400 %q", path, rec.Code, body, want)
		}
		if strings.Contains(body, "drs") || strings.Contains(body, "networkpolicy") {
			t.Errorf("%s: %q echoes a persisted resource name", path, body)
		}
	}
}
