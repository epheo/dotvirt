// Package desktops is the read side of DeskuBe: an SA-held snapshot of its
// classes, pools, desktops and claims, so a caller's pull filters in memory
// and no request reaches the cluster.
//
// Two reads leave here. Overview: each namespaced kind where the caller may
// list it, the classes for a caller who may list them cluster-wide, and the
// kinds it may not list beside those, from its own rights alone. Desktop and
// Instance: one desktop and the claim holding it, or the desktop alone, which
// an act addresses, trusted only while Fresh.
package desktops

import (
	"cmp"
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

// Group is DeskuBe's API group.
const Group = "vdi.desku.be"

const version = "v1alpha1"

// claimUIDLabel names, on a pool's desktop, the claim it is held for. A claim
// name is free once its claim is deleted: the UID is what tells a new claim
// under that name from the old one.
const claimUIDLabel = Group + "/claim-uid"

// RestartAnnotation asks DeskuBe to restart a desktop, a new value per
// request. DeskuBe never removes it, and writes the last value it rolled out
// to status.restart.
const RestartAnnotation = Group + "/restart"

// maxRestartBytes is the longest request DeskuBe rolls out: it copies the
// value into the Deployment, its ReplicaSets and the pod, and only logs a
// longer one.
const maxRestartBytes = 64

// The kinds the snapshot watches. Instance is the one whose presence turns the
// module on.
var (
	GVRClass    = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiclasses"}
	GVRPool     = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdipools"}
	GVRInstance = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiinstances"}
	GVRClaim    = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiclaims"}
)

// Source is the cluster as the snapshot reads it; *cluster.Client in production.
type Source interface {
	ServesAPIResource(schema.GroupVersionResource) (bool, error)
	DynamicListWatch(schema.GroupVersionResource) *cache.ListWatch
	Can(ctx context.Context, verb, group, resource, namespace string) (bool, error)
}

// pace is how often an idle snapshot rereads the module flag: an in-memory read
// on a cluster without DeskuBe, one review of the SA's rights where an operator
// release has not granted them yet.
var pace = 10 * time.Second

// Kind is one kind in one namespace. The classes are at "".
type Kind struct{ Namespace, Resource string }

// Namespaced are the kinds read per namespace; the classes are cluster-scoped.
var Namespaced = []schema.GroupVersionResource{GVRPool, GVRInstance, GVRClaim}

// watched is one watched kind and its projection.
type watched struct {
	gvr   schema.GroupVersionResource
	table interface {
		cache.Store
		Synced() bool
		Clear()
	}
}

// Snapshot holds the projections. Build with New, start with Run; reads are
// safe for concurrent callers.
type Snapshot struct {
	src    Source
	bus    *eventbus.Bus
	served func() bool

	mu        sync.RWMutex
	classes   *reflect.Table[model.DesktopClass]
	pools     *reflect.Table[model.DesktopPool]
	instances *reflect.Table[model.Desktop]
	claims    *reflect.Table[model.DesktopClaim]
	// gens counts each kind's moves per namespace, so a caller's watermark
	// moves with what it may list only.
	gens map[Kind]uint64
	// flips counts, per kind, what a pull reading it shows besides rows:
	// whether it is synced, its watch's health, and the module stopping.
	flips [4]uint64

	started atomic.Bool
	// healthy, absent and flips follow the order of kinds. absent is set only
	// on discovery's definite no: a failed read leaves a kind unknown.
	healthy [4]atomic.Bool
	absent  [4]atomic.Bool
}

// kinds are the watched kinds, in the order of healthy and absent.
func (s *Snapshot) kinds() []watched {
	return []watched{{GVRClass, s.classes}, {GVRPool, s.pools}, {GVRInstance, s.instances}, {GVRClaim, s.claims}}
}

// New builds the snapshot. served is the module flag, which already reads
// discovery, so a cluster without DeskuBe pays no API call here.
func New(src Source, bus *eventbus.Bus, served func() bool) *Snapshot {
	s := &Snapshot{src: src, bus: bus, served: served, gens: map[Kind]uint64{}}
	s.classes = newTable(s, GVRClass.Resource, decodeClass, nil)
	s.pools = newTable(s, GVRPool.Resource, decodePool, reflect.NamespacedKey)
	s.instances = newTable(s, GVRInstance.Resource, decodeInstance, reflect.NamespacedKey)
	s.claims = newTable(s, GVRClaim.Resource, decodeClaim, reflect.NamespacedKey)
	for i := range s.healthy {
		s.healthy[i].Store(true)
	}
	return s
}

func newTable[V any](s *Snapshot, resource string, decode func(*unstructured.Unstructured) (V, bool), key func(*unstructured.Unstructured) string) *reflect.Table[V] {
	return &reflect.Table[V]{Lock: &s.mu, Decode: decode, Key: key, Publish: s.publish,
		Touch: func(k string, _, _ *V) {
			at := Kind{Resource: resource}
			if key != nil {
				at.Namespace, _, _ = strings.Cut(k, "/")
			}
			s.gens[at]++
		}}
}

// Run keeps one reflector per kind running while the module is on, and returns
// at once. Off stops them, so an uninstalled DeskuBe leaves no watch retrying.
func (s *Snapshot) Run(ctx context.Context) {
	reflect.RunWhile(ctx, pace, func() bool { return s.ready(ctx) }, s.served, s.start, s.clear)
}

// start watches each kind once discovery serves it, so a DeskuBe release
// missing one leaves no failing watch. One served later is watched from then
// on.
func (s *Snapshot) start(ctx context.Context) {
	s.started.Store(true)
	s.publish()
	for i, k := range s.kinds() {
		reflect.RunWhenServed(ctx, s.probe(i), k.gvr, s.src.DynamicListWatch(k.gvr), k.table, &s.healthy[i], s.flipped(i))
	}
}

// probe is discovery's answer for kind i. Only a definite no lets Synced stop
// waiting for the kind, and that answer moving publishes, as no list follows
// it.
func (s *Snapshot) probe(i int) func(schema.GroupVersionResource) bool {
	return func(gvr schema.GroupVersionResource) bool {
		served, err := s.src.ServesAPIResource(gvr)
		if err != nil {
			log.Printf("desktops: discover %s: %v", gvr.Resource, err)
			return false
		}
		if s.absent[i].Swap(!served) != !served {
			s.flipped(i)()
		}
		return served
	}
}

// clear forgets what a stopped module's watches left.
func (s *Snapshot) clear() {
	s.started.Store(false)
	for i, k := range s.kinds() {
		k.table.Clear()
		s.absent[i].Store(false)
		s.healthy[i].Store(true)
	}
	s.mu.Lock()
	for i := range s.flips {
		s.flips[i]++
	}
	s.mu.Unlock()
	s.publish()
}

func (s *Snapshot) ready(ctx context.Context) bool {
	if !s.served() {
		return false
	}
	for _, k := range s.kinds() {
		ok, err := s.src.Can(ctx, "list", k.gvr.Group, k.gvr.Resource, "")
		if err != nil {
			log.Printf("desktops: review list %s: %v", k.gvr.Resource, err)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

// Started reports whether the reflectors run: until then every read is empty
// and a frame needs no watermark.
func (s *Snapshot) Started() bool { return s.started.Load() }

func (s *Snapshot) publish() {
	if s.bus != nil {
		s.bus.Publish(eventbus.DesktopsChanged)
	}
}

// flipped moves the watermark of each caller reading kind i: its pull says
// whether the kind is synced and whether it is stale.
func (s *Snapshot) flipped(i int) func() {
	return func() {
		s.mu.Lock()
		s.flips[i]++
		s.mu.Unlock()
		s.publish()
	}
}

// reads is which of kinds readable names.
func (s *Snapshot) reads(readable map[Kind]bool) (out [4]bool) {
	resources := map[string]bool{}
	for k := range readable {
		resources[k.Resource] = true
	}
	for i, k := range s.kinds() {
		out[i] = resources[k.gvr.Resource]
	}
	return out
}

// status is what the kinds in read say, and no other: whether they are
// synced, which are stale, and their level. A caller is never told of a kind
// it may not list. Runs under mu.
func (s *Snapshot) status(read [4]bool) (synced bool, stale []string, level uint64) {
	synced = s.started.Load()
	for i, k := range s.kinds() {
		if !read[i] {
			continue
		}
		if !s.absent[i].Load() && !k.table.Synced() {
			synced = false
		}
		if !s.healthy[i].Load() {
			stale = append(stale, k.gvr.Resource)
		}
		level += s.flips[i]
	}
	level <<= 1
	if synced {
		level |= 1
	}
	return synced, stale, level
}

// Base is the level of what a pull reading readable shows besides rows:
// whether its kinds are synced, and whether one is stale.
func (s *Snapshot) Base(readable map[Kind]bool) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _, level := s.status(s.reads(readable))
	return level
}

// Gen is the level of one kind in one namespace.
func (s *Snapshot) Gen(k Kind) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gens[k]
}

// none is the answer of a cluster without DeskuBe.
func none() model.Desktops {
	return model.Desktops{
		Classes:  []model.DesktopClass{},
		Pools:    []model.DesktopPool{},
		Desktops: []model.Desktop{},
		Claims:   []model.DesktopClaim{},
		Unlisted: []model.DesktopKind{},
	}
}

// Overview is what readable shows: each kind in the namespaces it names, and
// the classes when it holds them at "". A pool-made desktop sits under its
// pool where the pool is shown, else with the others. unlisted are the
// caller's own rights, said so that a pool's count beside no desktop does not
// read as none. A caller with neither, who may list no kind, gets what a
// cluster without DeskuBe answers, as does every caller while the module is
// off: not even whether the pull is synced.
func (s *Snapshot) Overview(readable map[Kind]bool, unlisted []Kind) model.Desktops {
	out := none()
	if len(readable) == 0 && len(unlisted) == 0 || !s.started.Load() {
		return out
	}
	for _, k := range unlisted {
		out.Unlisted = append(out.Unlisted, model.DesktopKind{Namespace: k.Namespace, Resource: k.Resource})
	}
	slices.SortFunc(out.Unlisted, func(a, b model.DesktopKind) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Resource, b.Resource))
	})
	s.mu.RLock()
	defer s.mu.RUnlock()
	out.Synced, out.Stale, _ = s.status(s.reads(readable))
	if readable[Kind{Resource: GVRClass.Resource}] {
		for _, c := range s.classes.Rows {
			out.Classes = append(out.Classes, c)
		}
	}
	claimed := map[string]model.DesktopClaim{}
	for _, c := range s.claims.Rows {
		if !readable[Kind{c.Namespace, GVRClaim.Resource}] {
			continue
		}
		out.Claims = append(out.Claims, c)
		if !holds(c) {
			continue
		}
		key := c.Namespace + "/" + c.Desktop
		if held, ok := claimed[key]; !ok || c.Name < held.Name {
			claimed[key] = c
		}
	}
	pools := map[string]int{}
	for key, p := range s.pools.Rows {
		if !readable[Kind{p.Namespace, GVRPool.Resource}] {
			continue
		}
		pools[key] = len(out.Pools)
		p.Desktops = []model.Desktop{}
		out.Pools = append(out.Pools, p)
	}
	for key, d := range s.instances.Rows {
		if !readable[Kind{d.Namespace, GVRInstance.Resource}] {
			continue
		}
		d.Claim = claimed[key].Name
		if i, ok := pools[d.Namespace+"/"+d.Pool]; ok {
			out.Pools[i].Desktops = append(out.Pools[i].Desktops, d)
			continue
		}
		out.Desktops = append(out.Desktops, d)
	}
	slices.SortFunc(out.Classes, func(a, b model.DesktopClass) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(out.Pools, func(a, b model.DesktopPool) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	for _, p := range out.Pools {
		slices.SortFunc(p.Desktops, byDesktop)
	}
	slices.SortFunc(out.Desktops, byDesktop)
	slices.SortFunc(out.Claims, func(a, b model.DesktopClaim) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	return out
}

// holds reports whether c holds the desktop it names. A claim naming a desktop
// it does not hold is no claim of the desktop's: a stranger's is refused.
func holds(c model.DesktopClaim) bool { return c.Desktop != "" && c.Phase == "Bound" }

// Fresh reports whether each of kinds is watched, synced and healthy: what an
// act reads of them is the cluster's latest.
func (s *Snapshot) Fresh(kinds ...schema.GroupVersionResource) bool {
	if !s.started.Load() {
		return false
	}
	for i, k := range s.kinds() {
		if slices.Contains(kinds, k.gvr) && (!s.healthy[i].Load() || !k.table.Synced()) {
			return false
		}
	}
	return true
}

// Instance is the desktop namespace/name alone, never paired with a claim:
// for an act a caller who may not list the claims there makes.
func (s *Snapshot) Instance(namespace, name string) (model.Desktop, bool) {
	if !s.started.Load() {
		return model.Desktop{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.instances.Rows[namespace+"/"+name]
	return d, ok
}

// HasPod reports whether a desktop's phase shows its pod: Ready, or Starting
// on a node. Any other has no session a restart would end: a Stopping or
// Stopped one stays stopped, a Provisioning one has no pod yet, a Failed one
// may have none.
func HasPod(phase string) bool { return phase == "Ready" || phase == "Starting" }

// ClassMissing reports whether DeskuBe holds d back for want of its class. It
// rolls out no restart then: one asked would wait for the class, and end
// whatever session runs when it comes.
func ClassMissing(d model.Desktop) bool {
	return slices.ContainsFunc(d.Conditions, func(c model.DesktopCondition) bool {
		return c.Type == "Ready" && c.Status == "False" && c.Reason == "ClassNotFound"
	})
}

// Desktop is the desktop namespace/name and the claim holding it, as Overview
// pairs them, for a caller who may list both kinds there.
func (s *Snapshot) Desktop(namespace, name string) (d model.Desktop, holder model.DesktopClaim, ok bool) {
	if !s.started.Load() {
		return d, holder, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok = s.instances.Rows[namespace+"/"+name]
	if !ok {
		return d, holder, false
	}
	for _, c := range s.claims.Rows {
		if c.Namespace == namespace && c.Desktop == name && holds(c) && (holder.Name == "" || c.Name < holder.Name) {
			holder = c
		}
	}
	d.Claim = holder.Name
	return d, holder, true
}

func byDesktop(a, b model.Desktop) int {
	return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
}

func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	// The apiserver validated the object: a field that fails to convert is
	// near impossible, and those converted before it still show the row.
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}

type classDoc struct {
	Spec struct {
		Image string `json:"image"`
	} `json:"spec"`
}

func decodeClass(u *unstructured.Unstructured) (model.DesktopClass, bool) {
	return model.DesktopClass{Name: u.GetName(), Image: decode[classDoc](u).Spec.Image}, true
}

// poolDoc reads each count DeskuBe leaves out as 0.
type poolDoc struct {
	Spec struct {
		ClassName  string `json:"className"`
		Seats      int32  `json:"seats"`
		Warm       int32  `json:"warm"`
		Assignment string `json:"assignment"`
		Closed     bool   `json:"closed"`
		Subjects   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"subjects"`
		IdleTimeout  *metav1.Duration `json:"idleTimeout"`
		ReleaseAfter *metav1.Duration `json:"releaseAfter"`
	} `json:"spec"`
	Status struct {
		model.DesktopPoolCounts `json:",inline"`
		Conditions              []model.DesktopCondition `json:"conditions"`
	} `json:"status"`
}

func decodePool(u *unstructured.Unstructured) (model.DesktopPool, bool) {
	doc := decode[poolDoc](u)
	p := model.DesktopPool{
		Namespace:    u.GetNamespace(),
		Name:         u.GetName(),
		Class:        doc.Spec.ClassName,
		Seats:        doc.Spec.Seats,
		KeepWarm:     doc.Spec.Warm,
		Assignment:   doc.Spec.Assignment,
		Closed:       doc.Spec.Closed,
		IdleTimeout:  duration(doc.Spec.IdleTimeout),
		ReleaseAfter: duration(doc.Spec.ReleaseAfter),
		Counts:       doc.Status.DesktopPoolCounts,
		Conditions:   doc.Status.Conditions,
	}
	for _, sub := range doc.Spec.Subjects {
		p.Subjects = append(p.Subjects, model.DesktopSubject{Kind: sub.Kind, Name: sub.Name})
	}
	return p, true
}

// duration writes a set duration as Go prints it, so one written as 30m and
// one written as 1800s read the same. Unset is empty: 0s is a setting.
func duration(d *metav1.Duration) string {
	if d == nil {
		return ""
	}
	return d.Duration.String()
}

// instanceDoc names the fields the console shows. Not spec.pairing nor
// status.pairing: both carry a live pairing PIN. Not status.adminSecret. Not
// status.lastActive: DeskuBe rewrites it about every 30s on a busy desktop,
// and each rewrite would publish.
type instanceDoc struct {
	Spec struct {
		ClassName string `json:"className"`
		Image     string `json:"image"`
		Owner     string `json:"owner"`
		Running   *bool  `json:"running"`
	} `json:"spec"`
	Status struct {
		Phase        string                   `json:"phase"`
		Address      string                   `json:"address"`
		SessionState string                   `json:"sessionState"`
		Restart      string                   `json:"restart"`
		Conditions   []model.DesktopCondition `json:"conditions"`
	} `json:"status"`
}

func decodeInstance(u *unstructured.Unstructured) (model.Desktop, bool) {
	doc := decode[instanceDoc](u)
	asked := u.GetAnnotations()[RestartAnnotation]
	return model.Desktop{
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
		UID:        string(u.GetUID()),
		Class:      doc.Spec.ClassName,
		Image:      doc.Spec.Image,
		Owner:      doc.Spec.Owner,
		Running:    doc.Spec.Running == nil || *doc.Spec.Running,
		Phase:      doc.Status.Phase,
		Address:    doc.Status.Address,
		Session:    doc.Status.SessionState,
		Restarting: asked != "" && asked != doc.Status.Restart && len(asked) <= maxRestartBytes,
		Pool:       madeBy(u),
		ClaimUID:   u.GetLabels()[claimUIDLabel],
		Conditions: doc.Status.Conditions,
	}, true
}

// madeBy is the pool controlling a desktop, as DeskuBe's own admission policy
// tells one: not the pool label, which an orphaned desktop keeps.
func madeBy(u *unstructured.Unstructured) string {
	for _, o := range u.GetOwnerReferences() {
		if o.Kind == "VdiPool" && strings.HasPrefix(o.APIVersion, Group+"/") && o.Controller != nil && *o.Controller {
			return o.Name
		}
	}
	return ""
}

// claimDoc reads a claim on a pool, or on one desktop by name. Its lastUsed
// moves when the claim gets its desktop, starts or stops, never on a session's
// heartbeat.
type claimDoc struct {
	Spec struct {
		PoolName    string `json:"poolName"`
		DesktopName string `json:"desktopName"`
		User        string `json:"user"`
		Running     *bool  `json:"running"`
	} `json:"spec"`
	Status struct {
		Phase       string `json:"phase"`
		DesktopName string `json:"desktopName"`
		Position    int32  `json:"position"`
		Message     string `json:"message"`
		LastUsed    string `json:"lastUsed"`
	} `json:"status"`
}

func decodeClaim(u *unstructured.Unstructured) (model.DesktopClaim, bool) {
	doc := decode[claimDoc](u)
	return model.DesktopClaim{
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		UID:       string(u.GetUID()),
		User:      doc.Spec.User,
		Pool:      doc.Spec.PoolName,
		Desktop:   cmp.Or(doc.Status.DesktopName, doc.Spec.DesktopName),
		Running:   doc.Spec.Running == nil || *doc.Spec.Running,
		Phase:     doc.Status.Phase,
		Position:  doc.Status.Position,
		Message:   doc.Status.Message,
		LastUsed:  doc.Status.LastUsed,
	}, true
}
