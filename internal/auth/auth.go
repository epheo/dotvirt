// Package auth authenticates dotvirt users by their Kubernetes bearer token.
// A user pastes a token (oc whoami -t / kubectl create token); dotvirt validates
// it with a TokenReview (as its own ServiceAccount) and, on success, hands back
// the token in an AES-GCM sealed httpOnly cookie. The cookie *is* the token, and
// every downstream cluster call re-presents it so cluster RBAC remains the sole
// authority; the only server-side session state is the logout record (sessions).
package auth

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"golang.org/x/time/rate"

	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// Identity is an authenticated user: the raw token (re-presented to the cluster
// on every call) plus the username/groups the cluster attributes to it.
type Identity struct {
	Token    string
	Username string
	Groups   []string
	// Issued is when the session cookie carrying Token was issued; zero when
	// the token came in a bearer header.
	Issued time.Time
	// SSO marks a session a sign-in flow started.
	SSO bool
	// Session keys an OIDC session, whose Token is renewed under it; empty
	// for a session that is its token.
	Session string
	// refresh is set only on the Identity Logout returns, for OIDC.Revoke.
	refresh string
}

// Key is what caches about id are keyed by: its OIDC session, which outlives
// each renewed token, else its token's hash.
func (id Identity) Key() string {
	if id.Session != "" {
		return id.Session
	}
	return restfactory.TokenKey(id.Token)
}

type ctxKey struct{}

// NewContext returns ctx carrying id.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext extracts the Identity injected by Middleware.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Authenticator validates bearer tokens via the Kubernetes TokenReview API,
// called as dotvirt's own ServiceAccount (TokenReview is a cluster operation, not
// the user's). Results are cached briefly by token hash so a request burst
// doesn't issue a TokenReview each time.
type Authenticator struct {
	saKube   kubernetes.Interface
	sealer   cipher.AEAD // seals the session cookie (newSealer)
	sessions *sessions
	oidc     *oidcSessions

	cache   *ttlcache.Cache[cachedIdentity] // keyed by sha256(token)
	reviews *rate.Limiter                   // TokenReview budget for uncached tokens (reviewRate)
}

type cachedIdentity struct {
	id  Identity
	err bool // negative cache: token was rejected
}

// authCacheTTL is how long a validation result (positive or negative) is reused.
// Short so a revoked token is rejected promptly.
const authCacheTTL = time.Minute

// New builds an Authenticator. saKube is the SA-identity kube client (from
// cluster.Factory.SAKube). secret keys the session cookie.
func New(saKube kubernetes.Interface, secret []byte) *Authenticator {
	return &Authenticator{
		saKube:   saKube,
		sealer:   newSealer(secret),
		sessions: newSessions(),
		oidc:     newOIDCSessions(),
		cache:    ttlcache.New[cachedIdentity](authCacheTTL),
		reviews:  rate.NewLimiter(reviewRate, reviewBurst),
	}
}

// SessionLive reports whether the session issued at issued may still
// authenticate: its token not rejected by the last TokenReview, the session
// not signed out, and not past SessionExpiry. key is its Identity's Key. A
// zero issued (a bearer header) has no session to end, only a token to reject.
func (a *Authenticator) SessionLive(key string, issued time.Time) bool {
	if strings.HasPrefix(key, oidcKeyPrefix) {
		return a.oidc.live(key)
	}
	tokenKey := key
	if c, ok := a.cache.Get(tokenKey); ok && c.err {
		return false
	}
	return a.sessions.live(tokenKey, issued)
}

// reviewRate bounds the TokenReviews this process opens for tokens it has not
// seen. The login route and any bearer header reach that path before any
// authentication, so without a bound a token spray turns dotvirt into an
// apiserver amplifier. Cached tokens (every live session) never queue behind it,
// and the budget sits far above real sign-in traffic.
const (
	reviewRate  rate.Limit = 20
	reviewBurst int        = 40
)

// ErrThrottled reports a TokenReview refused by the process-wide budget. Callers
// map it to 429 with a retry hint: not 401 (nothing is known about the token) and
// not 503 (the apiserver is fine).
var ErrThrottled = errors.New("token validation throttled")

// ErrRejected wraps a definitive authentication failure (the token is empty or the
// cluster says it's invalid), as opposed to a transient inability to validate (e.g.
// the API server is unreachable). Callers map ErrRejected -> 401 and everything else
// -> 503, so an API-server blip doesn't sign valid users out.
var ErrRejected = errors.New("token rejected")

