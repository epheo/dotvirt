package netstate

import (
	"slices"
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/reflect"
)

// memo holds one view decoded from the stores, tagged with the key it was
// built at: the generations of the stores it reads. The key is read under the
// lock, so a build reads stores at least as new as its tag, and readers that
// meet a moved key wait for one rebuild instead of each decoding the stores.
// The view is shared by every caller: nothing writes through it.
type memo[K comparable, V any] struct {
	mu    sync.Mutex
	built bool
	key   K
	val   V
}

func (m *memo[K, V]) get(key func() K, build func(K) V) V {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k := key(); !m.built || m.key != k {
		m.val, m.key, m.built = build(k), k, true
	}
	return m.val
}

// plane is one policy store decoded at one generation.
type plane struct {
	all   []*policy            // by (namespace, name), or the store's own order
	byNS  map[string][]*policy // each namespace's, by name
	byKey map[string]*policy   // by store key
}

// policyStore decodes one policy kind's store at most once per generation.
type policyStore struct {
	idx   cache.Indexer
	dec   decoder
	order func([]*policy) // reorders all, when the kind is read in another order
	memo  memo[uint64, *plane]
	// last is each object of the last build with its decode, which the next
	// build reuses: a stored object is replaced on update, never written, so
	// only what moved decodes again. Touched only inside a build, under
	// memo's lock.
	last map[*unstructured.Unstructured]*policy
}

func newPolicyStore(idx cache.Indexer, dec decoder, order func([]*policy)) *policyStore {
	return &policyStore{idx: idx, dec: dec, order: order}
}

func (p *policyStore) plane() *plane {
	return p.memo.get(func() uint64 { return reflect.StoreGeneration(p.idx) }, func(uint64) *plane {
		objs := sortedList(p.idx)
		pl := &plane{all: make([]*policy, 0, len(objs)), byNS: map[string][]*policy{}, byKey: make(map[string]*policy, len(objs))}
		decoded := make(map[*unstructured.Unstructured]*policy, len(objs))
		for _, u := range objs {
			pol, ok := p.last[u]
			if !ok {
				pol = p.dec(u)
			}
			decoded[u] = pol
			ns, key := u.GetNamespace(), u.GetName()
			if ns != "" {
				key = ns + "/" + key
			}
			pl.all = append(pl.all, pol)
			pl.byNS[ns] = append(pl.byNS[ns], pol)
			pl.byKey[key] = pol
		}
		// Clipped, so a reader's append can never write into the view.
		for ns, ps := range pl.byNS {
			pl.byNS[ns] = slices.Clip(ps)
		}
		if p.order != nil {
			p.order(pl.all)
		}
		p.last = decoded
		return pl
	})
}

// byPrecedence orders admin policies as OVN-K evaluates them: priority, then
// name.
func byPrecedence(ps []*policy) {
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i].view, ps[j].view
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.Name < b.Name
	})
}
