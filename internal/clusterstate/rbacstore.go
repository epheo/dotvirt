package clusterstate

import (
	"encoding/json"
	"hash/maphash"
	"maps"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

// rbacSignal is the sink of one signal-only RBAC watch: dotvirt never reads
// these objects, only learns when one can change what a token may see or do.
// It keeps one fingerprint per relevant object, never the object, so it
// publishes only when an object's grant moves, including the update or
// delete that ends its relevance (a revocation), and a relist that changed
// nothing stays silent. Only its reflector calls it, so it takes no lock.
type rbacSignal struct {
	// digest fingerprints obj's grant; relevant=false is churn no caller's
	// authorization can see. A false positive only costs a cache
	// invalidation; what it drops waits for the TTL backstop.
	digest   func(obj any) (fp uint64, relevant bool)
	onChange func()
	seen     map[string]uint64 // object key -> fingerprint, relevant objects only
	listed   bool
}

func newRBACSignal(onChange func(), digest func(any) (uint64, bool)) *rbacSignal {
	return &rbacSignal{digest: digest, onChange: onChange, seen: map[string]uint64{}}
}

func (s *rbacSignal) Add(obj any) error    { return s.observe(obj) }
func (s *rbacSignal) Update(obj any) error { return s.observe(obj) }

func (s *rbacSignal) observe(obj any) error {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		s.onChange() // fail-safe: an unreadable event still invalidates
		return nil
	}
	fp, relevant := s.digest(obj)
	old, had := s.seen[key]
	switch {
	case relevant && (!had || old != fp):
		s.seen[key] = fp
	case !relevant && had:
		delete(s.seen, key)
	default:
		return nil
	}
	s.onChange()
	return nil
}

func (s *rbacSignal) Delete(obj any) error {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		s.onChange()
		return nil
	}
	_, had := s.seen[key]
	delete(s.seen, key)
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	// An object never seen as relevant can be now: its namespace became a
	// project after it was last observed.
	if _, relevant := s.digest(obj); had || relevant {
		s.onChange()
	}
	return nil
}

// Replace publishes on the first list whatever it holds: a verdict cached
// before this watch began is not covered by it.
func (s *rbacSignal) Replace(list []any, _ string) error {
	next := make(map[string]uint64, len(s.seen))
	changed := !s.listed
	for _, obj := range list {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			changed = true
			continue
		}
		if fp, relevant := s.digest(obj); relevant {
			next[key] = fp
		}
	}
	changed = changed || !maps.Equal(s.seen, next)
	s.seen, s.listed = next, true
	if changed {
		s.onChange()
	}
	return nil
}

func (s *rbacSignal) List() []any                        { return nil }
func (s *rbacSignal) ListKeys() []string                 { return nil }
func (s *rbacSignal) Get(any) (any, bool, error)         { return nil, false, nil }
func (s *rbacSignal) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (s *rbacSignal) Resync() error                      { return nil }

// fingerprintSeed keys fingerprints, which are compared within one process only.
var fingerprintSeed = maphash.MakeSeed()

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

func roleBindingDigest(inProject func(string) bool) func(any) (uint64, bool) {
	return func(obj any) (uint64, bool) {
		rb, ok := obj.(*rbacv1.RoleBinding)
		if !ok {
			return 0, true
		}
		if !inProject(rb.Namespace) {
			return 0, false
		}
		return bindingDigest(rb.RoleRef, rb.Subjects)
	}
}

func clusterRoleBindingDigest(obj any) (uint64, bool) {
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		return 0, true
	}
	return bindingDigest(crb.RoleRef, crb.Subjects)
}

// bindingDigest drops bindings that grant only ServiceAccounts of the
// platform's own namespaces: the cluster's components, not dotvirt callers,
// and the bulk of binding churn on an operator install or upgrade.
func bindingDigest(ref rbacv1.RoleRef, subjects []rbacv1.Subject) (uint64, bool) {
	for _, sub := range subjects {
		if sub.Kind != rbacv1.ServiceAccountKind || !platformNamespace(sub.Namespace) {
			return fingerprint(ref, subjects), true
		}
	}
	return 0, false
}

func roleDigest(inProject func(string) bool) func(any) (uint64, bool) {
	return func(obj any) (uint64, bool) {
		r, ok := obj.(*rbacv1.Role)
		if !ok {
			return 0, true
		}
		if !inProject(r.Namespace) {
			return 0, false
		}
		return fingerprint(r.Rules), true
	}
}

// clusterRoleDigest drops the system: roles, the control plane's own. An
// aggregated role still signals: aggregation rewrites the target's rules.
func clusterRoleDigest(obj any) (uint64, bool) {
	cr, ok := obj.(*rbacv1.ClusterRole)
	if !ok {
		return 0, true
	}
	if strings.HasPrefix(cr.Name, "system:") {
		return 0, false
	}
	return fingerprint(cr.Rules), true
}

// groupDigest reads OpenShift Groups through the dynamic client: dotvirt
// carries no typed OpenShift API.
func groupDigest(obj any) (uint64, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return 0, true
	}
	return fingerprint(u.Object["users"]), true
}

// platformNamespace names the cluster's own namespaces.
func platformNamespace(ns string) bool {
	return ns == "openshift" || strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-")
}
