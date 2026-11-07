package api

import (
	"context"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// streaming counts a token's open inventory streams while each is served (the
// stream handler returns when its connection closes): the proposals refresher
// keeps an open stream's lanes fresh however long its frames stay unchanged, and
// the TTL backstop runs only while some stream is open.
func (s *Server) streaming(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.FromContext(r.Context())
		if !ok {
			next(w, r)
			return
		}
		key := restfactory.TokenKey(id.Token)
		s.streamMu.Lock()
		s.streams[key]++
		s.streamMu.Unlock()
		defer func() {
			s.streamMu.Lock()
			if s.streams[key]--; s.streams[key] <= 0 {
				delete(s.streams, key)
			}
			s.streamMu.Unlock()
		}()
		next(w, r)
	}
}

func (s *Server) streamOpen(key string) bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.streams[key] > 0
}

const (
	// ttlBackstopEvery is the TTL backstop's cadence. A cached entry serves while
	// younger than visibleTTL, so the first tick to find it expired comes up to one
	// period after it ages out: ticking at visibleTTL would double the bound.
	ttlBackstopEvery = visibleTTL / 2
	// ttlBackstopBound is how long an unwatched authorization change can take to
	// reach an open frame: the entry's TTL, then the wait for the next tick.
	ttlBackstopBound = visibleTTL + ttlBackstopEvery
)

// RunTTLBackstop is the TTL backstop for authorization no RBAC watch observes.
// The watches publish RBACChanged for RoleBindings and Roles in project
// namespaces, ClusterRoleBindings, ClusterRoles and OpenShift Groups. A token's
// visible namespaces and SSAR verdicts still move unseen with:
//   - RBAC outside project namespaces (the shared image namespaces the options
//     filter reviews),
//   - system: ClusterRoles bound directly, and bindings that grant only
//     platform ServiceAccounts,
//   - a watched kind the SA may not list yet,
//   - group membership kept anywhere but user.openshift.io Groups,
//   - authorizers other than RBAC.
//
// Those entries are cached for visibleTTL only. An expired entry is
// re-evaluated by the next frame build, so while a stream is open this
// publishes TTLBackstop every ttlBackstopEvery to cause one, which bounds such a
// change at ttlBackstopBound (7.5 minutes); a frame that comes out identical is
// not resent. TTLBackstop is in no cache's version stamp, so nothing is invalidated
// before its TTL.
func (s *Server) RunTTLBackstop(ctx context.Context) {
	t := time.NewTicker(ttlBackstopEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ttlBackstop()
		}
	}
}

func (s *Server) ttlBackstop() {
	s.streamMu.Lock()
	open := len(s.streams) > 0
	s.streamMu.Unlock()
	if open {
		s.bus.Publish(eventbus.TTLBackstop)
	}
}
