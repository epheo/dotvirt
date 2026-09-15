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
	if err != nil || !SameDocument(content, rendered) {
		// A renderer refusal is the same finding as a mismatch: the manifest
		// holds something the form has no field for (an untagged localnet, a
		// label selector, extra labels).
		return nil, fmt.Errorf("the manifest carries settings the form has no field for")
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

func decodePorts(docs []PortDoc) []PolicyPort {
	var out []PolicyPort
	for _, p := range docs {
		if p.PortNumber != nil {
			p = *p.PortNumber
		}
		port := 0
		if p.Port != nil {
			port = p.Port.IntValue()
		}
		out = append(out, PolicyPort{Protocol: p.Protocol, Port: port})
	}
	return out
}

func decodeNetwork(content []byte) (Spec, error) {
	var doc NetworkDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return Spec{}, err
	}
	s := Spec{Name: doc.Metadata.Name}
	if doc.Kind == "UserDefinedNetwork" {
		s.Scope, s.Namespace, s.Subnets = ScopeProject, doc.Metadata.Namespace, doc.Spec.Layer2.Subnets
		return s, nil
	}
	s.Namespaces = NamespaceNames(&doc.Spec.NamespaceSelector)
	if doc.Spec.Network.Topology == "Localnet" {
		ln := doc.Spec.Network.Localnet
		s.Scope, s.PhysicalNetwork, s.VLAN, s.Subnets = ScopeVLAN, ln.PhysicalNetworkName, ln.VLAN.Access.ID, ln.Subnets
		return s, nil
	}
	s.Scope, s.Subnets = ScopeShared, doc.Spec.Network.Layer2.Subnets
	return s, nil
}

func decodeNetworkPolicy(content []byte) (NetworkPolicySpec, error) {
	var doc NetworkPolicyDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return NetworkPolicySpec{}, err
	}
	s := NetworkPolicySpec{Name: doc.Metadata.Name, Namespace: doc.Metadata.Namespace, AppliedTo: doc.Spec.PodSelector.MatchLabels}
	for _, r := range doc.Spec.Ingress {
		rule := PolicyRule{Ports: decodePorts(r.Ports)}
		for _, f := range r.From {
			var group map[string]string
			if f.PodSelector != nil {
				group = f.PodSelector.MatchLabels
			}
			rule.From = append(rule.From, group)
		}
		s.Ingress = append(s.Ingress, rule)
	}
	return s, nil
}

func decodeAdminNetworkPolicy(content []byte) (AdminNetworkPolicySpec, error) {
	var doc AdminNetworkPolicyDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return AdminNetworkPolicySpec{}, err
	}
	s := AdminNetworkPolicySpec{
		Name:     doc.Metadata.Name,
		Baseline: doc.Kind == "BaselineAdminNetworkPolicy",
		Priority: doc.Spec.Priority,
	}
	if ns := doc.Spec.Subject.Namespaces; ns != nil {
		s.Subject = ns.MatchLabels
	}
	rules := func(docs []AdminPolicyRuleDoc, peers func(AdminPolicyRuleDoc) []AdminPeerDoc) []AdminPolicyRule {
		var out []AdminPolicyRule
		for _, r := range docs {
			rule := AdminPolicyRule{Action: r.Action, Ports: decodePorts(r.Ports)}
			for _, p := range peers(r) {
				group := map[string]string{} // the "all namespaces" peer
				if p.Namespaces != nil && p.Namespaces.MatchLabels != nil {
					group = p.Namespaces.MatchLabels
				}
				rule.Peers = append(rule.Peers, group)
			}
			out = append(out, rule)
		}
		return out
	}
	s.Ingress = rules(doc.Spec.Ingress, func(r AdminPolicyRuleDoc) []AdminPeerDoc { return r.From })
	s.Egress = rules(doc.Spec.Egress, func(r AdminPolicyRuleDoc) []AdminPeerDoc { return r.To })
	return s, nil
}

func decodeEgressFirewall(content []byte) (EgressFirewallSpec, error) {
	var doc EgressFirewallDoc
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
	var doc EgressIPDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return EgressIPSpec{}, err
	}
	return EgressIPSpec{Name: doc.Metadata.Name, EgressIPs: doc.Spec.EgressIPs, Namespaces: NamespaceNames(&doc.Spec.NamespaceSelector)}, nil
}

func decodeUplink(content []byte) (UplinkSpec, error) {
	var doc UplinkDoc
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
	var doc ExternalRouteDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return ExternalRouteSpec{}, err
	}
	s := ExternalRouteSpec{Name: doc.Metadata.Name, Namespaces: NamespaceNames(&doc.Spec.From.NamespaceSelector)}
	for _, h := range doc.Spec.NextHops.Static {
		s.NextHops = append(s.NextHops, h.IP)
	}
	return s, nil
}
