package clusterstate

import (
	"encoding/json"
	"hash/maphash"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

// rbacSignal is the sink of one signal-only RBAC watch: dotvirt never reads
// these objects, only learns when one can change what a token may see or do.
// It keeps a hashed key and a grant per tracked object, never the object, so
// it publishes only when a relevant object's grant moves, including the update
// or delete that ends its relevance (a revocation), and a relist that changed
// nothing stays silent. Only its reflector writes it, so seen takes no lock.
type rbacSignal struct {
	// digest reads obj's grant; tracked=false is churn no caller's
	// authorization can see. A false positive only costs a cache
	// invalidation; what it drops waits for the TTL backstop.
	digest   func(obj any) (g grant, tracked bool)
	onChange func()
	refs     *roleRefs // counts the ClusterRoles tracked objects bind; nil for kinds that bind none
	seen     map[uint64]grant
	listed   atomic.Bool // a list has landed; read by State.RBACWatched
}

// grant is what a signal keeps of a tracked object: a fingerprint of what it
// grants, the ClusterRole it binds, and whether it is quiet: tracked only to
// hold that role, as a binding in a namespace that is not a project yet, so
// the role signals the moment the namespace becomes one. A digest reports
// quiet for an untracked object too, so a stored quiet flag is always judged
// against the namespace as it is now.
type grant struct {
	fp    uint64
	role  string
	quiet bool
}

// moves reports whether replacing old (when had) with cur (when tracked)
// publishes: either side counts while its namespace is a project now, cur's
// quiet flag saying so for both, since one object never changes namespace.
func moves(old grant, had bool, cur grant, tracked bool) bool {
	return (had && (!old.quiet || !cur.quiet)) || (tracked && !cur.quiet)
}

func newRBACSignal(onChange func(), refs *roleRefs, digest func(any) (grant, bool)) *rbacSignal {
	return &rbacSignal{digest: digest, onChange: onChange, refs: refs, seen: map[uint64]grant{}}
}

// fingerprintSeed keys fingerprints and object keys, which are compared
// within one process only.
var fingerprintSeed = maphash.MakeSeed()

func (s *rbacSignal) Add(obj any) error    { return s.observe(obj) }
func (s *rbacSignal) Update(obj any) error { return s.observe(obj) }

func (s *rbacSignal) observe(obj any) error {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		s.onChange() // fail-safe: an unreadable event still invalidates
		return nil
	}
	k := maphash.String(fingerprintSeed, key)
	g, tracked := s.digest(obj)
	old, had := s.seen[k]
	if !tracked && !had || tracked && had && g == old {
		return nil
	}
	// Hold the new role before releasing the old: a binding moving within
	// one role must never read unbound to the ClusterRole sink.
	if tracked {
		s.refs.hold(g.role)
		s.seen[k] = g
	} else {
		delete(s.seen, k)
	}
	if had {
		s.refs.release(old.role)
	}
	if moves(old, had, g, tracked) {
		s.onChange()
	}
	return nil
}

func (s *rbacSignal) Delete(obj any) error {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		s.onChange()
		return nil
	}
	k := maphash.String(fingerprintSeed, key)
	old, had := s.seen[k]
	if had {
		delete(s.seen, k)
		s.refs.release(old.role)
	}
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	// An object never seen as relevant can be now: its namespace became a
	// project, or a binding came to reference it, after it was last observed.
	if g, tracked := s.digest(obj); moves(old, had, g, tracked) {
		s.onChange()
	}
	return nil
}

// Replace publishes on the first list whatever it holds: a verdict cached
// before this watch began is not covered by it. Later, only a move does.
func (s *rbacSignal) Replace(list []any, _ string) error {
	next := make(map[uint64]grant, len(s.seen))
	changed := !s.listed.Load()
	for _, obj := range list {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			changed = true
			continue
		}
		if g, tracked := s.digest(obj); tracked {
			next[maphash.String(fingerprintSeed, key)] = g
		}
	}
	s.listed.Store(true)
	if maps.Equal(s.seen, next) {
		if changed {
			s.onChange()
		}
		return nil
	}
	changed = changed || relistMoves(s.seen, next)
	for _, g := range next {
		s.refs.hold(g.role)
	}
	for _, g := range s.seen {
		s.refs.release(g.role)
	}
	s.seen = next
	if changed {
		s.onChange()
	}
	return nil
}

// relistMoves reports whether a relist moved a grant. An entry the relist no
// longer holds counts even if it was quiet: only its hashed key remains, so
// whether its namespace is a project now cannot be told, and relists are rare.
func relistMoves(seen, next map[uint64]grant) bool {
	for k, g := range next {
		if o, had := seen[k]; o != g && moves(o, had, g, true) {
			return true
		}
	}
	for k := range seen {
		if _, ok := next[k]; !ok {
			return true
		}
	}
	return false
}

func (s *rbacSignal) List() []any                        { return nil }
func (s *rbacSignal) ListKeys() []string                 { return nil }
func (s *rbacSignal) Get(any) (any, bool, error)         { return nil, false, nil }
func (s *rbacSignal) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (s *rbacSignal) Resync() error                      { return nil }

