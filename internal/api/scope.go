package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// viewMemo is a token's view as derived from the verdict cache (can), kept
// until a level it depends on moves or the first verdict it read expires, so
// the verdict cache stays the only place an answer is invalidated.
type viewMemo struct {
	ns    map[string]bool // the visible project namespaces; never mutated once cached
	names bool            // may also list namespaces: every namespace is visible (AllNamespaces)
	// reads is what the token may list in each visible namespace it was asked
	// in, wideReads in all of them. Never mutated once cached.
	reads     map[string]readSet
	wideReads readSet
	lv        visLevels
	built     time.Time
	until     time.Time
}

// readSet is the readKinds a token may list, one bit each.
type readSet uint32

// readKinds narrow what a caller is shown: listing one in Kubernetes is what
// shows it here.
var readKinds = []ssarRef{ssarVM, ssarFor(model.KindUDN), ssarFor(model.KindNAD), ssarNetpol, ssarFor(model.KindEgressFW), ssarVMI, ssarDataSources}

var allReads = readSet(1<<len(readKinds) - 1)

// callerView is what one token is shown: the namespaces it may get, and what
// it may list where, off its view for the kinds views read (readKinds) and
// asked for anything else: cluster-wide first, then where a RoleBinding names
// it. A failed review asked here reads as no.
type callerView struct {
	names map[string]bool
	memo  viewMemo
	s     *Server // nil asks nothing past the memo
	ctx   context.Context
	id    auth.Identity
	c     *cluster.Client
}

// lists reports whether the token may list ref in namespace.
func (v callerView) lists(ref ssarRef, namespace string) bool {
	if ok, held := v.memo.lists(ref, namespace); held {
		return ok
	}
	return len(v.ask(ref, []string{namespace})) == 1
}

// listsIn is those of namespaces where the token may list ref, cold ones
// asked at most authConcurrency at once.
func (v callerView) listsIn(ref ssarRef, namespaces []string) []string {
	var out, rest []string
	for _, ns := range namespaces {
		ok, held := v.memo.lists(ref, ns)
		switch {
		case !held:
			rest = append(rest, ns)
		case ok:
			out = append(out, ns)
		}
	}
	return append(out, v.ask(ref, rest)...)
}

// ask is those of namespaces where a review lets the token list ref. A list
// names no object, so a ClusterRoleBinding answers for every namespace or
// none: the cluster-wide verdict first, then each namespace a RoleBinding
// names the token in.
func (v callerView) ask(ref ssarRef, namespaces []string) []string {
	if v.s == nil || len(namespaces) == 0 {
		return nil
	}
	if all, _, err := v.s.canUntil(v.ctx, v.id, v.c, refQ(verbList, ref, "")); err == nil && all {
		return namespaces
	}
	namespaces = v.s.boundOnly(v.id, slices.DeleteFunc(slices.Clone(namespaces), func(ns string) bool { return ns == "" }))
	qs := make([]question, len(namespaces))
	for i, ns := range namespaces {
		qs[i] = refQ(verbList, ref, ns)
	}
	got, _, _ := v.s.askEach(v.ctx, v.id, v.c, qs)
	var out []string
	for i, ok := range got {
		if ok {
			out = append(out, namespaces[i])
		}
	}
	return out
}

// lists is what the view holds of whether the token may list ref in
// namespace: held=false for a kind views do not read, or a namespace it was
// not asked in.
func (m viewMemo) lists(ref ssarRef, namespace string) (ok, held bool) {
	i := slices.Index(readKinds, ref)
	if i < 0 {
		return false, false
	}
	if m.wideReads&(1<<i) != 0 {
		return true, true
	}
	r, asked := m.reads[namespace]
	return asked && r&(1<<i) != 0, asked
}

// shows reports whether a row of kind backing in namespace is the caller's:
// they see the namespace and may list the kind there. The one row filter of
// every view; optionsFor alone asks outside the view, where a binding names
// the caller (a golden-image namespace).
func (v callerView) shows(backing, namespace string) bool {
	return v.names[namespace] && v.listsBacking(backing, namespace)
}

// listsBacking is lists by the Kubernetes kind that backs a row. A kind the
// table does not name is not shown: unknown never opens anything.
func (v callerView) listsBacking(backing, namespace string) bool {
	_, k, ok := model.LookupKind(backing)
	return ok && v.lists(ssarFor(k), namespace)
}

// visLevels are the bus levels a view depends on: cluster-wide grant moves,
// grant moves in project namespaces, and the project namespaces. A move in
// any other namespace changes no answer a view holds. Each is read before
// what it covers, so a view stamped with them never misses a move they count.
type visLevels struct{ cluster, seq, ns uint64 }

