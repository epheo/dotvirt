package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

// VirtualMachineSnapshot/Restore are CRDs without a typed client method here, so
// they go through the dynamic client (like the wizard catalog). Snapshots are an
// imperative, RBAC-gated operation (the user's token is the gate) - not git-managed
// state - so ArgoCD never reverts them.
var (
	gvrSnapshots = schema.GroupVersionResource{Group: "snapshot.kubevirt.io", Version: "v1beta1", Resource: "virtualmachinesnapshots"}
	gvrRestores  = schema.GroupVersionResource{Group: "snapshot.kubevirt.io", Version: "v1beta1", Resource: "virtualmachinerestores"}
)

// ListSnapshots returns the VirtualMachineSnapshots whose source is vmName, newest
// first.
func (c *Client) ListSnapshots(ctx context.Context, namespace, vmName string) ([]model.Snapshot, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(gvrSnapshots).Namespace(namespace).List(ctx, metav1.ListOptions{})
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

// ListVMSnapshots returns every VirtualMachineSnapshot in namespaces, newest
// first - the Storage section's cluster-wide listing. Each namespace is listed
// with this client's token, so RBAC gates it and nothing leaks across tenants.
func (c *Client) ListVMSnapshots(ctx context.Context, namespaces []string) ([]model.Snapshot, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return nil, err
	}
	out := []model.Snapshot{}
	err = eachNamespace(ctx, namespaces, func(ctx context.Context, ns string) (func(), error) {
		list, err := dyn.Resource(gvrSnapshots).Namespace(ns).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) { // no KubeVirt: Storage still opens
			return func() {}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list snapshots in %s: %w", ns, err)
		}
		return func() {
			for _, item := range list.Items {
				out = append(out, snapshotFrom(item))
			}
		}, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Created != b.Created {
			return a.Created > b.Created
		}
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	return out, nil
}

func snapshotFrom(item unstructured.Unstructured) model.Snapshot {
	s := model.Snapshot{
		Namespace: item.GetNamespace(),
		Name:      item.GetName(),
		Created:   item.GetCreationTimestamp().UTC().Format(time.RFC3339),
	}
	s.VM, _, _ = unstructured.NestedString(item.Object, "spec", "source", "name")
	s.Volumes, _, _ = unstructured.NestedStringSlice(item.Object, "status", "snapshotVolumes", "includedVolumes")
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
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineSnapshot",
		"metadata":   map[string]any{"name": snapName, "namespace": namespace},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vmName},
		},
	}}
	_, err = dyn.Resource(gvrSnapshots).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

func (c *Client) DeleteSnapshot(ctx context.Context, namespace, snapName string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	return dyn.Resource(gvrSnapshots).Namespace(namespace).Delete(ctx, snapName, metav1.DeleteOptions{})
}

// RestoreSnapshot rolls the VM back to a snapshot via a VirtualMachineRestore. The
// VM must be stopped - the snapshot controller rejects a running target.
//
// InPlace keeps the VM's disk names. KubeVirt's default renames them, so a VM
// git declares drifts, and ArgoCD's self-heal points it back at the old names:
// KubeVirt then re-creates those disks from their sources and the restore is
// lost. Strict field validation makes a KubeVirt without the policy refuse the
// restore rather than drop the field and rename.
func (c *Client) RestoreSnapshot(ctx context.Context, namespace, vmName, snapName string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineRestore",
		"metadata":   map[string]any{"generateName": "restore-" + snapName + "-", "namespace": namespace},
		"spec": map[string]any{
			"target":                     map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vmName},
			"virtualMachineSnapshotName": snapName,
			"volumeRestorePolicy":        "InPlace",
		},
	}}
	_, err = dyn.Resource(gvrRestores).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
	if apierrors.IsBadRequest(err) && strings.Contains(err.Error(), `unknown field "spec.volumeRestorePolicy"`) {
		return fmt.Errorf("%w: this cluster's KubeVirt cannot restore disks in place, and a restore under new disk names would be undone by git", model.ErrUnavailable)
	}
	return err
}
