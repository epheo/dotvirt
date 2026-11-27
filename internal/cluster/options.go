package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/epheo/dotvirt/internal/model"
)

// GVRs for the cluster-provided wizard/editor choices: sizes (instancetypes),
// OS tuning (preferences), boot images (DataSources), storage classes.
var (
	gvrInstancetypes  = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterinstancetypes"}
	gvrNSInstancetype = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineinstancetypes"}
	gvrPreferences    = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterpreferences"}
	gvrDataSources    = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datasources"}
	gvrStorageClasses = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
)

// dynamic returns the client's dynamic interface, or a uniform error when it's
// unavailable (degraded mode) - the single home for that guard, shared by every
// CRD-backed method (options, snapshots, clones).
func (c *Client) dynamic() (dynamic.Interface, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	return c.dyn, nil
}

// ListOptions gathers all wizard/editor choices from the cluster. A failure in
// any single source is non-fatal: that list is left empty and the others fill in.
func (c *Client) ListOptions(ctx context.Context) (model.Options, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return model.Options{}, err
	}
	// Non-nil slices so a source the SA can't list (RBAC) serializes as [] not
	// null - the frontend treats each list as iterable unconditionally.
	opts := model.Options{
		Instancetypes:  []model.Instancetype{},
		Preferences:    []model.Preference{},
		OSImages:       []model.OSImage{},
		StorageClasses: []model.StorageClass{},
	}

	if items, err := listAll(ctx, dyn, gvrInstancetypes); err == nil {
		hosts := c.hosts(ctx, items)
		for i := range items {
			cpu, _, _ := unstructured.NestedInt64(items[i].Object, "spec", "cpu", "guest")
			mem, _, _ := unstructured.NestedString(items[i].Object, "spec", "memory", "guest")
			opts.Instancetypes = append(opts.Instancetypes, model.Instancetype{
				Name: items[i].GetName(), CPU: cpu, Memory: mem,
				NoHost: noHost(items[i].Object, hosts),
			})
		}
	}

	if items, err := listAll(ctx, dyn, gvrPreferences); err == nil {
		for i := range items {
			minCPU, _, _ := unstructured.NestedInt64(items[i].Object, "spec", "requirements", "cpu", "guest")
			minMem, _, _ := unstructured.NestedString(items[i].Object, "spec", "requirements", "memory", "guest")
			opts.Preferences = append(opts.Preferences, model.Preference{
				Name:        items[i].GetName(),
				DisplayName: items[i].GetAnnotations()["openshift.io/display-name"],
				MinCPU:      minCPU,
				MinMemory:   minMem,
			})
		}
	}

	if items, err := listAllNS(ctx, dyn, gvrDataSources); err == nil {
		for i := range items {
			img := model.OSImage{
				Name:      items[i].GetName(),
				Namespace: items[i].GetNamespace(),
				Ready:     dataSourceReady(&items[i]),
				Removable: notOwnShape(&items[i]) == "",
			}
			a := items[i].GetAnnotations()
			if kind, src := a[importKindAnnotation], a[importSourceAnnotation]; kind != "" && src != "" {
				img.Import = model.ImportFrom(kind, src)
			}
			opts.OSImages = append(opts.OSImages, img)
		}
	}

	if items, err := listAll(ctx, dyn, gvrStorageClasses); err == nil {
		for i := range items {
			opts.StorageClasses = append(opts.StorageClasses, model.StorageClass{
				Name:    items[i].GetName(),
				Default: items[i].GetAnnotations()["storageclass.kubernetes.io/is-default-class"] == "true",
			})
		}
	}

	return opts, nil
}

// Instancetype reads the instancetype a VM's spec.instancetype names: kind
// VirtualMachineInstancetype in the VM's namespace, the default
// VirtualMachineClusterInstancetype cluster-wide. Kinds match as KubeVirt
// matches them, case-insensitive, singular or plural.
func (c *Client) Instancetype(ctx context.Context, namespace, kind, name string) (map[string]any, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return nil, err
	}
	var r dynamic.ResourceInterface
	switch strings.ToLower(kind) {
	case "", "virtualmachineclusterinstancetype", "virtualmachineclusterinstancetypes":
		r = dyn.Resource(gvrInstancetypes)
	case "virtualmachineinstancetype", "virtualmachineinstancetypes":
		r = dyn.Resource(gvrNSInstancetype).Namespace(namespace)
	default:
		return nil, fmt.Errorf("instancetype kind %q is not one KubeVirt reads", kind)
	}
	u, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return u.Object, nil
}

// hosts are the nodes KubeVirt runs VMs on, whatever their heartbeat says
// now: a mark claims no host offers a capability, so a host restarting its
// virt-handler still counts. None when no instancetype needs a capability
// or the nodes cannot be read.
func (c *Client) hosts(ctx context.Context, instancetypes []unstructured.Unstructured) []corev1.Node {
	needed := slices.ContainsFunc(instancetypes, func(u unstructured.Unstructured) bool {
		dedicated, pageSize, _ := needs(u.Object)
		return dedicated || pageSize != ""
	})
	if !needed || c.kube == nil {
		return nil
	}
	// From the watch cache: a capability changes far slower than the catalog's TTL.
	list, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "kubevirt.io/schedulable", ResourceVersion: "0"})
	if err != nil {
		return nil
	}
	return list.Items
}

// needs is what of a host an instancetype asks for: dedicated CPUs, and
// hugepages of pageSize for guest memory.
func needs(instancetype map[string]any) (dedicated bool, pageSize, guest string) {
	dedicated, _, _ = unstructured.NestedBool(instancetype, "spec", "cpu", "dedicatedCPUPlacement")
	pageSize, _, _ = unstructured.NestedString(instancetype, "spec", "memory", "hugepages", "pageSize")
	guest, _, _ = unstructured.NestedString(instancetype, "spec", "memory", "guest")
	return dedicated, pageSize, guest
}

// noHost is why none of hosts can run instancetype, or "" when one can or no
// host is known. KubeVirt places dedicated CPUs only on a host whose CPU
// manager it labeled "true", and hugepages only where the host has the
// guest's memory of that page size allocatable. Only a host labeled "false"
// lacks dedicated CPUs: one without the label is unknown, so a KubeVirt that
// stops writing it leaves every size offered.
func noHost(instancetype map[string]any, hosts []corev1.Node) string {
	dedicated, pageSize, guest := needs(instancetype)
	if len(hosts) == 0 || (!dedicated && pageSize == "") {
		return ""
	}
	want, _ := resource.ParseQuantity(guest)
	for _, n := range hosts {
		if dedicated && n.Labels["kubevirt.io/cpumanager"] == "false" {
			continue
		}
		if pageSize != "" {
			have := n.Status.Allocatable[corev1.ResourceName("hugepages-"+pageSize)]
			if have.IsZero() || have.Cmp(want) < 0 {
				continue
			}
		}
		return ""
	}
	var what []string
	if dedicated {
		what = append(what, "dedicated CPUs")
	}
	if pageSize != "" {
		if guest != "" {
			what = append(what, guest+" of "+pageSize+" hugepages")
		} else {
			what = append(what, pageSize+" hugepages")
		}
	}
	return "needs " + strings.Join(what, " and ") + ", which no host offers"
}

func listAll(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func listAllNS(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	list, err := dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func dataSourceReady(ds *unstructured.Unstructured) bool {
	conds, found, _ := unstructured.NestedSlice(ds.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if ok && c["type"] == "Ready" && c["status"] == "True" {
			return true
		}
	}
	return false
}
