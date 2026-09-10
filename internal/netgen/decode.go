package netgen

import (
	"fmt"
	"reflect"

	"sigs.k8s.io/yaml"
)

// Decode reads a manifest this package rendered back into the spec that renders
// it, so the form that created an object can edit it. The decoded spec must
// re-render to the very same document: a manifest carrying anything the form
// cannot express (hand-written selectors, extra fields) is refused rather than
// silently rewritten without those settings.
func Decode(content []byte) (any, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := yaml.Unmarshal(content, &head); err != nil {
		return nil, err
	}
	var (
		spec     any
		rendered []byte
		err      error
	)
	switch head.Kind {
	case "UserDefinedNetwork", "ClusterUserDefinedNetwork":
		var s Spec
		if s, err = decodeNetwork(content); err == nil {
			_, rendered, err = Manifest(s)
		}
		spec = s
	case "NetworkPolicy":
		var s NetworkPolicySpec
		if s, err = decodeNetworkPolicy(content); err == nil {
			_, rendered, err = NetworkPolicyManifest(s)
		}
		spec = s
	case "AdminNetworkPolicy", "BaselineAdminNetworkPolicy":
		var s AdminNetworkPolicySpec
		if s, err = decodeAdminNetworkPolicy(content); err == nil {
			_, rendered, err = AdminNetworkPolicyManifest(s)
		}
		spec = s
	case "EgressFirewall":
		var s EgressFirewallSpec
		if s, err = decodeEgressFirewall(content); err == nil {
			_, rendered, err = EgressFirewallManifest(s)
		}
		spec = s
	case "EgressIP":
		var s EgressIPSpec
		if s, err = decodeEgressIP(content); err == nil {
			_, rendered, err = EgressIPManifest(s)
		}
		spec = s
	case "AdminPolicyBasedExternalRoute":
		var s ExternalRouteSpec
		if s, err = decodeExternalRoute(content); err == nil {
			_, rendered, err = ExternalRouteManifest(s)
		}
		spec = s
	case "NodeNetworkConfigurationPolicy":
		var s UplinkSpec
		if s, err = decodeUplink(content); err == nil {
			_, rendered, err = UplinkManifest(s)
		}
		spec = s
	default:
		return nil, fmt.Errorf("%s has no form", head.Kind)
	}
	if err != nil {
		return nil, err
	}
	if !SameDocument(content, rendered) {
		return nil, fmt.Errorf("the manifest carries settings the form cannot edit; change it in git")
	}
	return spec, nil
}

// SameDocument compares two manifests as data, so key order and quoting do not
// count and every field does.
func SameDocument(a, b []byte) bool {
	var da, db any
	if yaml.Unmarshal(a, &da) != nil || yaml.Unmarshal(b, &db) != nil {
		return false
	}
	return reflect.DeepEqual(da, db)
}

// The document shapes below mirror what the renderers write; unknown fields are
// dropped on decode and caught by Decode's re-render comparison.

type metaDoc struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type labelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	} `json:"matchExpressions"`
}

// names reads back the nsNameSelector shape: the namespaces published to.
func (s labelSelector) names() []string {
	for _, e := range s.MatchExpressions {
		if e.Key == "kubernetes.io/metadata.name" && e.Operator == "In" {
			return e.Values
		}
	}
	return nil
}

type portDoc struct {
	Protocol   string   `json:"protocol"`
	Port       int      `json:"port"`
	PortNumber *portDoc `json:"portNumber"` // the admin tiers' wrapper
}

func decodePorts(docs []portDoc) []PolicyPort {
	var out []PolicyPort
	for _, p := range docs {
		if p.PortNumber != nil {
			p = *p.PortNumber
		}
		out = append(out, PolicyPort{Protocol: p.Protocol, Port: p.Port})
	}
	return out
}

func decodeNetwork(content []byte) (Spec, error) {
	var doc struct {
		Kind     string  `json:"kind"`
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			// UDN: the network config sits directly under spec.
			Topology string `json:"topology"`
			Layer2   struct {
				Subnets []string `json:"subnets"`
			} `json:"layer2"`
			// CUDN: the same config nests under spec.network.
			NamespaceSelector labelSelector `json:"namespaceSelector"`
			Network           struct {
				Topology string `json:"topology"`
				Layer2   struct {
					Subnets []string `json:"subnets"`
				} `json:"layer2"`
				Localnet struct {
					PhysicalNetworkName string   `json:"physicalNetworkName"`
					Subnets             []string `json:"subnets"`
					VLAN                struct {
						Access struct {
							ID int `json:"id"`
						} `json:"access"`
					} `json:"vlan"`
				} `json:"localnet"`
			} `json:"network"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return Spec{}, err
	}
	s := Spec{Name: doc.Metadata.Name}
	if doc.Kind == "UserDefinedNetwork" {
		s.Scope, s.Namespace, s.Subnets = ScopeProject, doc.Metadata.Namespace, doc.Spec.Layer2.Subnets
		return s, nil
	}
	s.Namespaces = doc.Spec.NamespaceSelector.names()
	if doc.Spec.Network.Topology == "Localnet" {
		ln := doc.Spec.Network.Localnet
		s.Scope, s.PhysicalNetwork, s.VLAN, s.Subnets = ScopeVLAN, ln.PhysicalNetworkName, ln.VLAN.Access.ID, ln.Subnets
		return s, nil
	}
	s.Scope, s.Subnets = ScopeShared, doc.Spec.Network.Layer2.Subnets
	return s, nil
}

func decodeNetworkPolicy(content []byte) (NetworkPolicySpec, error) {
	var doc struct {
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			PodSelector labelSelector `json:"podSelector"`
			Ingress     []struct {
				From []struct {
					PodSelector labelSelector `json:"podSelector"`
				} `json:"from"`
				Ports []portDoc `json:"ports"`
			} `json:"ingress"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return NetworkPolicySpec{}, err
	}
	s := NetworkPolicySpec{Name: doc.Metadata.Name, Namespace: doc.Metadata.Namespace, AppliedTo: doc.Spec.PodSelector.MatchLabels}
	for _, r := range doc.Spec.Ingress {
		rule := PolicyRule{Ports: decodePorts(r.Ports)}
		for _, f := range r.From {
			rule.From = append(rule.From, f.PodSelector.MatchLabels)
		}
		s.Ingress = append(s.Ingress, rule)
	}
	return s, nil
}

