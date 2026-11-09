// Package replication is the read side of r-drbd: an SA-held snapshot of its
// Volumes, Pools and NodeDisks, and of the PersistentVolumes that say which
// claim each Volume serves, so a caller's pull filters in memory and no
// request reaches the cluster.
//
// Every r-drbd kind is cluster-scoped, so a tenant can never read them itself.
// Two reads leave here: Overview, where every copy lives, for a caller whose
// own token may list those kinds; and Protection, the standing of the disks in
// the namespaces a caller sees, which names no host.
package replication

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

const (
	group   = "r-drbd.io"
	version = "v1alpha1"
	driver  = "r-drbd.io"
)

// The kinds the snapshot watches. Volume is the one whose presence turns the
// module on.
var (
	GVRVolume    = schema.GroupVersionResource{Group: group, Version: version, Resource: "volumes"}
	GVRPool      = schema.GroupVersionResource{Group: group, Version: version, Resource: "pools"}
	GVRNodeDisks = schema.GroupVersionResource{Group: group, Version: version, Resource: "nodedisks"}
	GVRPV        = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
)

// Source is the cluster as the snapshot reads it; *cluster.Client in production.
type Source interface {
	DynamicListWatch(schema.GroupVersionResource) *cache.ListWatch
	Can(ctx context.Context, verb, group, resource, namespace string) (bool, error)
}

// pace is how often an idle snapshot rereads the module flag: an in-memory read
// on a cluster without r-drbd, one review of the SA's rights where an operator
// release has not granted them yet.
const pace = 10 * time.Second

// claim is who a bound PersistentVolume serves.
type claim struct{ Namespace, Name string }

type host struct {
	Blank []model.BlankDisk
}

// Snapshot holds the projections. Build with New, start with Run; reads are
// safe for concurrent callers.
type Snapshot struct {
	src    Source
	bus    *eventbus.Bus
	served func() bool

	mu      sync.RWMutex
	volumes *table[model.ReplicatedVolume]
	pools   *table[model.ReplicationPool]
	hosts   *table[host]
	claims  *table[claim]
	// gens counts each namespace's moves, so a tenant's watermark moves with
	// its own disks only; total counts every move, for an operator's.
	gens  map[string]uint64
	total uint64

	started atomic.Bool
	healthy [4]atomic.Bool
}

// New builds the snapshot. served is the module flag, which already reads
// discovery, so a cluster without r-drbd pays no API call here.
func New(src Source, bus *eventbus.Bus, served func() bool) *Snapshot {
	s := &Snapshot{src: src, bus: bus, served: served, gens: map[string]uint64{}}
	s.volumes = &table[model.ReplicatedVolume]{s: s, decode: decodeVolume, touch: s.touchVolume}
	s.pools = &table[model.ReplicationPool]{s: s, decode: decodePool, touch: touchTotal[model.ReplicationPool](s)}
	s.hosts = &table[host]{s: s, decode: decodeHost, touch: touchTotal[host](s)}
	s.claims = &table[claim]{s: s, decode: decodeClaim, touch: s.touchClaim}
	for i := range s.healthy {
		s.healthy[i].Store(true)
	}
	return s
}

// Run waits for the module and the SA's rights, then starts one reflector per
// kind for the rest of the process, and returns at once.
func (s *Snapshot) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(pace)
		defer t.Stop()
		for !s.ready(ctx) {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		s.started.Store(true)
		s.publish()
		for i, w := range []struct {
			gvr   schema.GroupVersionResource
			store cache.Store
		}{{GVRVolume, s.volumes}, {GVRPool, s.pools}, {GVRNodeDisks, s.hosts}, {GVRPV, s.claims}} {
			lw := reflect.TrackHealth(s.src.DynamicListWatch(w.gvr), &s.healthy[i], s.flipped)
			reflect.Run(ctx, lw, w.store)
		}
	}()
}

