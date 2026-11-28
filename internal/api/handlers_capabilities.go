package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// handleCapabilities answers GET /api/capabilities (see model.Capabilities):
// the cluster scope, or with ?namespace= that namespace and its project, or
// with ?desktops= the acts there on desktop ?desktop=, and on its pool's claim
// ?claim=. The UI asks on demand, never per frame. A field asks its route's
// questions, and for a tenancy or DRS change exactly what the coordinator's
// gate would ask of it (the Needs), weighed on the gate's one path; every
// review is cached, at most authConcurrency at once. A namespace outside the
// caller's projects is a 404 like every VM route.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if ns := r.URL.Query().Get("desktops"); ns != "" {
		sc, ok := s.resolveProject(w, r, byNamespace(ns))
		if !ok {
			return
		}
		q := r.URL.Query()
		caps, err := s.desktopCaps(r.Context(), sc, ns, q.Get("desktop"), q.Get("claim"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, model.Capabilities{Desktops: &caps})
		return
	}
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

// objectFields are dst's create, update and delete of kind k in ns; only
// delete when the cluster does not serve k (Server.unserved).
func objectFields(dst *model.ObjectCaps, k model.Kind, ns string, unserved map[model.Kind]bool) []capField {
	if unserved[k] {
		return []capField{{&dst.Delete, kindAction(verbDelete, k, ns)}}
	}
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
	out := model.ClusterCaps{ImageUpload: s.cfg.UploadProxyURL != "", UploadProxy: strings.TrimRight(s.cfg.UploadProxyURL, "/")}
	fields := []capField{{&out.Hosts, ask(verbList, ssarNodes, "")}}
	var un map[model.Kind]bool
	if s.replication != nil && s.replication.Started() {
		fields = append(fields, capField{&out.Replication, replicationRead}, capField{&out.ReplicationRepair, replicationRepair})
	}
	if s.cfg.PlatformRepo != "" {
		un = s.unserved(ctx, model.KindCUDN, model.KindNNCP, model.KindEgressIP, model.KindExtRoute, model.KindANP, model.KindBANP, model.KindUDN)
		fields = append(fields, objectFields(&out.Network, model.KindCUDN, "", un)...)
		fields = append(fields, objectFields(&out.Uplink, model.KindNNCP, "", un)...)
		fields = append(fields, objectFields(&out.EgressIP, model.KindEgressIP, "", un)...)
		fields = append(fields, objectFields(&out.ExternalRoute, model.KindExtRoute, "", un)...)
		fields = append(fields, objectFields(&out.AdminNetworkPolicy, model.KindANP, "", un)...)
		fields = append(fields, objectFields(&out.BaselineAdminNetworkPolicy, model.KindBANP, "", un)...)
		fields = append(fields, capField{&out.SharedLibrary, ask(verbCreate, ssarVMTemplate, "")})
	}
	if s.gateways != nil && s.gateways.Started() {
		fields = append(fields, capField{&out.Gateways, gatewaysRead})
	}
	if s.adminObjectsOffered() {
		fields = append(fields, capField{&out.AdminObjects, s.adminObjectsAction()})
	}
	if a := s.cfg.ForgeAdmin; a.Name != "" {
		q := refQ(verbGet, ssarSecrets, a.Namespace)
		q.Name = a.Name
		fields = append(fields, capField{&out.ForgeAdmin, action{q}})
	}
	s.fill(ctx, id, c, fields)
	if s.oauth != nil {
		out.OAuthClient = s.cfg.OwnNamespace != "" && s.oauthClientCap(ctx, id, c, adminobjects.SSOClientName(s.cfg.OwnNamespace))
	}
	if s.cfg.PlatformRepo == "" {
		return out
	}
	out.NamespaceCreate = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`"}`)
	if !un[model.KindUDN] {
		out.VMNetwork = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`","vmNetwork":{"name":"vm-network","subnet":"10.0.0.0/24"}}`)
	}
	if !un[model.KindCUDN] {
		out.ProjectNetwork = s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`","projectNetwork":{"name":"vm-network","subnet":"10.0.0.0/24"}}`)
	}
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
	un := s.unserved(ctx, drsKinds...)
	olm := !un[model.KindSub] && !un[model.KindOpGroup]
	if ok, _ := s.anyAllowed(ctx, id, c, drsSchedulerActions()); ok && olm && !s.noVMs() {
		need, err := s.draft.EnableDRSNeeds(platform, json.RawMessage(`{"mode":"Predictive"}`))
		enable = s.needsAllowed(ctx, id, c, need, err)
		if !un[model.KindMachCfg] {
			need, err = s.draft.EnableDRSNeeds(platform, json.RawMessage(`{"mode":"Predictive","installPSI":true}`))
			psi = s.needsAllowed(ctx, id, c, need, err)
		}
	}
	if s.capable(ctx, id, c, drsDisableAction()) {
		need, err := s.draft.DisableDRSNeeds(platform)
		disable = s.needsAllowed(ctx, id, c, need, err)
	}
	return enable, psi, disable
}

