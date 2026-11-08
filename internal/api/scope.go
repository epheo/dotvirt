package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// visibleSet is a token's visible-namespace set stamped with the RBAC version it was
// derived at; the read is valid only while that version still matches the bus and
// the entry is younger than its maxAge.
type visibleSet struct {
	ns        map[string]bool
	ver       uint64
	unsettled bool // its review began right after a grant move (see unsettled)
}

// ssarVerdict is one (token, question) SSAR verdict, stamped the same way.
type ssarVerdict struct {
	ok        bool
	ver       uint64
	unsettled bool
}

// unwatchedTTL is how long a cached visible set or verdict serves while some
// RBAC watch is not running: a grant move may then publish nothing, and this
// age is all that bounds a revocation.
const unwatchedTTL = 30 * time.Second

// authTTL is how long a cached visible set or verdict serves now: visibleTTL
// while every RBAC watch runs (clusterstate.RBACWatched) and RBACChanged
// reports each grant move, unwatchedTTL otherwise. Read on every use, so a
// watch failing shortens the entries already cached.
func (s *Server) authTTL() time.Duration {
	if s.state != nil && s.state.RBACWatched() {
		return visibleTTL
	}
	return unwatchedTTL
}

// grantSettle is how long after a grant move a review may still read the old
// grant: on a control plane of several apiservers, each one's RBAC informer
// catches up on its own, and the review may land on a lagging one.
const grantSettle = 5 * time.Second

// unsettled reports whether a review beginning at start is within grantSettle
// of the last grant move a watch reported. Its answer serves unwatchedTTL
// only: a stale verdict from a lagging apiserver, stamped with the new RBAC
// version, must not hold for the long TTL.
func (s *Server) unsettled(start time.Time) bool {
	return s.state != nil && start.Sub(s.state.LastGrantMove()) < grantSettle
}

// maxAge is how long an entry serves.
func (s *Server) maxAge(unsettled bool) time.Duration {
	if unsettled {
		return unwatchedTTL
	}
	return s.authTTL()
}

// rbacVersion is the change-version a token's derived authorization depends on: a
// watched grant move OR a namespace add/remove/relabel can change what it may see.
// It is one version for every token, so one tenant's grant move invalidates all of
// their entries: a visibility probe reviews every project namespace, so a
// per-namespace version would not narrow it, while one keyed by the moved
// bindings' subjects would.
func (s *Server) rbacVersion() uint64 {
	return s.bus.Version(eventbus.RBACChanged, eventbus.NamespaceChanged)
}

// This file is every handler's shared preamble: who is calling (identity ->
// cluster client), what they may see (visible namespaces -> projects), and which
// project a request targets (the pickers).

// userCluster builds the caller's cluster client (identity from context). It
// fails closed: no identity or no factory -> an error the caller turns into 401/503.
func (s *Server) userCluster(r *http.Request) (auth.Identity, *cluster.Client, error) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		return auth.Identity{}, nil, fmt.Errorf("no identity")
	}
	if s.clusterF == nil {
		return id, nil, fmt.Errorf("cluster not configured")
	}
	c, err := s.clusterF.For(id.Token)
	return id, c, err
}

// projectsFor resolves the caller's projects: the project topology comes from the
// SA-owned snapshot (shared, no per-request fetch), filtered to the namespaces the
// caller's token may see (TTL-cached per token). The visible set is the sole
// per-user authorization input - a user never learns a project outside their RBAC.
func (s *Server) projectsFor(ctx context.Context, id auth.Identity, c *cluster.Client) ([]project.ProjectInfo, error) {
	visible, err := s.visibleFor(ctx, id, c)
	if err != nil {
		return nil, err
	}
	return s.resolver.Resolve(s.state.Namespaces(), visible), nil
}

// reviewTimeout bounds a shared review (see review), which no single
// caller's deadline ends while others still wait on it.
const reviewTimeout = 15 * time.Second

