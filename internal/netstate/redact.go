package netstate

import (
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/reflect"
)

// Redaction is what a cluster policy's text may tell one caller: only what
// lies in the namespaces they see. A namespace selector renders in clear only
// when it can match no namespace outside their view, and even then hides a
// namespace name or project label value they do not see; otherwise it reads
// as the caller's own namespaces it selects, then otherNamespaces. Pod labels
// render only beside a namespace side so confined, node labels only to a
// caller who may read nodes. Actions, ports and directions render as they
// are, and matching never reads text, so verdicts are untouched.
//
// One Redaction serves one request: it memoizes per selector without locking.
type Redaction struct {
	ProjectLabel string
	Namespace    func(name string) bool // the caller sees this namespace
	Project      func(name string) bool // the caller sees this project
	// Namespaces lists every namespace in the cluster. Nil confines only the
	// selectors that pin their namespaces by name.
	Namespaces func() []NamespaceLabels
	Nodes      func() bool // the caller may read nodes; nil: they may not

	memo map[string]selection
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
	confined bool   // it can match no namespace outside the caller's view
	own      string // the caller's namespaces it selects, then otherNamespaces
}

// confined reports whether a namespace selector can match no namespace
// outside the caller's view. A nil Redaction confines everything.
func (r *Redaction) confined(sel *metav1.LabelSelector) bool {
	return r == nil || r.read(sel).confined
}

// hidesLabels reports whether the pod labels beside namespace side ns must
// not reach the caller. A nil side is a NetworkPolicy's own namespace, which
// only a caller who sees it reads.
func (r *Redaction) hidesLabels(pod, ns *metav1.LabelSelector) bool {
	return r != nil && ns != nil && !emptySelector(pod) && !r.confined(ns)
}

// hidesNodes reports whether a node selector's labels must not reach the
// caller: hosts lie in no namespace of theirs.
func (r *Redaction) hidesNodes(nodes *metav1.LabelSelector) bool {
	return r != nil && !emptySelector(nodes) && (r.Nodes == nil || !r.Nodes())
}

func (r *Redaction) read(sel *metav1.LabelSelector) selection {
	if sel == nil {
		sel = &metav1.LabelSelector{}
	}
	key := metav1.FormatLabelSelector(sel)
	if v, ok := r.memo[key]; ok {
		return v
	}
	v := selection{confined: true}
	switch {
	case r.pinned(sel):
	case r.Namespaces == nil:
		v = selection{own: otherNamespaces}
	default:
		var own []string
		for _, ns := range r.Namespaces() {
			m := r.selects(sel, ns)
			switch {
			case r.Namespace(ns.Name):
				if m == matchYes {
					own = append(own, ns.Name)
				}
			case m != matchNo:
				v.confined = false
			}
		}
		slices.Sort(own)
		v.own = strings.Join(append(own, otherNamespaces), ", ")
	}
	// An invalid selector formats to one shared key: never memoized.
	if key != "<error>" {
		if r.memo == nil {
			r.memo = map[string]selection{}
		}
		r.memo[key] = v
	}
	return v
}

// pinned reports whether sel names its namespaces outright, each one the
// caller sees: whatever else it requires only narrows that set.
func (r *Redaction) pinned(sel *metav1.LabelSelector) bool {
	if v, ok := sel.MatchLabels[netgen.NamespaceNameLabel]; ok && r.Namespace(v) {
		return true
	}
	for _, e := range sel.MatchExpressions {
		if vals, ok := netgen.NameIn(e); ok && !slices.ContainsFunc(vals, func(v string) bool { return !r.Namespace(v) }) {
			return true
		}
	}
	return false
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
		for i := range p.Rules {
			hideAll(nil, &p.Rules[i])
		}
	}
	if rule != nil && rule.Peer != "" {
		rule.Peer = otherNamespaces
		rule.Text = "" // the sentence names the peers it can no longer show
	}
}

// clusterPolicy decodes the cluster-scoped policy backing/name from its store.
func (s *Snapshot) clusterPolicy(backing, name string) *policy {
	var idx cache.Indexer
	var dec decoder
	switch backing {
	case model.KindANP.Kind:
		idx, dec = s.anp, decodeANP
	case model.KindBANP.Kind:
		idx, dec = s.banp, decodeBANP
	case model.KindEgressIP.Kind:
		idx, dec = s.egressip, decodeEgressIP
	case model.KindExtRoute.Kind:
		idx, dec = s.extroute, decodeExtRoute
	default:
		return nil
	}
	u, ok := reflect.Get(idx, name)
	if !ok {
		return nil
	}
	return dec(u)
}
