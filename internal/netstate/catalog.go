package netstate

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/reflect"
)

// Catalog gathers the whole networking picture as the UI's network objects from the
// watch-fed stores - a pure in-memory read, never a cluster call: the networks
// (UDN/CUDN/raw NAD), the physical fabric (Uplinks + node NICs), and whether nmstate is
// present. The handler filters project networks to the caller's visible namespaces and
// enriches each with ArgoCD drift; empty (pre-sync) stores yield an empty picture the
// caller repaints once NetworkChanged fires.
//
// The inventory's lists are the caller's to modify; the slices inside their
// entries are the snapshot's, read-only.
func (s *Snapshot) Catalog() model.NetworkInventory {
	return model.NetworkInventory{
		Networks:         slices.Clone(s.netView().networks),
		Uplinks:          slices.Clone(s.fabric()),
		PhysicalAdapters: slices.Clone(s.adapters()),
		NMStatePresent:   s.nmstatePresent.Load(),
	}
}

// fabric is uplinks decoded once per generation of the stores and the node
// set it reads.
func (s *Snapshot) fabric() []model.Uplink {
	return s.uplinkMemo.get(func() [3]uint64 {
		s.nodesMu.RLock()
		defer s.nodesMu.RUnlock()
		return [3]uint64{reflect.StoreGeneration(s.nncp), reflect.StoreGeneration(s.nnce), s.nodesGen}
	}, func([3]uint64) []model.Uplink { return s.uplinks() })
}

// adapters is every node's physical NICs, by node, decoded once per NNS
// generation.
func (s *Snapshot) adapters() []model.PhysicalAdapter {
	return s.adapterMemo.get(func() uint64 { return reflect.StoreGeneration(s.nns) }, func(uint64) []model.PhysicalAdapter {
		out := []model.PhysicalAdapter{}
		for _, u := range sortedList(s.nns) {
			out = append(out, adaptersFromNNS(u)...)
		}
		return out
	})
}

// uplinks builds the uplink list: the always-present builtin br-ex (OVN-K, on every
// node regardless of nmstate) plus any declared by an NNCP's ovn.bridge-mappings.
func (s *Snapshot) uplinks() []model.Uplink {
	all := s.nodesMatching(nil)
	out := []model.Uplink{{
		Name: "br-ex", Bridge: "br-ex", Builtin: true, Nodes: all, NodeCount: len(all),
	}}
	// Policy name order: the merge keeps the first policy's bridge.
	for _, u := range sortedList(s.nncp) {
		out = append(out, s.uplinksFromNNCP(u)...)
	}
	return mergeUplinks(out)
}

// mergeUplinks folds rows sharing a physical-network name into one uplink over the
// union of their nodes. A localnet declared by several NNCPs is the normal way to
// cover nodes whose NIC names differ, and it may also restate the builtin br-ex -
// one physical network is one uplink, so the name is the identity the whole UI
// keys on.
func mergeUplinks(in []model.Uplink) []model.Uplink {
	out := make([]model.Uplink, 0, len(in))
	at := make(map[string]int, len(in))
	for _, u := range in {
		i, seen := at[u.Name]
		if !seen {
			u.Nodes = sortedSet(u.Nodes)
			u.NodeCount = len(u.Nodes)
			at[u.Name] = len(out)
			out = append(out, u)
			continue
		}
		m := &out[i]
		m.Nodes = sortedSet(append(m.Nodes, u.Nodes...))
		m.NodeCount = len(m.Nodes)
		if m.Bridge == "" {
			m.Bridge = u.Bridge
		}
		if m.Policy != u.Policy {
			m.Policy = "" // several policies, no single object to act on
		}
		if len(u.Failures) > 0 {
			m.Failures = append(m.Failures, u.Failures...)
			sort.Slice(m.Failures, func(i, j int) bool { return m.Failures[i].Node < m.Failures[j].Node })
		}
	}
	return out
}

