package netstate

import (
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// Redaction is what a cluster policy's text may tell one caller: only what
// lies in the namespaces they see. A namespace selector renders in clear only
// when it selects some of their namespaces, can match none outside their
// view, and names no label value (or absent key) that none of their
// namespaces carries; otherwise it reads as the caller's own namespaces it
// selects, then otherNamespaces. A selector matching none of theirs speaks
// only of others, even one matching nothing at all. Pod labels render only
// beside a namespace side so confined, node labels only to a caller who may
// read nodes. Actions, ports and directions render as they are, and matching
// never reads text, so verdicts are untouched.
//
// One Redaction serves one request: it memoizes per selector without locking.
type Redaction struct {
	ProjectLabel string
	Namespace    func(name string) bool // the caller sees this namespace
	Project      func(name string) bool // the caller sees this project
	// Namespaces lists every namespace in the cluster. Nil confines only the
	// selectors that name their namespaces, each one the caller sees.
	Namespaces func() []NamespaceLabels
	Nodes      func() bool // the caller may read nodes; nil: they may not

	memo map[string]selection
	idx  *namespaceIndex
}

// NamespaceLabels is one namespace as the snapshot holds it. Labels is nil
// where only the name is held, which is never so for a namespace carrying the
// project label.
type NamespaceLabels struct {
	Name   string
	Labels map[string]string
}

const (
	otherNamespaces = "other namespaces"
	// selectedLabels stands in for pod or node labels the caller may not read.
	selectedLabels = "selected"
)

// selection is one namespace selector as the caller may read it.
type selection struct {
	confined bool   // it may render in clear (see Redaction)
	own      string // the caller's namespaces it selects, then otherNamespaces
}

// confined reports whether a namespace selector may render in clear for the
// caller (see Redaction). A nil Redaction confines everything.
func (r *Redaction) confined(sel selector) bool {
	return r == nil || r.read(sel).confined
}

// hidesLabels reports whether the pod labels beside namespace side ns must
// not reach the caller. A nil side is a NetworkPolicy's own namespace, which
// only a caller who sees it reads.
func (r *Redaction) hidesLabels(pod, ns selector) bool {
	return r != nil && ns.raw != nil && !emptySelector(pod.raw) && !r.confined(ns)
}

// hidesNodes reports whether a node selector's labels must not reach the
// caller: hosts lie in no namespace of theirs.
func (r *Redaction) hidesNodes(nodes selector) bool {
	return r != nil && !emptySelector(nodes.raw) && (r.Nodes == nil || !r.Nodes())
}

func (r *Redaction) read(s selector) selection {
	key := s.key
	if v, ok := r.memo[key]; ok {
		return v
	}
	sel := s.raw
	if sel == nil {
		sel = &metav1.LabelSelector{}
	}
	v := selection{own: otherNamespaces}
	if r.Namespaces == nil {
		v.confined = r.namedOnly(sel)
	} else {
		x := r.index()
		var own []string
		for _, ns := range x.visible {
			if r.selects(sel, ns) == matchYes {
				own = append(own, ns.Name)
			}
		}
		v.confined = len(own) > 0 && x.carries(sel) && !r.reachesOutside(sel, x)
		if !v.confined {
			v.own = strings.Join(append(own, otherNamespaces), ", ")
		}
	}
	// An invalid selector formats to one shared key, and one not built by
	// parseSelector has none: never memoized.
	if key != "<error>" && key != "" {
		if r.memo == nil {
			r.memo = map[string]selection{}
		}
		r.memo[key] = v
	}
	return v
}

// namedOnly reports whether every requirement of sel names namespaces, at
// least one and each one the caller sees.
func (r *Redaction) namedOnly(sel *metav1.LabelSelector) bool {
	if len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return false
	}
	for k, v := range sel.MatchLabels {
		if k != netgen.NamespaceNameLabel || !r.Namespace(v) {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		vals, ok := netgen.NameIn(e)
		if !ok || len(vals) == 0 || slices.ContainsFunc(vals, func(v string) bool { return !r.Namespace(v) }) {
			return false
		}
	}
	return true
}

// namespaceIndex is the namespace list split for one caller, built once per
// Redaction.
type namespaceIndex struct {
	visible   []NamespaceLabels            // the caller's, in name order
	outside   []NamespaceLabels            // outside their view, labels held
	byProject map[string][]NamespaceLabels // outside, by project label value
	blind     bool                         // some namespace outside has no labels held
	carried   map[string]map[string]bool   // label key: the values the caller's namespaces carry
}

func (r *Redaction) index() *namespaceIndex {
	if r.idx != nil {
		return r.idx
	}
	x := &namespaceIndex{byProject: map[string][]NamespaceLabels{}, carried: map[string]map[string]bool{}}
	carry := func(k, v string) {
		if x.carried[k] == nil {
			x.carried[k] = map[string]bool{}
		}
		x.carried[k][v] = true
	}
	for _, ns := range r.Namespaces() {
		switch {
		case r.Namespace(ns.Name):
			x.visible = append(x.visible, ns)
			carry(netgen.NamespaceNameLabel, ns.Name)
			for k, v := range ns.Labels {
				carry(k, v)
			}
		case ns.Labels == nil:
			x.blind = true
		default:
			x.outside = append(x.outside, ns)
			if v, ok := ns.Labels[r.ProjectLabel]; ok && r.ProjectLabel != "" {
				x.byProject[v] = append(x.byProject[v], ns)
			}
		}
	}
	slices.SortFunc(x.visible, func(a, b NamespaceLabels) int { return strings.Compare(a.Name, b.Name) })
	r.idx = x
	return x
}

// carries reports whether every label value sel names, and every key it
// requires absent, is one the caller's namespaces carry.
func (x *namespaceIndex) carries(sel *metav1.LabelSelector) bool {
	for k, v := range sel.MatchLabels {
		if !x.carried[k][v] {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		switch e.Operator {
		case metav1.LabelSelectorOpIn, metav1.LabelSelectorOpNotIn:
			for _, v := range e.Values {
				if !x.carried[e.Key][v] {
					return false
				}
			}
		case metav1.LabelSelectorOpDoesNotExist:
			if x.carried[e.Key] == nil {
				return false
			}
		}
	}
	return true
}

// reachesOutside reports whether sel, which carries only the caller's
// values, may match a namespace outside their view. Its namespace names are
// then all the caller's, and a project requirement leaves only that
// project's namespaces to test; a namespace whose labels are not held carries
// no project label, and may match any other selector.
func (r *Redaction) reachesOutside(sel *metav1.LabelSelector, x *namespaceIndex) bool {
	if _, named := sel.MatchLabels[netgen.NamespaceNameLabel]; named {
		return false
	}
	candidates := x.outside
	anchored := false
	if v, ok := sel.MatchLabels[r.ProjectLabel]; ok && r.ProjectLabel != "" {
		candidates, anchored = x.byProject[v], true
	}
	for _, e := range sel.MatchExpressions {
		if _, named := netgen.NameIn(e); named {
			return false
		}
		if anchored || r.ProjectLabel == "" || e.Key != r.ProjectLabel {
			continue
		}
		switch e.Operator {
		case metav1.LabelSelectorOpIn:
			candidates, anchored = nil, true
			for _, v := range e.Values {
				candidates = append(candidates, x.byProject[v]...)
			}
		case metav1.LabelSelectorOpExists:
			anchored = true
		}
	}
	if !anchored && x.blind {
		return true
	}
	return slices.ContainsFunc(candidates, func(ns NamespaceLabels) bool { return r.selects(sel, ns) != matchNo })
}

// selects evaluates sel against one namespace from what the snapshot holds
// of it: matchCond when sel reads a label the snapshot does not hold there.
// Every namespace carries its name label, and only a namespace whose labels
// are held carries the project label.
func (r *Redaction) selects(sel *metav1.LabelSelector, ns NamespaceLabels) match {
	lookup := func(key string) (value string, present, known bool) {
		switch {
		case key == netgen.NamespaceNameLabel:
			return ns.Name, true, true
		case ns.Labels != nil:
			value, present = ns.Labels[key]
			return value, present, true
		case r.ProjectLabel != "" && key == r.ProjectLabel:
			return "", false, true
		}
		return "", false, false
	}
	out := matchYes
	for k, want := range sel.MatchLabels {
		v, present, known := lookup(k)
		switch {
		case !known:
			out = matchCond
		case !present || v != want:
			return matchNo
		}
	}
	for _, e := range sel.MatchExpressions {
		v, present, known := lookup(e.Key)
		if !known {
			out = matchCond
			continue
		}
		var ok bool
		switch e.Operator {
		case metav1.LabelSelectorOpIn:
			ok = present && slices.Contains(e.Values, v)
		case metav1.LabelSelectorOpNotIn:
			ok = !present || !slices.Contains(e.Values, v)
		case metav1.LabelSelectorOpExists:
			ok = present
		case metav1.LabelSelectorOpDoesNotExist:
			ok = !present
		default:
			out, ok = matchCond, true
		}
		if !ok {
			return matchNo
		}
	}
	return out
}

// hides reports whether the namespace-selector label key=value names a
// namespace or project outside the caller's view. A nil Redaction hides
// nothing.
func (r *Redaction) hides(key, value string) bool {
	switch {
	case r == nil:
		return false
	case key == netgen.NamespaceNameLabel:
		return !r.Namespace(value)
	case r.ProjectLabel != "" && key == r.ProjectLabel:
		return !r.Project(value)
	}
	return false
}

// filter keeps the values of key the caller may read, standing one
// otherNamespaces in for those it may not.
func (r *Redaction) filter(key string, values []string) []string {
	out := make([]string, 0, len(values))
	hidden := false
	for _, v := range values {
		if r.hides(key, v) {
			hidden = true
			continue
		}
		out = append(out, v)
	}
	if hidden {
		out = append(out, otherNamespaces)
	}
	return out
}

// Redact re-renders a cluster-scoped policy's target and rule rows as red lets
// the caller read them, and rule too when it is one of those rows (a trace
// step's). A policy or row the snapshot no longer holds hides its target and
// peers whole.
func (s *Snapshot) Redact(p *model.Policy, rule *model.PolicyRuleView, red *Redaction) {
	pol := s.clusterPolicy(p.Backing, p.Name)
	if pol == nil {
		hideAll(p, rule)
		return
	}
	target, applies, rows := pol.render(red)
	p.Target = target
	if p.Applies != "" {
		p.Applies = applies
	}
	if p.Rules != nil {
		p.Rules = rows
	}
	if rule != nil {
		if i := slices.Index(pol.view.Rules, *rule); i >= 0 {
			*rule = rows[i]
		} else {
			hideAll(nil, rule)
		}
	}
}

func hideAll(p *model.Policy, rule *model.PolicyRuleView) {
	if p != nil {
		if p.Target != "" {
			p.Target = otherNamespaces
		}
		if p.Applies != "" {
			p.Applies = otherNamespaces
		}
		p.Rules = slices.Clone(p.Rules) // the rows are the snapshot's
		for i := range p.Rules {
			hideAll(nil, &p.Rules[i])
		}
	}
	if rule != nil && rule.Peer != "" {
		rule.Peer = otherNamespaces
		rule.Text = "" // the sentence names the peers it can no longer show
	}
}

// clusterPolicy is the cluster-scoped policy backing/name as the snapshot
// holds it; nil when it holds none.
func (s *Snapshot) clusterPolicy(backing, name string) *policy {
	var st *policyStore
	switch backing {
	case model.KindANP.Kind:
		st = s.anps
	case model.KindBANP.Kind:
		st = s.banps
	case model.KindEgressIP.Kind:
		st = s.egressIPs
	case model.KindExtRoute.Kind:
		st = s.extRoutes
	default:
		return nil
	}
	return st.plane().byKey[name]
}
