package stream

import (
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
)

// RepoLevels reads Levels off the bus: a frame depends on every kind in kinds,
// except scoped, which it depends on only for the repos its projects name - a
// push to one tenant's repo rebuilds only the frames that show that repo.
func RepoLevels(bus *eventbus.Bus, scoped eventbus.Kind, kinds ...eventbus.Kind) Levels {
	l := repoLevels{bus: bus, scoped: scoped, all: kinds}
	for _, k := range kinds {
		if k != scoped {
			l.unscoped = append(l.unscoped, k)
		}
	}
	return l
}

type repoLevels struct {
	bus           *eventbus.Bus
	scoped        eventbus.Kind
	all, unscoped []eventbus.Kind
}

func (l repoLevels) Total() uint64 { return l.bus.Version(l.all...) }

func (l repoLevels) Deps(inv model.Inventory) []string {
	var repos []string
	for _, p := range inv.Projects {
		if p.Repo != "" {
			repos = append(repos, forge.NormalizeRepoURL(p.Repo))
		}
	}
	return repos
}

func (l repoLevels) Stamp(deps []string) uint64 {
	v := l.bus.Version(l.unscoped...)
	for _, d := range deps {
		v += l.bus.KeyVersion(l.scoped, d)
	}
	return v
}
