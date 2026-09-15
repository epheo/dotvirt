package netgen

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The document shapes both planes read. Decode turns a manifest this package
// rendered back into its form spec; netstate converts each live object into
// the same struct once and reads every field from there. A field carried by
// neither side drops on decode, and Decode's re-render comparison refuses it.

// NamespaceNameLabel is the label every namespace carries with its own name,
// the key nsNameSelector pins namespaces by.
const NamespaceNameLabel = "kubernetes.io/metadata.name"

// NameIn returns the namespaces a requirement names when it is the name-In
// form nsNameSelector writes.
func NameIn(r metav1.LabelSelectorRequirement) ([]string, bool) {
	if r.Key != NamespaceNameLabel || r.Operator != metav1.LabelSelectorOpIn {
		return nil, false
	}
	return r.Values, true
}

// NamespaceNames enumerates the namespaces a selector provably pins to: the
// name-In requirement nsNameSelector writes, or a bare metadata.name
// matchLabel, and nothing else. Any other selector returns nil: label
// membership cannot be enumerated here, and a caller filtering rows by
// namespace must then keep the row rather than hide a possibly-applying one.
func NamespaceNames(sel *metav1.LabelSelector) []string {
	if sel == nil {
		return nil
	}
	if len(sel.MatchLabels) == 1 && len(sel.MatchExpressions) == 0 {
		if name := sel.MatchLabels[NamespaceNameLabel]; name != "" {
			return []string{name}
		}
		return nil
	}
	if len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 1 {
		vals, ok := NameIn(sel.MatchExpressions[0])
		if !ok {
			return nil
		}
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if v != "" {
				out = append(out, v)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

type Meta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// NetworkConfig is the OVN-K network body: directly under spec on a
// UserDefinedNetwork, under spec.network on a ClusterUserDefinedNetwork. The
// role and subnets sit under the lowercased topology's own key.
type NetworkConfig struct {
	Topology string   `json:"topology"`
	Layer2   Layer    `json:"layer2"`
	Layer3   Layer3   `json:"layer3"`
	Localnet Localnet `json:"localnet"`
}

type Layer struct {
	Role    string   `json:"role"`
	Subnets []string `json:"subnets"`
}

// Layer3 subnets are per-node blocks, so each entry carries the cluster CIDR
// plus a host-subnet size instead of a bare string.
type Layer3 struct {
	Role    string `json:"role"`
	Subnets []struct {
		CIDR string `json:"cidr"`
	} `json:"subnets"`
}

type Localnet struct {
	Role                string   `json:"role"`
	PhysicalNetworkName string   `json:"physicalNetworkName"`
	Subnets             []string `json:"subnets"`
	VLAN                struct {
		Access struct {
			ID int `json:"id"`
		} `json:"access"`
	} `json:"vlan"`
}

// Role reads the topology's own section.
func (c NetworkConfig) Role() string {
	switch strings.ToLower(c.Topology) {
	case "layer2":
		return c.Layer2.Role
	case "layer3":
		return c.Layer3.Role
	case "localnet":
		return c.Localnet.Role
	}
	return ""
}

// Subnets reads the topology's own section as CIDRs.
func (c NetworkConfig) Subnets() []string {
	switch strings.ToLower(c.Topology) {
	case "layer2":
		return c.Layer2.Subnets
	case "layer3":
		var out []string
		for _, s := range c.Layer3.Subnets {
			if s.CIDR != "" {
				out = append(out, s.CIDR)
			}
		}
		return out
	case "localnet":
		return c.Localnet.Subnets
	}
	return nil
}

type NetworkDoc struct {
	Kind     string `json:"kind"`
	Metadata Meta   `json:"metadata"`
	Spec     struct {
		NetworkConfig     `json:",inline"`
		NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
		Network           NetworkConfig        `json:"network"`
	} `json:"spec"`
}

// PortDoc is the port entry every policy kind carries, in both wire forms:
// NetworkPolicy and EgressFirewall {protocol, port, endPort}, and the admin
// tiers' {portNumber}, {portRange} and {namedPort} wrappers.
type PortDoc struct {
	Protocol   string              `json:"protocol"`
	Port       *intstr.IntOrString `json:"port"`
	EndPort    int                 `json:"endPort"`
	PortNumber *PortDoc            `json:"portNumber"`
	PortRange  *PortRangeDoc       `json:"portRange"`
	NamedPort  *string             `json:"namedPort"`
}

type PortRangeDoc struct {
	Protocol string `json:"protocol"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

// NamespacedPod is the admin tiers' pods subject or peer; both selectors are
// required by the API, so neither is a pointer.
type NamespacedPod struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
	PodSelector       metav1.LabelSelector `json:"podSelector"`
}

type NetworkPolicyDoc struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		PodSelector metav1.LabelSelector `json:"podSelector"`
		PolicyTypes []string             `json:"policyTypes"`
		Ingress     []NetworkPolicyRule  `json:"ingress"`
		Egress      []NetworkPolicyRule  `json:"egress"`
	} `json:"spec"`
}

type NetworkPolicyRule struct {
	From  []NetworkPolicyPeer `json:"from"`
	To    []NetworkPolicyPeer `json:"to"`
	Ports []PortDoc           `json:"ports"`
}

// NetworkPolicyPeer keeps presence: a podSelector without a namespaceSelector
// means the policy's own namespace, so nil and empty differ here.
type NetworkPolicyPeer struct {
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector"`
	PodSelector       *metav1.LabelSelector `json:"podSelector"`
	IPBlock           *IPBlock              `json:"ipBlock"`
}

type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except"`
}

type AdminNetworkPolicyDoc struct {
	Kind     string `json:"kind"`
	Metadata Meta   `json:"metadata"`
	Spec     struct {
		Priority int `json:"priority"`
		Subject  struct {
			Namespaces *metav1.LabelSelector `json:"namespaces"`
			Pods       *NamespacedPod        `json:"pods"`
		} `json:"subject"`
		Ingress []AdminPolicyRuleDoc `json:"ingress"`
		Egress  []AdminPolicyRuleDoc `json:"egress"`
	} `json:"spec"`
}

type AdminPolicyRuleDoc struct {
	Action string         `json:"action"`
	From   []AdminPeerDoc `json:"from"`
	To     []AdminPeerDoc `json:"to"`
	Ports  []PortDoc      `json:"ports"`
}

type AdminPeerDoc struct {
	Namespaces *metav1.LabelSelector `json:"namespaces"`
	Pods       *NamespacedPod        `json:"pods"`
	Nodes      *metav1.LabelSelector `json:"nodes"`
	Networks   []string              `json:"networks"`
}

type EgressFirewallDoc struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Egress []EgressFirewallRuleDoc `json:"egress"`
	} `json:"spec"`
}

