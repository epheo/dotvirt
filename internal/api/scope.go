package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/replication"
	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/routes"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// visibleSet is a token's visible namespaces and what they were reviewed at,
// so a grant move re-asks only what it can change: a cluster-wide move, or
// the age of the last sweep, sweeps every candidate again; a move in one
// namespace re-asks that namespace; a new project namespace is asked alone.
// It keeps one verdict per visible namespace, never one per candidate: what
// moved since comes from the bus (MovedSince).
type visibleSet struct {
	ns    map[string]bool // never mutated once cached
	cands *candidates     // the project namespaces ns was reviewed over
	wide  bool            // may list VMs in every namespace: every candidate is visible
	names bool            // wide, and may list namespaces: every namespace is (AllNamespaces)
	lv    visLevels       // what the set was reconciled at

	// swept is when the last sweep began, and sweptUnsettled whether right
	// after a cluster-wide grant move: a verdict reviewed since ages from it
	// all the same. unsettled is when each namespace reviewed right after a
	// move in it was, which serves the short TTL from then (see unsettled).
	swept          time.Time
	sweptUnsettled bool
	unsettled      map[string]time.Time
}

// visLevels are the bus levels a visible set depends on: cluster-wide grant
// moves, the keyed grant sequences of both RBAC kinds (which namespaces moved
// comes from them), and the project namespaces. Each is read before what it
// covers, so a set stamped with them never misses a move they count.
type visLevels struct{ cluster, seq, other, ns uint64 }

func (s *Server) visLevels() visLevels {
	return visLevels{
		cluster: s.bus.BroadVersion(eventbus.RBACChanged),
		seq:     s.bus.KeySeq(eventbus.RBACChanged),
		other:   s.bus.KeySeq(eventbus.OtherRBACChanged),
		ns:      s.bus.Version(eventbus.NamespaceChanged),
	}
}

// ssarVerdict is one (token, question) SSAR verdict, stamped with the grant
// versions of the namespaces it was asked in (grantVersion).
type ssarVerdict struct {
	ok        bool
	ver       grantStamps
	unsettled bool
}

// grantStamp is the grant levels a review in one namespace depends on, kept
// apart rather than summed: a namespace's marks drop to 0 once it is
// forgotten (eventbus.Forget), and a sum could then re-equal an old stamp as
// the cluster-wide level moves.
type grantStamp struct{ cluster, mark, other uint64 }

// grantStamps is a verdict's stamp: its namespace's, and for a dry run of a
// Namespace object the namespace itself's (dryRun).
type grantStamps struct{ ns, obj grantStamp }

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

// unsettled reports whether a review in ns beginning at start is within
// grantSettle of the last grant move it can read: a cluster-wide one, or one
// in ns. Its answer serves unwatchedTTL only: a stale verdict from a lagging
// apiserver, stamped with the new grant version, must not hold for the long
// TTL.
func (s *Server) unsettled(start time.Time, ns string) bool {
	return s.state != nil && start.Sub(s.state.LastGrantMoveFor(ns)) < grantSettle
}

// maxAge is how long an entry serves.
func (s *Server) maxAge(unsettled bool) time.Duration {
	if unsettled {
		return unwatchedTTL
	}
	return s.authTTL()
}

