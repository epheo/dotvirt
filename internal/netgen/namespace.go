package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// The tenancy keys dotvirt stamps on a namespace; everything else on it belongs
// to whoever created it.
const (
	projectLabel        = "dotvirt.io/project"
	repoAnnotation      = "dotvirt.io/repo"
	primaryNetworkLabel = "k8s.ovn.org/primary-user-defined-network"
)

// NamespaceSpec describes a namespace to create within a project, optionally with
// a primary "VM Network" (a primary UDN). A primary UDN requires a fresh,
// labeled namespace, so the two are created together in one manifest.
type NamespaceSpec struct {
	Name      string      `json:"name"`
	Project   string      `json:"project"` // dotvirt.io/project label
	Repo      string      `json:"repo"`    // dotvirt.io/repo annotation
	VMNetwork *PrimaryNet `json:"vmNetwork,omitempty"`
	// Live is what the namespace already carries when adoption meets an
	// existing one, nil for a new one. Never a wire field: the capture runs
	// under the caller's token, not from the request body.
	Live *LiveNamespace `json:"-"`
}

// LiveNamespace is an existing namespace's own metadata, kept verbatim beneath
// the tenancy keys. Argo applies an omitted label as its removal, and OVN
// refuses removing the primary-network label once the namespace exists, so a
// manifest rendered from the spec alone wedges the platform sync on it.
type LiveNamespace struct {
	Labels      map[string]string
	Annotations map[string]string
}

// PrimaryNet is the namespace's default "VM Network" - a primary Layer2 UDN. It's
// always Layer2 on purpose: a flat L2 segment follows a VM across nodes on live
// migration (the VM keeps its IP), whereas Layer3's per-node subnets don't suit
// VMs - so topology isn't a user choice here.
type PrimaryNet struct {
	Name   string `json:"name"`
	Subnet string `json:"subnet"` // required CIDR - a primary UDN must do IPAM (see below)
}

// ReleasedNamespaceManifest rewrites a declared Namespace with dotvirt's
// tenancy stripped - the declarative half of a project release. The FILE stays
// (handing Argo a deletion would prune the namespace itself) and so does
// everything else it declares: only the project label and repo annotation go,
// so the next sync unlabels the live namespace and touches nothing else.
func ReleasedNamespaceManifest(declared []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(declared, &doc); err != nil {
		return nil, fmt.Errorf("declared namespace: %w", err)
	}
	meta, _ := doc["metadata"].(map[string]any)
	if meta == nil {
		return nil, fmt.Errorf("declared namespace has no metadata")
	}
	dropMetaKey(meta, "labels", projectLabel)
	dropMetaKey(meta, "annotations", repoAnnotation)
	return yaml.Marshal(doc)
}

// dropMetaKey removes one key from a metadata map field, and the field once empty.
func dropMetaKey(meta map[string]any, field, key string) {
	m, _ := meta[field].(map[string]any)
	delete(m, key)
	if len(m) == 0 {
		delete(meta, field)
	}
}