// namespaceCaps answers namespace ns of sc's project, and that project over
// all its namespaces. The tenancy fields need a platform repo, resync ArgoCD,
// an upload the upload proxy. The repo reads ask what their routes' gateRepo
// asks.
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
		{&out.BootImageImport, bootImageImportAction(ns)},
		{&out.BootImageDelete, bootImageDeleteAction(ns)},
		{&out.NetworkAttachment.Delete, kindAction(verbDelete, model.KindNAD, ns)},
	}
	un := s.unserved(ctx, model.KindNAD, model.KindUDN, model.KindNetpol, model.KindEgressFW)
	if !un[model.KindNAD] {
		fields = append(fields, capField{&out.NetworkAttachment.Update, kindAction(verbUpdate, model.KindNAD, ns)})
	}
	if s.cfg.UploadProxyURL != "" {
		fields = append(fields,
			capField{&out.DataVolumeCreate, dataVolumeAction(ns)},
			capField{&out.BootImageCreate, bootImageAction(ns)},
		)
	}
	if s.drift != nil {
		fields = append(fields, capField{&out.Resync, vmAction(verbUpdate, ns)})
	}
	fields = append(fields, objectFields(&out.Network, model.KindUDN, ns, un)...)
	fields = append(fields, objectFields(&out.NetworkPolicy, model.KindNetpol, ns, un)...)
	fields = append(fields, objectFields(&out.EgressFirewall, model.KindEgressFW, ns, un)...)
	s.fill(ctx, sc.id, sc.cluster, fields)
	out.RepoRead = s.readsRepo(ctx, sc, ns) == nil
	out.Project.RepoRead = s.readsRepo(ctx, sc, "") == nil
	out.Project.TemplateEdit, _ = s.projectAuthor(ctx, sc.id, sc.cluster, sc.proj.Namespaces)
	if full, ok := s.projectByName(sc.proj.Name); ok && s.cfg.PlatformRepo != "" {
		s.projectCaps(ctx, sc, full, &out.Project)
		s.publishingCaps(ctx, sc, full, &out.Project)
	}
	return out
}

// publishingCaps answers the publishing route over project full as
// handlePublishing weighs it: every kind served, the whole project in view or
// any namespace claimable, and the gate's accesses allowed. PublishingOn is
// what Kubernetes shows the caller: the project's Gateway, told only to one
// who may list gateways in its namespace. A project on a VM Network is
// weighed as if publishing reached it, so the UI can say what it waits on.
func (s *Server) publishingCaps(ctx context.Context, sc scope, full project.ProjectInfo, out *model.ProjectCaps) {
	if !s.cfg.Publish.On() || len(s.unserved(ctx, s.publishingKinds()...)) > 0 {
		return
	}
	id, c := sc.id, sc.cluster
	plan, need, err := s.draft.PublishingNeeds(s.platformProject(), full, s.publishTarget(full))
	out.PublishingOn = plan.On && s.capable(ctx, id, c, kindAction(verbList, model.KindGateway, plan.Namespace))
	if len(sc.proj.Namespaces) < len(full.Namespaces) && !s.capable(ctx, id, c, claimAction("")) {
		return
	}
	if errors.Is(err, changeset.ErrApart) {
		out.PublishingApart = true
		return
	}
	waits := errors.Is(err, changeset.ErrPrimaryNetwork)
	unhoused := errors.Is(err, changeset.ErrNoPublishNamespace)
	if waits || unhoused {
		err = nil
	}
	allowed := s.needsAllowed(ctx, id, c, need, err)
	out.Publishing = allowed && !waits && !unhoused
	out.PublishingWaits = allowed && waits
	if unhoused {
		// The route reads the namespace first, should one by that name exist.
		need, err := s.draft.PublishNamespaceNeeds(s.platformProject(), full, s.publishTarget(full))
		if errors.Is(err, changeset.ErrApart) {
			out.PublishingApart = true
			return
		}
		out.PublishNamespaceCreate = s.tenancyCap(ctx, id, c, readNamespace(plan.Namespace), need, err)
		out.PublishingUnhoused = allowed || out.PublishNamespaceCreate
		if out.PublishingUnhoused {
			out.PublishNamespace = plan.Namespace
			out.PublishNameHeld = errors.Is(err, changeset.ErrPublishNameHeld)
			out.PublishNameRequested = errors.Is(err, changeset.ErrPublishNameRequested)
		}
		return
	}
	if out.PublishingOn || allowed {
		out.PublishingReach = plan.Reach
		out.PublishNamespace = plan.Namespace
	}
}

