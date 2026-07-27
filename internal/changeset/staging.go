package changeset

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/validate"
	"github.com/epheo/dotvirt/internal/vmgen"
	"github.com/epheo/dotvirt/pkg/forge"
)

// StageEdit records a VM edit in (id, proj)'s draft.
func (c *Coordinator) StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	edit := req.VMEdit
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no fields to edit", model.ErrInvalid)
	}
	// SourceFile addresses a file in the proposal diff — the one repo path a
	// client supplies directly, so it passes the same gate created names do.
	if err := validate.RequireRepoPath("source file", req.SourceFile); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: req.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// StageCreate records a new-VM spec in (id, proj)'s draft.
func (c *Coordinator) StageCreate(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid VM spec: %v", model.ErrInvalid, err)
	}
	// The pair becomes the manifest's repo path (ns/name.yaml); reject traversal
	// and non-DNS-1123 names at stage time, not at propose.
	if err := validate.RequireDNS1123("VM name", spec.Name); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := validate.RequireDNS1123("namespace", spec.Namespace); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:      draft.KindCreate,
		Namespace: spec.Namespace,
		Name:      spec.Name,
		Spec:      &spec,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// ClusterScopeNS is the placeholder "namespace" for cluster-scoped draft entries
// (CUDN networks, NNCP uplinks) — they have no real namespace, but the draft keys
// + the unstage route are ns/name-shaped, so a sentinel keeps both well-formed.
const ClusterScopeNS = "cluster"

// stageRendered is the shared tail of the netgen-backed StageCreateX methods:
// requireRepo, then one rendered manifest staged verbatim (the adopt-create path)
// so propose commits it and Argo applies it on merge. requireRepo runs before
// render so a repoless project fails ErrConflict, never ErrInvalid; any render
// error (spec decode included) is the caller's input, wrapped as ErrInvalid.
// render returns the entry minus Kind; the entry's Namespace is the object's own
// or the ClusterScopeNS sentinel.
func (c *Coordinator) stageRendered(id auth.Identity, proj project.ProjectInfo, render func() (draft.Entry, error)) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	entry, err := render()
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	entry.Kind = draft.KindCreate
	if err := c.store.Stage(id.Username, proj.Name, entry); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// stageSpec decodes rawSpec into S, renders its manifest, and stages it: the
// whole body of every single-manifest StageCreateX. meta derives the entry's
// identity from the decoded spec (per-kind quirks live there). A free function
// because methods cannot take type parameters.
func stageSpec[S any](c *Coordinator, id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage, what string,
	render func(S) (path string, content []byte, err error),
	meta func(S) (resource draft.Resource, ns, name string),
) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec S
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid %s spec: %v", what, err)
		}
		path, content, err := render(spec)
		resource, ns, name := meta(spec)
		return draft.Entry{
			Resource:   resource,
			Namespace:  ns,
			Name:       name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateNetwork records a new Distributed Port Group in (id, proj)'s draft:
// a namespace-scoped UDN (project scope) or a cluster-scoped CUDN (shared/vlan
// scope) — for the latter, proj is the platform repo (dotvirt routes cluster-scoped
// creates there by KIND, not an admin-picked repo).
func (c *Coordinator) StageCreateNetwork(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "network", netgen.Manifest,
		func(s netgen.Spec) (draft.Resource, string, string) {
			ns := s.Namespace
			if ns == "" {
				ns = ClusterScopeNS // cluster-scoped CUDN
			}
			return draft.ResourceNetwork, ns, s.Name
		})
}

// StageCreateNamespace records a new namespace (with an optional primary "VM
// Network"). A Namespace is cluster-scoped, so it is COMMITTED to commitProj (the
// platform repo) and applied by the platform Argo app — but it is labeled/annotated
// to joinProj, the tenant project it JOINS, so that project's per-project app syncs
// workloads into it once it exists. The namespace + primary UDN land as one
// multi-doc manifest.
func (c *Coordinator) StageCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, commitProj, func() (draft.Entry, error) {
		if joinProj.Repo == "" {
			return draft.Entry{}, fmt.Errorf("the joining project has no repo")
		}
		var spec netgen.NamespaceSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid namespace spec: %v", err)
		}
		// Stamp the namespace's dotvirt.io labels/annotations to the tenant it joins,
		// not the platform repo it's committed to. Host-free ref ONLY when the repo is
		// on this forge: stripping a genuinely foreign host would re-point the project.
		ref := joinProj.Repo
		if c.forge.SameForge(ref) {
			ref = forge.PathRef(ref)
		}
		spec.Project, spec.Repo = joinProj.Name, ref
		path, content, err := netgen.NamespaceManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceNamespace,
			Namespace:  spec.Name,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateUplink records a new Uplink (an nmstate NNCP) in (id, proj)'s draft —