// roleRefs counts the tracked bindings that bind each ClusterRole. A
// ClusterRole moves a token's grants only through a binding, so its signal
// fires only while one binds it: the roles an operator install or upgrade
// rewrites stay silent while only platform ServiceAccounts are bound to them,
// as for an operator living in an openshift-* or kube-* namespace. An
// aggregated role (admin, edit, view) is bound like any other, and the
// aggregation controller rewrites it when a source role moves. The binding sinks write it from their own
// reflectors, so it locks. Nil-safe: a kind that binds nothing holds none.
type roleRefs struct {
	mu sync.Mutex
	n  map[string]int
}

func newRoleRefs() *roleRefs { return &roleRefs{n: map[string]int{}} }

func (r *roleRefs) hold(role string) {
	if r == nil || role == "" {
		return
	}
	r.mu.Lock()
	r.n[role]++
	r.mu.Unlock()
}

func (r *roleRefs) release(role string) {
	if r == nil || role == "" {
		return
	}
	r.mu.Lock()
	if r.n[role]--; r.n[role] <= 0 {
		delete(r.n, role)
	}
	r.mu.Unlock()
}

func (r *roleRefs) bound(role string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[role] > 0
}

// fingerprint hashes the parts of an object that grant, never its metadata,
// so an annotation or ownership write moves nothing.
func fingerprint(parts ...any) uint64 {
	var h maphash.Hash
	h.SetSeed(fingerprintSeed)
	for _, p := range parts {
		b, _ := json.Marshal(p) // maps marshal with sorted keys: stable
		_, _ = h.Write(b)
		_ = h.WriteByte(0)
	}
	return h.Sum64()
}

// The per-kind digests. Each reads an object of a type other than its own as
// relevant: a fail-safe that cannot happen behind a typed reflector.
//
// A RoleBinding or Role matters only in a project namespace (inProject):
// visibility is computed over project namespaces only, and cluster-wide churn
// (operators, CI) would otherwise flush every token's cache. One that lands
// before its namespace is labelled is still covered: the label change fires
// NamespaceChanged, which the RBAC version sums in.

// A RoleBinding outside a project is tracked quiet: it publishes nothing, yet
// holds its ClusterRole, so once the namespace becomes a project (which
// NamespaceChanged covers) that role's edits publish.
func roleBindingDigest(inProject func(string) bool) func(any) (grant, bool) {
	return func(obj any) (grant, bool) {
		rb, ok := obj.(*rbacv1.RoleBinding)
		if !ok {
			return grant{}, true
		}
		g, tracked := bindingDigest(rb.RoleRef, rb.Subjects)
		g.quiet = !inProject(rb.Namespace)
		return g, tracked
	}
}

func clusterRoleBindingDigest(obj any) (grant, bool) {
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		return grant{}, true
	}
	return bindingDigest(crb.RoleRef, crb.Subjects)
}

// bindingDigest drops bindings that grant only ServiceAccounts of the
// platform's own namespaces, one by one or as their namespace's group: the
// cluster's components, not dotvirt callers, and the bulk of binding churn on
// an operator install or upgrade.
func bindingDigest(ref rbacv1.RoleRef, subjects []rbacv1.Subject) (grant, bool) {
	for _, sub := range subjects {
		if !platformSubject(sub) {
			g := grant{fp: fingerprint(ref, subjects)}
			if ref.Kind == "ClusterRole" {
				g.role = ref.Name
			}
			return g, true
		}
	}
	return grant{}, false
}

func roleDigest(inProject func(string) bool) func(any) (grant, bool) {
	return func(obj any) (grant, bool) {
		r, ok := obj.(*rbacv1.Role)
		if !ok {
			return grant{}, true
		}
		if !inProject(r.Namespace) {
			return grant{}, false
		}
		return grant{fp: fingerprint(r.Rules)}, true
	}
}

// clusterRoleDigest keeps only the roles a relevant binding binds (roleRefs).
func clusterRoleDigest(refs *roleRefs) func(any) (grant, bool) {
	return func(obj any) (grant, bool) {
		cr, ok := obj.(*rbacv1.ClusterRole)
		if !ok {
			return grant{}, true
		}
		if !refs.bound(cr.Name) {
			return grant{}, false
		}
		return grant{fp: fingerprint(cr.Rules)}, true
	}
}

// groupDigest reads OpenShift Groups through the dynamic client: dotvirt
// carries no typed OpenShift API.
func groupDigest(obj any) (grant, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return grant{}, true
	}
	return grant{fp: fingerprint(u.Object["users"])}, true
}

func platformSubject(sub rbacv1.Subject) bool {
	switch sub.Kind {
	case rbacv1.ServiceAccountKind:
		return platformNamespace(sub.Namespace)
	case rbacv1.GroupKind:
		ns, ok := strings.CutPrefix(sub.Name, "system:serviceaccounts:")
		return ok && platformNamespace(ns)
	}
	return false
}

// platformNamespace names the cluster's own namespaces.
func platformNamespace(ns string) bool {
	return ns == "openshift" || strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-")
}
