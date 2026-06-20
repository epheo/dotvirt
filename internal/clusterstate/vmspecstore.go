package clusterstate

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// vmSpecStore wraps the VirtualMachine indexer and splits its delta stream into two
// signals: onLive fires on EVERY mutation (VM status/existence — the live tree),
// while onSpec fires only when the exported manifest set could have moved — a VM was
// added, removed, or its metadata.generation (i.e. its spec) changed. KubeVirt
// writes VM.status frequently with generation unchanged; gating onSpec on generation
// is what keeps the exporter from re-running its marshal pipeline on every such
// status heartbeat (it subscribes to VMSpecChanged, not LiveChanged).
//
// Single-threaded: a reflector drives one store from one goroutine, so the gen map
// is written here and read nowhere else — no lock needed.
type vmSpecStore struct {
	cache.Indexer
	gen      map[string]int64 // key -> last-seen metadata.generation
	onSpec   func()
	onLive   func()
	onSynced func()
	didSync  bool
}

// newVMSpecStore wraps idx (the VM read indexer). onSpec fires on a generation change
// or add/remove; onLive fires on every mutation; onSynced fires once on first Replace.
func newVMSpecStore(idx cache.Indexer, onSpec, onLive, onSynced func()) cache.Indexer {
	return &vmSpecStore{Indexer: idx, gen: map[string]int64{}, onSpec: onSpec, onLive: onLive, onSynced: onSynced}
}

// keyAndGen extracts the namespace/name key and generation of a VM object.
func keyAndGen(obj any) (key string, generation int64, ok bool) {
	m, isMeta := obj.(metav1.Object)
	if !isMeta {
		return "", 0, false
	}
	return m.GetNamespace() + "/" + m.GetName(), m.GetGeneration(), true
}

func (c *vmSpecStore) Add(obj any) error {
	if err := c.Indexer.Add(obj); err != nil {
		return err
	}
	if k, g, ok := keyAndGen(obj); ok {
		c.gen[k] = g
	}
	c.onSpec() // a new VM grows the exported manifest set
	c.onLive()
	return nil
}

func (c *vmSpecStore) Update(obj any) error {
	k, g, ok := keyAndGen(obj)
	specChanged := !ok // fail-safe: if generation is unreadable, treat as a spec change
	if ok {
		old, had := c.gen[k]
		specChanged = !had || old != g
	}
	if err := c.Indexer.Update(obj); err != nil {
		return err // don't advance the gen map if the store rejected the object
	}
	if ok {
		c.gen[k] = g
	}
	c.onLive() // status churn — the live tree always cares
	if specChanged {
		c.onSpec()
	}
	return nil
}

func (c *vmSpecStore) Delete(obj any) error {
	if err := c.Indexer.Delete(obj); err != nil {
		return err
	}
	if k, _, ok := keyAndGen(obj); ok {
		delete(c.gen, k)
	}
	c.onSpec() // a removed VM prunes its manifest
	c.onLive()
	return nil
}

func (c *vmSpecStore) Replace(list []any, rv string) error {
	if err := c.Indexer.Replace(list, rv); err != nil {
		return err
	}
	// Rebuild the generation map from the relist. Don't diff across the gap (a relist
	// is rare — a watch bookmark gap / 410 Gone; a conservative spec signal is correct
	// and the exporter's content-signature skip makes a spurious wake a no-op).
	c.gen = make(map[string]int64, len(list))
	for _, obj := range list {
		if k, g, ok := keyAndGen(obj); ok {
			c.gen[k] = g
		}
	}
	if !c.didSync {
		c.didSync = true
		if c.onSynced != nil {
			c.onSynced()
		}
	}
	c.onSpec()
	c.onLive()
	return nil
}
