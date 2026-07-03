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
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// Snapshot holds the watched KubeDescheduler state. Build with New, start with
// Run; Live is safe for concurrent callers.
type Snapshot struct {
	sa         *cluster.Client
	store      cache.Indexer
	apiPresent atomic.Bool
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
				r := cache.NewReflector(s.sa.KubeDeschedulerListWatch(), &unstructured.Unstructured{}, s.store, 0)
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
// fields are read out of each object, so nothing escapes the store to be
// mutated. The CR is a singleton ("cluster"), so the loop reads at most one.
func (s *Snapshot) Live() model.DRSLive {
	out := model.DRSLive{APIPresent: s.apiPresent.Load()}
	for _, obj := range s.store.List() {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		out.Deployed = true
		out.Mode, _, _ = unstructured.NestedString(u.Object, "spec", "mode")
		out.ManagementState, _, _ = unstructured.NestedString(u.Object, "spec", "managementState")
		out.Profiles, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "profiles")
		out.IntervalSeconds, _, _ = unstructured.NestedInt64(u.Object, "spec", "deschedulingIntervalSeconds")
		conditions, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		if !found {
			continue
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
	}
	return out
}