// review runs fill once per key and RBAC version, however many frame builds
// and pulls of one token miss at once (ttlcache.Do): each caller leaves on its
// own deadline, and the review stops once none waits. A caller with a newer
// version never joins an older review.
func review[V any](ctx context.Context, cache *ttlcache.Cache[V], key string, ver uint64, fill func(context.Context) (V, error)) (V, error) {
	return cache.Do(ctx, key+"\x00"+strconv.FormatUint(ver, 10), func(ctx context.Context) (V, error) {
		ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
		defer cancel()
		return fill(ctx)
	})
}

// visibleFor returns the set of namespaces id's token may read VMs in, cached by
// token for authTTL. The snapshot's project namespaces are the candidates
// VisibleNamespaces probes when the token lacks cluster-wide VM read. A failed
// probe is never cached: the next read asks again.
func (s *Server) visibleFor(ctx context.Context, id auth.Identity, c *cluster.Client) (map[string]bool, error) {
	ver := s.rbacVersion()
	key := restfactory.TokenKey(id.Token)
	if e, age, ok := s.visible.GetAge(key); ok && e.ver == ver && age < s.maxAge(e.unsettled) {
		return e.ns, nil
	}
	e, err := review(ctx, s.visible, key, ver, func(ctx context.Context) (visibleSet, error) {
		start := time.Now()
		names, err := c.VisibleNamespaces(ctx, namespaceNames(s.state.Namespaces()))
		if err != nil {
			return visibleSet{}, err
		}
		e := visibleSet{ns: make(map[string]bool, len(names)), ver: ver, unsettled: s.unsettled(start)}
		for _, n := range names {
			e.ns[n] = true
		}
		s.visible.PutAt(key, e, start)
		return e, nil
	})
	if err != nil {
		return nil, s.reviewFailed(ctx, id, err)
	}
	return e.ns, nil
}

// can answers one question under id's token, behind a per-(token, question)
// cache stamped with rbacVersion, so a signal read on a polled or broadcast
// path does not post an SSAR per request. A watched grant or namespace move
// invalidates lazily via the stamp; authTTL backstops what no watch observes
// (see RunTTLBackstop). Only a verdict is cached: a failed review is returned,
// so a blip can never pin a denial. A gate answers the error with
// s.reviewFailed; a UI signal or a filter reads it as no.
func (s *Server) can(ctx context.Context, id auth.Identity, c *cluster.Client, q question) (bool, error) {
	ver := s.rbacVersion()
	k := strings.Join([]string{restfactory.TokenKey(id.Token), q.Verb, q.Group, q.Resource, q.Subresource, q.Namespace, q.Name}, "\x00")
	if e, age, ok := s.ssar.GetAge(k); ok && e.ver == ver && age < s.maxAge(e.unsettled) {
		return e.ok, nil
	}
	e, err := review(ctx, s.ssar, k, ver, func(ctx context.Context) (ssarVerdict, error) {
		start := time.Now()
		ok, err := c.Review(ctx, authzv1.ResourceAttributes{
			Verb: q.Verb, Group: q.Group, Resource: q.Resource, Subresource: q.Subresource, Namespace: q.Namespace, Name: q.Name,
		})
		if err != nil {
			return ssarVerdict{}, err
		}
		e := ssarVerdict{ok: ok, ver: ver, unsettled: s.unsettled(start)}
		s.ssar.PutAt(k, e, start)
		return e, nil
	})
	return e.ok, err
}

// saCached is the preamble of a catalog route: the caller's identity first
// (the normal gate, so a missing identity or cluster answers like every other
// route), then one SA-read value cached under a single key for everyone - a
// scoped tenant can't list these cluster-scoped kinds itself. ok=false means
// the response is written.
func saCached[T any](s *Server, w http.ResponseWriter, r *http.Request, cache *ttlcache.Cache[T], fetch func(*cluster.Client, context.Context) (T, error)) (id auth.Identity, c *cluster.Client, v T, ok bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return id, nil, v, false
	}
	if v, ok = cache.Get("all"); ok {
		return id, c, v, true
	}
	sa, err := s.clusterF.SA()
	if err != nil {
		fail(w, unavailable(err))
		return id, nil, v, false
	}
	if v, err = fetch(sa, r.Context()); err != nil {
		fail(w, unavailable(err))
		return id, nil, v, false
	}
	cache.Put("all", v)
	return id, c, v, true
}

