package reflect

import (
	"hash/maphash"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/cache"
)

// generations counts writes per namespace beside the indexer it wraps, so a
// reader scoped to some namespaces learns whether any of them moved by
// reading a few counters, never by scanning the store. "" counts the
// cluster-scoped objects.
type generations struct {
	cache.Indexer
	mu    sync.RWMutex
	gens  map[string]uint64
	total uint64 // the sum of gens
}

func (g *generations) Add(obj any) error {
	if err := g.Indexer.Add(obj); err != nil {
		return err
	}
	g.bump(obj)
	return nil
}

func (g *generations) Update(obj any) error {
	if err := g.Indexer.Update(obj); err != nil {
		return err
	}
	g.bump(obj)
	return nil
}

func (g *generations) Delete(obj any) error {
	if err := g.Indexer.Delete(obj); err != nil {
		return err
	}
	g.bump(obj)
	return nil
}

// Replace moves only the namespaces whose objects or their versions differ
// from the store's: a relist after a watch drop that changed nothing moves
// nothing, so no reader re-pulls for it.
func (g *generations) Replace(list []any, rv string) error {
	before := contents(g.Indexer.List())
	if err := g.Indexer.Replace(list, rv); err != nil {
		return err
	}
	after := contents(list)
	g.mu.Lock()
	defer g.mu.Unlock()
	for ns, d := range after {
		if before[ns] != d {
			g.gens[ns]++
			g.total++
		}
	}
	for ns := range before {
		if _, ok := after[ns]; !ok {
			g.gens[ns]++
			g.total++
		}
	}
	return nil
}

var contentSeed = maphash.MakeSeed()

// contents digests each namespace's objects by name and resource version.
func contents(objs []any) map[string]uint64 {
	out := map[string]uint64{}
	var h maphash.Hash
	for _, obj := range objs {
		m, err := meta.Accessor(obj)
		if err != nil {
			continue
		}
		h.SetSeed(contentSeed)
		_, _ = h.WriteString(m.GetName())
		_ = h.WriteByte(0)
		_, _ = h.WriteString(m.GetResourceVersion())
		out[m.GetNamespace()] += h.Sum64()
	}
	return out
}

// bump runs after a write the indexer accepted, so obj has object metadata.
func (g *generations) bump(obj any) {
	var ns string
	if m, err := meta.Accessor(obj); err == nil {
		ns = m.GetNamespace()
	}
	g.mu.Lock()
	g.gens[ns]++
	g.total++
	g.mu.Unlock()
}

// Generation is idx's write count over the namespaces nss ("" counts its
// cluster-scoped objects): it moves on every write to an object in one of
// them, and on nothing else. Equality-only, like the bus versions. 0 for an
// indexer NewIndexer did not build.
func Generation(idx cache.Indexer, nss ...string) uint64 {
	g, ok := idx.(*generations)
	if !ok {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	var v uint64
	for _, ns := range nss {
		v += g.gens[ns]
	}
	return v
}

// Counted reports whether idx counts its writes (NewIndexer built it): a
// generation of any other indexer never moves.
func Counted(idx cache.Indexer) bool {
	_, ok := idx.(*generations)
	return ok
}

// StoreGeneration is idx's write count over every object it holds: a view
// decoded from the whole store stays current while it holds still. Like
// Generation, equality-only, and 0 for an indexer NewIndexer did not build.
func StoreGeneration(idx cache.Indexer) uint64 {
	g, ok := idx.(*generations)
	if !ok {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.total
}
