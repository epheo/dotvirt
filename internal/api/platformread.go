package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

// kindProbe is how long discovery's word that a kind is not served, or could
// not be read, stands before it is asked again. A served kind stays served.
const kindProbe = time.Minute

// kindIndex is discovery's answer for the kinds the platform repo declared
// when it was asked: the resource serving each served one, and the groups it
// could not read, whose kinds are unknown.
type kindIndex struct {
	at       time.Time
	asked    map[schema.GroupKind]bool
	resource map[schema.GroupKind]string
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
	kinds, err := s.reader.DeclaredKinds(s.platformProject())
	if err != nil || len(kinds) == 0 {
		return nil, err
	}
	x, err := s.servedKinds(ctx, kinds)
	if err != nil {
		return nil, err
	}
	var act action
	for _, k := range kinds {
		res, served := x.resource[k]
		if !served && x.asked[k] && !x.failed[k.Group] {
			continue
		}
		if !served {
			_, mk, known := model.LookupKind(k.Kind)
			if !known || mk.Group != k.Group {
				return nil, fmt.Errorf("%w: discovery could not tell which resource serves %s", model.ErrUnavailable, k)
			}
			res = mk.Plural
		}
		act = append(act, refQ(verbList, ssarRef{k.Group, res}, ""))
	}
	return act, nil
}

// servedKinds is discovery's answer for kinds, read through the SA once for
// every caller until it no longer covers them.
func (s *Server) servedKinds(ctx context.Context, kinds []schema.GroupKind) (*kindIndex, error) {
	if x := s.kinds.Load(); x.covers(kinds, time.Now()) {
		return x, nil
	}
	return s.kindFill.Do(ctx, "platform", func(context.Context) (*kindIndex, error) {
		if x := s.kinds.Load(); x.covers(kinds, time.Now()) {
			return x, nil
		}
		sa, err := s.clusterF.SA()
		if err != nil {
			return nil, err
		}
		at := time.Now()
		served, failed, err := sa.KindResources(kinds)
		if err != nil {
			log.Printf("api: discovery for the platform repo's kinds: %v", err) // SA-side detail stays in the log
			return nil, fmt.Errorf("%w: cluster discovery failed", model.ErrUnavailable)
		}
		x := &kindIndex{at: at, asked: make(map[schema.GroupKind]bool, len(kinds)), resource: served, failed: failed}
		for _, k := range kinds {
			x.asked[k] = true
		}
		s.kinds.Store(x)
		return x, nil
	})
}
