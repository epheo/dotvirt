package nodehealth

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/hagen"
	"github.com/epheo/dotvirt/internal/model"
)

// cr builds a managed-CR unstructured with the given status.
func cr(status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "remediation.medik8s.io/v1alpha1",
		"kind":       "NodeHealthCheck",
		"metadata":   map[string]any{"name": hagen.CRName},
		"spec":       map[string]any{"minHealthy": "51%"},
	}}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func liveFor(t *testing.T, u *unstructured.Unstructured) model.HALive {
	t.Helper()
	s := New(nil)
	if err := s.store.Add(u); err != nil {
		t.Fatal(err)
	}
	return s.Live()
}

func TestLiveReadsStatus(t *testing.T) {
	l := liveFor(t, cr(map[string]any{
		"phase":         "Enabled",
		"observedNodes": int64(3),
		"healthyNodes":  int64(3),
	}))
	if !l.Deployed || l.Phase != "Enabled" || l.ObservedNodes != 3 || l.HealthyNodes != 3 {
		t.Errorf("unexpected live state: %+v", l)
	}
	if len(l.Remediating) != 0 {
		t.Errorf("nothing remediating, got %v", l.Remediating)
	}
}

// The names in inFlightRemediations are the hosts being fenced right now -
// they must come out sorted so the panel renders deterministically.
func TestLiveRemediating(t *testing.T) {
	l := liveFor(t, cr(map[string]any{
		"phase":         "Remediating",
		"observedNodes": int64(3),
		"healthyNodes":  int64(1),
		"inFlightRemediations": map[string]any{
			"worker-2": "2026-07-31T00:00:00Z",
			"worker-1": "2026-07-31T00:00:00Z",
		},
	}))
	if l.Phase != "Remediating" || len(l.Remediating) != 2 {
		t.Fatalf("unexpected live state: %+v", l)
	}
	if l.Remediating[0] != "worker-1" || l.Remediating[1] != "worker-2" {
		t.Errorf("remediating not sorted: %v", l.Remediating)
	}
}

// NHC disables itself (phase Disabled + reason) when a conflicting
// machine-api MachineHealthCheck exists - the reason must surface verbatim so
// the panel can say why HA is off.
func TestLiveDisabledReason(t *testing.T) {
	l := liveFor(t, cr(map[string]any{
		"phase":  "Disabled",
		"reason": "MachineHealthCheck resources conflict",
	}))
	if l.Phase != "Disabled" || l.Reason == "" {
		t.Errorf("unexpected live state: %+v", l)
	}
}

func TestLiveEmptyStore(t *testing.T) {
	if l := New(nil).Live(); l.Deployed {
		t.Errorf("empty store must not read as deployed: %+v", l)
	}
}
