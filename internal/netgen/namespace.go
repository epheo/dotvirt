package netgen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
// always Layer2 on purpose: a flat L2 network follows a VM across nodes on live
// migration (the VM keeps its IP), whereas Layer3's per-node subnets don't suit
// VMs - so topology isn't a user choice here.
type PrimaryNet struct {
	Name   string `json:"name"`
	Subnet string `json:"subnet"` // required CIDR - a primary UDN must do IPAM (see below)
}

// ReleasedNamespaceManifest rewrites a declared Namespace with dotvirt's
// tenancy stripped - the declarative half of a project release. The FILE stays
// (handing Argo a deletion would prune the namespace itself) and so does
// everything else it declares: only the project label and the repo and path annotations go,
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
	dropMetaKey(meta, "annotations", tenancy.PathAnnotation)
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
	// A publish namespace holds a project's Gateway: in a project, that
	// project's people would administer another's.
	if strings.HasSuffix(s.Name, tenancy.PublishSuffix) {
		return "", nil, fmt.Errorf("namespace %q: names ending in %s are kept for publish namespaces", s.Name, tenancy.PublishSuffix)
	}
	if s.Live != nil && s.Live.Labels[tenancy.PublishLabel] != "" {
		return "", nil, fmt.Errorf("namespace %q is a publish namespace and joins no project", s.Name)
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
	return model.KindNS.RepoPath("", s.Name), out, nil
}

// PublishNamespaceManifest renders project's publish namespace, labeled for
// it under tenancy.PublishLabel and never into the project: no tenant
// Application or grant reaches the Gateway it holds.
func PublishNamespaceManifest(project string) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("project", project); err != nil {
		return "", nil, err
	}
	name := tenancy.PublishNamespace(project)
	if err := validate.RequireDNS1123("publish namespace", name); err != nil {
		return "", nil, err
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": model.KindNS.APIVersion(), "kind": model.KindNS.Kind,
		"metadata": map[string]any{"name": name, "labels": map[string]any{tenancy.PublishLabel: project}},
	})
	if err != nil {
		return "", nil, err
	}
	return model.KindNS.RepoPath("", name), out, nil
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
	// Kept are the subjects the declared binding already names, granted beside
	// Owners: re-rendering a grant never revokes access it did not ask to,
	// except another project's sync account (SyncAccount).
	Kept []Subject `json:"-"`
	// SyncAccount is the project's Argo sync ServiceAccount
	// (tenancy.SyncAccount), nil for none. It joins the owners in every grant,
	// so the project's repo holds exactly the project's rights. Any other
	// project's sync account a kept subject names is dropped: a namespace
	// grants one project's repo, never two.
	SyncAccount *Subject `json:"-"`
}

// OwnerRole is the ClusterRole a project's owners are bound to by default: the
// built-in namespace-admin role.
const OwnerRole = "admin"

// Subject is a RoleBinding subject.
type Subject struct {
	Kind      string `json:"kind"`
	APIGroup  string `json:"apiGroup,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ServiceAccountSubject is the subject naming ServiceAccount namespace/name.
func ServiceAccountSubject(namespace, name string) *Subject {
	return &Subject{Kind: "ServiceAccount", Name: name, Namespace: namespace}
}

// RoleBindingManifest renders a project namespace's grant: the RoleBinding
// granting the given ClusterRole (default OwnerRole) to the kept subjects,
// each owner (kind User) and the sync account. With a sync account it is
// rendered even without owners: the project's repo syncs as that account.
func RoleBindingManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	role := s.Role
	if role == "" {
		role = OwnerRole
	}
	return grantManifest(s, tenancy.AdminBinding(s.Namespace), role)
}

// EgressManifest renders a project namespace's EgressFirewall grant
// (tenancy.TenantEgressRole), a tenant kind the admin role lacks, to the same
// subjects as its RoleBindingManifest: the project's people may stage what
// its repo may sync, and no more. s.Role does not apply.
func EgressManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	return grantManifest(s, tenancy.EgressBinding(s.Namespace), tenancy.TenantEgressRole)
}

// RoutesManifest renders a publishing namespace's HTTPRoute grant
// (tenancy.TenantRoutesRole) to the same subjects as its
// RoleBindingManifest. s.Role does not apply.
func RoutesManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	return grantManifest(s, tenancy.RoutesBinding(s.Namespace), tenancy.TenantRoutesRole)
}

// GatewayViewManifest renders the read of a project's Gateway in its publish
// namespace s.Namespace (tenancy.TenantGatewayViewRole) for s's kept
// subjects. s.Role does not apply.
func GatewayViewManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	return grantManifest(s, tenancy.GatewayViewBinding, tenancy.TenantGatewayViewRole)
}

// grantManifest renders RoleBinding name binding role to s's kept subjects,
// each owner (kind User) and the sync account, each once.
func grantManifest(s RoleBindingSpec, name, role string) (string, []byte, error) {
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	var subjects []Subject
	add := func(sub Subject) {
		if !slices.Contains(subjects, sub) {
			subjects = append(subjects, sub)
		}
	}
	for _, sub := range s.Kept {
		if s.SyncAccount != nil && sub != *s.SyncAccount && sub.Kind == "ServiceAccount" &&
			sub.Namespace == s.SyncAccount.Namespace && strings.HasPrefix(sub.Name, tenancy.AppProjectPrefix) {
			continue
		}
		add(sub)
	}
	for _, o := range s.Owners {
		if o == "" {
			return "", nil, fmt.Errorf("an owner name cannot be empty")
		}
		add(Subject{Kind: "User", APIGroup: rbacGroup, Name: o})
	}
	if s.SyncAccount != nil {
		add(*s.SyncAccount)
	}
	if len(subjects) == 0 {
		return "", nil, fmt.Errorf("at least one owner is required")
	}
	rb, err := roleBinding(name, s.Namespace, s.Project, role, subjects)
	if err != nil {
		return "", nil, err
	}
	return model.KindRB.RepoPath(s.Namespace, name), rb, nil
}

const rbacGroup = "rbac.authorization.k8s.io"

func roleBinding(name, namespace, project, role string, subjects []Subject) ([]byte, error) {
	meta := map[string]any{"name": name, "namespace": namespace}
	if project != "" {
		meta["labels"] = map[string]any{projectLabel: project}
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": model.KindRB.APIVersion(),
		"kind":       model.KindRB.Kind,
		"metadata":   meta,
		"roleRef":    map[string]any{"apiGroup": rbacGroup, "kind": "ClusterRole", "name": role},
		"subjects":   subjects,
	})
}

// DeclaredGrant reads RoleBinding name from a file declaring it: the
// ClusterRole it binds and its subjects. ok=false when the file declares none.
func DeclaredGrant(content []byte, name string) (role string, subjects []Subject, ok bool, err error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(content), 4096)
	for {
		var rb struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			RoleRef struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"roleRef"`
			Subjects []Subject `json:"subjects"`
		}
		if err := dec.Decode(&rb); errors.Is(err, io.EOF) {
			return "", nil, false, nil
		} else if err != nil {
			return "", nil, false, fmt.Errorf("declared grant: %w", err)
		}
		if rb.Kind != model.KindRB.Kind || rb.Metadata.Name != name {
			continue
		}
		// A namespaced Role cannot be re-rendered as the ClusterRole RoleBindingManifest binds.
		if rb.RoleRef.Kind != "ClusterRole" {
			return "", nil, false, fmt.Errorf("%s binds a %s, not a ClusterRole", rb.Metadata.Name, rb.RoleRef.Kind)
		}
		return rb.RoleRef.Name, rb.Subjects, true, nil
	}
}