func (s *Snapshot) ready(ctx context.Context) bool {
	if !s.served() {
		return false
	}
	for _, gvr := range []schema.GroupVersionResource{GVRVolume, GVRPool, GVRNodeDisks, GVRPV} {
		ok, err := s.src.Can(ctx, "list", gvr.Group, gvr.Resource, "")
		if err != nil {
			log.Printf("replication: review list %s: %v", gvr.Resource, err)
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
		s.bus.Publish(eventbus.ReplicationChanged)
	}
}

// flipped moves the operator's watermark, which is all that shows staleness.
func (s *Snapshot) flipped() {
	s.mu.Lock()
	s.total++
	s.mu.Unlock()
	s.publish()
}

func (s *Snapshot) touchVolume(name string, _, _ *model.ReplicatedVolume) {
	s.gens[s.claims.rows[name].Namespace]++
	s.total++
}

func (s *Snapshot) touchClaim(_ string, old, now *claim) {
	if old != nil {
		s.gens[old.Namespace]++
	}
	if now != nil {
		s.gens[now.Namespace]++
	}
	s.total++
}

// touchTotal is for what only an operator reads.
func touchTotal[V any](s *Snapshot) func(string, *V, *V) {
	return func(string, *V, *V) { s.total++ }
}

// Total is the level of everything an operator's pull reads.
func (s *Snapshot) Total() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.total
}

// Gen is the level of the disks in one namespace.
func (s *Snapshot) Gen(namespace string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gens[namespace]
}

// table is one kind's projection. Only what decode names is kept, which is why
// the Volume's DRBD secret is never held past the reflector's hand-off, and a
// write that moves none of it publishes nothing. It is a cache.Store because
// a reflector needs one; nothing reads it through that interface.
type table[V any] struct {
	s      *Snapshot
	decode func(*unstructured.Unstructured) (V, bool)
	touch  func(name string, old, now *V)
	rows   map[string]V
	synced atomic.Bool
}

func (t *table[V]) Add(obj any) error    { t.put(obj); return nil }
func (t *table[V]) Update(obj any) error { t.put(obj); return nil }

func (t *table[V]) Delete(obj any) error {
	if m, ok := obj.(*unstructured.Unstructured); ok {
		t.s.mu.Lock()
		moved := t.drop(m.GetName())
		t.s.mu.Unlock()
		if moved {
			t.s.publish()
		}
	}
	return nil
}

func (t *table[V]) put(obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	now, keep := t.decode(u)
	t.s.mu.Lock()
	var moved bool
	if keep {
		moved = t.set(u.GetName(), now)
	} else {
		moved = t.drop(u.GetName())
	}
	t.s.mu.Unlock()
	if moved {
		t.s.publish()
	}
}

// set and drop run under the snapshot's lock.
func (t *table[V]) set(name string, now V) bool {
	if t.rows == nil {
		t.rows = map[string]V{}
	}
	old, had := t.rows[name]
	if had && equality.Semantic.DeepEqual(old, now) {
		return false
	}
	t.rows[name] = now
	if had {
		t.touch(name, &old, &now)
	} else {
		t.touch(name, nil, &now)
	}
	return true
}

func (t *table[V]) drop(name string) bool {
	old, had := t.rows[name]
	if !had {
		return false
	}
	delete(t.rows, name)
	t.touch(name, &old, nil)
	return true
}

// Replace applies a relist as the moves it amounts to, so one that changed
// nothing publishes nothing.
func (t *table[V]) Replace(list []any, _ string) error {
	next := make(map[string]V, len(list))
	for _, obj := range list {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			if v, keep := t.decode(u); keep {
				next[u.GetName()] = v
			}
		}
	}
	t.s.mu.Lock()
	moved := false
	for name := range t.rows {
		if _, ok := next[name]; !ok {
			moved = t.drop(name) || moved
		}
	}
	for name, v := range next {
		moved = t.set(name, v) || moved
	}
	t.s.mu.Unlock()
	first := t.synced.CompareAndSwap(false, true)
	if moved || first {
		t.s.publish()
	}
	return nil
}

func (t *table[V]) Resync() error                      { return nil }
func (t *table[V]) List() []any                        { return nil }
func (t *table[V]) ListKeys() []string                 { return nil }
func (t *table[V]) Get(any) (any, bool, error)         { return nil, false, nil }
func (t *table[V]) GetByKey(string) (any, bool, error) { return nil, false, nil }

// volumeDoc names the fields the console shows. Not spec.assignment.secret.
type volumeDoc struct {
	Spec struct {
		SizeBytes  int64    `json:"sizeBytes"`
		Replicas   int      `json:"replicas"`
		Tier       string   `json:"tier"`
		Evict      []string `json:"evict"`
		Assignment struct {
			Replicas []struct {
				Node string `json:"node"`
				Pool string `json:"pool"`
				Role string `json:"role"`
			} `json:"replicas"`
		} `json:"assignment"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Primary    string `json:"primary"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		Replicas []replicaDoc `json:"replicas"`
	} `json:"status"`
}

