package netstate

import (
	"slices"

	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/reflect"
)

// Redaction is what a cluster policy's text must not tell one caller: a
// namespace name, or a project label value, outside their view. A hidden value
// renders as otherNamespaces. Actions, ports, directions and every other label
// render as they are, and matching never reads text, so verdicts are untouched.
type Redaction struct {
	ProjectLabel string
	Namespace    func(name string) bool // the caller sees this namespace
	Project      func(name string) bool // the caller sees this project
}

const otherNamespaces = "other namespaces"

// hides reports whether the namespace-selector label key=value must not reach
// the caller. A nil Redaction hides nothing.
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
	target, rows := pol.render(red)
	p.Target = target
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
		for i := range p.Rules {
			hideAll(nil, &p.Rules[i])
		}
	}
	if rule != nil && rule.Peer != "" {
		rule.Peer = otherNamespaces
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