// ssarRef is one authority tuple (API group + plural resource).
type ssarRef struct{ group, resource string }

// ssarNodes is node read: the gate on node-level data (physical fabric, host
// metrics, node ops in the task feed).
var ssarNodes = ssarRef{resource: "nodes"}

// ssarDataSources is the boot images: listed for the options catalog, created
// when an upload registers one.
var ssarDataSources = ssarRef{group: "cdi.kubevirt.io", resource: "datasources"}

// ssarFor is the authority over one managed kind.
func ssarFor(k model.Kind) ssarRef { return ssarRef{k.Group, k.Plural} }

// platformProject is the synthetic platform-tier project over the platform repo.
func (s *Server) platformProject() project.ProjectInfo {
	return project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}
}

// vmScope is the preamble of every route addressing {namespace}/{name} by
// path (name is empty on the namespace-only routes): resolve the tenant project
// owning the path's namespace (the authorization point) and hand back the pair.
func (s *Server) vmScope(w http.ResponseWriter, r *http.Request) (sc scope, ns, name string, ok bool) {
	ns, name = r.PathValue("namespace"), r.PathValue("name")
	sc, ok = s.resolveProject(w, r, byNamespace(ns))
	return sc, ns, name, ok
}

// The platform-tier authorities, each named once and spelled by the kind
// table: the policy views and the remaining platformScope routes gate on
// them, and the authoring signal ORs a subset.
var (
	ssarCUDN        = ssarFor(model.KindCUDN)
	ssarUplink      = ssarFor(model.KindNNCP)
	ssarNamespace   = ssarFor(model.KindNS)
	ssarEgressIP    = ssarFor(model.KindEgressIP)
	ssarExtRoute    = ssarFor(model.KindExtRoute)
	ssarANP         = ssarFor(model.KindANP)
	ssarBANP        = ssarFor(model.KindBANP)
	ssarDescheduler = ssarFor(model.KindDesched)
	ssarMachineCfg  = ssarFor(model.KindMachCfg)
	ssarVMTemplate  = ssarFor(model.KindTemplate)
)

// platformReaders are the cluster-wide signals that open the platform tier's
// shared reads - its history, commit and proposal diffs, revert, its merges
// in the task feed and its rollup detail: listing namespaces cluster-wide, or
// creating CUDNs, KubeDeschedulers or templates in every namespace. The
// platform repo names every project, namespace and owner, so a right scoped
// to one namespace (an operator namespace's admin role) never opens them.
func platformReaders() []action {
	return []action{
		ask(verbList, ssarNamespace, ""),
		ask(verbCreate, ssarCUDN, ""),
		ask(verbCreate, ssarDescheduler, ""),
		ask(verbCreate, ssarVMTemplate, ""),
	}
}

// canReadPlatform reports whether id may read the platform tier: any of
// platformReaders, each a review cached per token. False when no platform
// repo is configured. An error means none allowed and at least one review
// failed.
func (s *Server) canReadPlatform(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
	if s.cfg.PlatformRepo == "" {
		return false, nil
	}
	return s.anyAllowed(ctx, id, c, platformReaders())
}

func namespaceNames(nss []project.Namespace) []string {
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		out = append(out, ns.Name)
	}
	return out
}

// scope is the per-request context every draft/VM handler resolves up front: the
// caller's identity, their cluster client, and the target project. No field
// stands for authority: every gate asks its own questions.
type scope struct {
	id      auth.Identity
	cluster *cluster.Client
	proj    project.ProjectInfo
}

// resolveProject is the shared preamble for every draft/VM route: identity -> user
// cluster -> the caller's projects -> pick(projects). pick selects the target
// project (by path namespace, ?project=, or spec namespace) and, when it can't,
// supplies the not-found message. It writes the error status and returns ok=false
// on any failure; handlers just `if !ok { return }`. The returned scope carries the
// cluster client so a handler that needs it (e.g. resync's permission check) reuses
// it instead of re-minting.
func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request, pick projectPicker) (scope, bool) {
	sc, projects, ok := s.callerProjects(w, r)
	if !ok {
		return scope{}, false
	}
	if sc.proj, ok = pickOrFail(w, pick, projects); !ok {
		return scope{}, false
	}
	return sc, true
}

