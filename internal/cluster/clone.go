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

// VirtualMachineClone is a CRD without a typed client here, so it goes through
// the dynamic client like snapshots. The clone op itself is imperative and
// RBAC-gated (the user's token is the gate), but UNLIKE snapshots its outcome
// is config state: the target VM exists only in the cluster, so it surfaces as
// NotTracked until "Adopt into git" proposes its manifest into main.
var gvrClones = schema.GroupVersionResource{Group: "clone.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclones"}

// ListClones returns the VirtualMachineClones whose source is vmName, newest
// first.
func (c *Client) ListClones(ctx context.Context, namespace, vmName string) ([]model.Clone, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	list, err := c.dyn.Resource(gvrClones).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := []model.Clone{}
	for _, item := range list.Items {
		kind, _, _ := unstructured.NestedString(item.Object, "spec", "source", "kind")
		src, _, _ := unstructured.NestedString(item.Object, "spec", "source", "name")
		if kind != "VirtualMachine" || src != vmName {
			continue
		}
		out = append(out, cloneFrom(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

func cloneFrom(item unstructured.Unstructured) model.Clone {
	cl := model.Clone{Name: item.GetName(), Created: item.GetCreationTimestamp().UTC().Format(time.RFC3339)}
	cl.Target, _, _ = unstructured.NestedString(item.Object, "spec", "target", "name")
	cl.Phase, _, _ = unstructured.NestedString(item.Object, "status", "phase")
	return cl
}

// CreateClone clones vmName into a new VM named target via a
// VirtualMachineClone (snapshot + restore under the hood; the source may be
// running). The target VM is created by the clone controller — dotvirt writes
// no config state here.
func (c *Client) CreateClone(ctx context.Context, namespace, vmName, cloneName, target string) error {
	if c.dyn == nil {
		return fmt.Errorf("dynamic client unavailable")
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "clone.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineClone",
		"metadata":   map[string]any{"name": cloneName, "namespace": namespace},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vmName},
			"target": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": target},
		},
	}}
	_, err := c.dyn.Resource(gvrClones).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}
