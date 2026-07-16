// Package netgen renders OVN-K user-defined networks and nmstate uplink policies
// — the manifests behind dotvirt's "Distributed Port Group" and "Uplink" creates
// — from small specs, the way vmgen renders VirtualMachines. Owns-nothing: the
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