// callerProjects is the identity -> cluster -> visible-projects preamble the
// project and container resolvers share. ok=false means the response is written.
func (s *Server) callerProjects(w http.ResponseWriter, r *http.Request) (scope, []project.ProjectInfo, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return scope{}, nil, false
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return scope{}, nil, false
	}
	return scope{id: id, cluster: c}, projects, true
}

func pickOrFail(w http.ResponseWriter, pick projectPicker, projects []project.ProjectInfo) (project.ProjectInfo, bool) {
	proj, msg, ok := pick(projects)
	if !ok {
		fail(w, fmt.Errorf("%w: %s", model.ErrNotFound, msg))
	}
	return proj, ok
}

// containerScope resolves a container-level read (all, project, namespace,
// node): the repo-backed projects' namespaces, the same VMs the inventory grid
// shows, narrowed by ?project= and ?namespace=. A named project or namespace
// outside the caller's view is a 404, never an empty scope that reads as
// "nothing here".
func (s *Server) containerScope(w http.ResponseWriter, r *http.Request) (scope, []string, bool) {
	sc, projects, ok := s.callerProjects(w, r)
	if !ok {
		return scope{}, nil, false
	}
	q := r.URL.Query()
	if want := q.Get("project"); want != "" {
		if sc.proj, ok = pickOrFail(w, byName(want), projects); !ok {
			return scope{}, nil, false
		}
		projects = []project.ProjectInfo{sc.proj}
	}
	if ns := q.Get("namespace"); ns != "" {
		if sc.proj, ok = pickOrFail(w, byNamespace(ns), projects); !ok {
			return scope{}, nil, false
		}
		if sc.proj.Repo == "" {
			return sc, nil, true
		}
		return sc, []string{ns}, true
	}
	var nss []string
	for _, p := range projects {
		if p.Repo != "" {
			nss = append(nss, p.Namespaces...)
		}
	}
	return sc, nss, true
}

// projectPicker selects the target project from the caller's visible projects,
// returning a not-found message when none matches.
type projectPicker func([]project.ProjectInfo) (project.ProjectInfo, string, bool)

// byNamespace picks the project owning ns - the per-request authorization point
// for VM routes (a VM in a namespace outside the caller's projects is not found).
func byNamespace(ns string) projectPicker {
	return func(projects []project.ProjectInfo) (project.ProjectInfo, string, bool) {
		for _, p := range projects {
			for _, n := range p.Namespaces {
				if n == ns {
					return p, "", true
				}
			}
		}
		return project.ProjectInfo{}, "namespace is in none of the visible projects", false
	}
}

// byName picks the project named want (for whole-draft routes carrying ?project=).
func byName(want string) projectPicker {
	return func(projects []project.ProjectInfo) (project.ProjectInfo, string, bool) {
		if p, ok := findProject(projects, want); ok {
			return p, "", true
		}
		return project.ProjectInfo{}, "no visible project by that name", false
	}
}

// AllProjects is every project the SA-owned snapshot resolves, WITHOUT the
// caller's RBAC filter. Only safe behind a platform-admin gate (platformScope)
// or in dotvirt's own background acts: it's how the platform tier addresses a
// tenant it's about to adopt, and the view the webhook sweep and the
// ApplicationSet plugin enumerate.
func (s *Server) AllProjects() []project.ProjectInfo {
	return s.resolver.Resolve(s.state.Namespaces(), nil)
}

func (s *Server) projectByName(name string) (project.ProjectInfo, bool) {
	return findProject(s.AllProjects(), name)
}

// draftScope resolves the whole-draft routes (GET/DELETE/propose) that carry the
// project via ?project= rather than a VM namespace.
func (s *Server) draftScope(w http.ResponseWriter, r *http.Request) (scope, bool) {
	want := r.URL.Query().Get("project")
	if want == "" {
		fail(w, invalid(errors.New("project query parameter is required")))
		return scope{}, false
	}
	return s.pickDraft(w, r, want)
}

