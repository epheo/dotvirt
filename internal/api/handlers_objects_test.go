package api

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// declaredDraft counts repo index reads; the embedded interface panics on
// anything else.
type declaredDraft struct {
	Draft
	reads int
}

func (d *declaredDraft) DeclaredFiles(project.ProjectInfo) (map[model.ObjectRef]string, error) {
	d.reads++
	return map[model.ObjectRef]string{{Kind: "NodeNetworkConfigurationPolicy", Name: "uplink-1"}: "uplinks/uplink-1.yaml"}, nil
}

// The declared-files index is read once per push, not once per request: a
// second lookup is served from the cache, and only a GitChanged publish
// (a poll or webhook that moved a head) makes the next lookup read again.
func TestSourceFilesCachedUntilGitChanges(t *testing.T) {
	bus := eventbus.New()
	sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
	d := &declaredDraft{}
	s := NewServer(Deps{
		State:    clusterstate.New(sa, "dotvirt.io/project", bus),
		Bus:      bus,
		Resolver: project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
		Draft:    d,
		Config:   Config{PlatformRepo: "https://forge/platform.git"},
	})
	id := auth.Identity{Token: "admin-token", Username: "admin"}
	// A seeded visible set keeps projectsFor off the cluster; the platform repo
	// needs no tenant namespace to resolve.
	s.visible.Put(restfactory.TokenKey(id.Token), visibleSet{ns: map[string]bool{}, ver: s.rbacVersion()})
	lookup := func() string {
		return s.sourceFiles(context.Background(), id, nil, true)("NodeNetworkConfigurationPolicy", "", "uplink-1")
	}

	if got := lookup(); got != "uplinks/uplink-1.yaml" {
		t.Fatalf("sourceFile = %q", got)
	}
	lookup()
	if d.reads != 1 {
		t.Fatalf("second lookup read the repo index again: %d reads", d.reads)
	}
	bus.Publish(eventbus.GitChanged)
	if got := lookup(); got != "uplinks/uplink-1.yaml" || d.reads != 2 {
		t.Fatalf("after GitChanged: sourceFile = %q, reads = %d, want a fresh read", got, d.reads)
	}
}
