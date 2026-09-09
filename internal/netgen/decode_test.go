package netgen

import (
	"reflect"
	"strings"
	"testing"
)

// Every renderer's output decodes back to the spec that produced it: the edit
// form starts from exactly what the create form submitted.
func TestDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		spec   any
		render func() ([]byte, error)
	}{
		{"udn", Spec{Name: "db-net", Scope: ScopeProject, Namespace: "tenant-a", Subnets: []string{"10.20.0.0/24"}}, nil},
		{"cudn", Spec{Name: "shared", Scope: ScopeShared, Namespaces: []string{"a", "b"}}, nil},
		{"vlan", Spec{Name: "prod", Scope: ScopeVLAN, VLAN: 200, PhysicalNetwork: "physnet", Namespaces: []string{"a"}, Subnets: []string{"10.0.0.0/24"}}, nil},
		{"netpol", NetworkPolicySpec{Name: "web", Namespace: "a", AppliedTo: map[string]string{"app": "db"},
			Ingress: []PolicyRule{{From: []map[string]string{{"app": "web"}}, Ports: []PolicyPort{{Protocol: "TCP", Port: 5432}}}}}, nil},
		{"anp", AdminNetworkPolicySpec{Name: "iso", Priority: 10, Subject: map[string]string{"tier": "prod"},
			Ingress: []AdminPolicyRule{{Action: "Deny", Peers: []map[string]string{{}}}},
			Egress:  []AdminPolicyRule{{Action: "Allow", Peers: []map[string]string{{"team": "x"}}, Ports: []PolicyPort{{Protocol: "UDP", Port: 53}}}}}, nil},
		{"banp", AdminNetworkPolicySpec{Name: "default", Baseline: true, Ingress: []AdminPolicyRule{{Action: "Deny", Peers: []map[string]string{{}}}}}, nil},
		{"egressfw", EgressFirewallSpec{Namespace: "a", Rules: []EgressRule{
			{Action: "Allow", DNSName: "api.example.com", Ports: []PolicyPort{{Protocol: "TCP", Port: 443}}},
			{Action: "Deny", CIDR: "0.0.0.0/0"}}}, nil},
		{"egressip", EgressIPSpec{Name: "snat", EgressIPs: []string{"192.0.2.10"}, Namespaces: []string{"a"}}, nil},
		{"route", ExternalRouteSpec{Name: "gw", Namespaces: []string{"a"}, NextHops: []string{"10.0.0.1"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var content []byte
			var err error
			switch s := tc.spec.(type) {
			case Spec:
				_, content, err = Manifest(s)
			case NetworkPolicySpec:
				_, content, err = NetworkPolicyManifest(s)
			case AdminNetworkPolicySpec:
				_, content, err = AdminNetworkPolicyManifest(s)
			case EgressFirewallSpec:
				_, content, err = EgressFirewallManifest(s)
			case EgressIPSpec:
				_, content, err = EgressIPManifest(s)
			case ExternalRouteSpec:
				_, content, err = ExternalRouteManifest(s)
			}
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			got, err := Decode(content)
			if err != nil {
				t.Fatalf("Decode: %v\n%s", err, content)
			}
			if !reflect.DeepEqual(got, tc.spec) {
				t.Errorf("round trip mismatch\n got: %#v\nwant: %#v", got, tc.spec)
			}
		})
	}
}

// A manifest carrying what the form cannot express is refused, not approximated:
// decoding it and re-rendering would silently drop the extra settings.
func TestDecodeRefusesForeignSettings(t *testing.T) {
	_, content, err := NetworkPolicyManifest(NetworkPolicySpec{Name: "web", Namespace: "a"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(content), "policyTypes:", "egress: []\n  policyTypes:", 1)
	if _, err := Decode([]byte(foreign)); err == nil {
		t.Errorf("a NetworkPolicy with an egress section must not decode")
	}
	if _, err := Decode([]byte("kind: Deployment\nmetadata:\n  name: x\n")); err == nil {
		t.Errorf("a kind without a form must not decode")
	}
}
