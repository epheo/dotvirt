// Package nodehealth is dotvirt's HA status plane: an SA-maintained, watch-fed
// snapshot of the Medik8s NodeHealthCheck CR, following the desched reflector
// model - the read (Live) is a pure in-memory scan, never a cluster call.
//
// Like desched, this API may be legitimately absent: installing the Node
// Health Check operator is exactly what the HA panel proposes. Run therefore
// gates the reflector on API discovery and re-probes slowly until the CRD
// appears, so absence reads as "not installed", never a reflector error loop.
package nodehealth

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/hagen"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

// nodehealthchecksGVR is the Medik8s configuration CR behind dotvirt's HA
// status plane. Cluster-scoped.
var nodehealthchecksGVR = schema.GroupVersionResource{
	Group:    "remediation.medik8s.io",
	Version:  "v1alpha1",
	Resource: "nodehealthchecks",
}

// Snapshot holds the watched NodeHealthCheck state. Build with New, start with
// Run; Live is safe for concurrent callers.
type Snapshot struct {
	sa    *cluster.Client
	store cache.Indexer

	// The three signals Live folds into the status so the panel never lies:
	// apiPresent (the CRD is served), synced (the initial LIST landed - before
	// that an empty store means "unknown", not "absent"), healthy (the watch is
	// currently established - false means the store may be stale).
	apiPresent atomic.Bool
	synced     atomic.Bool
	healthy    atomic.Bool
}

// New builds the snapshot over sa (dotvirt's ServiceAccount client).
func New(sa *cluster.Client) *Snapshot {
	return &Snapshot{sa: sa, store: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})}
}

// discoveryInterval paces the API probe while the NodeHealthCheck CRD is
// absent - one lightweight discovery GET per tick, ending once the API appears
// (the reflector then owns a watch connection). CRD removal afterwards is not
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
			if s.sa.HasAPIResource(nodehealthchecksGVR) {
				s.apiPresent.Store(true)
				s.healthy.Store(true) // optimistic until a list/watch actually errors
				store := reflect.NewStore(s.store, func() {}, func() { s.synced.Store(true) })
				lw := reflect.TrackHealth(s.sa.DynamicListWatch(nodehealthchecksGVR), &s.healthy)
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

// Live reads the current HA live state from the in-memory store. Only scalar
// fields are read out of the object, so nothing escapes the store to be
// mutated.
func (s *Snapshot) Live() model.HALive {
	out := model.HALive{
		APIPresent: s.apiPresent.Load(),
		Synced:     s.synced.Load(),
		Stale:      s.apiPresent.Load() && !s.healthy.Load(),
	}
	u, ok := s.managedCR()
	if !ok {
		return out
	}
	out.Deployed = true
	out.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	out.Reason, _, _ = unstructured.NestedString(u.Object, "status", "reason")
	out.ObservedNodes, _, _ = unstructured.NestedInt64(u.Object, "status", "observedNodes")
	out.HealthyNodes, _, _ = unstructured.NestedInt64(u.Object, "status", "healthyNodes")
	// inFlightRemediations is a node-name -> start-time map; the names are the
	// hosts being fenced right now.
	if remediating, found, _ := unstructured.NestedMap(u.Object, "status", "inFlightRemediations"); found {
		for name := range remediating {
			out.Remediating = append(out.Remediating, name)
		}
		sort.Strings(out.Remediating)
	}
	return out
}

// managedCR picks ONE NodeHealthCheck to report: the CR dotvirt itself
// proposes (hagen's CRName) when present, else the first sorted key so a
// hand-installed check still surfaces - deterministically, never mixing
// fields across objects.
func (s *Snapshot) managedCR() (*unstructured.Unstructured, bool) {
	if u, ok := reflect.Get(s.store, hagen.CRName); ok {
		return u, true
	}
	keys := s.store.ListKeys()
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	return reflect.Get(s.store, keys[0])
}