// pickDraft resolves the project whose draft a route acts on: pickProject,
// and for the platform tier also any caller holding a draft there, whatever
// right staged it (a claim on one namespace), so nothing staged is stranded.
func (s *Server) pickDraft(w http.ResponseWriter, r *http.Request, want string) (scope, bool) {
	if want != platformProjectName {
		return s.pickProject(w, r, want)
	}
	return s.platformScopeWith(w, r, func(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
		if ok, err := s.canReadPlatform(ctx, id, c); ok || err != nil {
			return ok, err
		}
		v, err := s.draft.Get(id, s.platformProject())
		return err == nil && v.Count > 0, nil
	}, "not authorized to author platform changes")
}

// pickProject resolves a project named by a ?project= query or {project} path
// segment (the whole-project routes: history, revert, and through pickDraft
// the draft routes). The platform tier resolves ONLY for callers holding a
// cluster-wide platform signal (platformReaders), so neither a plain tenant
// nor a namespace-scoped right reaches the platform repo's history or
// revert. Any other name resolves from the caller's visible projects.
func (s *Server) pickProject(w http.ResponseWriter, r *http.Request, want string) (scope, bool) {
	if want == platformProjectName {
		return s.platformScopeAny(w, r)
	}
	return s.resolveProject(w, r, byName(want))
}

// platformScopeAny resolves the platform tier for the whole-project routes:
// any of platformReaders suffices. The per-kind create routes keep their own
// gate.
func (s *Server) platformScopeAny(w http.ResponseWriter, r *http.Request) (scope, bool) {
	return s.platformScopeWith(w, r, s.canReadPlatform, "not authorized to read the platform tier")
}

// platformProjectName is the synthetic project name for the platform tier - the
// repo holding cluster-scoped + tenancy manifests. It is config-only (-platform-repo),
// never a dotvirt.io/project-labeled namespace, so project discovery never emits it.
const platformProjectName = project.PlatformName

// platformScope resolves the platform tier for a cluster-scoped create. It
// SSAR-gates on the caller's authority to create ref's kind - the authoring
// SIGNAL (the user never applies it; Argo does, from the platform repo), so the
// author-time check matches the apply-time AppProject boundary.
func (s *Server) platformScope(w http.ResponseWriter, r *http.Request, ref ssarRef) (scope, bool) {
	return s.platformScopeWith(w, r, func(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
		_, ok, err := s.allowed(ctx, id, c, ask(verbCreate, ref, ""))
		return ok, err
	}, "not authorized to create "+ref.resource)
}

// platformScopeGated resolves the platform tier for a route that gates its
// own verb or claim once it knows what it acts on (a namespace that exists or
// not); the coordinator then checks each object the change writes. It grants
// nothing by itself.
func (s *Server) platformScopeGated(w http.ResponseWriter, r *http.Request) (scope, bool) {
	return s.platformScopeWith(w, r, func(context.Context, auth.Identity, *cluster.Client) (bool, error) {
		return true, nil
	}, "")
}

// platformScopeWith is the shared platform-tier preamble - the caller's identity
// + cluster, the draft and -platform-repo availability gates, and the synthetic
// platform ProjectInfo - parameterized only by the authoring authorization, so
// the two security gates above cannot drift apart. Fails closed: 503 when the
// platform tier isn't configured, 403 with deny when the caller lacks authority,
// 503 when the review itself failed.
func (s *Server) platformScopeWith(w http.ResponseWriter, r *http.Request, authorized func(context.Context, auth.Identity, *cluster.Client) (bool, error), deny string) (scope, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return scope{}, false
	}
	if s.cfg.PlatformRepo == "" {
		fail(w, fmt.Errorf("%w: platform repo not configured (set -platform-repo)", model.ErrUnavailable))
		return scope{}, false
	}
	ok, err := authorized(r.Context(), id, c)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), id, err))
		return scope{}, false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: %s", model.ErrForbidden, deny))
		return scope{}, false
	}
	return scope{id: id, cluster: c, proj: s.platformProject()}, true
}
