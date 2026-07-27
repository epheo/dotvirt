// Package netgen renders OVN-K user-defined networks and nmstate uplink policies
// - the manifests behind dotvirt's "Distributed Port Group" and "Uplink" creates
// - from small specs, the way vmgen renders VirtualMachines. Owns-nothing: the
// output is proposed via PR and applied by Argo, never written to the cluster.
// One file per kind family: udn.go (port groups), namespace.go (namespace +
// tenant RBAC), uplink.go (NNCP), egress.go (Tier-0/Tier-1 egress), policy.go
// (DFW policies); this file holds the validation and marshalling shared by all.
package netgen

import (
	"fmt"
	"net"
)

// validCIDR reports whether s parses as a CIDR (e.g. 10.0.0.0/24). Subnet/egress
// values only ever land in YAML scalars, so this is correctness, not safety: a bad
// value would otherwise render a manifest OVN-K rejects at apply time. The raw value
// is validated (no trimming) so what passes here is exactly what the manifest emits.
func validCIDR(s string) bool {
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// validIP reports whether s parses as a bare IP address.
func validIP(s string) bool {
	return net.ParseIP(s) != nil
}

// requireCIDRs validates each subnet as a CIDR.
func requireCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		if !validCIDR(c) {
			return fmt.Errorf("subnet %q must be a CIDR (e.g. 10.0.0.0/24)", c)
		}
	}
	return nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func toStrAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// nsNameSelector selects namespaces by their metadata.name - the one selector
// shape every cluster-scoped manifest (CUDN, EgressIP, external route) uses to
// publish to a chosen set of projects.
func nsNameSelector(namespaces []string) map[string]any {
	return map[string]any{
		"matchExpressions": []any{map[string]any{
			"key":      "kubernetes.io/metadata.name",
			"operator": "In",
			"values":   toAny(namespaces),
		}},
	}
}

// layer2Spec is the shared Layer2 body. A subnet-less L2 network is a pure
// switch (no IPAM); OVN-K defaults ipam.mode to Enabled (which then requires
// subnets), so Disabled must be set explicitly - valid because these networks
// are Secondary.
func layer2Spec(subnets []string) map[string]any {
	layer2 := map[string]any{"role": "Secondary"}
	if len(subnets) > 0 {
		layer2["subnets"] = toAny(subnets)
	} else {
		layer2["ipam"] = map[string]any{"mode": "Disabled"}
	}
	return layer2
}

// portEntries validates ports (TCP/UDP/SCTP, 1..65535) and renders them; the
// admin tiers wrap each entry in portNumber. where prefixes errors ("rule 2").
func portEntries(where string, ports []PolicyPort, wrapPortNumber bool) ([]any, error) {
	out := make([]any, 0, len(ports))
	for i, p := range ports {
		if p.Protocol != "TCP" && p.Protocol != "UDP" && p.Protocol != "SCTP" {
			return nil, fmt.Errorf("%s port %d: protocol must be TCP, UDP or SCTP", where, i+1)
		}
		if p.Port <= 0 || p.Port > 65535 {
			return nil, fmt.Errorf("%s port %d: port must be 1..65535", where, i+1)
		}
		entry := map[string]any{"protocol": p.Protocol, "port": p.Port}
		if wrapPortNumber {
			entry = map[string]any{"portNumber": entry}
		}
		out = append(out, entry)
	}
	return out, nil
}
