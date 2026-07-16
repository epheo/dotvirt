package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

// NetworkPolicySpec describes a NetworkPolicy — the east-west Distributed Firewall.
// It protects a Group (AppliedTo: a podSelector) inside one namespace, allowing
// ingress only from the peer Groups named in its rules (a NetworkPolicy that selects
// pods default-denies all other ingress). Namespace-scoped, so it rides the tenant's
// own repo. Groups are label selectors — the same primitive NSX-T's dynamic Groups
// compile to.
type NetworkPolicySpec struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	AppliedTo map[string]string `json:"appliedTo,omitempty"` // podSelector matchLabels; empty = the whole namespace
	Ingress   []PolicyRule      `json:"ingress,omitempty"`
}

// PolicyRule is one ingress rule: allow traffic from the peer Groups (From) to the
// applied-to Group, optionally narrowed to Ports. An empty From means "any source";
// an empty Ports means "any port".
type PolicyRule struct {
	From  []map[string]string `json:"from,omitempty"` // peer Groups (podSelector matchLabels)
	Ports []PolicyPort        `json:"ports,omitempty"`
}

// PolicyPort narrows a rule to a transport port.
type PolicyPort struct {
	Protocol string `json:"protocol"` // TCP | UDP | SCTP
	Port     int    `json:"port"`
}

// NetworkPolicyManifest renders the NetworkPolicy YAML plus its repo-relative path.
func NetworkPolicyManifest(s NetworkPolicySpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("name", s.Name); err != nil {
		return "", nil, err
	}
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	// An empty podSelector ({}) selects every pod in the namespace — the "applied to
	// the whole project" case; otherwise scope to the Group's labels.
	podSelector := map[string]any{}
	if len(s.AppliedTo) > 0 {
		podSelector = map[string]any{"matchLabels": toStrAny(s.AppliedTo)}
	}
	spec := map[string]any{"podSelector": podSelector, "policyTypes": []any{"Ingress"}}
	if len(s.Ingress) > 0 {
		ingress := make([]any, 0, len(s.Ingress))
		for _, r := range s.Ingress {
			rule := map[string]any{}
			if len(r.From) > 0 {
				from := make([]any, 0, len(r.From))
				for _, peer := range r.From {
					from = append(from, map[string]any{"podSelector": map[string]any{"matchLabels": toStrAny(peer)}})
				}
				rule["from"] = from
			}
			if len(r.Ports) > 0 {
				ports := make([]any, 0, len(r.Ports))
				for i, p := range r.Ports {
					if p.Protocol != "TCP" && p.Protocol != "UDP" && p.Protocol != "SCTP" {
						return "", nil, fmt.Errorf("rule port %d: protocol must be TCP, UDP or SCTP", i+1)
					}
					if p.Port <= 0 || p.Port > 65535 {
						return "", nil, fmt.Errorf("rule port %d: port must be 1..65535", i+1)
					}
					ports = append(ports, map[string]any{"protocol": p.Protocol, "port": p.Port})
				}
				rule["ports"] = ports
			}
			ingress = append(ingress, rule)
		}
		spec["ingress"] = ingress
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata":   map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec":       spec,
	})
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/networkpolicies/" + s.Name + ".yaml", out, nil
}

// AdminNetworkPolicySpec describes the cluster-wide admin DFW tier —
// AdminNetworkPolicy (priority-ordered, actions Allow/Deny/Pass) or, when Baseline,
// BaselineAdminNetworkPolicy (the cluster default: a singleton named "default", no
// priority, actions Allow/Deny only). Both override or backstop tenant
// NetworkPolicies, so they are cluster-scoped, platform-tier, and admin-only. Subject
// and peers are namespace selectors — Groups of projects.
type AdminNetworkPolicySpec struct {
	Name     string            `json:"name"`
	Baseline bool              `json:"baseline,omitempty"` // render a BaselineAdminNetworkPolicy
	Priority int               `json:"priority,omitempty"` // 0..1000, lower = higher precedence (ANP only)
	Subject  map[string]string `json:"subject,omitempty"`  // namespaceSelector matchLabels; empty = all namespaces
	Ingress  []AdminPolicyRule `json:"ingress,omitempty"`
	Egress   []AdminPolicyRule `json:"egress,omitempty"`
}

