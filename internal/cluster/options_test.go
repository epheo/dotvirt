package cluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
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

// A size no host can run is named with what it needs, so the forms can say
// why instead of staging a VM that never leaves scheduling.
func TestNoHostNamesWhatNoHostOffers(t *testing.T) {
	it := func(dedicated bool, pageSize, guest string) map[string]any {
		spec := map[string]any{"cpu": map[string]any{"guest": int64(4)}, "memory": map[string]any{"guest": guest}}
		if dedicated {
			spec["cpu"].(map[string]any)["dedicatedCPUPlacement"] = true
		}
		if pageSize != "" {
			spec["memory"].(map[string]any)["hugepages"] = map[string]any{"pageSize": pageSize}
		}
		return map[string]any{"spec": spec}
	}
	host := func(cpumanager string, hugepages map[string]string) corev1.Node {
		n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "h", Labels: map[string]string{"kubevirt.io/cpumanager": cpumanager}}}
		n.Status.Allocatable = corev1.ResourceList{}
		for size, q := range hugepages {
			n.Status.Allocatable[corev1.ResourceName("hugepages-"+size)] = resource.MustParse(q)
		}
		return n
	}
	plain := host("false", map[string]string{"1Gi": "0", "2Mi": "0"})
	pinned := host("true", nil)
	huge := host("false", map[string]string{"1Gi": "8Gi"})
	for _, c := range []struct {
		name  string
		it    map[string]any
		hosts []corev1.Node
		want  string
	}{
		{"shared CPUs run anywhere", it(false, "", "4Gi"), []corev1.Node{plain}, ""},
		{"no host known", it(true, "1Gi", "4Gi"), nil, ""},
		{"dedicated CPUs", it(true, "", "4Gi"), []corev1.Node{plain}, "needs dedicated CPUs, which no host offers"},
		{"a pinned host", it(true, "", "4Gi"), []corev1.Node{plain, pinned}, ""},
		{"hugepages", it(false, "1Gi", "4Gi"), []corev1.Node{plain}, "needs 4Gi of 1Gi hugepages, which no host offers"},
		{"enough hugepages", it(false, "1Gi", "4Gi"), []corev1.Node{huge}, ""},
		{"too few hugepages", it(false, "1Gi", "16Gi"), []corev1.Node{huge}, "needs 16Gi of 1Gi hugepages, which no host offers"},
		{"both, never on one host", it(true, "1Gi", "4Gi"), []corev1.Node{pinned, huge}, "needs dedicated CPUs and 4Gi of 1Gi hugepages, which no host offers"},
	} {
		if got := noHost(c.it, c.hosts); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The catalog reads the hosts KubeVirt schedules on, and marks sizes by them.
func TestListOptionsMarksSizesNoHostRuns(t *testing.T) {
	obj := func(name string, dedicated bool) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "instancetype.kubevirt.io/v1beta1", "kind": "VirtualMachineClusterInstancetype",
			"metadata": map[string]any{"name": name},
			"spec": map[string]any{
				"cpu":    map[string]any{"guest": int64(2), "dedicatedCPUPlacement": dedicated},
				"memory": map[string]any{"guest": "4Gi"},
			},
		}}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrInstancetypes: "VirtualMachineClusterInstancetypeList", gvrPreferences: "VirtualMachineClusterPreferenceList",
		gvrDataSources: "DataSourceList", gvrStorageClasses: "StorageClassList",
	})
	ctx := context.Background()
	for _, o := range []*unstructured.Unstructured{obj("u1.medium", false), obj("n1.medium", true)} {
		if _, err := dyn.Resource(gvrInstancetypes).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	kube := kubefake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "host", Labels: map[string]string{"kubevirt.io/schedulable": "true", "kubevirt.io/cpumanager": "false"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "control", Labels: map[string]string{"kubevirt.io/cpumanager": "true"}}},
	)
	opts, err := NewClient(kube, nil, dyn).ListOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, i := range opts.Instancetypes {
		got[i.Name] = i.NoHost
	}
	if got["u1.medium"] != "" || got["n1.medium"] != "needs dedicated CPUs, which no host offers" {
		t.Errorf("marks %q", got)
	}
}
