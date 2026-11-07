package changeset

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// A namespaced document without metadata.namespace keeps its directory reading
// in the index and is reported as implied; the rest are grouped by the
// namespace they name. Cluster-scoped kinds and the template library are
// neither.
func TestPlacementOnBranch(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/web.yaml":    []byte(liveVMYAML("web", "")),
		"alpha/loose.yaml":  []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: loose\n"),
		"alpha/policy.yaml": []byte("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: open\n---\napiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: pinned\n  namespace: beta\n"),
		"root.yaml":         []byte("apiVersion: cdi.kubevirt.io/v1beta1\nkind: DataVolume\nmetadata:\n  name: disk\n"),
		"networks/wan.yaml": []byte("apiVersion: k8s.ovn.org/v1\nkind: ClusterUserDefinedNetwork\nmetadata:\n  name: wan\n"),
		"alpha/vlan.yaml":   []byte("apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: vlan\n  namespace: alpha\n"),
		"templates/t.yaml":  []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: t\n"),
	})
	c := newTestCoordinator(t, false)
	read := mustRead(t, c, project.ProjectInfo{Name: "p", Repo: bare})

	got, err := PlacementOnBranch(read, "main")
	if err != nil {
		t.Fatal(err)
	}
	implied := []model.ObjectRef{
		{Kind: "VirtualMachine", Namespace: "alpha", Name: "loose"},
		{Kind: "NetworkPolicy", Namespace: "alpha", Name: "open"},
		{Kind: "DataVolume", Namespace: "default", Name: "disk"},
	}
	if !slices.Equal(got.Implied, implied) {
		t.Errorf("implied = %+v, want %+v", got.Implied, implied)
	}
	platform := []model.ObjectRef{
		{Kind: "NetworkAttachmentDefinition", Namespace: "alpha", Name: "vlan"},
		{Kind: "ClusterUserDefinedNetwork", Name: "wan"},
	}
	if !slices.Equal(got.PlatformKinds, platform) {
		t.Errorf("platform kinds = %+v, want %+v", got.PlatformKinds, platform)
	}
	if !slices.Equal(got.Namespaces, []string{"alpha", "beta"}) ||
		!slices.Equal(got.Named["alpha"], []model.ObjectRef{{Kind: "NetworkAttachmentDefinition", Namespace: "alpha", Name: "vlan"}, {Kind: "VirtualMachine", Namespace: "alpha", Name: "web"}}) ||
		!slices.Equal(got.Named["beta"], []model.ObjectRef{{Kind: "NetworkPolicy", Namespace: "beta", Name: "pinned"}}) {
		t.Errorf("named = %v %+v", got.Namespaces, got.Named)
	}
	idx, err := indexDeclared(read, "main")
	if err != nil {
		t.Fatal(err)
	}
	if idx.Files[implied[0]] != "alpha/loose.yaml" {
		t.Errorf("the directory reading changed: %v", idx.Files)
	}
}

// An edit, a form render or an adoption cannot stage a document that leaves
// its namespace to the directory: the manifest editor stripping
// metadata.namespace is refused, not staged.
func TestStagingRefusesImpliedNamespace(t *testing.T) {
	pinned := "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  podSelector: {}\n"
	bare := seedBareFiles(t, map[string][]byte{"alpha/web.yaml": []byte(pinned)})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}

	stripped := strings.Replace(pinned, "  namespace: alpha\n", "", 1)
	stripped = strings.Replace(stripped, "podSelector: {}", "podSelector: {matchLabels: {a: b}}", 1)
	if _, err := c.StageUpdateManifest(id, proj, string(draft.ResourceNetworkPolicy), "alpha", "web", stripped); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("namespace-less edit: err = %v, want ErrInvalid", err)
	}
	loose := model.Adoptable{Kind: "NetworkPolicy", Namespace: "alpha", Name: "new", Path: "alpha/networkpolicies/new.yaml",
		Manifest: []byte("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: new\n")}
	if _, err := c.AdoptObject(id, proj, loose); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("namespace-less adoption: err = %v, want ErrInvalid", err)
	}
	if _, err := c.AdoptObjects(id, proj, "alpha", []model.Adoptable{loose}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("namespace-less bulk adoption: err = %v, want ErrInvalid", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("refused stages left %+v", entries)
	}
}

