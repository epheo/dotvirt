package argo

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

// app builds an unstructured ArgoCD Application with the given status.resources.
func app(ns, name string, resources []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"status":     map[string]any{"resources": resources},
	}}
}

func vmResource(ns, name, status, health string) any {
	return map[string]any{
		"group":     "kubevirt.io",
		"kind":      "VirtualMachine",
		"namespace": ns,
		"name":      name,
		"status":    status,
		"health":    map[string]any{"status": health},
	}
}

func TestDriftFromApps(t *testing.T) {
	drift := driftFromApps([]any{
		app("openshift-gitops", "managed", []any{
			vmResource("prod", "synced-vm", "Synced", "Healthy"),
			vmResource("prod", "drifted-vm", "OutOfSync", "Degraded"),
			// A non-VM resource that must be ignored.
			map[string]any{"group": "", "kind": "Service", "namespace": "prod", "name": "svc", "status": "Synced"},
		}),
	})

	if len(drift) != 2 {
		t.Fatalf("want 2 VM drift entries (Service ignored), got %d: %v", len(drift), drift)
	}
	if got := drift["prod/synced-vm"]; got.Sync != model.SyncSynced || got.Health != "Healthy" {
		t.Errorf("synced-vm: got %+v", got)
	}
	if got := drift["prod/drifted-vm"]; got.Sync != model.SyncOutOfSync || got.Health != "Degraded" {
		t.Errorf("drifted-vm: got %+v", got)
	}
}

// appWithSyncResult builds an Application whose live tree (status.resources) and
// operationState.syncResult.resources are supplied separately.
func appWithSyncResult(ns, name string, resources, syncResult []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"status": map[string]any{
			"resources":      resources,
			"operationState": map[string]any{"syncResult": map[string]any{"resources": syncResult}},
		},
	}}
}

func TestDriftFromAppsSyncMessage(t *testing.T) {
	drift := driftFromApps([]any{appWithSyncResult("openshift-gitops", "managed",
		[]any{
			vmResource("prod", "bad-vm", "OutOfSync", "Healthy"),
			vmResource("prod", "ok-vm", "Synced", "Healthy"),
		},
		[]any{
			// Failed apply: carries the webhook error we want to surface.
			map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod",
				"name": "bad-vm", "status": "SyncFailed", "message": "admission webhook denied: 0 vCPU"},
			// Synced row: benign "unchanged" message must NOT be surfaced as an error.
			map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod",
				"name": "ok-vm", "status": "Synced", "message": "virtualmachine ok-vm unchanged"},
		},
	)})

	if got := drift["prod/bad-vm"].Message; got != "admission webhook denied: 0 vCPU" {
		t.Errorf("bad-vm message not surfaced: %q", got)
	}
	if got := drift["prod/ok-vm"].Message; got != "" {
		t.Errorf("synced-vm should carry no error message, got %q", got)
	}
}

// TestDriftFromAppsEmptySyncSkipped is the regression for the empty-Sync badge
// crash: a VM that appears ONLY in operationState.syncResult.resources (a failed
// first apply that never entered the live status.resources tree) must NOT be
// synthesized into the drift map with a zero Sync — it stays absent, so the caller
// reports NotTracked rather than handing the frontend an empty sync status.
func TestDriftFromAppsEmptySyncSkipped(t *testing.T) {
	drift := driftFromApps([]any{appWithSyncResult("openshift-gitops", "managed",
		// Live tree is empty — the object never got created.
		[]any{},
		[]any{
			map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod",
				"name": "never-applied", "status": "SyncFailed", "message": "admission webhook denied"},
		},
	)})

	if got, ok := drift["prod/never-applied"]; ok {
		t.Errorf("a VM present only in syncResult must be absent from drift (NotTracked), got %+v", got)
	}
}
