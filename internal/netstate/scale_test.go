package netstate

import (
	"fmt"
	"maps"
	"strconv"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The scale the read paths are benchmarked at: 5000 namespaces, 100 UDNs
// and 100 CUDNs with their generated NADs, 500 policies across every kind,
// and a 50-node fabric.
const (
	scaleNamespaces = 5000
	scaleUDNs       = 100
	scaleCUDNs      = 100
	scaleCUDNSpan   = 20 // namespaces each CUDN generates a NAD in
	scaleNetpolNS   = 200
	scaleANPs       = 40
	scaleFirewalls  = 39
	scaleEgressIPs  = 10
	scaleRoutes     = 10
	scaleNodes      = 50
	scaleUplinks    = 5
	scaleNICs       = 40
)

func scaleNS(i int) string      { return fmt.Sprintf("ns-%04d", i) }
func scaleProject(i int) string { return fmt.Sprintf("proj-%03d", i/10) }

func scaleNSLabels(i int) map[string]string {
	env := "dev"
	if i%2 == 0 {
		env = "prod"
	}
	return map[string]string{"kubernetes.io/metadata.name": scaleNS(i), "dotvirt.io/project": scaleProject(i), "env": env}
}

func labelSel(kv ...string) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return map[string]any{"matchLabels": m}
}

func put(tb testing.TB, idx interface{ Add(any) error }, obj map[string]any) {
	tb.Helper()
	if err := idx.Add(&unstructured.Unstructured{Object: obj}); err != nil {
		tb.Fatal(err)
	}
}

func ownedNAD(ns, name, kind, owner, uid, config string) map[string]any {
	return map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{
			"name": name, "namespace": ns, "uid": "nad-" + ns + "-" + name, "resourceVersion": "1",
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": kind, "name": owner, "uid": uid}},
		},
		"spec": map[string]any{"config": config},
	}
}

