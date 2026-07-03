package desched

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/drsgen"
)

// cr builds a managed-CR unstructured with the given status conditions.
func cr(conditions ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operator.openshift.io/v1",
		"kind":       "KubeDescheduler",
		"metadata":   map[string]any{"name": "cluster", "namespace": drsgen.Namespace},
		"spec":       map[string]any{"mode": "Predictive"},
	}}
	if conditions != nil {
		raw := make([]any, 0, len(conditions))
		for _, c := range conditions {
			raw = append(raw, any(c))
		}
		u.Object["status"] = map[string]any{"conditions": raw}
	}
	return u
}

func liveFor(t *testing.T, u *unstructured.Unstructured) (out struct {
	Available bool
	Degraded  string
}) {
	t.Helper()
	s := New(nil)
	if err := s.store.Add(u); err != nil {
		t.Fatal(err)
	}
	l := s.Live()
	out.Available, out.Degraded = l.Available, l.Degraded
	return out
}

// The operator reports only per-controller <Name>Degraded conditions — no
// Available roll-up exists on the CR, so available must mean "reported with
// nothing degraded", not the presence of a condition type that never occurs.
func TestLiveAvailabilityFromDegradedConditions(t *testing.T) {
	healthy := liveFor(t, cr(
		map[string]any{"type": "TargetConfigControllerDegraded", "status": "False"},
		map[string]any{"type": "ResourceSyncControllerDegraded", "status": "False"},
		map[string]any{"type": "ConfigObservationDegraded", "status": "False"},
	))
	if !healthy.Available || healthy.Degraded != "" {
		t.Errorf("all *Degraded=False: got available=%v degraded=%q, want true/empty", healthy.Available, healthy.Degraded)
	}

	sick := liveFor(t, cr(
		map[string]any{"type": "TargetConfigControllerDegraded", "status": "True",
			"message": "profile KubeVirtRelieveAndMigrate can only be used when PSI metrics are enabled"},
		map[string]any{"type": "ResourceSyncControllerDegraded", "status": "False"},
	))
	if sick.Available || sick.Degraded == "" {
		t.Errorf("a *Degraded=True: got available=%v degraded=%q, want false + message", sick.Available, sick.Degraded)
	}

	// No conditions reported yet: not available (the operator hasn't spoken).
	if empty := liveFor(t, cr()); empty.Available {
		t.Error("no conditions: available must be false")
	}

	// An explicit Available condition wins over the degraded-derived roll-up.
	explicit := liveFor(t, cr(
		map[string]any{"type": "Available", "status": "False"},
	))
	if explicit.Available {
		t.Error("explicit Available=False must win even with nothing degraded")
	}
}