// grantVersion is the change-version a review in ns depends on. The
// apiserver weighs a request in a namespace by the cluster-wide grants and
// the RoleBindings in that namespace, and a request in none by the
// cluster-wide grants alone, so a move in one namespace leaves every other
// namespace's verdicts standing. A Namespace object is asked in itself (ask).
// A namespace's moves go out as RBACChanged or OtherRBACChanged by whether
// it was a project's then, so both marks count.
func (s *Server) grantVersion(ns string) grantStamp {
	g := grantStamp{cluster: s.bus.BroadVersion(eventbus.RBACChanged)}
	if ns != "" {
		g.mark = s.bus.KeyMark(eventbus.RBACChanged, ns)
		g.other = s.bus.KeyMark(eventbus.OtherRBACChanged, ns)
	}
	return g
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

// review runs fill once per key and version, however many frame builds
// and pulls of one token miss at once (ttlcache.Do): each caller leaves on its
// own deadline, and the review stops once none waits. A caller with a newer
// version never joins an older review. ver is a comparable stamp, keyed by
// every field it holds.
func review[V any](ctx context.Context, cache *ttlcache.Cache[V], key string, ver any, fill func(context.Context) (V, error)) (V, error) {
	return cache.Do(ctx, key+"\x00"+fmt.Sprintf("%+v", ver), func(ctx context.Context) (V, error) {
		ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
		defer cancel()
		return fill(ctx)
	})
}

// noVMs reports a known absence of KubeVirt. Until discovery has answered, VMs
// are assumed: a namespace admin sees nothing for a moment, never too much.
func (s *Server) noVMs() bool {
	if s.modules == nil {
		return false
	}
	m, ok := s.modules.Get()
	return ok && !m.VMs
}

// visibilityRef is what a token must be able to list in a namespace to see it:
// VMs, or where the cluster serves none, NetworkPolicies. The rights to VMs reach
// admin, edit and view through KubeVirt's aggregated roles, which then do not
// exist, so asking about VMs would refuse every namespace admin. Not pods: log
// shippers and monitoring accounts may list those, and seeing a namespace opens
// its project's repo and network data.
func (s *Server) visibilityRef() ssarRef {
	if s.noVMs() {
		return ssarNetpol
	}
	return ssarVM
}

// visibleFor returns the set of namespaces id's token may read VMs in, or
// NetworkPolicies without KubeVirt (visibilityRef), cached by token. A token that may list them
// in every namespace sees every candidate;
// one that may also list namespaces sees every namespace's name, read off the
// SA's own watch (AllNamespaces). Any other token's candidates, the snapshot's
// project namespaces, are reviewed one by one. A failed review is never
// cached: the next read asks again.
func (s *Server) visibleFor(ctx context.Context, id auth.Identity, c *cluster.Client) (map[string]bool, error) {
	key := restfactory.TokenKey(id.Token)
	lv := s.visLevels()
	if e, ok := s.visible.Get(key); ok && e.lv == lv && s.fresh(e, time.Now()) {
		return s.visibleNames(e), nil
	}
	e, err := review(ctx, s.visible, key, lv, func(ctx context.Context) (visibleSet, error) {
		prev, had := s.visible.Get(key)
		e, err := s.reconcile(ctx, c, prev, had, lv)
		if err != nil {
			return visibleSet{}, err
		}
		s.visible.Put(key, e)
		return e, nil
	})
	if err != nil {
		return nil, s.reviewFailed(ctx, id, err)
	}
	return s.visibleNames(e), nil
}

func (s *Server) visibleNames(e visibleSet) map[string]bool {
	if e.names {
		return s.state.AllNamespaces()
	}
	return e.ns
}

// fresh reports whether every verdict e holds may still serve at now.
func (s *Server) fresh(e visibleSet, now time.Time) bool {
	if now.Sub(e.swept) >= s.maxAge(e.sweptUnsettled) {
		return false
	}
	for _, at := range e.unsettled {
		if now.Sub(at) >= unwatchedTTL {
			return false
		}
	}
	return true
}

// reconcile brings prev up to lv: a sweep when there is none, a cluster-wide
// grant moved, or the sweep aged out; otherwise the namespaces whose grants
// moved since prev, the project namespaces new since prev, and those whose
// short TTL ran out are reviewed again, and the ones no longer projects
// dropped.
func (s *Server) reconcile(ctx context.Context, c *cluster.Client, prev visibleSet, had bool, lv visLevels) (visibleSet, error) {
	cands := s.candidates(lv.ns)
	now := time.Now()
	if !had || prev.cands == nil || prev.lv.cluster != lv.cluster || now.Sub(prev.swept) >= s.maxAge(prev.sweptUnsettled) {
		return s.sweep(ctx, c, cands, lv)
	}
	next := prev
	next.lv, next.cands = lv, cands
	if prev.wide {
		next.ns = cands.names
		return next, nil
	}
	due := map[string]bool{}
	for _, ns := range s.bus.MovedSince(eventbus.RBACChanged, prev.lv.seq, cands.list) {
		due[ns] = true
	}
	for _, ns := range s.bus.MovedSince(eventbus.OtherRBACChanged, prev.lv.other, cands.list) {
		due[ns] = true
	}
	if cands != prev.cands {
		for _, ns := range cands.list {
			if !prev.cands.names[ns] {
				due[ns] = true
			}
		}
	}
	for ns, at := range prev.unsettled {
		if now.Sub(at) >= unwatchedTTL && cands.names[ns] {
			due[ns] = true
		}
	}
	if len(due) == 0 && cands == prev.cands {
		return next, nil
	}
	start := time.Now()
	see := s.visibilityRef()
	allowed, err := c.CanIn(ctx, "list", see.group, see.resource, slices.Sorted(maps.Keys(due)))
	if err != nil {
		return visibleSet{}, err
	}
	next.ns = make(map[string]bool, len(prev.ns)+len(allowed))
	for ns := range prev.ns {
		if cands.names[ns] && !due[ns] {
			next.ns[ns] = true
		}
	}
	for _, ns := range allowed {
		next.ns[ns] = true
	}
	next.unsettled = make(map[string]time.Time, len(prev.unsettled))
	for ns, at := range prev.unsettled {
		if cands.names[ns] && !due[ns] {
			next.unsettled[ns] = at
		}
	}
	for ns := range due {
		if s.unsettled(start, ns) {
			next.unsettled[ns] = start
		}
	}
	return next, nil
}

// sweep reviews the token's visibility from scratch: whether it may list VMs
// in every namespace (and namespaces too), else each candidate. The cluster-
// wide review goes first, so a token holding it costs no per-namespace one.
func (s *Server) sweep(ctx context.Context, c *cluster.Client, cands *candidates, lv visLevels) (visibleSet, error) {
	e := visibleSet{cands: cands, lv: lv, swept: time.Now()}
	see := s.visibilityRef()
	wide, err := c.Can(ctx, "list", see.group, see.resource, "")
	if err != nil {
		return visibleSet{}, err
	}
	if wide {
		e.wide, e.ns = true, cands.names
		if e.names, err = c.Can(ctx, "list", "", "namespaces", ""); err != nil {
			return visibleSet{}, err
		}
	} else {
		allowed, err := c.CanIn(ctx, "list", see.group, see.resource, cands.list)
		if err != nil {
			return visibleSet{}, err
		}
		e.ns = make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			e.ns[ns] = true
		}
	}
	if e.sweptUnsettled = s.unsettled(e.swept, ""); !e.wide && !e.sweptUnsettled {
		for _, ns := range cands.list {
			if s.unsettled(e.swept, ns) {
				if e.unsettled == nil {
					e.unsettled = map[string]time.Time{}
				}
				e.unsettled[ns] = e.swept
			}
		}
	}
	return e, nil
}

