package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// kindProbe is how long discovery's word that a kind is not served, or could
// not be read, stands before it is asked again. A served kind stays served.
const kindProbe = time.Minute

// kindIndex is discovery's answer for the kinds repos declared when it was
// asked: the resource serving each served one, and the groups it
// could not read, whose kinds are unknown.
type kindIndex struct {
	at       time.Time
	asked    map[schema.GroupKind]bool
	resource map[schema.GroupKind]string
	cluster  map[schema.GroupKind]bool // served without a namespace
	failed   map[string]bool
}

// covers reports whether x may answer for kinds at now: each was asked, and
// one not served was asked within kindProbe.
func (x *kindIndex) covers(kinds []schema.GroupKind, now time.Time) bool {
	if x == nil {
		return false
	}
	for _, k := range kinds {
		if _, served := x.resource[k]; !served && (!x.asked[k] || now.Sub(x.at) >= kindProbe) {
			return false
		}
	}
	return true
}

// platformListing is listing, cluster-wide, every kind the platform repo
// declares on its base branch that the cluster serves: reading the repo reads
// those objects, so a caller the apiserver would refuse one of them is
// refused the repo. A namespaced kind (a project grant's RoleBinding) is
// asked cluster-wide too. A kind the cluster does not serve needs nothing,
// as kubectl would answer it NotFound; one discovery could not read is asked
// all the same. The kinds are memoized per repo head, discovery's answer
// until a kind it found missing is due again.
func (s *Server) platformListing(ctx context.Context) (action, error) {
	return s.repoListing(ctx, s.platformProject(), []string{""})
}

// repoListing is listing every kind proj's repo declares in each of
// namespaces, "" being cluster-wide.
func (s *Server) repoListing(ctx context.Context, proj project.ProjectInfo, namespaces []string) (action, error) {
	kinds, err := s.reader.DeclaredKinds(proj)
	if err != nil || len(kinds) == 0 {
		return nil, err
	}
	x, err := s.servedKinds(ctx, kinds)
	if err != nil {
		return nil, err
	}
	var act action
	for _, k := range kinds {
		ref, served, err := x.ref(k)
		if err != nil {
			return nil, err
		}
		if served {
			for _, ns := range namespaces {
				act = append(act, refQ(verbList, ref, ns))
			}
		}
	}
	return act, nil
}

// ref is the resource serving k. served is false when discovery found k not
// served. A kind discovery could not read is served all the same, its
// resource named by the kinds table; err when the table does not name it.
// A nil x knows nothing.
func (x *kindIndex) ref(k schema.GroupKind) (ref ssarRef, served bool, err error) {
	if x != nil {
		if res, ok := x.resource[k]; ok {
			return ssarRef{k.Group, res}, true, nil
		}
		if x.asked[k] && !x.failed[k.Group] {
			return ssarRef{}, false, nil
		}
	}
	if _, mk, known := model.LookupKind(k.Kind); known && mk.Group == k.Group {
		return ssarFor(mk), true, nil
	}
	return ssarRef{}, false, fmt.Errorf("%w: discovery could not tell which resource serves %s", model.ErrUnavailable, k)
}

// ClusterScoped reports whether discovery serves group/kind without a
// namespace, from the served-kinds index as the repo reads left it: it reads
// nothing. A kind the index has not seen reads as namespaced; a tenant
// AppProject, which admits no cluster-scoped kind, still refuses it if not.
func (s *Server) ClusterScoped(group, kind string) bool {
	x := s.kinds.Load()
	return x != nil && x.cluster[schema.GroupKind{Group: group, Kind: kind}]
}

// ProjectNamespace reports whether namespace belongs to a project: the
// snapshot watches project namespaces alone. It reads nothing new.
func (s *Server) ProjectNamespace(namespace string) (project, known bool) {
	if s.state == nil || !s.state.NamespacesListed() {
		return false, false
	}
	return s.state.Holds(model.KindNS.Group, model.KindNS.Plural, "", namespace), true
}

// tenantKindRule is what a tenant repo may declare on this install, with the
// scope discovery gave: the rule the frame's placement issues are read by.
func (s *Server) tenantKindRule() changeset.KindRule {
	return changeset.KindRule{Policy: s.cfg.TenantKinds, ClusterScoped: s.ClusterScoped, ProjectNamespace: s.ProjectNamespace}
}

// unserved are those of kinds discovery found not served, from one read. git
// may declare one, but nothing syncs it, so dotvirt stages no new or changed
// one; removing one stays open. A failed read, or a kind discovery could not
// read, counts as served: the apiserver still answers the change at sync.
func (s *Server) unserved(ctx context.Context, kinds ...model.Kind) map[model.Kind]bool {
	gks := make([]schema.GroupKind, len(kinds))
	for i, k := range kinds {
		gks[i] = schema.GroupKind{Group: k.Group, Kind: k.Kind}
	}
	out := map[model.Kind]bool{}
	x, err := s.servedKinds(ctx, gks)
	if err != nil {
		return out
	}
	for i, k := range kinds {
		if _, served, err := x.ref(gks[i]); err == nil && !served {
			out[k] = true
		}
	}
	return out
}

// gateServed refuses staging a new or changed object on a cluster that serves
// none of kinds, the ones the request could write: with any served, the
// coordinator weighs the kind git declares.
func (s *Server) gateServed(w http.ResponseWriter, r *http.Request, kinds ...model.Kind) bool {
	un := s.unserved(r.Context(), kinds...)
	if len(kinds) == 0 || len(un) < len(kinds) {
		return true
	}
	fail(w, fmt.Errorf("%w: this cluster does not serve %s, so one in git would never sync", model.ErrConflict, kinds[0].Plural))
	return false
}

// servedKinds is discovery's answer for kinds, read through the SA once for
// every caller until it no longer covers them.
func (s *Server) servedKinds(ctx context.Context, kinds []schema.GroupKind) (*kindIndex, error) {
	if x := s.kinds.Load(); x.covers(kinds, time.Now()) {
		return x, nil
	}
	return s.kindFill.Do(ctx, "kinds", func(context.Context) (*kindIndex, error) {
		prev := s.kinds.Load()
		if prev.covers(kinds, time.Now()) {
			return prev, nil
		}
		// The read and rollup paths ask different sets: asking their union
		// keeps one index covering both. kinds may be the memoized index's.
		want := slices.Clone(kinds)
		if prev != nil {
			for k := range prev.asked {
				if !slices.Contains(want, k) {
					want = append(want, k)
				}
			}
		}
		if s.clusterF == nil {
			return nil, unavailable(errors.New("cluster not configured"))
		}
		sa, err := s.clusterF.SA()
		if err != nil {
			return nil, err
		}
		at := time.Now()
		served, cluster, failed, err := sa.KindResources(want)
		if err != nil {
			log.Printf("api: discovery for the repos' kinds: %v", err) // SA-side detail stays in the log
			return nil, fmt.Errorf("%w: cluster discovery failed", model.ErrUnavailable)
		}
		x := &kindIndex{at: at, asked: make(map[schema.GroupKind]bool, len(want)), resource: served, cluster: cluster, failed: failed}
		for _, k := range want {
			x.asked[k] = true
		}
		s.kinds.Store(x)
		return x, nil
	})
}