// sortedSet dedupes and orders names, never nil: the web reads a missing node
// list as every node.
func sortedSet(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// uplinksFromNNCP reads the ovn.bridge-mappings an NNCP declares - each one is an
// uplink (physical-network name -> OVS bridge) - scoped to the policy's nodes.
func (s *Snapshot) uplinksFromNNCP(u *unstructured.Unstructured) []model.Uplink {
	doc := decode[netgen.UplinkDoc](u)
	nodes := s.nodesMatching(doc.Spec.NodeSelector)
	var out []model.Uplink
	for _, m := range doc.Spec.DesiredState.OVN.BridgeMappings {
		if m.State == "absent" {
			continue
		}
		out = append(out, model.Uplink{
			Name: m.Localnet, Bridge: m.Bridge, Policy: u.GetName(),
			Nodes: nodes, NodeCount: len(nodes),
			Failures: s.failures(u.GetName()),
		})
	}
	return out
}

// failures lists the hosts where a policy's enactment failed or was aborted,
// by node. nmstate's own report can run to pages; its first line names the
// cause.
func (s *Snapshot) failures(policy string) []model.UplinkFailure {
	var out []model.UplinkFailure
	for _, u := range reflect.List(s.nnce) {
		lbl := u.GetLabels()
		if lbl["nmstate.io/policy"] != policy {
			continue
		}
		node := lbl["nmstate.io/node"]
		if node == "" {
			node = strings.TrimSuffix(u.GetName(), "."+policy)
		}
		conds, _ := nestedSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]any)
			if (m["type"] != "Failing" && m["type"] != "Aborted") || m["status"] != "True" {
				continue
			}
			msg, _, _ := strings.Cut(strings.TrimSpace(str(m["message"])), "\n")
			if len(msg) > 300 {
				msg = msg[:300]
			}
			out = append(out, model.UplinkFailure{Node: node, Reason: str(m["reason"]), Message: msg})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// nodesMatching returns the names of cached nodes matching sel (all nodes when sel is
// empty), filtered in memory - the node cache stands in for a per-request node LIST.
func (s *Snapshot) nodesMatching(sel map[string]string) []string {
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()
	out := make([]string, 0, len(s.nodes))
	for _, n := range s.nodes {
		if matchesLabels(n.Labels, sel) {
			out = append(out, n.Name)
		}
	}
	return out
}

func matchesLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// udnConfig and cudnConfig read the one OVN-K network body the two kinds
// nest at different depths: directly under spec, or under spec.network.
func udnConfig(u *unstructured.Unstructured) netgen.NetworkConfig {
	return decode[netgen.NetworkDoc](u).Spec.NetworkConfig
}

func cudnConfig(u *unstructured.Unstructured) netgen.NetworkConfig {
	return decode[netgen.NetworkDoc](u).Spec.Network
}

// nadConfig decodes a NAD's spec.config CNI JSON blob; ok is false when absent
// or unparsable.
func nadConfig(u *unstructured.Unstructured) (cfg struct {
	Name                string `json:"name"`
	Topology            string `json:"topology"`
	PhysicalNetworkName string `json:"physicalNetworkName"`
	VLANID              int    `json:"vlanID"`
	Subnets             string `json:"subnets"`
}, ok bool) {
	raw, _, _ := unstructured.NestedString(u.Object, "spec", "config")
	if raw == "" || json.Unmarshal([]byte(raw), &cfg) != nil {
		return cfg, false
	}
	return cfg, true
}

// networkFromUDN decodes a namespace-scoped UserDefinedNetwork.
func networkFromUDN(u *unstructured.Unstructured) model.Network {
	cfg := udnConfig(u)
	return model.Network{
		Name:      u.GetName(),
		Kind:      kindFor(cfg.Topology, cfg.Role()),
		Scope:     model.ScopeProject,
		Namespace: u.GetNamespace(),
		Subnets:   cfg.Subnets(),
		Backing:   "UserDefinedNetwork",
		Topology:  cfg.Topology,
		AttachRef: u.GetNamespace() + "/" + u.GetName(),
	}
}

// networkFromCUDN decodes a cluster-scoped ClusterUserDefinedNetwork.
func networkFromCUDN(u *unstructured.Unstructured) model.Network {
	cfg := cudnConfig(u)
	n := model.Network{
		Name:      u.GetName(),
		Kind:      kindFor(cfg.Topology, cfg.Role()),
		Scope:     model.ScopeShared,
		Subnets:   cfg.Subnets(),
		Backing:   "ClusterUserDefinedNetwork",
		Topology:  cfg.Topology,
		AttachRef: u.GetName(),
	}
	if n.Kind == model.NetworkVLAN {
		n.Uplink = cfg.Localnet.PhysicalNetworkName
		n.VLAN = cfg.Localnet.VLAN.Access.ID
	}
	return n
}

// networkFromNAD decodes a raw NetworkAttachmentDefinition (one not generated by a
// UDN/CUDN). The interesting bits live in spec.config, a JSON CNI string.
func networkFromNAD(u *unstructured.Unstructured) model.Network {
	n := model.Network{
		Name:      u.GetName(),
		Kind:      model.NetworkInternal,
		Scope:     model.ScopeProject,
		Namespace: u.GetNamespace(),
		Backing:   "NetworkAttachmentDefinition",
		AttachRef: u.GetNamespace() + "/" + u.GetName(),
	}
	c, ok := nadConfig(u)
	if !ok {
		return n
	}
	n.Topology = c.Topology
	if c.Topology != "" {
		n.Kind = kindFor(c.Topology, "")
	}
	n.VLAN = c.VLANID
	if c.Subnets != "" {
		n.Subnets = []string{c.Subnets}
	}
	return n
}

// kindFor maps an OVN-K topology + role to the network kind: localnet
// is a VLAN/physical network; a primary Layer2/3 network is the project's
// "VM Network"; any other Layer2/3 network is an isolated internal network.
func kindFor(topology, role string) model.NetworkKind {
	if strings.EqualFold(topology, "Localnet") {
		return model.NetworkVLAN
	}
	if strings.EqualFold(role, "Primary") {
		return model.NetworkDefault
	}
	return model.NetworkInternal
}

// isSystemNamespace reports whether a namespace holds platform plumbing rather
// than tenant networks - e.g. the openshift-ovn-kubernetes/default NAD that backs
// the cluster's own pod network, which is not a network a user attaches to.
func isSystemNamespace(ns string) bool {
	return strings.HasPrefix(ns, "openshift-") || ns == "kube-system"
}

// adaptersFromNNS pulls the physical NICs (ethernet/bond) out of one node's
// NodeNetworkState. The NNS name is the node name; interfaces live under
// status.currentState in nmstate's own schema.
func adaptersFromNNS(u *unstructured.Unstructured) []model.PhysicalAdapter {
	ifaces, found := nestedSlice(u.Object, "status", "currentState", "interfaces")
	if !found {
		return nil
	}
	node := u.GetName()
	out := []model.PhysicalAdapter{}
	for _, raw := range ifaces {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := str(m["type"])
		if typ != "ethernet" && typ != "bond" {
			continue // the Physical adapters view is real NICs, not derived devices
		}
		a := model.PhysicalAdapter{
			Node:  node,
			Name:  str(m["name"]),
			Type:  typ,
			MAC:   str(m["mac-address"]),
			State: str(m["state"]),
			Role:  nicRole(m),
		}
		if mtu, ok := toInt(m["mtu"]); ok {
			a.MTU = mtu
		}
		out = append(out, a)
	}
	return out
}

// nicRole reports what a NIC is already doing, from its nmstate controller (the
// master it's enslaved to): a member of br-ex carries the cluster network; any
// other controller means it's already in a bond/bridge; none means it's free.
func nicRole(m map[string]any) string {
	switch ctrl := str(m["controller"]); {
	case ctrl == "":
		return "available"
	case strings.Contains(ctrl, "br-ex"):
		return "cluster-uplink"
	default:
		return "enslaved"
	}
}

// nestedSlice reads a list field in place; found is false when it is absent
// or not a list. unstructured.NestedSlice deep-copies every entry, and a
// host's interface list runs to hundreds of them; callers only read scalars
// out of the entries and must never write through them (they are the
// reflector store's objects).
func nestedSlice(m map[string]any, keys ...string) (s []any, found bool) {
	v, _, _ := unstructured.NestedFieldNoCopy(m, keys...)
	s, found = v.([]any)
	return s, found
}

func str(v any) string { s, _ := v.(string); return s }

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
