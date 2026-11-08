package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// handleCapabilities answers GET /api/capabilities (see model.Capabilities):
// the cluster scope, or with ?namespace= that namespace and its project. The UI
// asks on demand, never per frame. A field asks its route's questions, and for
// a tenancy or DRS change exactly what the coordinator's gate would ask of it
// (the Needs), weighed on the gate's one path; every review is cached, at
// most authConcurrency at once. A namespace outside the caller's projects is a
// 404 like every VM route.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		sc, ok := s.resolveProject(w, r, byNamespace(ns))
		if !ok {
			return
		}
		caps := s.namespaceCaps(r.Context(), sc, ns)
		writeJSON(w, model.Capabilities{Namespace: &caps})
		return
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	caps := s.clusterCaps(r.Context(), id, c)
	writeJSON(w, model.Capabilities{Cluster: &caps})
}

// capField is one capability and the action that decides it.
type capField struct {
	dst *bool
	act action
}

// objectFields are dst's create, update and delete of kind k in ns.
func objectFields(dst *model.ObjectCaps, k model.Kind, ns string) []capField {
	return []capField{
		{&dst.Create, kindAction(verbCreate, k, ns)},
		{&dst.Update, kindAction(verbUpdate, k, ns)},
		{&dst.Delete, kindAction(verbDelete, k, ns)},
	}
}

// fill answers every field.
func (s *Server) fill(ctx context.Context, id auth.Identity, c *cluster.Client, fields []capField) {
	acts := make([]action, len(fields))
	for i, f := range fields {
		acts[i] = f.act
	}
	for i, ok := range s.capableAll(ctx, id, c, acts) {
		*fields[i].dst = ok
	}
}

// newNamespace names the namespace a capability for a new one weighs: drawn
// per process so no namespace holds it, and its dry runs answer as any new
// one's.
var newNamespace = func() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "dotvirt-probe-" + hex.EncodeToString(b)
}()

// tenancyCap is a tenancy route's verdict: its route actions, then the
// coordinator's needs for the change.
func (s *Server) tenancyCap(ctx context.Context, id auth.Identity, c *cluster.Client, route action, need []changeset.Access, err error) bool {
	return s.capable(ctx, id, c, route) && s.needsAllowed(ctx, id, c, need, err)
}

// clusterCaps answers the cluster scope. The platform tier's actions need a
// platform repo, SSO's an OAuth flow: without one they stay false, as their
// routes then answer 503. Platform is the union of the platform-tier fields.
func (s *Server) clusterCaps(ctx context.Context, id auth.Identity, c *cluster.Client) model.ClusterCaps {
	var out model.ClusterCaps
	var fields []capField
	if s.cfg.PlatformRepo != "" {
		fields = append(fields, objectFields(&out.Network, model.KindCUDN, "")...)
		fields = append(fields, objectFields(&out.Uplink, model.KindNNCP, "")...)
		fields = append(fields, objectFields(&out.EgressIP, model.KindEgressIP, "")...)
		fields = append(fields, objectFields(&out.ExternalRoute, model.KindExtRoute, "")...)
		fields = append(fields, objectFields(&out.AdminNetworkPolicy, model.KindANP, "")...)
		fields = append(fields, objectFields(&out.BaselineAdminNetworkPolicy, model.KindBANP, "")...)
		fields = append(fields, capField{&out.SharedLibrary, ask(verbCreate, ssarVMTemplate, "")})
	}
	s.fill(ctx, id, c, fields)
	if s.oauth != nil {
		clientID, _, _ := s.oauth.DesiredClient()
		out.OAuthClient = s.oauthClientCap(ctx, id, c, clientID)
	}
	if s.cfg.PlatformRepo == "" {
		return out
	}
	out.NamespaceCreate = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`"}`)
	out.VMNetwork = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`","vmNetwork":{"name":"vm-network","subnet":"10.0.0.0/24"}}`)
	owners, _ := json.Marshal([]string{id.Username})
	out.GrantAdmin = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`","owners":`+string(owners)+`}`)
	out.DRSEnable, out.DRSPSI, out.DRSDisable = s.drsCaps(ctx, id, c)
	for _, o := range []model.ObjectCaps{out.Network, out.Uplink, out.EgressIP, out.ExternalRoute, out.AdminNetworkPolicy, out.BaselineAdminNetworkPolicy} {
		out.Platform = out.Platform || o.Create || o.Update || o.Delete
	}
	out.Platform = out.Platform || out.SharedLibrary || out.NamespaceCreate || out.VMNetwork || out.GrantAdmin ||
		out.DRSEnable || out.DRSPSI || out.DRSDisable
	out.PlatformRead, _ = s.canReadPlatform(ctx, id, c)
	return out
}

// newProjectCap is creating the project spec describes over a namespace that
// does not exist yet: the route reads it, the coordinator weighs the change.
func (s *Server) newProjectCap(ctx context.Context, id auth.Identity, c *cluster.Client, spec string) bool {
	need, err := s.draft.ProjectNeeds(s.platformProject(), json.RawMessage(spec))
	return s.tenancyCap(ctx, id, c, readNamespace(""), need, err)
}