// AdminPolicyRule is one ordered admin rule: an action against the peer Groups
// (namespace selectors), optionally narrowed to ports.
type AdminPolicyRule struct {
	Action string              `json:"action"`          // Allow | Deny | Pass (Pass is ANP-only)
	Peers  []map[string]string `json:"peers,omitempty"` // namespaceSelector matchLabels — the from/to Groups
	Ports  []PolicyPort        `json:"ports,omitempty"`
}

// AdminNetworkPolicyManifest renders the (Baseline)AdminNetworkPolicy YAML plus its
// repo-relative path. A baseline policy is the cluster singleton "default" — no
// priority, and Pass is not a valid action.
func AdminNetworkPolicyManifest(s AdminNetworkPolicySpec) (path string, content []byte, err error) {
	name := s.Name
	if s.Baseline {
		name = "default" // BANP is a cluster singleton named default
	} else if !validate.DNS1123Name(name) {
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", name)
	} else if s.Priority < 0 || s.Priority > 1000 {
		return "", nil, fmt.Errorf("priority must be 0..1000")
	}
	subjectSel := map[string]any{}
	if len(s.Subject) > 0 {
		subjectSel = map[string]any{"matchLabels": toStrAny(s.Subject)}
	}
	spec := map[string]any{"subject": map[string]any{"namespaces": subjectSel}}
	if !s.Baseline {
		spec["priority"] = s.Priority
	}
	renderRules := func(rules []AdminPolicyRule, peerKey string) ([]any, error) {
		out := make([]any, 0, len(rules))
		for i, r := range rules {
			switch r.Action {
			case "Allow", "Deny":
				// always valid
			case "Pass":
				if s.Baseline {
					return nil, fmt.Errorf("rule %d: a baseline policy has no Pass action (Allow or Deny only)", i+1)
				}
			default:
				return nil, fmt.Errorf("rule %d: action must be Allow, Deny or Pass", i+1)
			}
			if len(r.Peers) == 0 {
				return nil, fmt.Errorf("rule %d: at least one peer Group is required", i+1)
			}
			peers := make([]any, 0, len(r.Peers))
			for _, p := range r.Peers {
				sel := map[string]any{}
				if len(p) > 0 {
					sel = map[string]any{"matchLabels": toStrAny(p)}
				}
				peers = append(peers, map[string]any{"namespaces": sel})
			}
			rule := map[string]any{"action": r.Action, peerKey: peers}
			if len(r.Ports) > 0 {
				ports := make([]any, 0, len(r.Ports))
				for j, pt := range r.Ports {
					if pt.Protocol != "TCP" && pt.Protocol != "UDP" && pt.Protocol != "SCTP" {
						return nil, fmt.Errorf("rule %d port %d: protocol must be TCP, UDP or SCTP", i+1, j+1)
					}
					if pt.Port <= 0 || pt.Port > 65535 {
						return nil, fmt.Errorf("rule %d port %d: port must be 1..65535", i+1, j+1)
					}
					ports = append(ports, map[string]any{"portNumber": map[string]any{"protocol": pt.Protocol, "port": pt.Port}})
				}
				rule["ports"] = ports
			}
			out = append(out, rule)
		}
		return out, nil
	}
	if len(s.Ingress) > 0 {
		ing, err := renderRules(s.Ingress, "from")
		if err != nil {
			return "", nil, err
		}
		spec["ingress"] = ing
	}
	if len(s.Egress) > 0 {
		eg, err := renderRules(s.Egress, "to")
		if err != nil {
			return "", nil, err
		}
		spec["egress"] = eg
	}
	kind, dir := "AdminNetworkPolicy", "adminnetworkpolicies"
	if s.Baseline {
		kind, dir = "BaselineAdminNetworkPolicy", "baselineadminnetworkpolicies"
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	})
	if err != nil {
		return "", nil, err
	}
	return dir + "/" + name + ".yaml", out, nil
}
