package argo

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
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

func udnResource(ns, name, status, health string) any {
	return map[string]any{
		"group":     "k8s.ovn.org",
		"kind":      "UserDefinedNetwork",
		"namespace": ns,
		"name":      name,
		"status":    status,
		"health":    map[string]any{"status": health},
	}
}

// TestResourceDriftAllKinds: the general per-object map keeps every kind (so a segment
// has drift), while the VM view still filters to VirtualMachine only.
func TestResourceDriftAllKinds(t *testing.T) {
	objs := []any{app("openshift-gitops", "drs-lab", []any{
		vmResource("drs-lab", "web", "Synced", "Healthy"),
		udnResource("drs-lab", "db-net", "OutOfSync", "Progressing"),
	})}

	all := resourceDriftFromApps(objs)
	udn, ok := all[resKey{"k8s.ovn.org", "UserDefinedNetwork", "drs-lab", "db-net"}]
	if !ok || udn.Sync != model.SyncOutOfSync || udn.Health != "Progressing" {
		t.Errorf("segment drift missing from general map: %+v ok=%v", udn, ok)
	}

	// The VM view carries the VM and drops the segment (it's keyed ns/name, VM-only).
	vms := vmView(all)
	if _, ok := vms["drs-lab/web"]; !ok {
		t.Error("VM missing from vmView")
	}
	if _, ok := vms["drs-lab/db-net"]; ok {
		t.Error("segment leaked into the VM-only view")
	}
}

// A segment apply error (from syncResult) attaches to the general map just like a VM's.
func TestResourceDriftSegmentSyncMessage(t *testing.T) {
	all := resourceDriftFromApps([]any{appWithSyncResult("openshift-gitops", "net",
		[]any{udnResource("drs-lab", "db-net", "OutOfSync", "Degraded")},
		[]any{map[string]any{"group": "k8s.ovn.org", "kind": "UserDefinedNetwork",
			"namespace": "drs-lab", "name": "db-net", "status": "SyncFailed",
			"message": "spec.topology immutable"}},
	)})
	if got := all[resKey{"k8s.ovn.org", "UserDefinedNetwork", "drs-lab", "db-net"}].Message; got != "spec.topology immutable" {
		t.Errorf("segment apply error not surfaced: %q", got)
	}
}

// appWithSync builds an Application with a primary repoURL and top-level
// sync/health/operationState — the fields the per-project rollup reads.
func appWithSync(name, repo, sync, health, opPhase, opMsg string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": "openshift-gitops", "name": name},
		"spec":       map[string]any{"source": map[string]any{"repoURL": repo}},
		"status": map[string]any{
			"sync":           map[string]any{"status": sync, "revision": "a360db7e0559187412d6ae17d7b149434de01e6c"},
			"health":         map[string]any{"status": health},
			"operationState": map[string]any{"phase": opPhase, "message": opMsg},
		},
	}}
}

func TestAppSyncFromApps(t *testing.T) {
	sync := appSyncFromApps([]any{
		appWithSync("drs-lab", "https://forge.example/dotvirt/drs-lab.git",
			"Synced", "Healthy", "Succeeded", "successfully synced (all tasks run)"),
		appWithSync("fewa", "https://forge.example/dotvirt/fewa.git",
			"OutOfSync", "Degraded", "Failed", "one or more objects failed to apply"),
	})

	// Keyed by canonical repoURL (NormalizeRepoURL strips the .git and scheme).
	got := sync[forge.NormalizeRepoURL("https://forge.example/dotvirt/drs-lab.git")]
	if got.Sync != model.SyncSynced || got.Health != "Healthy" || got.Operation != "Succeeded" {
		t.Errorf("drs-lab rollup: got %+v", got)
	}
	// A clean sync must not surface its benign message as an error.
	if got.SyncError != "" {
		t.Errorf("synced app should carry no SyncError, got %q", got.SyncError)
	}
	if got.Revision != "a360db7" {
		t.Errorf("revision not shortened: got %q", got.Revision)
	}

	bad := sync[forge.NormalizeRepoURL("https://forge.example/dotvirt/fewa.git")]
	if bad.Sync != model.SyncOutOfSync || bad.Health != "Degraded" || bad.Operation != "Failed" {
		t.Errorf("fewa rollup: got %+v", bad)
	}
	if bad.SyncError != "one or more objects failed to apply" {
		t.Errorf("failed app should surface its message, got %q", bad.SyncError)
	}
}

// TestAppSyncRollupCoversNonVMKinds is the whole point: an app whose only OutOfSync
// object is a segment (no VM anywhere) is still reported degraded at the project level,
// where the per-VM driftFromApps would report nothing.
func TestAppSyncRollupCoversNonVMKinds(t *testing.T) {
	a := appWithSync("net", "https://forge.example/dotvirt/net.git",
		"OutOfSync", "Progressing", "Running", "")
	// Its live tree holds only a UserDefinedNetwork — no VM.
	a.Object["status"].(map[string]any)["resources"] = []any{
		map[string]any{"group": "k8s.ovn.org", "kind": "UserDefinedNetwork",
			"namespace": "drs-lab", "name": "db-net", "status": "OutOfSync"},
	}

	if d := driftFromApps([]any{a}); len(d) != 0 {
		t.Fatalf("per-VM drift should be empty for a VM-less app, got %v", d)
	}
	sync := appSyncFromApps([]any{a})
	got := sync[forge.NormalizeRepoURL("https://forge.example/dotvirt/net.git")]
	if got.Sync != model.SyncOutOfSync || got.Operation != "Running" {
		t.Errorf("segment-only app must roll up OutOfSync/Running at the project level, got %+v", got)
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
