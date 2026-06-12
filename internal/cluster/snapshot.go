package cluster

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

// VirtualMachineSnapshot/Restore are CRDs without a typed client method here, so
// they go through the dynamic client (like the wizard catalog). Snapshots are an
// imperative, RBAC-gated operation (the user's token is the gate) — not git-managed
// state — so ArgoCD never reverts them.
var (
	gvrSnapshots = schema.GroupVersionResource{Group: "snapshot.kubevirt.io", Version: "v1beta1", Resource: "virtualmachinesnapshots"}
	gvrRestores  = schema.GroupVersionResource{Group: "snapshot.kubevirt.io", Version: "v1beta1", Resource: "virtualmachinerestores"}
)

// ListSnapshots returns the VirtualMachineSnapshots whose source is vmName, newest
// first.
func (c *Client) ListSnapshots(ctx context.Context, namespace, vmName string) ([]model.Snapshot, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	list, err := c.dyn.Resource(gvrSnapshots).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := []model.Snapshot{}
	for _, item := range list.Items {
		if src, _, _ := unstructured.NestedString(item.Object, "spec", "source", "name"); src != vmName {
			continue
		}
		out = append(out, snapshotFrom(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

func snapshotFrom(item unstructured.Unstructured) model.Snapshot {
	s := model.Snapshot{Name: item.GetName(), Created: item.GetCreationTimestamp().UTC().Format(time.RFC3339)}
	if t, ok, _ := unstructured.NestedString(item.Object, "status", "creationTime"); ok && t != "" {
		s.Created = t
	}
	s.Phase, _, _ = unstructured.NestedString(item.Object, "status", "phase")
	s.ReadyToUse, _, _ = unstructured.NestedBool(item.Object, "status", "readyToUse")
	if inds, ok, _ := unstructured.NestedStringSlice(item.Object, "status", "indications"); ok {
		s.Indications = inds
	}
	s.Error, _, _ = unstructured.NestedString(item.Object, "status", "error", "message")
	return s
}

// CreateSnapshot takes a point-in-time snapshot of the VM.
func (c *Client) CreateSnapshot(ctx context.Context, namespace, vmName, snapName string) error {
	if c.dyn == nil {
		return fmt.Errorf("dynamic client unavailable")
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineSnapshot",
		"metadata":   map[string]any{"name": snapName, "namespace": namespace},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vmName},
		},
	}}
	_, err := c.dyn.Resource(gvrSnapshots).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// DeleteSnapshot removes a VirtualMachineSnapshot.
func (c *Client) DeleteSnapshot(ctx context.Context, namespace, snapName string) error {
	if c.dyn == nil {
		return fmt.Errorf("dynamic client unavailable")
	}
	return c.dyn.Resource(gvrSnapshots).Namespace(namespace).Delete(ctx, snapName, metav1.DeleteOptions{})
}

// RestoreSnapshot rolls the VM back to a snapshot via a VirtualMachineRestore. The
// VM must be stopped — the snapshot controller rejects a running target.
func (c *Client) RestoreSnapshot(ctx context.Context, namespace, vmName, snapName string) error {
	if c.dyn == nil {
		return fmt.Errorf("dynamic client unavailable")
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineRestore",
		"metadata":   map[string]any{"generateName": "restore-" + snapName + "-", "namespace": namespace},
		"spec": map[string]any{
			"target":                     map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vmName},
			"virtualMachineSnapshotName": snapName,
		},
	}}
	_, err := c.dyn.Resource(gvrRestores).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}
