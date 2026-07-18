package netstate

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func wl(ns, name string, nsLabels, podLabels map[string]string, ips ...string) TraceWorkload {
	if nsLabels == nil {
		nsLabels = map[string]string{"kubernetes.io/metadata.name": ns}
	}
	return TraceWorkload{Namespace: ns, Name: name, NSLabels: nsLabels, PodLabels: podLabels, IPs: ips, PodNet: true}
}

func stepKinds(t *testing.T, res model.TraceResult) []string {
	t.Helper()
	var out []string
	for _, s := range res.Steps {
		k := s.Stage
		if s.Direction != "" {
			k += "/" + strings.ToLower(s.Direction)
		}
		k += ":" + s.Action
		out = append(out, k)
	}
	return out
}

// A netpol allowing team-b on TCP/443 into team-a's web pods: the traced port
// decides — 443 allowed by the rule, 22 default-denied by the same selection.
func TestTraceEastWestNetpol(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "web-ingress", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"namespaceSelector": nameInSelector("team-b")}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}},
			}},
		},
	})
	src := wl("team-b", "client", nil, map[string]string{"app": "client"})
	dst := wl("team-a", "web", nil, map[string]string{"app": "web"})

	res := s.Trace(src, &dst, "", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("443: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var decisive *model.TraceStep
	for i := range res.Steps {
		if res.Steps[i].Stage == "dfw" && res.Steps[i].Decisive {
			decisive = &res.Steps[i]
		}
	}
	if decisive == nil || decisive.Policy.Name != "web-ingress" || decisive.Direction != "Ingress" {
		t.Fatalf("want decisive dfw ingress step for web-ingress, got %v", stepKinds(t, res))
	}

	res = s.Trace(src, &dst, "", "TCP", 22)
	if res.Verdict != "Deny" {
		t.Fatalf("22: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Stage != "dfw" || last.Action != "Deny" || !last.Decisive {
		t.Fatalf("22: want decisive dfw default-deny, got %v", stepKinds(t, res))
	}
}

// ANP precedence: a higher-priority (lower number) Deny wins before the
// project tier is ever consulted; a Pass delegates down to the netpol allow.
func TestTraceANPPrecedenceAndPass(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "lockdown"},
		"spec": map[string]any{
			"priority": int64(5),
			"subject":  map[string]any{"namespaces": nameInSelector("team-a")},
			"ingress": []any{map[string]any{
				"action": "Deny",
				"from":   []any{map[string]any{"namespaces": nameInSelector("team-b")}},
			}},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "allow-all", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
			"ingress":     []any{map[string]any{}},
		},
	})
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, map[string]string{"app": "web"})

	res := s.Trace(src, &dst, "", "TCP", 443)
	if res.Verdict != "Deny" {
		t.Fatalf("admin deny must win over netpol allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	// Pass at higher precedence delegates to the project tier instead.
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "delegate"},
		"spec": map[string]any{
			"priority": int64(1),
			"subject":  map[string]any{"namespaces": nameInSelector("team-a")},
			"ingress": []any{map[string]any{
				"action": "Pass",
				"from":   []any{map[string]any{"namespaces": nameInSelector("team-b")}},
			}},
		},
	})
	res = s.Trace(src, &dst, "", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("pass then netpol allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var sawPass bool
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Action == "Pass" && st.Decisive {
			sawPass = true
		}
	}
	if !sawPass {
		t.Fatalf("want a decisive Pass step, got %v", stepKinds(t, res))
	}
}

