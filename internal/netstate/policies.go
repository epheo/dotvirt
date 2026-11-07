package netstate

import (
	"maps"
	"slices"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// Policies renders the policy plane from the watch-fed stores - the same pure
// in-memory scan Catalog does for port groups: the DFW tiers (NetworkPolicy,
// AdminNetworkPolicy/Baseline), the per-project Gateway Firewall (EgressFirewall)
// and the Tier-0 planes (EgressIP, external routes). Rules come out as display
// summaries, not spec mirrors: the view reads them, edits go through git. The
// handler scopes namespace-tier rows to visible namespaces and gates the
// cluster-tier rows on authoring authority.
func (s *Snapshot) Policies() []model.Policy {
	out := []model.Policy{}
	for _, src := range []struct {
		idx cache.Indexer
		dec decoder
	}{
		{s.netpol, decodeNetpol},
		{s.anp, decodeANP},
		{s.banp, decodeBANP},
		{s.egressfw, decodeGateway},
		{s.egressip, decodeEgressIP},
		{s.extroute, decodeExtRoute},
	} {
		for _, p := range decoded(src.idx, src.dec) {
			out = append(out, p.view)
		}
	}
	// One deterministic order for every consumer: tier (kind), then ANP
	// precedence, then identity - so the view needs no re-sort and repaints are
	// stable across watch churn.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return kindRank(a.Kind) < kindRank(b.Kind)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

func kindRank(k model.PolicyKind) int {
	switch k {
	case model.PolicyAdmin:
		return 0
	case model.PolicyBaseline:
		return 1
	case model.PolicyDFW:
		return 2
	case model.PolicyGateway:
		return 3
	case model.PolicyEgressIP:
		return 4
	case model.PolicyRoute:
		return 5
	}
	return 6
}

// selectorSummary renders a LabelSelector for humans: "k=v, k2=v2" plus
// "key op (values)" expressions. The name-In expression netgen writes for
// namespace pickers collapses to the bare namespace list. Empty selector = "".
// red, for a namespace selector, replaces the values it hides with
// otherNamespaces; nil renders everything.
func selectorSummary(sel *metav1.LabelSelector, red *Redaction) string {
	if sel == nil {
		return ""
	}
	var parts []string
	add := func(part string) {
		if part != otherNamespaces || !slices.Contains(parts, otherNamespaces) {
			parts = append(parts, part)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(sel.MatchLabels)) {
		if red.hides(k, sel.MatchLabels[k]) {
			add(otherNamespaces)
			continue
		}
		add(k + "=" + sel.MatchLabels[k])
	}
	for _, e := range sel.MatchExpressions {
		values := red.filter(e.Key, e.Values)
		if _, ok := netgen.NameIn(e); ok {
			add(strings.Join(values, ", "))
			continue
		}
		s := e.Key + " " + strings.ToLower(string(e.Operator))
		if len(values) > 0 {
			s += " (" + strings.Join(values, ", ") + ")"
		}
		add(s)
	}
	return strings.Join(parts, ", ")
}

func orAny(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// prefixed returns prefix+s, or "" when s is empty - for optional summary parts.
func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