func (s *Server) visLevels() visLevels {
	return visLevels{
		cluster: s.bus.BroadVersion(eventbus.RBACChanged),
		seq:     s.bus.KeySeq(eventbus.RBACChanged),
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

// unwatchedTTL is how long a cached verdict serves while some
// RBAC watch is not running: a grant move may then publish nothing, and this
// age is all that bounds a revocation.
const unwatchedTTL = 30 * time.Second

// authTTL is how long a cached verdict serves now: visibleTTL
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
// caller's token may see (TTL-cached per token). The view is the sole
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

// visibleFor is the namespaces viewFor shows.
func (s *Server) visibleFor(ctx context.Context, id auth.Identity, c *cluster.Client) (map[string]bool, error) {
	v, err := s.viewFor(ctx, id, c)
	return v.names, err
}

// viewFor is what id's token is shown, off its memoized view. A failed review
// in the view fails the request, as a gate would; a failed one asked for
// another kind hides what it covers.
func (s *Server) viewFor(ctx context.Context, id auth.Identity, c *cluster.Client) (callerView, error) {
	key := restfactory.TokenKey(id.Token)
	lv := s.visLevels()
	m, ok := s.views.Get(key)
	if now := time.Now(); !ok || m.lv != lv || !now.Before(m.until) || now.Sub(m.built) >= s.authTTL() {
		var err error
		m, err = review(ctx, s.views, key, lv, func(ctx context.Context) (viewMemo, error) {
			m, err := s.deriveView(ctx, id, c, lv)
			if err == nil {
				s.views.Put(key, m)
			}
			return m, err
		})
		if err != nil {
			return callerView{}, s.reviewFailed(ctx, id, err)
		}
	}
	names := m.ns
	if m.names {
		names = s.state.AllNamespaces()
	}
	return callerView{names: names, memo: m, s: s, ctx: ctx, id: id, c: c}, nil
}

// askEach answers qs at most authConcurrency at once: each verdict, when the
// first of them stops serving, and the first review that failed, whose
// question reads as no.
func (s *Server) askEach(ctx context.Context, id auth.Identity, c *cluster.Client, qs []question) ([]bool, time.Time, error) {
	got := make([]bool, len(qs))
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
		until = time.Now().Add(s.authTTL())
		sem   = make(chan struct{}, authConcurrency)
	)
	tk := restfactory.TokenKey(id.Token)
	for i, q := range qs {
		if ok, served, held := s.cached(tk, q); held {
			mu.Lock()
			got[i] = ok
			if served.Before(until) {
				until = served
			}
			mu.Unlock()
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ok, served, err := s.canUntil(ctx, id, c, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				first = cmp.Or(first, err)
				return
			}
			got[i] = ok
			if served.Before(until) {
				until = served
			}
		})
	}
	wg.Wait()
	return got, until, first
}

// deriveView builds id's view at lv from the verdict cache. A namespace is
// visible if the token may get it, as kubectl get namespace answers: every
// project namespace for a token that may get them all, and every namespace's
// name for one that may also list them; otherwise each project namespace a
// binding may open (gateOnly), asked by name. Then what it may list in each,
// cluster-wide first for a token that may get every namespace. Questions go
// out at most authConcurrency at once.
func (s *Server) deriveView(ctx context.Context, id auth.Identity, c *cluster.Client, lv visLevels) (viewMemo, error) {
	m := viewMemo{lv: lv, built: time.Now()}
	m.until = m.built.Add(s.authTTL())
	ask := func(qs []question) ([]bool, error) {
		got, until, err := s.askEach(ctx, id, c, qs)
		if until.Before(m.until) {
			m.until = until
		}
		return got, err
	}
	cands := s.candidates(lv.ns)
	wide, err := ask([]question{refQ(verbGet, ssarNamespace, "")})
	if err != nil {
		return viewMemo{}, err
	}
	if wide[0] {
		m.ns = cands.names
		kinds := []question{refQ(verbList, ssarNamespace, "")}
		for _, k := range readKinds {
			kinds = append(kinds, refQ(verbList, k, ""))
		}
		got, err := ask(kinds)
		if err != nil {
			return viewMemo{}, err
		}
		m.names = got[0]
		for i, ok := range got[1:] {
			if ok {
				m.wideReads |= 1 << i
			}
		}
	} else {
		gate := s.gateOnly(id, cands.list)
		qs := make([]question, len(gate))
		for i, ns := range gate {
			qs[i] = question{Verb: verbGet, Resource: ssarNamespace.resource, Namespace: ns, Name: ns}
		}
		got, err := ask(qs)
		if err != nil {
			return viewMemo{}, err
		}
		m.ns = make(map[string]bool, len(gate))
		for i, ns := range gate {
			if got[i] {
				m.ns[ns] = true
			}
		}
	}
	if m.wideReads == allReads {
		return m, nil
	}
	type at struct {
		ns   string
		kind int
	}
	var (
		qs    []question
		where []at
	)
	m.reads = map[string]readSet{}
	for _, ns := range s.boundOnly(id, slices.Sorted(maps.Keys(m.ns))) {
		m.reads[ns] = 0
		for i, k := range readKinds {
			if m.wideReads&(1<<i) == 0 {
				qs = append(qs, refQ(verbList, k, ns))
				where = append(where, at{ns, i})
			}
		}
	}
	got, err := ask(qs)
	if err != nil {
		return viewMemo{}, err
	}
	for j, ok := range got {
		if ok {
			m.reads[where[j].ns] |= 1 << where[j].kind
		}
	}
	return m, nil
}