// projectCaps answers the tenancy routes over project full: joining a new
// namespace to it, attaching a repo (with owners named: GrantAdmin) or
// recovering a lost one, granting its sync account, and for a repoless one
// releasing it, which the route refuses otherwise.
func (s *Server) projectCaps(ctx context.Context, sc scope, full project.ProjectInfo, out *model.ProjectCaps) {
	platform := s.platformProject()
	id, c := sc.id, sc.cluster
	need, err := s.draft.NamespaceNeeds(platform, full, json.RawMessage(`{"name":"`+newNamespace+`"}`))
	out.NewNamespace = s.tenancyCap(ctx, id, c, readNamespace(""), need, err)
	out.ProjectNetwork = s.projectNetwork(full)
	// The rest resolve their target as tenancyTarget does: a project seen
	// only in part is refused.
	if len(sc.proj.Namespaces) < len(full.Namespaces) && !s.capable(ctx, id, c, claimAction("")) {
		return
	}
	// The route reads and claims each namespace before the coordinator weighs
	// the change. A recovery asks what an attach does: only the repo URL its
	// Namespaces carry differs, which changes no access.
	var claims action
	for _, ns := range full.Namespaces {
		claims = append(claims, readNamespace(ns)...)
		claims = append(claims, claimAction(ns)...)
	}
	need, err = s.draft.AdoptNeeds(platform, full, nil)
	out.Adopt = s.tenancyCap(ctx, id, c, claims, need, err)
	need, err = s.draft.AdoptNeeds(platform, full, []string{id.Username})
	out.GrantAdmin = s.tenancyCap(ctx, id, c, claims, need, err)
	need, err = s.draft.SyncAccessNeeds(platform, full)
	out.SyncAccess = s.needsAllowed(ctx, id, c, need, err)
	if full.Repo != "" {
		return
	}
	// The release patches the namespaces git does not declare under the
	// caller's token; the apiserver asks that patch.
	need, residue, err := s.draft.ReleaseNeeds(platform, full)
	var patches action
	for _, ns := range residue {
		patches = append(patches, question{Verb: verbPatch, Group: ssarNamespace.group, Resource: ssarNamespace.resource, Name: ns})
	}
	out.Release = (len(patches) == 0 || s.capable(ctx, id, c, patches)) && s.needsAllowed(ctx, id, c, need, err) && (len(need) > 0 || len(patches) > 0)
}

// desktopCaps answers the acts on desktop name in namespace ns of sc's
// project, and on a pool's desktop those on claim, a claim of its pool: what
// desktopScope reads, then what each act's route and DeskuBe's admission ask
// of that desktop and that claim, by name, as RBAC may grant either by name.
// A caller who may list the desktops and claims there is answered only for a
// desktop and a claim it is shown, so each answer is one it can see a reason
// to ask.
func (s *Server) desktopCaps(ctx context.Context, sc scope, ns, name, claim string) (model.DesktopCaps, error) {
	out := model.DesktopCaps{Namespace: ns, Desktop: name, Claim: claim}
	if name == "" {
		return out, invalid(errors.New("name the desktop"))
	}
	if s.desktops == nil || !s.desktops.Started() || sc.proj.Repo == "" {
		return out, nil
	}
	v, err := s.viewFor(ctx, sc.id, sc.cluster)
	if err != nil || !v.lists(ssarDesktops, ns) || !v.lists(ssarDesktopClaims, ns) {
		return out, nil
	}
	d, _, ok := s.desktops.Desktop(ns, name)
	if !ok {
		return out, fmt.Errorf("%w: desktop %s/%s", model.ErrNotFound, ns, name)
	}
	var c model.DesktopClaim
	if claim != "" {
		if c, ok = s.desktops.Claim(ns, claim); !ok || d.Pool == "" || c.Pool != d.Pool {
			return out, fmt.Errorf("%w: no claim %s on the pool of desktop %s/%s", model.ErrNotFound, claim, ns, name)
		}
	}
	out.Restart = s.capable(ctx, sc.id, sc.cluster, desktopRestartAction(ns, name))
	if claim == "" {
		return out, nil
	}
	own := c.User == sc.id.Username
	opens := func(verb string) bool {
		ok, _ := s.anyAllowed(ctx, sc.id, sc.cluster, claimActions(verb, ns, c.Pool, c.Name, own))
		return ok
	}
	out.Power, out.Release = opens(verbPatch), opens(verbDelete)
	return out, nil
}

// noVMs is a known no for KubeVirt: the modules snapshot, as the UI reads
// it, says the cluster serves no VirtualMachines. Unknown is not no.
func (s *Server) noVMs() bool {
	if s.modules == nil {
		return false
	}
	m, ok := s.modules.Get()
	return ok && !m.VMs
}

// handleModules answers GET /api/modules from the SA's discovery snapshot.
// What the cluster can install comes from the served-kinds index the
// capabilities already fill, so it asks discovery nothing more.
func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	m, ok := s.modules.Get()
	if !ok {
		fail(w, unavailable(errors.New("the cluster's APIs have not all answered yet")))
		return
	}
	un := s.unserved(r.Context(), model.KindSub, model.KindOpGroup, model.KindMachCfg)
	m.OLM = !un[model.KindSub] && !un[model.KindOpGroup]
	m.MachineConfig = !un[model.KindMachCfg]
	m.ExtraTenantKinds = len(s.cfg.TenantKinds.Allowed) > 0
	if s.cfg.Publish.On() && len(s.unserved(r.Context(), s.publishingKinds()...)) == 0 {
		m.PublishDomain = s.cfg.Publish.Domain
	}
	writeJSON(w, m)
}