// candidates is the project namespaces visibility is reviewed over, one value
// shared by every token's set until the namespaces move.
type candidates struct {
	ver   uint64
	list  []string
	names map[string]bool // never mutated once built
}

// candidates is the snapshot's project namespaces at ver, a
// Version(NamespaceChanged) reading taken before it.
func (s *Server) candidates(ver uint64) *candidates {
	if c := s.cands.Load(); c != nil && c.ver == ver {
		return c
	}
	nss := s.state.Namespaces()
	c := &candidates{ver: ver, list: make([]string, 0, len(nss)), names: make(map[string]bool, len(nss))}
	for _, ns := range nss {
		c.list = append(c.list, ns.Name)
		c.names[ns.Name] = true
	}
	s.cands.Store(c)
	return c
}

// can answers one question under id's token, behind a per-(token, question)
// cache stamped with the grant version of the question's namespace, so a
// signal read on a polled or broadcast path does not post an SSAR per
// request, and a grant move re-asks only the questions it can change. A
// watched grant move invalidates lazily via the stamp; authTTL backstops what
// no watch observes (see RunTTLBackstop). Only a verdict is cached: a failed
// review is returned, so a blip can never pin a denial. A gate answers the
// error with s.reviewFailed; a UI signal or a filter reads it as no.
func (s *Server) can(ctx context.Context, id auth.Identity, c *cluster.Client, q question) (bool, error) {
	ver := grantStamps{ns: s.grantVersion(q.Namespace)}
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
		e := ssarVerdict{ok: ok, ver: ver, unsettled: s.unsettled(start, q.Namespace)}
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

// ssarClaims is reading a namespace's PersistentVolumeClaims.
var ssarClaims = ssarRef{resource: "persistentvolumeclaims"}

// replicationRead is everything GET /api/storage/replication shows. Those
// kinds are cluster-scoped and the snapshot behind it is the SA's, so each is
// asked of the caller.
var replicationRead = action{
	refQ(verbList, ssarRef{replication.GVRVolume.Group, replication.GVRVolume.Resource}, ""),
	refQ(verbList, ssarRef{replication.GVRPool.Group, replication.GVRPool.Resource}, ""),
	refQ(verbList, ssarRef{replication.GVRNodeDisks.Group, replication.GVRNodeDisks.Resource}, ""),
	refQ(verbList, ssarRef{replication.GVRPV.Group, replication.GVRPV.Resource}, ""),
}

// replicationRepair is resolving a split brain or moving copies: the placement
// it acts on, and the patch the apiserver then asks of the caller.
var replicationRepair = append(slices.Clone(replicationRead),
	refQ(verbPatch, ssarRef{replication.GVRVolume.Group, replication.GVRVolume.Resource}, ""))

// gatewaysRead is reading the front door across namespaces. The kinds a
// cluster may not serve are asked one by one, as the answer includes them.
var gatewaysRead = action{
	refQ(verbList, ssarRef{routes.GVRGateway.Group, routes.GVRGateway.Resource}, ""),
	refQ(verbList, ssarRef{routes.Group, "httproutes"}, ""),
}

// ssarDataSources is the boot images: listed for the options catalog, created
// when an upload registers one.
var ssarDataSources = ssarRef{group: "cdi.kubevirt.io", resource: "datasources"}

var ssarNetpol = ssarFor(model.KindNetpol)

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

// canReadPlatform reports whether id may read the platform tier: its
// history, commit and proposal diffs, revert, its merges in the task feed
// and its rollup detail. The repo names every project, namespace and owner,
// and declares the objects it holds, so the caller must list namespaces
// cluster-wide (a right scoped to one namespace, an operator namespace's
// admin role, never opens it), then every kind it declares (platformListing).
// Each question is a review cached per token, asked until one is denied; a
// tenant is asked the first alone. False when no platform repo is
// configured. An error means a read the answer needed failed.
func (s *Server) canReadPlatform(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
	if s.cfg.PlatformRepo == "" {
		return false, nil
	}
	if _, ok, err := s.allowed(ctx, id, c, ask(verbList, ssarNamespace, "")); !ok || err != nil {
		return ok, err
	}
	act, err := s.platformListing(ctx)
	if err != nil || len(act) == 0 {
		return err == nil, err
	}
	_, ok, err := s.allowed(ctx, id, c, act)
	return ok, err
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
// and for the platform tier canDraftPlatform.
func (s *Server) pickDraft(w http.ResponseWriter, r *http.Request, want string) (scope, bool) {
	if want != platformProjectName {
		return s.pickProject(w, r, want)
	}
	return s.platformScopeWith(w, r, s.canDraftPlatform, "not authorized to author platform changes")
}

// canDraftPlatform reports whether id reaches their platform draft: a
// platform reader, or any caller holding a draft there, whatever right staged
// it (a claim on one namespace), so nothing staged is stranded, even while
// platform read cannot be told or the repo read (a forge or review outage).
func (s *Server) canDraftPlatform(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
	ok, err := s.canReadPlatform(ctx, id, c)
	if ok {
		return true, nil
	}
	if held, herr := s.draft.Holds(id, s.platformProject()); herr == nil && held {
		return true, nil
	}
	return false, err
}

// pickProject resolves a project named by a ?project= query or {project} path
// segment (the whole-project routes: history, revert, and through pickDraft
// the draft routes). The platform tier resolves ONLY for platform readers
// (canReadPlatform), so neither a plain tenant nor a namespace-scoped right
// reaches the platform repo's history or revert. Any other name resolves
// from the caller's visible projects.
func (s *Server) pickProject(w http.ResponseWriter, r *http.Request, want string) (scope, bool) {
	if want == platformProjectName {
		return s.platformScopeAny(w, r)
	}
	return s.resolveProject(w, r, byName(want))
}

// platformScopeAny resolves the platform tier for the whole-project routes:
// canReadPlatform. The per-kind create routes keep their own gate.
func (s *Server) platformScopeAny(w http.ResponseWriter, r *http.Request) (scope, bool) {
	return s.platformScopeWith(w, r, s.canReadPlatform, platformReadDenied)
}

const platformReadDenied = "not authorized to read the platform repo"

// platformReadScope resolves the platform tier for a route that serves a
// caller what they own there even without platform read: reads is
// canReadPlatform's verdict. A failed review fails the route, whatever the
// caller owns.
func (s *Server) platformReadScope(w http.ResponseWriter, r *http.Request) (sc scope, reads, ok bool) {
	sc, ok = s.platformScopeWith(w, r, func(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
		var err error
		reads, err = s.canReadPlatform(ctx, id, c)
		return true, err
	}, "")
	return sc, reads, ok
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
