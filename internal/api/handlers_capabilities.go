package api

import (
	"context"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// handleCapabilities answers GET /api/capabilities (see model.Capabilities):
// the cluster scope, or with ?namespace= that namespace and its project. The UI
// asks on demand, never per frame; each field is built from the actions its
// route and the coordinator ask, and every review is cached, at most
// authConcurrency at once. A namespace outside the caller's projects is a 404
// like every VM route.
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

// clusterCaps answers the cluster scope. The platform tier's actions need a
// platform repo, SSO's an OAuth flow: without one they stay false, as their
// routes then answer 503.
func (s *Server) clusterCaps(ctx context.Context, id auth.Identity, c *cluster.Client) model.ClusterCaps {
	var out model.ClusterCaps
	var fields []capField
	if s.cfg.PlatformRepo != "" {
		out.Platform, _ = s.canAuthorPlatform(ctx, id, c)
		fields = append(fields, objectFields(&out.Network, model.KindCUDN, "")...)
		fields = append(fields, objectFields(&out.Uplink, model.KindNNCP, "")...)
		fields = append(fields, objectFields(&out.EgressIP, model.KindEgressIP, "")...)
		fields = append(fields, objectFields(&out.ExternalRoute, model.KindExtRoute, "")...)
		fields = append(fields, objectFields(&out.AdminNetworkPolicy, model.KindANP, "")...)
		fields = append(fields, objectFields(&out.BaselineAdminNetworkPolicy, model.KindBANP, "")...)
		fields = append(fields,
			capField{&out.NamespaceCreate, newNamespaceAction(false, false)},
			capField{&out.VMNetwork, newNamespaceAction(false, true)},
			capField{&out.GrantAdmin, newNamespaceAction(true, false)},
			capField{&out.SharedLibrary, ask(verbCreate, ssarVMTemplate, "")},
			capField{&out.DRSEnable, drsEnableAction(false)},
			capField{&out.DRSPSI, drsEnableAction(true)},
			capField{&out.DRSDisable, drsDisableAction()},
		)
	}
	if s.oauth != nil {
		clientID, _, _ := s.oauth.DesiredClient()
		fields = append(fields, capField{&out.OAuthClient, oauthClientAction(clientID)})
	}
	s.fill(ctx, id, c, fields)
	return out
}

// namespaceCaps answers namespace ns of sc's project, and that project over
// all its namespaces. The tenancy fields need a platform repo, resync ArgoCD.
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
		{&out.DataVolumeCreate, dataVolumeAction(ns)},
	}
	if s.drift != nil {
		fields = append(fields, capField{&out.Resync, vmAction(verbUpdate, ns)})
	}
	fields = append(fields, objectFields(&out.Network, model.KindUDN, ns)...)
	fields = append(fields, objectFields(&out.NetworkPolicy, model.KindNetpol, ns)...)
	fields = append(fields, objectFields(&out.EgressFirewall, model.KindEgressFW, ns)...)
	// A project's namespaces run, so attaching a repo weighs as relabelling
	// each, the update the snapshot finds their Namespace objects to be.
	if full, ok := s.projectByName(sc.proj.Name); ok && s.cfg.PlatformRepo != "" {
		fields = append(fields,
			capField{&out.Project.Adopt, relabelAction(full.Namespaces)},
			capField{&out.Project.GrantAdmin, grantAdminAction(full.Namespaces, sc.proj.Name, sc.id.Username)},
			capField{&out.Project.Release, s.releaseAction(full.Namespaces)},
		)
	}
	s.fill(ctx, sc.id, sc.cluster, fields)
	out.Project.TemplateEdit, _ = s.projectAuthor(ctx, sc.id, sc.cluster, sc.proj.Namespaces)
	return out
}
