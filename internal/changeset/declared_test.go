package changeset

import (
	"slices"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// A namespaced document without metadata.namespace keeps its directory reading
// in the index and is reported; cluster-scoped kinds and the template library
// are not.
func TestUnnamespacedOnBranch(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/web.yaml":    []byte(liveVMYAML("web", "")),
		"alpha/loose.yaml":  []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: loose\n"),
		"alpha/policy.yaml": []byte("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: open\n---\napiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: pinned\n  namespace: alpha\n"),
		"root.yaml":         []byte("apiVersion: cdi.kubevirt.io/v1beta1\nkind: DataVolume\nmetadata:\n  name: disk\n"),
		"networks/wan.yaml": []byte("apiVersion: k8s.ovn.org/v1\nkind: ClusterUserDefinedNetwork\nmetadata:\n  name: wan\n"),
		"templates/t.yaml":  []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: t\n"),
	})
	c := newTestCoordinator(t, false)
	read := mustRead(t, c, project.ProjectInfo{Name: "p", Repo: bare})

	got, err := UnnamespacedOnBranch(read, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ObjectRef{
		{Kind: "VirtualMachine", Namespace: "alpha", Name: "loose"},
		{Kind: "NetworkPolicy", Namespace: "alpha", Name: "open"},
		{Kind: "DataVolume", Namespace: "default", Name: "disk"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("unnamespaced = %+v, want %+v", got, want)
	}
	idx, err := indexDeclared(read, "main")
	if err != nil {
		t.Fatal(err)
	}
	if idx.Files[want[0]] != "alpha/loose.yaml" {
		t.Errorf("the directory reading changed: %v", idx.Files)
	}
}

// Every namespaced manifest dotvirt renders into a tenant repo names its
// namespace: the Application applies a namespace-less one to its destination
// namespace, which need not be the file's directory.
func TestRenderedTenantManifestsCarryNamespace(t *testing.T) {
	render := map[string]func() (string, []byte, error){
		"vm": func() (string, []byte, error) {
			return vmgen.Manifest(vmgen.Spec{
				Name: "web", Namespace: "alpha", Instancetype: "u1.medium", Preference: "fedora",
				OSImage: vmgen.OSImageRef{Name: "fedora", Namespace: "os-images"},
			})
		},
		"udn": func() (string, []byte, error) {
			return netgen.Manifest(netgen.Spec{Name: "net", Namespace: "alpha"})
		},
		"networkpolicy": func() (string, []byte, error) {
			return netgen.NetworkPolicyManifest(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha"})
		},
		"egressfirewall": func() (string, []byte, error) {
			return netgen.EgressFirewallManifest(netgen.EgressFirewallSpec{Namespace: "alpha", Rules: []netgen.EgressRule{{Action: "Deny", CIDR: "0.0.0.0/0"}}})
		},
		"namespace with primary network": func() (string, []byte, error) {
			return netgen.NamespaceManifest(netgen.NamespaceSpec{Name: "alpha", Project: "p", VMNetwork: &netgen.PrimaryNet{Name: "net", Subnet: "10.40.0.0/16"}})
		},
		"rolebinding": func() (string, []byte, error) {
			return netgen.RoleBindingManifest(netgen.RoleBindingSpec{Namespace: "alpha", Project: "p", Owners: []string{"alice"}})
		},
	}
	for name, r := range render {
		path, content, err := r()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		refs, implied, _ := declared(path, content)
		if len(refs) == 0 || len(implied) != 0 {
			t.Errorf("%s: refs %v, namespace-less %v", name, refs, implied)
		}
	}
}
