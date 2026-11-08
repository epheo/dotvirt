package cluster

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The instancetype a VM names is read where KubeVirt reads it: the
// cluster-scoped kind by default, the namespaced kind in the VM's namespace,
// whatever case or number the manifest spells the kind in.
func TestInstancetypeReadsTheNamedKind(t *testing.T) {
	obj := func(kind, ns, model string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "instancetype.kubevirt.io/v1beta1", "kind": kind,
			"metadata": map[string]any{"name": "big", "namespace": ns},
			"spec":     map[string]any{"cpu": map[string]any{"model": model}},
		}}
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	ctx := context.Background()
	if _, err := dyn.Resource(gvrInstancetypes).Create(ctx, obj("VirtualMachineClusterInstancetype", "", "cluster"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(gvrNSInstancetype).Namespace("alpha").Create(ctx, obj("VirtualMachineInstancetype", "alpha", "alpha"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := NewClient(nil, nil, dyn)
	for kind, want := range map[string]string{
		"":                                   "cluster",
		"VirtualMachineClusterInstancetype":  "cluster",
		"virtualmachineclusterinstancetypes": "cluster",
		"VirtualMachineInstancetype":         "alpha",
		"virtualmachineinstancetype":         "alpha",
	} {
		got, err := c.Instancetype(ctx, "alpha", kind, "big")
		if err != nil {
			t.Errorf("%q: %v", kind, err)
			continue
		}
		if m, _, _ := unstructured.NestedString(got, "spec", "cpu", "model"); m != want {
			t.Errorf("%q read the %s one, want %s", kind, m, want)
		}
	}
	if _, err := c.Instancetype(ctx, "beta", "VirtualMachineInstancetype", "big"); err == nil {
		t.Error("a namespaced instancetype was read outside the VM's namespace")
	}
	if _, err := c.Instancetype(ctx, "alpha", "VirtualMachinePreference", "big"); err == nil {
		t.Error("a kind KubeVirt does not size from was read")
	}
}