// drsCaps are the DRS verdicts - enabling the default configuration, with
// the PSI MachineConfig, and disabling - each its route's gate and what the
// coordinator asks of the change.
func (s *Server) drsCaps(ctx context.Context, id auth.Identity, c *cluster.Client) (enable, psi, disable bool) {
	platform := s.platformProject()
	if ok, _ := s.anyAllowed(ctx, id, c, drsSchedulerActions()); ok {
		need, err := s.draft.EnableDRSNeeds(platform, json.RawMessage(`{"mode":"Predictive"}`))
		enable = s.needsAllowed(ctx, id, c, need, err)
		need, err = s.draft.EnableDRSNeeds(platform, json.RawMessage(`{"mode":"Predictive","installPSI":true}`))
		psi = s.needsAllowed(ctx, id, c, need, err)
	}
	if s.capable(ctx, id, c, drsDisableAction()) {
		need, err := s.draft.DisableDRSNeeds(platform)
		disable = s.needsAllowed(ctx, id, c, need, err)
	}
	return enable, psi, disable
}

// namespaceCaps answers namespace ns of sc's project, and that project over
// all its namespaces. The tenancy fields need a platform repo, resync ArgoCD,
// an upload the upload proxy.
func (s *Server) namespaceCaps(ctx context.Context, sc scope, ns string) model.NamespaceCaps {
	out := model.NamespaceCaps{Namespace: ns, Project: model.ProjectCaps{Name: sc.proj.Name}}
	fields := []capField{
		{&out.VM.Create, vmAction(verbCreate, ns)},
		{&out.VM.Update, vmAction(verbUpdate, ns)},
		{&out.VM.Delete, vmAction(verbDelete, ns)},
		{&out.Restart, restartAction(ns)},
		{&out.Migrate, migrateAction(ns)},
		{&out.Pause, pauseAction(ns)},
		{&out.Unpause, unpauseAction(ns)},
		{&out.Console, consoleAction(ns)},
		{&out.SnapshotTake, snapshotTakeAction(ns)},
		{&out.SnapshotRestore, snapshotRestoreAction(ns)},
		{&out.SnapshotDelete, snapshotDeleteAction(ns)},
		{&out.Clone, cloneAction(ns)},
		{&out.NetworkAttachment.Update, kindAction(verbUpdate, model.KindNAD, ns)},
		{&out.NetworkAttachment.Delete, kindAction(verbDelete, model.KindNAD, ns)},
	}
	if s.cfg.UploadProxyURL != "" {
		fields = append(fields, capField{&out.DataVolumeCreate, dataVolumeAction(ns)})
	}
	if s.drift != nil {
		fields = append(fields, capField{&out.Resync, vmAction(verbUpdate, ns)})
	}
	fields = append(fields, objectFields(&out.Network, model.KindUDN, ns)...)
	fields = append(fields, objectFields(&out.NetworkPolicy, model.KindNetpol, ns)...)
	fields = append(fields, objectFields(&out.EgressFirewall, model.KindEgressFW, ns)...)
	s.fill(ctx, sc.id, sc.cluster, fields)
	out.Project.TemplateEdit, _ = s.projectAuthor(ctx, sc.id, sc.cluster, sc.proj.Namespaces)
	if full, ok := s.projectByName(sc.proj.Name); ok && s.cfg.PlatformRepo != "" {
		s.projectCaps(ctx, sc, full, &out.Project)
	}
	return out
}

// projectCaps answers the tenancy routes over project full: joining a new
// namespace to it, and for a repoless one attaching a repo (with owners
// named: GrantAdmin) or releasing it, which the routes refuse otherwise.
func (s *Server) projectCaps(ctx context.Context, sc scope, full project.ProjectInfo, out *model.ProjectCaps) {
	platform := s.platformProject()
	id, c := sc.id, sc.cluster
	need, err := s.draft.NamespaceNeeds(platform, full, json.RawMessage(`{"name":"`+newNamespace+`"}`))
	out.NewNamespace = s.tenancyCap(ctx, id, c, readNamespace(""), need, err)
	if full.Repo != "" {
		return
	}
	// The route reads and claims each namespace before the coordinator weighs
	// the change.
	var claims action
	for _, ns := range full.Namespaces {
		claims = append(claims, readNamespace(ns)...)
		claims = append(claims, claimAction(ns)...)
	}
	need, err = s.draft.AdoptNeeds(platform, full, nil)
	out.Adopt = s.tenancyCap(ctx, id, c, claims, need, err)
	need, err = s.draft.AdoptNeeds(platform, full, []string{id.Username})
	out.GrantAdmin = s.tenancyCap(ctx, id, c, claims, need, err)
	// The release patches the namespaces git does not declare under the
	// caller's token; the apiserver asks that patch.
	need, residue, err := s.draft.ReleaseNeeds(platform, full)
	var patches action
	for _, ns := range residue {
		patches = append(patches, question{Verb: verbPatch, Group: ssarNamespace.group, Resource: ssarNamespace.resource, Name: ns})
	}
	out.Release = (len(patches) == 0 || s.capable(ctx, id, c, patches)) && s.needsAllowed(ctx, id, c, need, err) && (len(need) > 0 || len(patches) > 0)
}
