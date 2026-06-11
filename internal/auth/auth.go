// Package auth authenticates dotvirt users by their Kubernetes bearer token.
// A user pastes a token (oc whoami -t / kubectl create token); dotvirt validates
// it with a TokenReview (as its own ServiceAccount) and, on success, hands back
// the token in a signed httpOnly cookie. There is no server-side session store:
// the cookie *is* the token, and every downstream cluster call re-presents it so
// cluster RBAC remains the sole authority.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// Identity is an authenticated user: the raw token (re-presented to the cluster
// on every call) plus the username/groups the cluster attributes to it.
type Identity struct {
	Token    string
	Username string
	Groups   []string
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
	saKube kubernetes.Interface
	secret []byte

	cache *ttlcache.Cache[cachedIdentity] // keyed by sha256(token)
}

type cachedIdentity struct {
	id  Identity
	err bool // negative cache: token was rejected
}

// authCacheTTL is how long a validation result (positive or negative) is reused.
// Short so a revoked token is rejected promptly.
const authCacheTTL = time.Minute

// New builds an Authenticator. saKube is the SA-identity kube client (from
// cluster.Factory.SAKube). secret signs the session cookie.
func New(saKube kubernetes.Interface, secret []byte) *Authenticator {
	return &Authenticator{
		saKube: saKube,
		secret: secret,
		cache:  ttlcache.New[cachedIdentity](authCacheTTL),
	}
}

// ErrRejected wraps a definitive authentication failure (the token is empty or the
// cluster says it's invalid), as opposed to a transient inability to validate (e.g.
// the API server is unreachable). Callers map ErrRejected → 401 and everything else
// → 503, so an API-server blip doesn't sign valid users out.
var ErrRejected = errors.New("token rejected")

// Validate checks token via TokenReview and returns the attributed Identity.
// Cached by token hash (positive and negative) for ttl.
func (a *Authenticator) Validate(ctx context.Context, token string) (Identity, error) {
	if token == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrRejected)
	}
	key := restfactory.TokenKey(token)

	if c, ok := a.cache.Get(key); ok {
		if c.err {
			return Identity{}, ErrRejected
		}
		return c.id, nil
	}

	review := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}
	res, err := a.saKube.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		// A transient API error is NOT a rejection; don't poison the cache and don't
		// wrap ErrRejected, so the caller returns 503 rather than signing the user out.
		return Identity{}, fmt.Errorf("token review: %w", err)
	}
	if !res.Status.Authenticated {
		a.cache.Put(key, cachedIdentity{err: true})
		reason := res.Status.Error
		if reason == "" {
			reason = "not authenticated"
		}
		return Identity{}, fmt.Errorf("%w: %s", ErrRejected, reason)
	}

	id := Identity{
		Token:    token,
		Username: res.Status.User.Username,
		Groups:   res.Status.User.Groups,
	}
	a.cache.Put(key, cachedIdentity{id: id})
	return id, nil
}