// scaleSnapshot seeds every store at the benchmark scale.
func scaleSnapshot(tb testing.TB) *Snapshot {
	tb.Helper()
	s := New(nil, nil)
	s.nmstatePresent.Store(true)
	s.clusterNet = parsePrefixes([]string{"10.128.0.0/14"})
	s.svcNet = parsePrefixes([]string{"172.30.0.0/16"})

	for i := range scaleNodes {
		zone := "a"
		if i%2 == 1 {
			zone = "b"
		}
		s.nodes = append(s.nodes, cluster.NodeLabels{
			Name:       fmt.Sprintf("worker-%02d", i),
			Labels:     map[string]string{"zone": zone, "node-role.kubernetes.io/worker": ""},
			PodSubnets: fmt.Sprintf(`{"default":["10.%d.%d.0/23"]}`, 128+i/128, (i%128)*2),
		})
	}
	s.podNet = defaultPodSubnets(s.nodes)

	for i := range scaleUDNs {
		ns, subnet := scaleNS(i), fmt.Sprintf("10.%d.%d.0/24", 64+i/256, i%256)
		uid := "udn-" + ns
		put(tb, s.udn, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
			"metadata": map[string]any{"name": "vm-net", "namespace": ns, "uid": uid, "resourceVersion": "1"},
			"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{subnet}}},
		})
		put(tb, s.nad, ownedNAD(ns, "vm-net", "UserDefinedNetwork", "vm-net", uid,
			fmt.Sprintf(`{"cniVersion":"1.0.0","type":"ovn-k8s-cni-overlay","name":"%s.vm-net","topology":"layer2","role":"primary","subnets":"%s"}`, ns, subnet)))
	}
	for c := range scaleCUDNs {
		name, uid := fmt.Sprintf("shared-%03d", c), fmt.Sprintf("cudn-%03d", c)
		network := map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary", "subnets": []any{fmt.Sprintf("10.%d.%d.0/24", 80+c/256, c%256)}}}
		topology, base := "layer2", c*scaleCUDNSpan%scaleNamespaces
		switch {
		case c >= 50 && c < 60:
			network = map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{fmt.Sprintf("10.%d.0.0/16", 40+c)}}}
		case c >= scaleCUDNs-10:
			network = map[string]any{"topology": "Localnet", "localnet": map[string]any{
				"role": "Secondary", "physicalNetworkName": "physnet-dc",
				"vlan": map[string]any{"mode": "Access", "access": map[string]any{"id": int64(100 + c)}},
			}}
			topology = "localnet"
		}
		nss := make([]any, 0, scaleCUDNSpan)
		for k := range scaleCUDNSpan {
			nss = append(nss, scaleNS(base+k))
		}
		put(tb, s.cudn, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
			"metadata": map[string]any{"name": name, "uid": uid, "resourceVersion": "1"},
			"spec":     map[string]any{"namespaceSelector": nameInSelector(nss...), "network": network},
		})
		for _, ns := range nss {
			put(tb, s.nad, ownedNAD(ns.(string), name, "ClusterUserDefinedNetwork", name, uid,
				fmt.Sprintf(`{"cniVersion":"1.0.0","type":"ovn-k8s-cni-overlay","name":"%s","topology":"%s","physicalNetworkName":"physnet-dc","vlanID":%d}`, name, topology, 100+c)))
		}
	}
	for i := range 50 {
		put(tb, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{"name": "legacy", "namespace": scaleNS(2000 + i), "resourceVersion": "1"},
			"spec":     map[string]any{"config": fmt.Sprintf(`{"cniVersion":"0.3.1","type":"ovn-k8s-cni-overlay","name":"legacy","topology":"localnet","vlanID":%d,"subnets":"192.168.%d.0/24"}`, 2000+i, i)},
		})
	}

	for i := range scaleNetpolNS {
		ns := scaleNS(i)
		put(tb, s.netpol, map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": "allow-web", "namespace": ns, "resourceVersion": "1"},
			"spec": map[string]any{
				"podSelector": labelSel("app", "web"),
				"policyTypes": []any{"Ingress"},
				"ingress": []any{map[string]any{
					"from": []any{map[string]any{"namespaceSelector": labelSel("dotvirt.io/project", scaleProject(i)), "podSelector": labelSel("app", "frontend")}},
					"ports": []any{
						map[string]any{"protocol": "TCP", "port": int64(80)},
						map[string]any{"protocol": "TCP", "port": int64(443)},
					},
				}},
			},
		})
		put(tb, s.netpol, map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": "egress-db", "namespace": ns, "resourceVersion": "1"},
			"spec": map[string]any{
				"podSelector": labelSel("app", "web"),
				"policyTypes": []any{"Egress"},
				"egress": []any{map[string]any{
					"to": []any{
						map[string]any{"namespaceSelector": nameInSelector(scaleNS(i + 1)), "podSelector": labelSel("app", "db")},
						map[string]any{"ipBlock": map[string]any{"cidr": "10.0.0.0/8", "except": []any{"10.1.0.0/16"}}},
					},
					"ports": []any{map[string]any{"protocol": "TCP", "port": int64(5432)}},
				}},
			},
		})
	}
	for i := range scaleANPs {
		subject := labelSel("env", "prod")
		if i%2 == 1 {
			subject = map[string]any{"matchExpressions": []any{map[string]any{
				"key": "dotvirt.io/project", "operator": "In", "values": []any{scaleProject(i * 10), scaleProject(i*10 + 10)},
			}}}
		}
		put(tb, s.anp, map[string]any{
			"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
			"metadata": map[string]any{"name": fmt.Sprintf("tier-%02d", i), "resourceVersion": "1"},
			"spec": map[string]any{
				"priority": int64(10 + i*2),
				"subject":  map[string]any{"namespaces": subject},
				"ingress": []any{
					map[string]any{"action": "Allow", "from": []any{map[string]any{"namespaces": labelSel("dotvirt.io/project", scaleProject(i*10))}},
						"ports": []any{map[string]any{"portNumber": map[string]any{"protocol": "TCP", "port": int64(22)}}}},
					map[string]any{"action": "Pass", "from": []any{map[string]any{"pods": map[string]any{
						"namespaceSelector": labelSel("env", "dev"), "podSelector": labelSel("app", "monitor"),
					}}}},
				},
				"egress": []any{
					map[string]any{"action": "Deny", "to": []any{map[string]any{"networks": []any{fmt.Sprintf("192.0.%d.0/24", i)}}}},
				},
			},
		})
	}
	put(tb, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default", "resourceVersion": "1"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"ingress": []any{map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": map[string]any{}}}}},
		},
	})
	for i := range scaleFirewalls {
		put(tb, s.egressfw, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
			"metadata": map[string]any{"name": "default", "namespace": scaleNS(i), "resourceVersion": "1"},
			"spec": map[string]any{"egress": []any{
				map[string]any{"type": "Allow", "to": map[string]any{"cidrSelector": "10.0.0.0/8"}},
				map[string]any{"type": "Allow", "to": map[string]any{"dnsName": "registry.example.com"}, "ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}}},
				map[string]any{"type": "Allow", "to": map[string]any{"nodeSelector": labelSel("node-role.kubernetes.io/worker", "")}},
				map[string]any{"type": "Allow", "to": map[string]any{"cidrSelector": "203.0.113.0/24"}, "ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}}},
				map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
			}},
		})
	}
	for i := range scaleEgressIPs {
		put(tb, s.egressip, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
			"metadata": map[string]any{"name": fmt.Sprintf("pool-%02d", i), "resourceVersion": "1"},
			"spec": map[string]any{
				"egressIPs":         []any{fmt.Sprintf("192.0.2.%d", 10+i)},
				"namespaceSelector": labelSel("dotvirt.io/project", scaleProject(i*10)),
				"podSelector":       labelSel("app", "web"),
			},
		})
	}
	for i := range scaleRoutes {
		put(tb, s.extroute, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "AdminPolicyBasedExternalRoute",
			"metadata": map[string]any{"name": fmt.Sprintf("route-%02d", i), "resourceVersion": "1"},
			"spec": map[string]any{
				"from":     map[string]any{"namespaceSelector": labelSel("dotvirt.io/project", scaleProject(100+i*10))},
				"nextHops": map[string]any{"static": []any{map[string]any{"ip": fmt.Sprintf("198.51.100.%d", 1+i)}}},
			},
		})
	}

	for p := range scaleUplinks {
		policy := fmt.Sprintf("uplink-%d", p)
		zone := "a"
		if p%2 == 1 {
			zone = "b"
		}
		put(tb, s.nncp, map[string]any{
			"apiVersion": "nmstate.io/v1", "kind": "NodeNetworkConfigurationPolicy",
			"metadata": map[string]any{"name": policy, "resourceVersion": "1"},
			"spec": map[string]any{
				"nodeSelector": map[string]any{"zone": zone},
				"desiredState": map[string]any{
					"interfaces": []any{map[string]any{"name": "br-" + policy, "type": "ovs-bridge", "bridge": map[string]any{"port": []any{map[string]any{"name": "eno2"}}}}},
					"ovn":        map[string]any{"bridge-mappings": []any{map[string]any{"localnet": fmt.Sprintf("physnet-%d", p), "bridge": "br-" + policy}}},
				},
			},
		})
		for _, n := range s.nodes {
			cond := map[string]any{"type": "Available", "status": "True", "reason": "SuccessfullyConfigured", "message": ""}
			if n.Name == "worker-07" {
				cond = map[string]any{"type": "Failing", "status": "True", "reason": "FailedToConfigure", "message": "error reconciling\nfull nmstate dump"}
			}
			put(tb, s.nnce, map[string]any{
				"apiVersion": "nmstate.io/v1beta1", "kind": "NodeNetworkConfigurationEnactment",
				"metadata": map[string]any{"name": n.Name + "." + policy, "resourceVersion": "1",
					"labels": map[string]any{"nmstate.io/policy": policy, "nmstate.io/node": n.Name}},
				"status": map[string]any{"conditions": []any{
					map[string]any{"type": "Progressing", "status": "False", "reason": "SuccessfullyConfigured"},
					cond,
				}},
			})
		}
	}
	for _, n := range s.nodes {
		ifaces := make([]any, 0, scaleNICs)
		for k := range scaleNICs {
			typ, ctrl := "veth", "br-int"
			switch {
			case k < 2:
				typ, ctrl = "ethernet", "br-ex"
			case k < 4:
				typ, ctrl = "ethernet", ""
			case k == 4:
				typ, ctrl = "bond", ""
			}
			ifaces = append(ifaces, map[string]any{
				"name": fmt.Sprintf("if%02d", k), "type": typ, "state": "up", "mtu": int64(1500),
				"mac-address": fmt.Sprintf("52:54:00:00:%02x:%02x", len(ifaces), k), "controller": ctrl,
				"ipv4": map[string]any{"enabled": true, "address": []any{map[string]any{"ip": "10.0.0.1", "prefix-length": int64(24)}}},
			})
		}
		put(tb, s.nns, map[string]any{
			"apiVersion": "nmstate.io/v1beta1", "kind": "NodeNetworkState",
			"metadata": map[string]any{"name": n.Name, "resourceVersion": "1"},
			"status":   map[string]any{"currentState": map[string]any{"interfaces": ifaces}},
		})
	}
	return s
}

