package tenancy

import (
	"fmt"
	"slices"
	"strings"
)

// Policy is what a tenant repo may declare beyond the listed kinds: the kinds
// a provider allowed (AllowTenantKinds), none in the zero value. The operator
// decides it and the AppProjects enforce it; dotvirt reads the same answer so
// it stages nothing they would refuse.
type Policy struct {
	Allowed []GroupKind
}

// Allows reports whether the provider allowed group/kind. A tenant AppProject
// admits no cluster-scoped kind, so its caller still weighs the scope.
func (p Policy) Allows(group, kind string) bool {
	return slices.Contains(p.Allowed, GroupKind{group, kind})
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
