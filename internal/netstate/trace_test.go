package netstate

import (
	"slices"
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
// decides - 443 allowed by the rule, 22 default-denied by the same selection.
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

	res := s.Trace(nil, src, &dst, "", "TCP", 443)
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

	res = s.Trace(nil, src, &dst, "", "TCP", 22)
	if res.Verdict != "Deny" {
		t.Fatalf("22: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Stage != "dfw" || last.Action != "Deny" || !last.Decisive {
		t.Fatalf("22: want decisive dfw default-deny, got %v", stepKinds(t, res))
	}
}

// ANP precedence: a higher-priority (lower number) Deny wins before the
// project rule is ever consulted; a Pass delegates down to the netpol allow.
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

	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Deny" {
		t.Fatalf("admin deny must win over netpol allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	// Pass at higher precedence delegates to the project rules instead.
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
	res = s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("pass then netpol allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var sawPass bool
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Action == "Pass" && st.Decisive {
			sawPass = st.Note == "Delegates this flow to the project rules."
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

	res := s.Trace(nil, src, &dst, "", "TCP", 80)
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
	res = s.Trace(nil, src, &dst, "", "TCP", 80)
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

// Isolated primary networks carry nothing, so no policy runs; a shared
// CUDN-generated secondary network is then the only path, unfiltered: the
// answer is Conditional with the bypass as its reason, never a certain
// Unreachable. Without one, Unreachable is certain.
func TestTraceUnreachableAndSegmentBypass(t *testing.T) {
	s := New(nil, nil)
	s.routeAds.Store(routeAdsAbsent)
	primaryUDN(t, s, "team-a", "isolated", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary"}})
	secondaryCUDN(t, s, "shared-vlan", "x")
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

	res := s.Trace(nil, src, &dst, "", "TCP", 5432)
	if res.Verdict != "Conditional" {
		t.Fatalf("isolated primaries sharing a network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var bypass bool
	for _, st := range res.Steps {
		if st.Stage == "segment" && st.Action == "Bypass" && st.Conditional {
			bypass = true
		}
		if st.Decisive || st.Stage == "admin" || st.Stage == "dfw" || st.Stage == "baseline" {
			t.Fatalf("no step decides a flow only the bypass can carry, got %+v", res.Steps)
		}
	}
	if !bypass {
		t.Fatalf("shared network must surface as a flagged bypass, got %v", stepKinds(t, res))
	}

	dst.Nets = nil
	if res := s.Trace(nil, src, &dst, "", "TCP", 5432); res.Verdict != "Unreachable" {
		t.Fatalf("isolated primaries, nothing shared: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// External flow: the outbound rules are first-match with default allow, the
// fixed outbound IP pool reports informationally, and an ANP networks-peer deny is final.
func TestTraceExternal(t *testing.T) {
	s := New(nil, nil)
	s.clusterNet = parsePrefixes([]string{"10.128.0.0/14"}) // the pod range the outbound rules exclude
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

	res := s.Trace(nil, src, nil, "198.51.100.7", "TCP", 443)
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

	if res := s.Trace(nil, src, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Deny" {
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
	if res := s.Trace(nil, src, nil, "198.51.100.7", "TCP", 443); res.Verdict != "Deny" {
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
	res := s.Trace(nil, src, &dst, "", "TCP", 8080)
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
	if res := s2.Trace(nil, stopped, &dst, "", "TCP", 443); res.Verdict != "Conditional" {
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
	if res := s3.Trace(nil, live, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Conditional" {
		t.Fatalf("dns rule before catch-all deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// A gateway rule whose destination the trace cannot resolve (nodeSelector)
// must stay visible as Conditional - dropping it would let the catch-all
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

	res := s.Trace(nil, src, nil, "203.0.113.9", "TCP", 443)
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

// An unresolvable gateway rule that could only allow, followed by nothing,
// agrees with the gateway's default allow: the answer is certain, since no
// resolution of the rule could change it.
func TestTraceGatewayMaybeAllowAgreesWithDefault(t *testing.T) {
	s := New(nil, nil)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"dnsName": "mirror.example.com"}},
		}},
	})
	src := wl("team-a", "web", nil, nil, "10.128.2.5")

	res := s.Trace(nil, src, nil, "203.0.113.9", "TCP", 443)
	if res.Verdict != "Allow" {
		t.Fatalf("dns allow before the default allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
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

	if res := s.Trace(nil, src, nil, "192.0.2.20", "TCP", 22); res.Verdict != "Conditional" {
		t.Fatalf("external target may be a node: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	dst := wl("team-b", "db", nil, nil, "10.128.3.7")
	if res := s.Trace(nil, src, &dst, "", "TCP", 22); res.Verdict != "Allow" {
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

	res := s.Trace(nil, src, &dst, "", "TCP", 443)
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

// A Multus default binding is a cluster NIC only to a NAD a Primary-role
// network generated: two such bindings on one network walk the policies like
// pods on it. Any other default binding is off the cluster network, so no
// policy walk runs and nothing is certain: a shared one is the layer-2
// bypass, and one side off the cluster network reaches the other's exposed
// services through the fabric.
func TestTraceDefaultMultusSegment(t *testing.T) {
	s := isolated(t, false)
	secondaryCUDN(t, s, "shared-vlan", "y")
	for _, ns := range []string{"team-a", "team-b"} {
		add(t, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "shared-vlan", "namespace": ns,
				"ownerReferences": []any{map[string]any{
					"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
					"name": "shared-vlan", "uid": "y",
				}},
			},
		})
	}
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "vlan42", "namespace": "team-a"},
		"spec":     map[string]any{"config": `{"type":"ovn-k8s-cni-overlay","topology":"localnet","vlanID":42}`},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny", "namespace": "team-a"},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}},
	})
	bound := func(ns, name, ref string) TraceWorkload {
		w := wl(ns, name, nil, nil)
		w.PodNet, w.DefaultNet = false, ref
		return w
	}
	maybe := func(name string, src, dst TraceWorkload, bypass string) {
		t.Helper()
		res := s.Trace(nil, src, &dst, "", "TCP", 5432)
		if res.Verdict != "Conditional" {
			t.Errorf("%s: verdict = %s (%v)", name, res.Verdict, stepKinds(t, res))
		}
		var named bool
		for _, st := range res.Steps {
			if st.Decisive || st.Stage != "connectivity" && st.Stage != "segment" {
				t.Errorf("%s: a policy step or a certain one, %+v", name, st)
			}
			named = named || st.Action == "Bypass" && st.Conditional && strings.Contains(st.Note, bypass)
		}
		if !named {
			t.Errorf("%s: no conditional bypass naming %s in %v", name, bypass, stepKinds(t, res))
		}
	}

	maybe("a localnet default binding on each side, a deny-all beside",
		bound("team-a", "web", "team-a/vlan42"), bound("team-a", "db", "team-a/vlan42"), "team-a/vlan42")
	web, db := bound("team-a", "web", "team-a/vlan42"), bound("team-a", "db", "team-a/vlan42")
	if !slices.ContainsFunc(s.Trace(nil, web, &db, "", "TCP", 5432).Steps, func(st model.TraceStep) bool {
		return st.Stage == "segment" && st.Action == "Bypass"
	}) {
		t.Error("one localnet default binding on each side: no shared segment")
	}
	maybe("a secondary network's default binding on each side",
		bound("team-a", "web", "team-a/shared-vlan"), bound("team-b", "db", "team-b/shared-vlan"), "shared-vlan")
	primaryUDN(t, s, "team-a", "side-udn", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary"}})
	maybe("a secondary UDN's default binding against the cluster network",
		bound("team-a", "web", "team-a/side-udn"), wl("team-b", "db", nil, nil), "side-udn")
	add(t, s.nad, ownedNAD("tenant-a", "alias", "UserDefinedNetwork", "net-a", "tenant-a/net-a", ""))
	maybe("a NAD its primary UDN owns under another name, against that UDN",
		bound("tenant-a", "a1", "tenant-a/alias"), wl("tenant-a", "a2", nil, nil), "net-a")
	maybe("an unread NAD's default binding against the cluster network",
		bound("team-a", "web", "team-a/vlan-x"), wl("team-b", "db", nil, nil), "team-a/vlan-x")
	maybe("the cluster network against a localnet default binding",
		wl("team-b", "db", nil, nil), bound("team-a", "web", "team-a/vlan42"), "team-a/vlan42")
	maybe("a localnet default binding against a VM Network",
		bound("team-a", "web", "team-a/vlan42"), wl("tenant-a", "a1", nil, nil), "team-a/vlan42")

	a, b := bound("tenant-c", "web", "tenant-c/shared"), wl("tenant-d", "db", nil, nil)
	if res := s.Trace(nil, a, &b, "", "TCP", 5432); res.Verdict != "Allow" {
		t.Errorf("a primary network's default binding beside a pod on it: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	none := wl("team-a", "web", nil, nil)
	none.PodNet = false
	if res := s.Trace(nil, none, &b, "", "TCP", 5432); res.Verdict != "Unreachable" {
		t.Errorf("a VM with no NIC: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// netpol peer semantics: a bare podSelector means the policy's own namespace -
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
	if res := s.Trace(nil, same, &dst, "", "TCP", 5432); res.Verdict != "Allow" {
		t.Fatalf("same-ns web: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	other := wl("team-b", "web", nil, map[string]string{"app": "web"})
	if res := s.Trace(nil, other, &dst, "", "TCP", 5432); res.Verdict != "Deny" {
		t.Fatalf("other-ns web: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Localnet networks meet on the fabric: different objects mapping one physical
// network and VLAN are one layer-2 domain, so the trace must surface the
// bypass; another VLAN on the same physical network is not.
func TestTraceLocalnetSegmentByPhysnetVLAN(t *testing.T) {
	s := New(nil, nil)
	nad := func(ns, name, config string, owner ...any) {
		meta := map[string]any{"name": name, "namespace": ns}
		if len(owner) > 0 {
			meta["ownerReferences"] = owner
		}
		add(t, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": meta, "spec": map[string]any{"config": config},
		})
	}
	secondaryCUDN(t, s, "dc-100", "x")
	nad("team-a", "dc-100", `{"name":"cluster_udn_dc-100","topology":"localnet","physicalNetworkName":"datacenter","vlanID":100}`,
		map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "dc-100", "uid": "x"})
	nad("team-b", "legacy", `{"name":"datacenter","topology":"localnet","vlanID":100}`)
	nad("team-b", "other-vlan", `{"name":"other","topology":"localnet","physicalNetworkName":"datacenter","vlanID":200}`)

	src := wl("team-a", "web", nil, nil)
	src.Nets = []string{"team-a/dc-100"}
	dst := wl("team-b", "db", nil, nil)
	dst.Nets = []string{"team-b/legacy"}
	res := s.Trace(nil, src, &dst, "", "TCP", 5432)
	var bypass int
	for _, st := range res.Steps {
		if st.Stage == "segment" && st.Action == "Bypass" {
			bypass++
		}
	}
	if bypass != 1 {
		t.Fatalf("same physnet+VLAN must be one shared network, got %v", stepKinds(t, res))
	}

	dst.Nets = []string{"team-b/other-vlan"}
	for _, st := range s.Trace(nil, src, &dst, "", "TCP", 5432).Steps {
		if st.Stage == "segment" {
			t.Fatalf("another VLAN is another domain, got %+v", st)
		}
	}
}

func anp(t *testing.T, s *Snapshot, name string, priority int64, egress ...any) {
	t.Helper()
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"priority": priority,
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"egress":   egress,
		},
	})
}

func anpEgress(action string, namespaces ...any) map[string]any {
	return map[string]any{"action": action, "to": []any{map[string]any{"namespaces": nameInSelector(namespaces...)}}}
}

// OVN-K leaves the order between equal-priority ANPs undefined: when more than
// one of them may decide, no verdict is certain. One deciding policy at a
// shared priority is still certain, and agreeing Passes still converge.
func TestTraceEqualPriorityANPs(t *testing.T) {
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, nil)

	s := New(nil, nil)
	anp(t, s, "a-deny", 5, anpEgress("Deny", "team-a"))
	anp(t, s, "b-allow", 5, anpEgress("Allow", "team-a"))
	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Conditional" {
		t.Fatalf("conflicting equal-priority ANPs: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var flagged int
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Conditional && strings.Contains(st.Note, "same priority") {
			flagged++
		}
	}
	if flagged != 2 {
		t.Fatalf("both rules must stay visible and flagged, got %+v", res.Steps)
	}

	s = New(nil, nil)
	anp(t, s, "a-deny", 5, anpEgress("Deny", "team-a"))
	anp(t, s, "b-elsewhere", 5, anpEgress("Allow", "team-c"))
	if res := s.Trace(nil, src, &dst, "", "TCP", 443); res.Verdict != "Deny" {
		t.Fatalf("one deciding policy at the priority: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	s = New(nil, nil)
	anp(t, s, "a-pass", 5, anpEgress("Pass", "team-a"))
	anp(t, s, "b-pass", 5, anpEgress("Pass", "team-a"))
	if res := s.Trace(nil, src, &dst, "", "TCP", 443); res.Verdict != "Allow" {
		t.Fatalf("agreeing passes converge: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// A NetworkPolicy whose selection cannot be resolved may not apply, so its
// allow rule is a maybe, never the deciding rule.
func TestTraceProjectSelectionUnresolved(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "isolate", "namespace": "team-a"},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "unreadable", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchExpressions": []any{
				map[string]any{"key": "app", "operator": "Bogus"},
			}},
			"policyTypes": []any{"Ingress"},
			"ingress":     []any{map[string]any{}},
		},
	})
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, map[string]string{"app": "web"})
	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Conditional" {
		t.Fatalf("allow from an unresolved selection: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// An external flow is filtered only on the primary NIC. With no primary, a
// secondary NIC is the only way out and no cluster control applies; beside a
// primary, a VLAN NIC leaves a cluster Deny inconclusive; with no NIC at all
// nothing leaves.
func TestTraceExternalVLAN(t *testing.T) {
	s := New(nil, nil)
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "vlan10", "namespace": "team-a"},
		"spec":     map[string]any{"config": `{"name":"vlan10","topology":"localnet","physicalNetworkName":"dc","vlanID":10}`},
	})
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})

	vlanOnly := wl("team-a", "appliance", nil, nil, "192.0.2.5")
	vlanOnly.PodNet, vlanOnly.Nets = false, []string{"team-a/vlan10"}
	res := s.Trace(nil, vlanOnly, nil, "203.0.113.9", "TCP", 443)
	if res.Verdict != "Conditional" {
		t.Fatalf("VLAN-only source: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	for _, st := range res.Steps {
		if st.Stage == "gateway" || st.Stage == "admin" || st.Stage == "default" {
			t.Fatalf("no cluster control filters a VLAN-only source, got %v", stepKinds(t, res))
		}
	}

	both := wl("team-a", "web", nil, nil, "10.128.2.5")
	both.Nets = []string{"team-a/vlan10"}
	if res := s.Trace(nil, both, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Conditional" {
		t.Fatalf("primary plus VLAN under a gateway deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	internal := wl("team-a", "backend", nil, nil)
	internal.PodNet, internal.Nets = false, []string{"team-a/db-net"}
	if res := s.Trace(nil, internal, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Conditional" {
		t.Fatalf("secondary-only source, a router may carry it: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	raw := wl("team-a", "router", nil, nil, "192.168.1.5")
	raw.PodNet, raw.DefaultNet = false, "team-a/bridge"
	res = s.Trace(nil, raw, nil, "203.0.113.9", "TCP", 443)
	if res.Verdict != "Conditional" || slices.ContainsFunc(res.Steps, func(st model.TraceStep) bool { return st.Stage == "gateway" }) {
		t.Fatalf("a default binding no primary network generated, under a gateway deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	bare := wl("team-a", "island", nil, nil)
	bare.PodNet = false
	if res := s.Trace(nil, bare, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Unreachable" {
		t.Fatalf("no NIC: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// The gateway runs in its own OVN ACL stage: an admin Allow ends the egress
// tiers but never skips the EgressFirewall.
func TestTraceExternalAdminAllowMeetsGateway(t *testing.T) {
	s := New(nil, nil)
	s.clusterNet = parsePrefixes([]string{"10.128.0.0/14"}) // the pod range the outbound rules exclude
	anp(t, s, "allow-partner", 1, map[string]any{
		"action": "Allow", "to": []any{map[string]any{"networks": []any{"198.51.100.0/24"}}},
	})
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	src := wl("team-a", "web", nil, nil, "10.128.2.5")
	res := s.Trace(nil, src, nil, "198.51.100.7", "TCP", 443)
	if res.Verdict != "Deny" {
		t.Fatalf("admin allow then gateway deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Stage != "gateway" || last.Action != "Deny" || !last.Decisive {
		t.Fatalf("the gateway must decide, got %v", stepKinds(t, res))
	}
}

// OVN-K never programs an ANP past priority 99, but a stale in-range version
// may still be: its rules stay visible and only ever weigh as maybes, never as
// the deciding Deny.
func TestTraceUnenforcedANPPriority(t *testing.T) {
	src := wl("team-b", "client", nil, nil)
	dst := wl("team-a", "web", nil, nil)

	s := New(nil, nil)
	anp(t, s, "too-low", 500, anpEgress("Deny", "team-a"))
	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Conditional" {
		t.Fatalf("unenforced deny vs default allow: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var noted bool
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Action == "Deny" {
			if st.Decisive || !st.Conditional || !strings.Contains(st.Note, "does not enforce priority 500") {
				t.Fatalf("unenforced rule must be a flagged maybe, got %+v", st)
			}
			noted = true
		}
	}
	if !noted {
		t.Fatalf("unenforced rule must stay visible, got %v", stepKinds(t, res))
	}

	// An enforced lower priority number still decides, and agrees.
	anp(t, s, "lockdown", 10, anpEgress("Deny", "team-a"))
	if res := s.Trace(nil, src, &dst, "", "TCP", 443); res.Verdict != "Deny" {
		t.Fatalf("enforced deny agreeing with the unenforced one: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// secondaryCUDN adds a secondary CUDN whose generated NADs reference uid.
func secondaryCUDN(t *testing.T, s *Snapshot, name, uid string) {
	t.Helper()
	add(t, s.cudn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
		"metadata": map[string]any{"name": name, "uid": uid},
		"spec":     map[string]any{"network": map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary"}}},
	})
}