type replicaDoc struct {
	Node        string            `json:"node"`
	Present     bool              `json:"present"`
	DiskState   string            `json:"diskState"`
	SyncPercent *int              `json:"syncPercent"`
	InUse       bool              `json:"inUse"`
	Peers       map[string]string `json:"peers"`
}

func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	// The apiserver validated the object: a field that fails to convert is
	// near impossible, and those converted before it still show the row.
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}

func decodeVolume(u *unstructured.Unstructured) (model.ReplicatedVolume, bool) {
	doc := decode[volumeDoc](u)
	v := model.ReplicatedVolume{
		Name:      u.GetName(),
		SizeBytes: doc.Spec.SizeBytes,
		Copies:    doc.Spec.Replicas,
		Tier:      doc.Spec.Tier,
		Phase:     doc.Status.Phase,
		Primary:   doc.Status.Primary,
		Evicting:  doc.Spec.Evict,
	}
	for _, c := range doc.Status.Conditions {
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "SplitBrain":
			v.SplitBrain = true
		case "QuorumLost":
			v.QuorumLost = true
		}
	}
	reports := make(map[string]replicaDoc, len(doc.Status.Replicas))
	for _, r := range doc.Status.Replicas {
		reports[r.Node] = r
	}
	for _, a := range doc.Spec.Assignment.Replicas {
		rep := model.VolumeReplica{Node: a.Node, Pool: a.Pool, Role: a.Role}
		r, reported := reports[a.Node]
		if reported {
			rep.DiskState = r.DiskState
			rep.SyncPercent = r.SyncPercent
			rep.InUse = r.InUse
		}
		rep.State = replicaState(a.Role, a.Node, r, reported, doc.Status.Replicas)
		v.Replicas = append(v.Replicas, rep)
	}
	return v, true
}

// replicaState reads a replica from every report. A dead node's own entry
// goes stale at UpToDate, so what its peers see outranks what it last said.
func replicaState(role, node string, r replicaDoc, reported bool, all []replicaDoc) string {
	switch {
	case unreachable(node, all):
		return model.ReplicaUnreachable
	case !reported:
		return model.ReplicaUnknown
	case role != "diskful":
		return model.ReplicaStandby
	case r.SyncPercent != nil:
		return model.ReplicaCatchingUp
	case r.Present && r.DiskState == "UpToDate":
		return model.ReplicaInSync
	}
	return model.ReplicaBehind
}

// unreachable takes the peers' majority: an isolated node reports every peer
// down, and one vote from it must not mark the healthy ones.
func unreachable(node string, all []replicaDoc) bool {
	var up, down int
	for _, peer := range all {
		state, ok := peer.Peers[node]
		switch {
		case !ok || peer.Node == node:
		case state == "Connected":
			up++
		default:
			down++
		}
	}
	return down > up
}