// NamespaceManifest renders the Namespace (labeled into the project) plus, when a
// VM Network is requested, its primary UDN - as one multi-document file. A primary
// UDN needs the namespace label + an empty namespace, both of which hold because
// the namespace is created in the same change.
func NamespaceManifest(s NamespaceSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Name); err != nil {
		return "", nil, err
	}
	if s.Project == "" {
		return "", nil, fmt.Errorf("a project is required")
	}
	labels, annotations := map[string]any{}, map[string]any{}
	if s.Live != nil {
		for k, v := range s.Live.Labels {
			labels[k] = v
		}
		for k, v := range s.Live.Annotations {
			annotations[k] = v
		}
	}
	labels[projectLabel] = s.Project
	if s.VMNetwork != nil {
		// Required for OVN-K to treat a UDN in this namespace as primary.
		labels[primaryNetworkLabel] = ""
	}
	meta := map[string]any{"name": s.Name, "labels": labels}
	if s.Repo != "" {
		annotations[repoAnnotation] = s.Repo
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}
	ns, err := yaml.Marshal(map[string]any{
		"apiVersion": model.KindNS.APIVersion(), "kind": model.KindNS.Kind, "metadata": meta,
	})
	if err != nil {
		return "", nil, err
	}
	docs := [][]byte{ns}

	if p := s.VMNetwork; p != nil {
		if err := validate.RequireDNS1123("VM Network name", p.Name); err != nil {
			return "", nil, err
		}
		// A primary UDN must do IPAM: OVN-K rejects a subnet-less primary network
		// and only allows ipam.mode=Disabled on Secondary networks, so unlike the
		// secondary projectUDN above, a VM Network's subnet is mandatory.
		if !validCIDR(p.Subnet) {
			return "", nil, fmt.Errorf("the VM Network needs a subnet CIDR (a primary network must do IPAM)")
		}
		layer2 := map[string]any{"role": "Primary", "subnets": []any{p.Subnet}}
		// The UDN and its Namespace commit to the same (platform) Application, so they
		// share a sync wave: ArgoCD's built-in kind ordering already applies the
		// Namespace before the UserDefinedNetwork. An explicit sync-wave here would
		// have to be >= the Namespace's (default 0); a negative wave inverts the order
		// and wedges the sync on "namespace not found", so we set none.
		udn, err := yaml.Marshal(map[string]any{
			"apiVersion": model.KindUDN.APIVersion(),
			"kind":       model.KindUDN.Kind,
			"metadata":   map[string]any{"name": p.Name, "namespace": s.Name},
			"spec":       map[string]any{"topology": "Layer2", "layer2": layer2},
		})
		if err != nil {
			return "", nil, err
		}
		docs = append(docs, udn)
	}

	out := docs[0]
	for _, d := range docs[1:] {
		out = append(out, append([]byte("---\n"), d...)...)
	}
	return "namespaces/" + s.Name + ".yaml", out, nil
}

// RoleBindingSpec grants a tenant's owners admin on one of its namespaces - the
// RBAC delegation that turns a project from a folder into a real tenant. The
// RoleBinding is cluster-tenancy, so it is committed to the PLATFORM repo (granting
// access is an admin op) and applied by the platform Application, never a tenant.
type RoleBindingSpec struct {
	Namespace string   `json:"namespace"`
	Project   string   `json:"project"`        // dotvirt.io/project label
	Owners    []string `json:"owners"`         // usernames granted the role
	Role      string   `json:"role,omitempty"` // ClusterRole to bind; default "admin"
}

// RoleBindingManifest renders a namespace-scoped RoleBinding granting each owner the
// given ClusterRole (default "admin" - the built-in namespace-admin role) on the
// tenant namespace. One subject per owner (kind User).
func RoleBindingManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	if len(s.Owners) == 0 {
		return "", nil, fmt.Errorf("at least one owner is required")
	}
	role := s.Role
	if role == "" {
		role = "admin" // the built-in namespace-admin ClusterRole
	}
	subjects := make([]any, 0, len(s.Owners))
	for _, o := range s.Owners {
		if o == "" {
			return "", nil, fmt.Errorf("an owner name cannot be empty")
		}
		subjects = append(subjects, map[string]any{
			"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": o,
		})
	}
	meta := map[string]any{"name": s.Namespace + "-admins", "namespace": s.Namespace}
	if s.Project != "" {
		meta["labels"] = map[string]any{projectLabel: s.Project}
	}
	rb, err := yaml.Marshal(map[string]any{
		"apiVersion": model.KindRB.APIVersion(),
		"kind":       model.KindRB.Kind,
		"metadata":   meta,
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role,
		},
		"subjects": subjects,
	})
	if err != nil {
		return "", nil, err
	}
	return "rbac/" + s.Namespace + ".yaml", rb, nil
}