// gateOnly is boundOnly for the gate, a review naming the namespace:
// resourceNames answers one, so a ClusterRoleBinding can open a single
// namespace and a caller one names is asked everywhere.
func (s *Server) gateOnly(id auth.Identity, namespaces []string) []string {
	if !s.indexBindings || s.state == nil || s.state.ClusterBound(id.Username, id.Groups) {
		return namespaces
	}
	return s.boundOnly(id, namespaces)
}

// boundOnly narrows namespaces to those a RoleBinding names id in (bound).
func (s *Server) boundOnly(id auth.Identity, namespaces []string) []string {
	b, ok := s.bound(id)
	if !ok {
		return namespaces
	}
	return slices.DeleteFunc(slices.Clone(namespaces), func(ns string) bool { return !b[ns] })
}

// bound is the namespaces a RoleBinding names id in, so a user costs reviews
// for where they belong, not for every namespace on the cluster. For a list
// review, which names no object, a ClusterRoleBinding reaches everywhere, the
// cluster-wide review's answer, or nowhere. ok=false narrows nothing: no index,
// or its watch incomplete.
func (s *Server) bound(id auth.Identity) (map[string]bool, bool) {
	if !s.indexBindings || s.state == nil {
		return nil, false
	}
	return s.state.BoundNamespaces(id.Username, id.Groups)
}

// candidates is the project namespaces and their projects at one namespace
// version, one value shared by every caller until the namespaces move.
type candidates struct {
	ver   uint64
	list  []string
	names map[string]bool // never mutated once built
	// projects is every project at ver, resolved once, on first use.
	projects func() resolvedProjects
}

// resolvedProjects is every project at one namespace version, shared by every
// caller, none of whom may modify it.
type resolvedProjects struct {
	list   []project.ProjectInfo
	byName map[string]project.ProjectInfo
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
	c.projects = sync.OnceValue(func() resolvedProjects {
		list := s.resolver.Resolve(nss, nil)
		byName := make(map[string]project.ProjectInfo, len(list))
		for _, p := range list {
			byName[p.Name] = p
		}
		return resolvedProjects{list: list, byName: byName}
	})
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
	ok, _, err := s.canUntil(ctx, id, c, q)
	return ok, err
}

// canUntil is can with when its verdict stops serving.
func (s *Server) canUntil(ctx context.Context, id auth.Identity, c *cluster.Client, q question) (bool, time.Time, error) {
	tk := restfactory.TokenKey(id.Token)
	if ok, until, held := s.cached(tk, q); held {
		return ok, until, nil
	}
	ver := grantStamps{ns: s.grantVersion(q.Namespace)}
	k := verdictKey(tk, q)
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
	if err != nil {
		return false, time.Time{}, err
	}
	return e.ok, time.Now().Add(s.maxAge(e.unsettled)), nil
}

// cached is q's verdict for the token keyed tk while it still serves, and
// when it stops.
func (s *Server) cached(tk string, q question) (ok bool, until time.Time, held bool) {
	e, age, found := s.ssar.GetAge(verdictKey(tk, q))
	if !found || e.ver != (grantStamps{ns: s.grantVersion(q.Namespace)}) || age >= s.maxAge(e.unsettled) {
		return false, time.Time{}, false
	}
	return e.ok, time.Now().Add(s.maxAge(e.unsettled) - age), true
}

