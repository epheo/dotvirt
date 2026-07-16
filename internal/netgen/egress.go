package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

// EgressFirewallSpec describes a namespace's north-south egress firewall — the
// gateway-firewall analog on a project's Tier-1. OVN-K allows exactly one
// EgressFirewall per namespace and it must be named "default"; its ordered rules
// permit or deny egress from the namespace's pods (and VMs) to external CIDRs or DNS
// names, optionally narrowed to a port. Namespace-scoped, so it rides the tenant's
// own repo (unlike a cluster-scoped CUDN/uplink).
type EgressFirewallSpec struct {
	Namespace string       `json:"namespace"`
	Rules     []EgressRule `json:"rules"`
}

// EgressRule is one ordered allow/deny against an external destination. Exactly one
// of CIDR or DNSName names the destination; Ports optionally narrows it.
type EgressRule struct {
	Action  string       `json:"action"`            // Allow | Deny
	CIDR    string       `json:"cidr,omitempty"`    // cidrSelector destination
	DNSName string       `json:"dnsName,omitempty"` // dnsName destination
	Ports   []EgressPort `json:"ports,omitempty"`
}

// EgressPort narrows a rule to a transport port.
type EgressPort struct {
	Protocol string `json:"protocol"` // TCP | UDP | SCTP
	Port     int    `json:"port"`
}

// EgressFirewallManifest renders the EgressFirewall YAML plus its repo-relative
// path. The object is always named "default" (OVN-K permits one per namespace); the
// rules render in order, since an EgressFirewall is first-match.
func EgressFirewallManifest(s EgressFirewallSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	if len(s.Rules) == 0 {
		return "", nil, fmt.Errorf("at least one egress rule is required")
	}
	egress := make([]any, 0, len(s.Rules))
	for i, r := range s.Rules {
		if r.Action != "Allow" && r.Action != "Deny" {
			return "", nil, fmt.Errorf("rule %d: action must be Allow or Deny", i+1)
		}
		// Exactly one destination: a CIDR or a DNS name (XOR).
		if (r.CIDR == "") == (r.DNSName == "") {
			return "", nil, fmt.Errorf("rule %d: set exactly one of cidr or dnsName", i+1)
		}
		if r.CIDR != "" && !validCIDR(r.CIDR) {
			return "", nil, fmt.Errorf("rule %d: cidr %q must be a CIDR (e.g. 0.0.0.0/0)", i+1, r.CIDR)
		}
		to := map[string]any{}
		if r.CIDR != "" {
			to["cidrSelector"] = r.CIDR
		} else {
			to["dnsName"] = r.DNSName
		}
		rule := map[string]any{"type": r.Action, "to": to}
		if len(r.Ports) > 0 {
			ports := make([]any, 0, len(r.Ports))
			for j, p := range r.Ports {
				if p.Protocol != "TCP" && p.Protocol != "UDP" && p.Protocol != "SCTP" {
					return "", nil, fmt.Errorf("rule %d port %d: protocol must be TCP, UDP or SCTP", i+1, j+1)
				}
				if p.Port <= 0 || p.Port > 65535 {
					return "", nil, fmt.Errorf("rule %d port %d: port must be 1..65535", i+1, j+1)
				}
				ports = append(ports, map[string]any{"protocol": p.Protocol, "port": p.Port})
			}
			rule["ports"] = ports
		}
		egress = append(egress, rule)
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "EgressFirewall",
		"metadata":   map[string]any{"name": "default", "namespace": s.Namespace},
		"spec":       map[string]any{"egress": egress},
	})
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/egressfirewalls/default.yaml", out, nil
}

// EgressIPSpec describes a cluster-scoped EgressIP — the Tier-0 source-NAT pool that
// pins a project's egress to fixed, routable IPs. OVN-K applies it to the namespaces
// its selector matches; we render a namespaceSelector matching the chosen namespaces
// by name. Cluster-scoped, so it lands in the platform repo.
type EgressIPSpec struct {
	Name       string   `json:"name"`
	EgressIPs  []string `json:"egressIPs"`
	Namespaces []string `json:"namespaces"`
}

// EgressIPManifest renders the EgressIP YAML plus its repo-relative path.
func EgressIPManifest(s EgressIPSpec) (path string, content []byte, err error) {
	switch {
	case !validate.DNS1123Name(s.Name):
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", s.Name)
	case len(s.EgressIPs) == 0:
		return "", nil, fmt.Errorf("at least one egress IP is required")
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	}
	for _, ip := range s.EgressIPs {
		if !validIP(ip) {
			return "", nil, fmt.Errorf("egress IP %q must be an IP address", ip)
		}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "EgressIP",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"egressIPs": toAny(s.EgressIPs),
			"namespaceSelector": map[string]any{
				"matchExpressions": []any{map[string]any{
					"key":      "kubernetes.io/metadata.name",
					"operator": "In",
					"values":   toAny(s.Namespaces),
				}},
			},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "egressips/" + s.Name + ".yaml", out, nil
}

// ExternalRouteSpec describes a cluster-scoped AdminPolicyBasedExternalRoute — the
// Tier-0 static route that steers a project's egress through external next-hop
// gateways. Cluster-scoped, so it lands in the platform repo.
type ExternalRouteSpec struct {
	Name       string   `json:"name"`
	Namespaces []string `json:"namespaces"`
	NextHops   []string `json:"nextHops"` // static next-hop IPs
}

// ExternalRouteManifest renders the AdminPolicyBasedExternalRoute YAML plus its
// repo-relative path.
func ExternalRouteManifest(s ExternalRouteSpec) (path string, content []byte, err error) {
	switch {
	case !validate.DNS1123Name(s.Name):
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", s.Name)
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	case len(s.NextHops) == 0:
		return "", nil, fmt.Errorf("at least one next-hop IP is required")
	}
	static := make([]any, 0, len(s.NextHops))
	for _, ip := range s.NextHops {
		if !validIP(ip) {
			return "", nil, fmt.Errorf("next-hop %q must be an IP address", ip)
		}
		static = append(static, map[string]any{"ip": ip})
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "AdminPolicyBasedExternalRoute",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"from": map[string]any{
				"namespaceSelector": map[string]any{
					"matchExpressions": []any{map[string]any{
						"key":      "kubernetes.io/metadata.name",
						"operator": "In",
						"values":   toAny(s.Namespaces),
					}},
				},
			},
			"nextHops": map[string]any{"static": static},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "externalroutes/" + s.Name + ".yaml", out, nil
}
