package tenancy

import (
	"fmt"
	"slices"
	"strings"
)

// Policy is what a tenant repo may declare: the listed kinds (the zero
// value), or, under Any, every namespaced kind no Denied entry covers. The
// operator decides it and the AppProjects enforce it; dotvirt reads the same
// answer so it stages nothing they would refuse.
type Policy struct {
	Any    bool
	Denied []GroupKind
}

// AnyPolicy is every namespaced kind but DeniedTenantKinds(extra...), the list
// the operator puts on each tenant AppProject.
func AnyPolicy(extra ...GroupKind) Policy {
	return Policy{Any: true, Denied: DeniedTenantKinds(extra...)}
}

// Declarable reports whether a tenant repo may declare group/kind.
// clusterScoped matters under Any only: a tenant AppProject admits no
// cluster-scoped kind, while the listed kinds are decided by name.
func (p Policy) Declarable(group, kind string, clusterScoped bool) bool {
	if !p.Any {
		return TenantKind(kind)
	}
	return !clusterScoped && !slices.ContainsFunc(p.Denied, func(g GroupKind) bool { return g.Covers(group, kind) })
}

// FormatGroupKinds spells gks as "group/kind" entries joined by commas, the
// core group's as "/kind": the form the operator hands dotvirt.
func FormatGroupKinds(gks []GroupKind) string {
	parts := make([]string, len(gks))
	for i, g := range gks {
		parts[i] = g.Group + "/" + g.Kind
	}
	return strings.Join(parts, ",")
}

// ParseGroupKinds reads FormatGroupKinds' form; "" is none.
func ParseGroupKinds(s string) ([]GroupKind, error) {
	var out []GroupKind
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		group, kind, ok := strings.Cut(part, "/")
		if !ok || kind == "" || strings.Contains(kind, "/") {
			return nil, fmt.Errorf("%q is not group/kind (the core group is /kind)", part)
		}
		out = append(out, GroupKind{group, kind})
	}
	return out, nil
}