// scaleUniverse is every namespace with its labels, as the api hands them to
// a Redaction.
func scaleUniverse() []NamespaceLabels {
	out := make([]NamespaceLabels, 0, scaleNamespaces)
	for i := range scaleNamespaces {
		out = append(out, NamespaceLabels{Name: scaleNS(i), Labels: scaleNSLabels(i)})
	}
	return out
}

// scaleTenant is one tenant's redaction: project proj-000's namespaces.
func scaleTenant(all []NamespaceLabels) *Redaction {
	return &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns < scaleNS(10) },
		Project:      func(p string) bool { return p == scaleProject(0) },
		Namespaces:   func() []NamespaceLabels { return all },
	}
}

// scaleLabelsOf answers a namespace's labels as clusterstate does: a lookup
// and a copy.
func scaleLabelsOf() func(string) map[string]string {
	held := make(map[string]map[string]string, scaleNamespaces)
	for i := range scaleNamespaces {
		held[scaleNS(i)] = scaleNSLabels(i)
	}
	return func(ns string) map[string]string { return maps.Clone(held[ns]) }
}

func scaleWorkload(i int, ip string, nets ...string) TraceWorkload {
	return TraceWorkload{
		Namespace: scaleNS(i), Name: "vm", NSLabels: scaleNSLabels(i),
		PodLabels: map[string]string{"app": "web", "vm.kubevirt.io/name": "vm"},
		IPs:       []string{ip}, PodNet: true, Nets: nets,
	}
}

