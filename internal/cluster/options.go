package cluster

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/epheo/dotvirt/internal/model"
)

// GVRs for the cluster-provided wizard/editor choices: sizes (instancetypes),
// OS tuning (preferences), boot images (DataSources), networks (NADs).
var (
	gvrInstancetypes  = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterinstancetypes"}
	gvrPreferences    = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterpreferences"}
	gvrDataSources    = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datasources"}
	gvrNADs           = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	gvrStorageClasses = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
)

// dynamic returns the client's dynamic interface, or a uniform error when it's
// unavailable (degraded mode) — the single home for that guard, shared by every
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
	// null — the frontend treats each list as iterable unconditionally.
	opts := model.Options{
		Instancetypes:  []model.Instancetype{},
		Preferences:    []model.Preference{},
		OSImages:       []model.OSImage{},
		Networks:       []model.NetworkOption{},
		StorageClasses: []model.StorageClass{},
	}

	if items, err := listAll(ctx, dyn, gvrInstancetypes); err == nil {
		for i := range items {
			cpu, _, _ := unstructured.NestedInt64(items[i].Object, "spec", "cpu", "guest")
			mem, _, _ := unstructured.NestedString(items[i].Object, "spec", "memory", "guest")
			opts.Instancetypes = append(opts.Instancetypes, model.Instancetype{Name: items[i].GetName(), CPU: cpu, Memory: mem})
		}
	}

	if items, err := listAll(ctx, dyn, gvrPreferences); err == nil {
		for i := range items {
			opts.Preferences = append(opts.Preferences, model.Preference{
				Name:        items[i].GetName(),
				DisplayName: items[i].GetAnnotations()["openshift.io/display-name"],
			})
		}
	}

	if items, err := listAllNS(ctx, dyn, gvrDataSources); err == nil {
		for i := range items {
			opts.OSImages = append(opts.OSImages, model.OSImage{
				Name:      items[i].GetName(),
				Namespace: items[i].GetNamespace(),
				Ready:     dataSourceReady(&items[i]),
			})
		}
	}

	if items, err := listAllNS(ctx, dyn, gvrNADs); err == nil {
		for i := range items {
			opts.Networks = append(opts.Networks, model.NetworkOption{Name: items[i].GetName(), Namespace: items[i].GetNamespace()})
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