type EgressFirewallRuleDoc struct {
	Type  string            `json:"type"`
	To    EgressDestination `json:"to"`
	Ports []PortDoc         `json:"ports"`
}

type EgressDestination struct {
	CIDRSelector string                `json:"cidrSelector"`
	DNSName      string                `json:"dnsName"`
	NodeSelector *metav1.LabelSelector `json:"nodeSelector"`
}

type EgressIPDoc struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		EgressIPs         []string             `json:"egressIPs"`
		NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
		PodSelector       metav1.LabelSelector `json:"podSelector"`
	} `json:"spec"`
}

type ExternalRouteDoc struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		From struct {
			NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
		} `json:"from"`
		NextHops struct {
			Static []struct {
				IP string `json:"ip"`
			} `json:"static"`
		} `json:"nextHops"`
	} `json:"spec"`
}

type UplinkDoc struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		NodeSelector map[string]string `json:"nodeSelector"`
		DesiredState struct {
			Interfaces []struct {
				Name   string `json:"name"`
				Bridge struct {
					Port []struct {
						Name string `json:"name"`
					} `json:"port"`
				} `json:"bridge"`
			} `json:"interfaces"`
			OVN struct {
				BridgeMappings []BridgeMapping `json:"bridge-mappings"`
			} `json:"ovn"`
		} `json:"desiredState"`
	} `json:"spec"`
}

type BridgeMapping struct {
	Localnet string `json:"localnet"`
	Bridge   string `json:"bridge"`
	State    string `json:"state"`
}