// BANP decides only when nothing above did; with no policy at all the network
// default allows.
func TestTraceBaselineAndDefault(t *testing.T) {
	s := New(nil, nil)
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, nil)

	res := s.Trace(src, &dst, "", "TCP", 80)
	if res.Verdict != "Allow" {
		t.Fatalf("no policy: verdict = %s", res.Verdict)
	}

	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"ingress": []any{map[string]any{
				"action": "Deny",
				"from":   []any{map[string]any{"namespaces": map[string]any{}}},
			}},
		},
	})
	res = s.Trace(src, &dst, "", "TCP", 80)
	if res.Verdict != "Deny" {
		t.Fatalf("baseline deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var baseline bool
	for _, st := range res.Steps {
		if st.Stage == "baseline" && st.Decisive {
			baseline = true
		}
	}
	if !baseline {
		t.Fatalf("want a decisive baseline step, got %v", stepKinds(t, res))
	}
}

// Isolated primary networks are unreachable before any policy runs; a shared
// CUDN-generated secondary segment is surfaced as a bypass, not a verdict.
func TestTraceUnreachableAndSegmentBypass(t *testing.T) {
	s := New(nil, nil)
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "isolated", "namespace": "team-a"},
		"spec": map[string]any{
			"topology": "Layer2",
			"layer2":   map[string]any{"role": "Primary"},
		},
	})
	for _, ns := range []string{"team-a", "team-b"} {
		add(t, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "shared-vlan", "namespace": ns,
				"ownerReferences": []any{map[string]any{
					"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
					"name": "shared-vlan", "uid": "x",
				}},
			},
		})
	}
	src := wl("team-a", "web", nil, nil)
	src.Nets = []string{"team-a/shared-vlan"}
	dst := wl("team-b", "db", nil, nil)
	dst.Nets = []string{"team-b/shared-vlan"}

	res := s.Trace(src, &dst, "", "TCP", 5432)
	if res.Verdict != "Unreachable" {
		t.Fatalf("isolated primaries: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var bypass bool
	for _, st := range res.Steps {
		if st.Stage == "segment" && st.Action == "Bypass" {
			bypass = true
		}
		if st.Stage == "admin" || st.Stage == "dfw" || st.Stage == "baseline" {
			t.Fatalf("unreachable flow must not walk policy, got %v", stepKinds(t, res))
		}
	}
	if !bypass {
		t.Fatalf("shared segment must surface as bypass, got %v", stepKinds(t, res))
	}
}

// External flow: the gateway firewall is first-match with default allow, the
// SNAT pool reports informationally, and an ANP networks-peer deny is final.
func TestTraceExternal(t *testing.T) {
	s := New(nil, nil)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"cidrSelector": "198.51.100.0/24"}},
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "prod-egress"},
		"spec": map[string]any{
			"egressIPs":         []any{"192.0.2.10"},
			"namespaceSelector": nameInSelector("team-a"),
		},
	})
	src := wl("team-a", "web", nil, nil, "10.128.2.5")

	res := s.Trace(src, nil, "198.51.100.7", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("allowed CIDR: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var snat bool
	for _, st := range res.Steps {
		if st.Stage == "snat" && st.Action == "SNAT" {
			snat = true
		}
	}
	if !snat {
		t.Fatalf("want the SNAT plane reported, got %v", stepKinds(t, res))
	}

	if res := s.Trace(src, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Deny" {
		t.Fatalf("catch-all deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	// An admin networks-peer deny decides before the gateway is consulted.
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "no-metadata"},
		"spec": map[string]any{
			"priority": int64(2),
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"egress": []any{map[string]any{
				"action": "Deny",
				"to":     []any{map[string]any{"networks": []any{"198.51.100.0/24"}}},
			}},
		},
	})
	if res := s.Trace(src, nil, "198.51.100.7", "TCP", 443); res.Verdict != "Deny" {
		t.Fatalf("admin networks deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Unresolvable rules downgrade to Conditional, never silently drop: a named
// port, a stopped VM's unknown addresses against a CIDR peer, a DNS rule.
func TestTraceConditional(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "named-port", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"ports": []any{map[string]any{"protocol": "TCP", "port": "web"}},
			}},
		},
	})
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, nil)
	res := s.Trace(src, &dst, "", "TCP", 8080)
	if res.Verdict != "Conditional" {
		t.Fatalf("named port: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var cond bool
	for _, st := range res.Steps {
		if st.Conditional && st.Note != "" {
			cond = true
		}
	}
	if !cond {
		t.Fatalf("conditional step must say why, got %+v", res.Steps)
	}

	// A stopped VM (no addresses) against an ipBlock allow: unresolvable.
	s2 := New(nil, nil)
	add(t, s2.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "cidr-only", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{"ipBlock": map[string]any{"cidr": "10.128.0.0/14"}}},
			}},
		},
	})
	stopped := wl("team-b", "client", nil, nil) // no IPs
	if res := s2.Trace(stopped, &dst, "", "TCP", 443); res.Verdict != "Conditional" {
		t.Fatalf("no addresses vs ipBlock: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	// A DNS-name gateway rule against a bare IP: unresolvable.
	s3 := New(nil, nil)
	add(t, s3.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"dnsName": "mirror.example.com"}},
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	live := wl("team-a", "web", nil, nil, "10.128.2.5")
	if res := s3.Trace(live, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Conditional" {
		t.Fatalf("dns rule before catch-all deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// A gateway rule whose destination the trace cannot resolve (nodeSelector)
// must stay visible as Conditional — dropping it would let the catch-all
// decide with false certainty.
func TestTraceGatewayNodeSelector(t *testing.T) {
	s := New(nil, nil)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Deny", "to": map[string]any{"nodeSelector": map[string]any{}}},
			map[string]any{"type": "Allow", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	src := wl("team-a", "web", nil, nil, "10.128.2.5")

	res := s.Trace(src, nil, "203.0.113.9", "TCP", 443)
	if res.Verdict != "Conditional" {
		t.Fatalf("node-selector deny before catch-all allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var cond bool
	for _, st := range res.Steps {
		if st.Stage == "gateway" && st.Conditional && st.Rule != nil && st.Rule.Peer == "cluster nodes" {
			cond = true
		}
	}
	if !cond {
		t.Fatalf("want a conditional gateway step for the node rule, got %+v", res.Steps)
	}
}

// An ANP nodes-peer can match an external target (the address may be a
// node's) but never a VM's pod-net address.
func TestTraceANPNodesPeer(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "protect-nodes"},
		"spec": map[string]any{
			"priority": int64(3),
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"egress": []any{map[string]any{
				"action": "Deny",
				"to":     []any{map[string]any{"nodes": map[string]any{}}},
			}},
		},
	})
	src := wl("team-a", "web", nil, nil, "10.128.2.5")

	if res := s.Trace(src, nil, "192.0.2.20", "TCP", 22); res.Verdict != "Conditional" {
		t.Fatalf("external target may be a node: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	dst := wl("team-b", "db", nil, nil, "10.128.3.7")
	if res := s.Trace(src, &dst, "", "TCP", 22); res.Verdict != "Allow" {
		t.Fatalf("nodes peer must not match a VM: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// A maybe-matching Pass converges once the walk leaves the admin tier: both
// branches continue into the same tiers, so the verdict stays certain.
func TestTracePassConvergence(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "maybe-pass"},
		"spec": map[string]any{
			"priority": int64(1),
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"egress": []any{map[string]any{
				"action": "Pass",
				"to":     []any{map[string]any{"networks": []any{"10.0.0.0/8"}}},
			}},
		},
	})
	src := wl("team-b", "client", nil, nil, "10.128.2.5")
	dst := wl("team-a", "web", nil, nil) // stopped: the Pass peer can't resolve

	res := s.Trace(src, &dst, "", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("conditional Pass with converging branches: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var sawCondPass bool
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Action == "Pass" && st.Conditional {
			sawCondPass = true
		}
	}
	if !sawCondPass {
		t.Fatalf("the maybe-matching Pass must stay visible, got %v", stepKinds(t, res))
	}
}

// A default multus binding substitutes the NAD's segment for the pod network:
// it never rides the namespace primary, and two such bindings meet only on
// the same segment identity.
func TestTraceDefaultMultusSegment(t *testing.T) {
	s := New(nil, nil)
	for _, ns := range []string{"team-a", "team-b"} {
		add(t, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "shared-vlan", "namespace": ns,
				"ownerReferences": []any{map[string]any{
					"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
					"name": "shared-vlan", "uid": "x",
				}},
			},
		})
	}

	onDefault := wl("team-a", "web", nil, nil)
	onDefault.PodNet = false
	onDefault.DefaultNet = "team-a/vlan-x"
	onPod := wl("team-b", "db", nil, nil)
	if res := s.Trace(onDefault, &onPod, "", "TCP", 5432); res.Verdict != "Unreachable" {
		t.Fatalf("substituted default vs cluster default: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	a := wl("team-a", "web", nil, nil)
	a.PodNet, a.DefaultNet = false, "team-a/shared-vlan"
	b := wl("team-b", "db", nil, nil)
	b.PodNet, b.DefaultNet = false, "team-b/shared-vlan"
	if res := s.Trace(a, &b, "", "TCP", 5432); res.Verdict != "Allow" {
		t.Fatalf("same CUDN segment as default: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// netpol peer semantics: a bare podSelector means the policy's own namespace —
// the same labels in another namespace must not match.
func TestTraceNetpolSameNamespacePeer(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "from-web", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}},
			}},
		},
	})
	dst := wl("team-a", "db", nil, map[string]string{"app": "db"})

	same := wl("team-a", "web", nil, map[string]string{"app": "web"})
	if res := s.Trace(same, &dst, "", "TCP", 5432); res.Verdict != "Allow" {
		t.Fatalf("same-ns web: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	other := wl("team-b", "web", nil, map[string]string{"app": "web"})
	if res := s.Trace(other, &dst, "", "TCP", 5432); res.Verdict != "Deny" {
		t.Fatalf("other-ns web: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}