// Every namespaced manifest dotvirt renders names its namespace: the
// Application applies a namespace-less one to its destination namespace, which
// need not be the file's directory. What it renders into a tenant repo is a
// tenant kind: a project's networks are UDNs, never NADs.
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
	tenant := project.ProjectInfo{Name: "p"}
	for name, r := range render {
		path, content, err := r()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		refs, implied, _ := declared(path, content)
		if len(refs) == 0 || len(implied) != 0 {
			t.Errorf("%s: refs %v, namespace-less %v", name, refs, implied)
		}
		// The namespace and its grant are committed to the platform repo.
		if name == "namespace with primary network" || name == "rolebinding" {
			continue
		}
		if err := requirePlacement(tenant, path, content); err != nil {
			t.Errorf("%s renders what a tenant repo may not hold: %v", name, err)
		}
	}
}

// A tenant repo never gains a NetworkAttachmentDefinition through dotvirt: the
// manifest editor cannot turn a network into one or rewrite a hand-written
// one, adopting one names the platform tier, and adopting a namespace leaves
// its raw NADs out while its VMs come under git.
func TestTenantRepoRefusesNAD(t *testing.T) {
	udn := "apiVersion: k8s.ovn.org/v1\nkind: UserDefinedNetwork\nmetadata:\n  name: net\n  namespace: alpha\nspec:\n  topology: Layer2\n"
	nad := func(name, vlan string) string {
		return "apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: " + name + "\n  namespace: alpha\nspec:\n  config: '{\"cniVersion\":\"0.4.0\",\"name\":\"" + name + "\",\"type\":\"ovn-k8s-cni-overlay\",\"topology\":\"localnet\",\"physicalNetworkName\":\"dc-vlan\",\"vlanID\":" + vlan + "}'\n"
	}
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/networks/net.yaml": []byte(udn),
		"alpha/raw.yaml":          []byte(nad("raw", "10")),
	})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	platformTier := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, model.ErrInvalid) || !strings.Contains(fmt.Sprint(err), "platform tier") {
			t.Errorf("%s: err = %v, want a refusal naming the platform tier", what, err)
		}
	}

	_, err := c.StageUpdateManifest(id, proj, string(draft.ResourceNetwork), "alpha", "net", nad("net", "20"))
	platformTier("network rewritten into a NAD", err)
	_, err = c.StageUpdateManifest(id, proj, string(draft.ResourceNetwork), "alpha", "raw", nad("raw", "30"))
	platformTier("hand-written NAD edited", err)

	live := model.Adoptable{Kind: "NetworkAttachmentDefinition", Namespace: "alpha", Name: "live", Path: "alpha/live.networkattachmentdefinition.yaml", Manifest: []byte(nad("live", "40"))}
	_, err = c.AdoptObject(id, proj, live)
	platformTier("NAD adopted", err)
	_, err = c.AdoptObjects(id, proj, "alpha", []model.Adoptable{live})
	platformTier("namespace holding only a NAD adopted", err)
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Fatalf("refused stages left %+v", entries)
	}

	vm := model.Adoptable{Kind: "VirtualMachine", Namespace: "alpha", Name: "web", Path: "alpha/web.yaml", Manifest: []byte(liveVMYAML("web", ""))}
	if _, err := c.AdoptObjects(id, proj, "alpha", []model.Adoptable{vm, live}); err != nil {
		t.Fatalf("namespace adoption: %v", err)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].Name != "web" {
		t.Errorf("staged %+v, want the VM alone", entries)
	}

	// The platform repo keeps them.
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	if err := requirePlacement(plat, live.Path, live.Manifest); err != nil {
		t.Errorf("platform repo refused a NAD: %v", err)
	}
}
