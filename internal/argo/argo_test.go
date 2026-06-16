package argo

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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

func TestVMDrift(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[any]string{} // not needed for List with our GVR
	_ = gvrToListKind

	objs := []runtime.Object{
		app("openshift-gitops", "managed", []any{
			vmResource("prod", "synced-vm", "Synced", "Healthy"),
			vmResource("prod", "drifted-vm", "OutOfSync", "Degraded"),
			// A non-VM resource that must be ignored.
			map[string]any{"group": "", "kind": "Service", "namespace": "prod", "name": "svc", "status": "Synced"},
		}),
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, objs...)

	c := &Client{dyn: dyn}
	drift, err := c.VMDrift(context.Background())
	if err != nil {
		t.Fatalf("VMDrift: %v", err)
	}

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

func TestVMDriftSyncMessage(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": "openshift-gitops", "name": "managed"},
		"status": map[string]any{
			"resources": []any{
				vmResource("prod", "bad-vm", "OutOfSync", "Healthy"),
				vmResource("prod", "ok-vm", "Synced", "Healthy"),
			},
			"operationState": map[string]any{
				"syncResult": map[string]any{
					"resources": []any{
						// Failed apply: carries the webhook error we want to surface.
						map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod",
							"name": "bad-vm", "status": "SyncFailed", "message": "admission webhook denied: 0 vCPU"},
						// Synced row: benign "unchanged" message must NOT be surfaced as an error.
						map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod",
							"name": "ok-vm", "status": "Synced", "message": "virtualmachine ok-vm unchanged"},
					},
				},
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)

	drift, err := (&Client{dyn: dyn}).VMDrift(context.Background())
	if err != nil {
		t.Fatalf("VMDrift: %v", err)
	}
	if got := drift["prod/bad-vm"].Message; got != "admission webhook denied: 0 vCPU" {
		t.Errorf("bad-vm message not surfaced: %q", got)
	}
	if got := drift["prod/ok-vm"].Message; got != "" {
		t.Errorf("synced-vm should carry no error message, got %q", got)
	}
}