// verdictKey is where can caches q's verdict for the token keyed tk
// (restfactory.TokenKey, computed once per batch: it hashes the token).
func verdictKey(tk string, q question) string {
	return strings.Join([]string{tk, q.Verb, q.Group, q.Resource, q.Subresource, q.Namespace, q.Name}, "\x00")
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

var (
	ssarNetpol = ssarFor(model.KindNetpol)
	ssarVMI    = ssarRef{group: "kubevirt.io", resource: "virtualmachineinstances"}
)

// ssarFor is the authority over one managed kind.
func ssarFor(k model.Kind) ssarRef { return ssarRef{k.Group, k.Plural} }

// platformProject is the synthetic platform-tier project over the platform repo.
func (s *Server) platformProject() project.ProjectInfo {
	return project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}
}

// vmReads is who reads what a VM route serves. The zero value is the gated
// one, so a route that names neither still asks.
type vmReads int

const (
	// dotvirtReads: dotvirt reads it from git or its own account, so the
	// caller must be able to list the VM there (gateVMRead).
	dotvirtReads vmReads = iota
	// callerReads: the caller's own token reads or acts, so the apiserver
	// answers for the object and nothing is asked here.
	callerReads
)

// vmScope is the preamble of every route addressing {namespace}/{name} by
// path (name is empty on the namespace-only routes): resolve the tenant project
// owning the path's namespace (the authorization point) and hand back the
// pair, gated as reads says.
func (s *Server) vmScope(w http.ResponseWriter, r *http.Request, reads vmReads) (sc scope, ns, name string, ok bool) {
	ns, name = r.PathValue("namespace"), r.PathValue("name")
	sc, ok = s.resolveProject(w, r, byNamespace(ns))
	if !ok || name == "" || reads == callerReads {
		return sc, ns, name, ok
	}
	return sc, ns, name, s.gateVMRead(w, r, sc, ns, name)
}

// gateVMRead answers 404 unless the caller may list VMs in ns: what dotvirt
// reads from git or its own account for a VM is theirs only if Kubernetes
// would show them the VM.
func (s *Server) gateVMRead(w http.ResponseWriter, r *http.Request, sc scope, ns, name string, also ...ssarRef) bool {
	v, err := s.viewFor(r.Context(), sc.id, sc.cluster)
	if err != nil {
		fail(w, err)
		return false
	}
	for _, ref := range append([]ssarRef{ssarVM}, also...) {
		if !v.lists(ref, ns) {
			fail(w, fmt.Errorf("%w: vm %s/%s", model.ErrNotFound, ns, name))
			return false
		}
	}
	return true
}

// vmNamespaces keeps those of nss whose VMs the caller may list. A failed read
// keeps none: a smaller count is honest, a larger one is not.
func (s *Server) vmNamespaces(ctx context.Context, sc scope, nss []string) []string {
	v, err := s.viewFor(ctx, sc.id, sc.cluster)
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(nss), func(ns string) bool { return !v.lists(ssarVM, ns) })
}

// The platform-tier authorities, each named once and spelled by the kind
// table: the policy views and the remaining platformScope routes gate on
// them, and the authoring signal ORs a subset.
var (
	ssarCUDN        = ssarFor(model.KindCUDN)
	ssarUplink      = ssarFor(model.KindNNCP)
	ssarNNS         = ssarRef{group: "nmstate.io", resource: "nodenetworkstates"}
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
func (s *Server) AllProjects() []project.ProjectInfo { return s.resolved().list }

func (s *Server) projectByName(name string) (project.ProjectInfo, bool) {
	p, ok := s.resolved().byName[name]
	return p, ok
}

func (s *Server) resolved() resolvedProjects {
	return s.candidates(s.bus.Version(eventbus.NamespaceChanged)).projects()
}

// seesPartOf reports whether p, as the caller sees it, lacks a namespace of
// its project. A project no longer known counts: nothing says it is whole.
func (s *Server) seesPartOf(p project.ProjectInfo) bool {
	full, ok := s.projectByName(p.Name)
	return !ok || len(full.Namespaces) > len(p.Namespaces)
}

// partialProjects names those of projects the caller sees part of: what their
// repos show narrows to the caller's own. A map lookup per project, on the
// frame's hot path.
func (s *Server) partialProjects(projects []project.ProjectInfo) map[string]bool {
	if s.state == nil || s.resolver == nil {
		return nil
	}
	var out map[string]bool
	for _, p := range projects {
		if p.Name != platformProjectName && s.seesPartOf(p) {
			if out == nil {
				out = map[string]bool{}
			}
			out[p.Name] = true
		}
	}
	return out
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
