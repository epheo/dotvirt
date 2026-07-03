// Package desched is dotvirt's DRS status plane: an SA-maintained, watch-fed
// snapshot of the KubeDescheduler CR, following the clusterstate/argo reflector
// model — the read (Live) is a pure in-memory scan, never a cluster call.
//
// Unlike those planes, this API may be legitimately absent: installing the
// descheduler operator is exactly what the DRS panel proposes. Run therefore
// gates the reflector on API discovery and re-probes slowly until the CRD
// appears, so absence reads as "not installed", never a reflector error loop.
package desched

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/drsgen"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

// Snapshot holds the watched KubeDescheduler state. Build with New, start with
// Run; Live is safe for concurrent callers.
type Snapshot struct {
	sa    *cluster.Client
	store cache.Indexer

	// The three signals Live folds into the status so the panel never lies:
	// apiPresent (the CRD is served), synced (the initial LIST landed — before
	// that an empty store means "unknown", not "absent"), healthy (the watch is
	// currently established — false means the store may be stale, e.g. the
	// SA's RBAC hasn't reconciled yet or the apiserver is failing).
	apiPresent atomic.Bool
	synced     atomic.Bool
	healthy    atomic.Bool
}

// New builds the snapshot over sa (dotvirt's ServiceAccount client).
func New(sa *cluster.Client) *Snapshot {
	return &Snapshot{sa: sa, store: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})}
}

// discoveryInterval paces the API probe while the descheduler CRD is absent —
// one lightweight discovery GET per tick, ending once the API appears (the
// reflector then owns a watch connection). CRD removal afterwards is not
// re-probed: the watch goes quiet on a last-known state, and a full uninstall
// of the operator is outside dotvirt's flow anyway.
const discoveryInterval = time.Minute

// Run starts the discovery-gated reflector and returns immediately; everything
// stops when ctx is cancelled.
func (s *Snapshot) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(discoveryInterval)
		defer t.Stop()
		for {
			if s.sa.HasKubeDeschedulerAPI() {
				s.apiPresent.Store(true)
				s.healthy.Store(true) // optimistic until a list/watch actually errors
				store := reflect.NewStore(s.store, func() {}, func() { s.synced.Store(true) })
				lw := reflect.TrackHealth(s.sa.KubeDeschedulerListWatch(), &s.healthy)
				r := cache.NewReflector(lw, &unstructured.Unstructured{}, store, 0)
				r.Run(ctx.Done()) // blocks until shutdown; owns its own relist/backoff
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Live reads the current DRS live state from the in-memory store. Only scalar
// fields are read out of the object, so nothing escapes the store to be
// mutated.
func (s *Snapshot) Live() model.DRSLive {
	out := model.DRSLive{
		APIPresent: s.apiPresent.Load(),
		Synced:     s.synced.Load(),
		Stale:      s.apiPresent.Load() && !s.healthy.Load(),
	}
	u, ok := s.managedCR()
	if !ok {
		return out
	}
	out.Deployed = true
	out.Mode, _, _ = unstructured.NestedString(u.Object, "spec", "mode")
	out.ManagementState, _, _ = unstructured.NestedString(u.Object, "spec", "managementState")
	out.Profiles, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "profiles")
	out.IntervalSeconds, _, _ = unstructured.NestedInt64(u.Object, "spec", "deschedulingIntervalSeconds")
	conditions, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return out
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := cond["type"].(string)
		status, _ := cond["status"].(string)
		switch typ {
		case "Available":
			out.Available = status == "True"
		case "Degraded":
			if status == "True" {
				out.Degraded, _ = cond["message"].(string)
			}
		}
	}
	return out
}

// managedCR picks ONE KubeDescheduler to report: the CR dotvirt itself
// proposes (drsgen's namespace, name "cluster") when present, else the first
// sorted key so a nonstandard install still surfaces — deterministically,
// never mixing fields across objects.
func (s *Snapshot) managedCR() (*unstructured.Unstructured, bool) {
	if obj, ok, _ := s.store.GetByKey(drsgen.Namespace + "/cluster"); ok {
		u, uok := obj.(*unstructured.Unstructured)
		return u, uok
	}
	keys := s.store.ListKeys()
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	obj, ok, _ := s.store.GetByKey(keys[0])
	if !ok {
		return nil, false
	}
	u, uok := obj.(*unstructured.Unstructured)
	return u, uok
}