// proj is the platform repo (an uplink is cluster-scoped, so it always routes to the
// platform tier). Stages under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateUplink(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "uplink", netgen.UplinkManifest,
		func(s netgen.UplinkSpec) (draft.Resource, string, string) {
			return draft.ResourceUplink, ClusterScopeNS, s.Name
		})
}

// StageCreateEgressFirewall records a new namespace egress firewall (the Tier-1
// gateway firewall) in (id, proj)'s draft. It is namespace-scoped, so proj is the
// tenant project owning the namespace (handlers route it via resolveProject, like a
// project UDN). The object is always named "default" (OVN-K permits one per namespace).
func (c *Coordinator) StageCreateEgressFirewall(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "egress firewall", netgen.EgressFirewallManifest,
		func(s netgen.EgressFirewallSpec) (draft.Resource, string, string) {
			return draft.ResourceEgressFirewall, s.Namespace, "default"
		})
}

// StageCreateEgressIP records a new cluster-scoped EgressIP (the Tier-0 source-NAT
// pool) in (id, proj)'s draft. proj is the platform repo — cluster-scoped, so it
// always routes to the platform tier (handlers gate it on the caller's EgressIP-create
// authority). Staged under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateEgressIP(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "egress IP", netgen.EgressIPManifest,
		func(s netgen.EgressIPSpec) (draft.Resource, string, string) {
			return draft.ResourceEgressIP, ClusterScopeNS, s.Name
		})
}

// StageCreateExternalRoute records a new cluster-scoped AdminPolicyBasedExternalRoute
// (the Tier-0 external next-hop route) in (id, proj)'s draft — proj is the platform
// repo. Staged under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateExternalRoute(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "external route", netgen.ExternalRouteManifest,
		func(s netgen.ExternalRouteSpec) (draft.Resource, string, string) {
			return draft.ResourceExternalRoute, ClusterScopeNS, s.Name
		})
}

// StageCreateNetworkPolicy records a new NetworkPolicy (the east-west Distributed
// Firewall) in (id, proj)'s draft — namespace-scoped, so proj is the tenant project
// owning the namespace.
func (c *Coordinator) StageCreateNetworkPolicy(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "network policy", netgen.NetworkPolicyManifest,
		func(s netgen.NetworkPolicySpec) (draft.Resource, string, string) {
			return draft.ResourceNetworkPolicy, s.Namespace, s.Name
		})
}

// StageCreateAdminNetworkPolicy records a new cluster-wide admin DFW policy (the
// AdminNetworkPolicy that overrides tenant NetworkPolicies, or the singleton
// BaselineAdminNetworkPolicy default) in (id, proj)'s draft. proj is the platform
// repo — cluster-scoped + admin-only, so it always routes to the platform tier
// (handlers gate it on the caller's ANP/BANP-create authority). Staged under the
// ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateAdminNetworkPolicy(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return stageSpec(c, id, proj, rawSpec, "admin network policy", netgen.AdminNetworkPolicyManifest,
		func(s netgen.AdminNetworkPolicySpec) (draft.Resource, string, string) {
			if s.Baseline {
				return draft.ResourceBaselineAdminNetworkPolicy, ClusterScopeNS, "default"
			}
			return draft.ResourceAdminNetworkPolicy, ClusterScopeNS, s.Name
		})
}

// StageDelete records the removal of an existing VM in (id, proj)'s draft. The VM
// must exist on the base branch (you can't delete what isn't in git — an unstaged
// create should be unstaged, not deleted); its manifest path is captured so the
// propose step removes that file and Argo prunes the VM on merge.
func (c *Coordinator) StageDelete(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	vm, ok, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !ok {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s not on %s", model.ErrNotFound, namespace, name, c.baseBranch)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindDelete,
		Namespace:  namespace,
		Name:       name,
		SourceFile: vm.SourceFile,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// Unstage removes one pending change (of the given resource — empty means VM)
// from (id, proj)'s draft. An atomic resource unstages as a whole set: its
// entries are one logical change, so removing a single file from under it
// would leave a proposable half-change.
func (c *Coordinator) Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error {
	if r := draft.Resource(resource); r.Atomic() {
		_, err := c.unstageResource(id, proj, r)
		return err
	}
	return c.store.Unstage(id.Username, proj.Name, draft.Resource(resource), namespace, name)
}

// Discard clears (id, proj)'s draft.
func (c *Coordinator) Discard(id auth.Identity, proj project.ProjectInfo) error {
	return c.store.Clear(id.Username, proj.Name)
}