// ErrSessionEnded rejects one session (signed out, or past its lifetime), not
// its token: other sessions of the token stand.
var ErrSessionEnded = fmt.Errorf("%w: session ended", ErrRejected)

// Recheck re-runs the TokenReview for id's token after the apiserver refused
// it where the cache vouched for it, and caches the new verdict; a cached
// rejection answers at once. An OIDC session's current token is the one
// checked. It returns Validate's error: ErrRejected only when the token, or
// the session, is dead.
func (a *Authenticator) Recheck(ctx context.Context, id Identity) error {
	token := id.Token
	if id.Session != "" {
		t, err := a.oidc.current(ctx, id.Session)
		if err != nil {
			return err
		}
		token = t
	}
	key := restfactory.TokenKey(token)
	if c, ok := a.cache.Get(key); ok && c.err {
		a.endRejected(id.Session)
		return ErrRejected
	}
	a.cache.Delete(key)
	_, err := a.Validate(ctx, token)
	if errors.Is(err, ErrRejected) {
		a.endRejected(id.Session)
	}
	return err
}

// endRejected ends the OIDC session under key, if any, whose token the
// cluster rejected: no renewal makes it valid again.
func (a *Authenticator) endRejected(key string) {
	if key != "" {
		a.oidc.end(key)
	}
}

// Validate checks token via TokenReview and returns the attributed Identity.
// Cached by token hash (positive and negative) for ttl; an uncached token
// spends the review budget.
func (a *Authenticator) Validate(ctx context.Context, token string) (Identity, error) {
	return a.validate(ctx, token, restfactory.TokenKey(token), true)
}

// Revalidate re-checks a session an open stream already presented and returns
// its Identity as of now: an OIDC session's carries its current token. An
// ended session (signed out, past its lifetime) is rejected outright, else the
// cached verdict when there is one, else a review outside the budget unknown
// tokens spend, so streams re-checking cannot starve sign-ins and a re-check
// is never refused into a pass. Its load is bounded by the open sessions: one
// review per token per authCacheTTL. A transient failure is returned as such,
// never as a rejection.
func (a *Authenticator) Revalidate(ctx context.Context, id Identity) (Identity, error) {
	if id.Session != "" {
		token, err := a.oidc.current(ctx, id.Session)
		if err != nil {
			return id, err
		}
		cur, err := a.validate(ctx, token, restfactory.TokenKey(token), false)
		if err != nil {
			if errors.Is(err, ErrRejected) {
				a.endRejected(id.Session)
			}
			return id, err
		}
		cur.Issued, cur.SSO, cur.Session = id.Issued, id.SSO, id.Session
		return cur, nil
	}
	key := restfactory.TokenKey(id.Token)
	if !a.sessions.live(key, id.Issued) {
		return id, ErrSessionEnded
	}
	_, err := a.validate(ctx, id.Token, key, false)
	return id, err
}

// validate is Validate for a token whose key the caller already holds, with the
// budget optional. Concurrent misses on one token share one TokenReview.
func (a *Authenticator) validate(ctx context.Context, token, key string, budgeted bool) (Identity, error) {
	if token == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrRejected)
	}

	if c, ok := a.cache.Get(key); ok {
		if c.err {
			return Identity{}, ErrRejected
		}
		return c.id, nil
	}

	if budgeted && !a.reviews.Allow() {
		return Identity{}, ErrThrottled
	}
	c, err := a.cache.Do(ctx, key, func(ctx context.Context) (cachedIdentity, error) {
		return a.review(ctx, key, token)
	})
	return c.id, err
}

// review runs one TokenReview and caches its verdict.
func (a *Authenticator) review(ctx context.Context, key, token string) (cachedIdentity, error) {
	review := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}
	res, err := a.saKube.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		// A transient API error is NOT a rejection; don't poison the cache and don't
		// wrap ErrRejected, so the caller returns 503 rather than signing the user out.
		return cachedIdentity{}, fmt.Errorf("token review: %w", err)
	}
	if !res.Status.Authenticated {
		a.cache.Put(key, cachedIdentity{err: true})
		reason := res.Status.Error
		if reason == "" {
			reason = "not authenticated"
		}
		return cachedIdentity{}, fmt.Errorf("%w: %s", ErrRejected, reason)
	}
	c := cachedIdentity{id: Identity{
		Token:    token,
		Username: res.Status.User.Username,
		Groups:   res.Status.User.Groups,
	}}
	a.cache.Put(key, c)
	return c, nil
}