func decodeAdminNetworkPolicy(content []byte) (AdminNetworkPolicySpec, error) {
	type peerDoc struct {
		Namespaces labelSelector `json:"namespaces"`
	}
	type ruleDoc struct {
		Action string    `json:"action"`
		From   []peerDoc `json:"from"`
		To     []peerDoc `json:"to"`
		Ports  []portDoc `json:"ports"`
	}
	var doc struct {
		Kind     string  `json:"kind"`
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			Priority int `json:"priority"`
			Subject  struct {
				Namespaces labelSelector `json:"namespaces"`
			} `json:"subject"`
			Ingress []ruleDoc `json:"ingress"`
			Egress  []ruleDoc `json:"egress"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return AdminNetworkPolicySpec{}, err
	}
	s := AdminNetworkPolicySpec{
		Name:     doc.Metadata.Name,
		Baseline: doc.Kind == "BaselineAdminNetworkPolicy",
		Priority: doc.Spec.Priority,
		Subject:  doc.Spec.Subject.Namespaces.MatchLabels,
	}
	rules := func(docs []ruleDoc, peers func(ruleDoc) []peerDoc) []AdminPolicyRule {
		var out []AdminPolicyRule
		for _, r := range docs {
			rule := AdminPolicyRule{Action: r.Action, Ports: decodePorts(r.Ports)}
			for _, p := range peers(r) {
				sel := p.Namespaces.MatchLabels
				if sel == nil {
					sel = map[string]string{} // the "all namespaces" peer
				}
				rule.Peers = append(rule.Peers, sel)
			}
			out = append(out, rule)
		}
		return out
	}
	s.Ingress = rules(doc.Spec.Ingress, func(r ruleDoc) []peerDoc { return r.From })
	s.Egress = rules(doc.Spec.Egress, func(r ruleDoc) []peerDoc { return r.To })
	return s, nil
}

func decodeEgressFirewall(content []byte) (EgressFirewallSpec, error) {
	var doc struct {
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			Egress []struct {
				Type string `json:"type"`
				To   struct {
					CIDRSelector string `json:"cidrSelector"`
					DNSName      string `json:"dnsName"`
				} `json:"to"`
				Ports []portDoc `json:"ports"`
			} `json:"egress"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return EgressFirewallSpec{}, err
	}
	s := EgressFirewallSpec{Namespace: doc.Metadata.Namespace}
	for _, r := range doc.Spec.Egress {
		s.Rules = append(s.Rules, EgressRule{Action: r.Type, CIDR: r.To.CIDRSelector, DNSName: r.To.DNSName, Ports: decodePorts(r.Ports)})
	}
	return s, nil
}

func decodeEgressIP(content []byte) (EgressIPSpec, error) {
	var doc struct {
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			EgressIPs         []string      `json:"egressIPs"`
			NamespaceSelector labelSelector `json:"namespaceSelector"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return EgressIPSpec{}, err
	}
	return EgressIPSpec{Name: doc.Metadata.Name, EgressIPs: doc.Spec.EgressIPs, Namespaces: doc.Spec.NamespaceSelector.names()}, nil
}

func decodeUplink(content []byte) (UplinkSpec, error) {
	var doc struct {
		Spec struct {
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
					BridgeMappings []struct {
						Localnet string `json:"localnet"`
						Bridge   string `json:"bridge"`
					} `json:"bridge-mappings"`
				} `json:"ovn"`
			} `json:"desiredState"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return UplinkSpec{}, err
	}
	s := UplinkSpec{NodeSelector: doc.Spec.NodeSelector}
	if m := doc.Spec.DesiredState.OVN.BridgeMappings; len(m) > 0 {
		s.Name, s.Bridge = m[0].Localnet, m[0].Bridge
	}
	if i := doc.Spec.DesiredState.Interfaces; len(i) > 0 && len(i[0].Bridge.Port) > 0 {
		s.NIC = i[0].Bridge.Port[0].Name
	}
	// The renderer's defaults read back as "unset", so the form shows them as such.
	if s.Bridge == "br-"+s.Name {
		s.Bridge = ""
	}
	if len(s.NodeSelector) == 1 && s.NodeSelector["node-role.kubernetes.io/worker"] == "" {
		if _, ok := s.NodeSelector["node-role.kubernetes.io/worker"]; ok {
			s.NodeSelector = nil
		}
	}
	return s, nil
}

func decodeExternalRoute(content []byte) (ExternalRouteSpec, error) {
	var doc struct {
		Metadata metaDoc `json:"metadata"`
		Spec     struct {
			From struct {
				NamespaceSelector labelSelector `json:"namespaceSelector"`
			} `json:"from"`
			NextHops struct {
				Static []struct {
					IP string `json:"ip"`
				} `json:"static"`
			} `json:"nextHops"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return ExternalRouteSpec{}, err
	}
	s := ExternalRouteSpec{Name: doc.Metadata.Name, Namespaces: doc.Spec.From.NamespaceSelector.names()}
	for _, h := range doc.Spec.NextHops.Static {
		s.NextHops = append(s.NextHops, h.IP)
	}
	return s, nil
}
