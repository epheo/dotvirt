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

// backstopEvery is the TTL backstop's period for entries serving ttl. An entry
// serves while younger than ttl, so the first backstop to find it expired comes
// up to one period after it ages out: a period of ttl would double the bound.
func backstopEvery(ttl time.Duration) time.Duration { return ttl / 2 }

// backstopBound is how long an unwatched authorization change can take to
// reach an open frame: the entry's TTL, then the wait for the next backstop.
func backstopBound(ttl time.Duration) time.Duration { return ttl + backstopEvery(ttl) }

// backstopTick is the backstop's clock: the short TTL's period, so a switch to
// it takes effect at the next tick; the long TTL's period is a whole number of
// ticks.
var backstopTick = backstopEvery(unwatchedTTL)

// backstopDue reports whether ticks backstop ticks since the last publish make
// a period for entries serving ttl.
func backstopDue(ticks int, ttl time.Duration) bool {
	return time.Duration(ticks)*backstopTick >= backstopEvery(ttl)
}

// backstopTTL is the entry life the backstop's period follows: authTTL, or
// the short TTL while an entry reviewed right after a grant move (unsettled)
// may still be serving and expiring on it.
func (s *Server) backstopTTL() time.Duration {
	if s.state != nil && unsettledMayServe(time.Since(s.state.LastGrantMove())) {
		return unwatchedTTL
	}
	return s.authTTL()
}

// unsettledMayServe reports whether an unsettled entry can still be serving,
// or have just expired unseen, sinceMove after the last grant move: one
// reviewed up to grantSettle after it serves unwatchedTTL, and the expiry
// waits up to a tick for a backstop to notice.
func unsettledMayServe(sinceMove time.Duration) bool {
	return sinceMove < grantSettle+unwatchedTTL+backstopTick
}

// RunTTLBackstop is the TTL backstop for authorization no RBAC watch observes.
// The watches publish RBACChanged (OtherRBACChanged outside the projects) for
// RoleBindings, ClusterRoleBindings, the Roles and ClusterRoles those bind,
// and OpenShift Groups. A token's visible namespaces and SSAR verdicts still
// move unseen with:
//   - bindings that grant only platform ServiceAccounts, and the roles only
//     they bind,
//   - a watched kind the SA may not list yet,
//   - group membership kept anywhere but user.openshift.io Groups,
//   - authorizers other than RBAC.
//
// Those entries serve for authTTL only. An expired entry is re-evaluated by
// the next frame build, so while a stream is open this publishes TTLBackstop
// once per backstopEvery(backstopTTL) to cause one. That bounds such a change
// at backstopBound: 7.5 minutes while every RBAC watch runs (5 minute
// entries), 45 seconds otherwise (30 second entries, as on an operator role
// older than the app, or for a review right after a grant move). A frame that comes out identical is not resent. TTLBackstop is in
// no cache's version stamp, so nothing is invalidated before its TTL.
func (s *Server) RunTTLBackstop(ctx context.Context) {
	t := time.NewTicker(backstopTick)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ticks++; backstopDue(ticks, s.backstopTTL()) {
				ticks = 0
				s.ttlBackstop()
			}
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