type poolDoc struct {
	Spec struct {
		Node string `json:"node"`
		Tier string `json:"tier"`
	} `json:"spec"`
	Status struct {
		TotalBytes     int64 `json:"totalBytes"`
		FreeBytes      int64 `json:"freeBytes"`
		PlaceableBytes int64 `json:"placeableBytes"`
		Conditions     []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

func decodePool(u *unstructured.Unstructured) (model.ReplicationPool, bool) {
	doc := decode[poolDoc](u)
	p := model.ReplicationPool{
		Name:           u.GetName(),
		Node:           doc.Spec.Node,
		Tier:           doc.Spec.Tier,
		TotalBytes:     doc.Status.TotalBytes,
		FreeBytes:      doc.Status.FreeBytes,
		PlaceableBytes: doc.Status.PlaceableBytes,
	}
	for _, c := range doc.Status.Conditions {
		if c.Type == "Available" {
			p.Available = c.Status == "True"
			p.Reason = c.Reason
		}
	}
	return p, true
}

type nodeDisksDoc struct {
	Status struct {
		Disks []struct {
			Path      string `json:"path"`
			State     string `json:"state"`
			SizeBytes int64  `json:"sizeBytes"`
			Model     string `json:"model"`
		} `json:"disks"`
	} `json:"status"`
}

func decodeHost(u *unstructured.Unstructured) (host, bool) {
	var h host
	for _, d := range decode[nodeDisksDoc](u).Status.Disks {
		if d.State == "Blank" {
			h.Blank = append(h.Blank, model.BlankDisk{Path: d.Path, SizeBytes: d.SizeBytes, Model: d.Model})
		}
	}
	return h, true
}

type pvDoc struct {
	Spec struct {
		CSI struct {
			Driver string `json:"driver"`
		} `json:"csi"`
		ClaimRef struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// decodeClaim keeps r-drbd's PersistentVolumes. Only a Bound one names a
// claim: a Released one still carries the old claimRef, and its namespace may
// have changed hands since.
func decodeClaim(u *unstructured.Unstructured) (claim, bool) {
	doc := decode[pvDoc](u)
	if doc.Spec.CSI.Driver != driver {
		return claim{}, false
	}
	if doc.Status.Phase != "Bound" {
		return claim{}, true
	}
	return claim{Namespace: doc.Spec.ClaimRef.Namespace, Name: doc.Spec.ClaimRef.Name}, true
}

// Overview is where every copy lives and the room left. Operator-only: the
// caller's own token must be allowed to list what it shows.
func (s *Snapshot) Overview() model.Replication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := model.Replication{
		Synced:  s.volumes.synced.Load() && s.pools.synced.Load() && s.hosts.synced.Load() && s.claims.synced.Load(),
		Stale:   !reflect.AllHealthy(s.healthy[:]),
		Pools:   make([]model.ReplicationPool, 0, len(s.pools.rows)),
		Volumes: make([]model.ReplicatedVolume, 0, len(s.volumes.rows)),
		Hosts:   make([]model.ReplicationHost, 0, len(s.hosts.rows)),
	}
	for _, p := range s.pools.rows {
		out.Pools = append(out.Pools, p)
	}
	slices.SortFunc(out.Pools, func(a, b model.ReplicationPool) int {
		return strings.Compare(a.Node+"\x00"+a.Name, b.Node+"\x00"+b.Name)
	})
	for name, v := range s.volumes.rows {
		c := s.claims.rows[name]
		v.Namespace, v.Claim = c.Namespace, c.Name
		out.Volumes = append(out.Volumes, v)
	}
	slices.SortFunc(out.Volumes, func(a, b model.ReplicatedVolume) int {
		return strings.Compare(a.Namespace+"\x00"+a.Claim+"\x00"+a.Name, b.Namespace+"\x00"+b.Claim+"\x00"+b.Name)
	})
	for name, h := range s.hosts.rows {
		out.Hosts = append(out.Hosts, model.ReplicationHost{Name: name, Blank: h.Blank})
	}
	slices.SortFunc(out.Hosts, func(a, b model.ReplicationHost) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Protection is the standing of the disks claimed in namespaces. It is built
// field by field from what a tenant may know: no host, pool or condition text
// crosses here.
func (s *Snapshot) Protection(namespaces []string) []model.DiskProtection {
	wanted := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		wanted[ns] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.DiskProtection{}
	for name, c := range s.claims.rows {
		if c.Name == "" || !wanted[c.Namespace] {
			continue
		}
		v, ok := s.volumes.rows[name]
		if !ok {
			continue
		}
		p := model.DiskProtection{Namespace: c.Namespace, Claim: c.Name, Copies: v.Copies}
		p.InSync, p.SyncPercent, p.Standing = standing(v)
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b model.DiskProtection) int {
		return strings.Compare(a.Namespace+"\x00"+a.Claim, b.Namespace+"\x00"+b.Claim)
	})
	return out
}

// standing folds a volume into what its owner can act on.
func standing(v model.ReplicatedVolume) (inSync int, percent *int, st string) {
	for _, r := range v.Replicas {
		if r.Role != "diskful" {
			continue
		}
		switch r.State {
		case model.ReplicaInSync:
			inSync++
		case model.ReplicaCatchingUp:
			if percent == nil || (r.SyncPercent != nil && *r.SyncPercent < *percent) {
				percent = r.SyncPercent
			}
		}
	}
	switch {
	case v.Phase == "Failed", v.SplitBrain, v.QuorumLost:
		st = model.StandingNeedsOperator
	case v.Phase == "" || v.Phase == "Pending" || v.Phase == "Provisioning" || v.Phase == "Deleting":
		st = model.StandingPending
	case inSync == 0:
		st = model.StandingNeedsOperator
	case v.Copies <= 1:
		st = model.StandingLocal
	case inSync >= v.Copies:
		st = model.StandingProtected
	case percent != nil:
		st = model.StandingCatchingUp
	default:
		st = model.StandingExposed
	}
	return inSync, percent, st
}