// redactEffective redacts eff's cluster-tier bindings as the api does for a
// caller who may not list them.
func (s *Snapshot) redactEffective(eff *model.EffectivePolicy, red *Redaction) {
	for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
		for i := range bs {
			if bs[i].Policy.Namespace == "" {
				s.Redact(&bs[i].Policy, nil, red)
			}
		}
	}
}

func BenchmarkCatalog(b *testing.B) {
	s := scaleSnapshot(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Catalog()
	}
}

// A host's NodeNetworkState moves (its status churns), then a caller
// re-pulls the catalog.
func BenchmarkCatalogAfterHostMove(b *testing.B) {
	s := scaleSnapshot(b)
	host, _, _ := s.nns.GetByKey("worker-01")
	b.ReportAllocs()
	for b.Loop() {
		_ = s.nns.Update(host)
		_ = s.Catalog()
	}
}

// One NetworkPolicy moves, as a watch delivers it (a new copy at a new
// resourceVersion), then a caller re-pulls the policy plane.
func BenchmarkPoliciesAfterMove(b *testing.B) {
	s := scaleSnapshot(b)
	np, _, _ := s.netpol.GetByKey(scaleNS(4) + "/allow-web")
	rv := 1
	b.ReportAllocs()
	for b.Loop() {
		rv++
		u := np.(*unstructured.Unstructured).DeepCopy()
		u.SetResourceVersion(strconv.Itoa(rv))
		_ = s.netpol.Update(u)
		_ = s.Policies()
	}
}

func BenchmarkPolicies(b *testing.B) {
	s := scaleSnapshot(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Policies()
	}
}

func BenchmarkEffective(b *testing.B) {
	s := scaleSnapshot(b)
	all := scaleUniverse()
	pod := map[string]string{"app": "web"}
	b.ReportAllocs()
	for b.Loop() {
		eff := s.Effective(scaleNS(4), scaleNSLabels(4), pod, true, true, "")
		s.redactEffective(&eff, scaleTenant(all))
	}
}

func BenchmarkNetworkRules(b *testing.B) {
	s := scaleSnapshot(b)
	all := scaleUniverse()
	nss := make([]string, 0, scaleCUDNSpan)
	for k := range scaleCUDNSpan {
		nss = append(nss, scaleNS(k))
	}
	labelsOf := scaleLabelsOf()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.NetworkRules(nss, labelsOf, scaleTenant(all), "dotvirt.io/project")
	}
}

func BenchmarkTrace(b *testing.B) {
	s := scaleSnapshot(b)
	b.Run("east-west", func(b *testing.B) {
		src := scaleWorkload(150, "10.128.0.10", scaleNS(150)+"/shared-007")
		dst := scaleWorkload(151, "10.128.0.11", scaleNS(151)+"/shared-007")
		b.ReportAllocs()
		for b.Loop() {
			_ = s.Trace(nil, src, &dst, "", "TCP", 443)
		}
	})
	b.Run("external", func(b *testing.B) {
		src := scaleWorkload(10, "10.64.10.10", scaleNS(10)+"/shared-000")
		view := &TraceView{NADs: func(ns string) bool { return ns < scaleNS(20) }, UDNs: func(ns string) bool { return ns < scaleNS(20) }}
		b.ReportAllocs()
		for b.Loop() {
			_ = s.Trace(view, src, nil, "203.0.113.5", "TCP", 443)
		}
	})
}
