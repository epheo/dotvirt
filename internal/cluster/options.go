package cluster

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Options are the cluster-provided choices the New VM wizard and the editor
// present: sizes (instancetypes), OS tuning (preferences), boot images
// (DataSources), and networks (NetworkAttachmentDefinitions).
type Options struct {
	Instancetypes []Instancetype `json:"instancetypes"`
	Preferences   []Preference   `json:"preferences"`
	OSImages      []OSImage      `json:"osImages"`
	Networks      []Network      `json:"networks"`
}

type Instancetype struct {
	Name   string `json:"name"`
	CPU    int64  `json:"cpu"`
	Memory string `json:"memory"`
}

type Preference struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}

type OSImage struct {
	Name      string `json:"name"`      // DataSource name (used in sourceRef)
	Namespace string `json:"namespace"` // DataSource namespace
	Ready     bool   `json:"ready"`
}

type Network struct {
	Name      string `json:"name"`      // NAD name
	Namespace string `json:"namespace"` // NAD namespace
}

var (
	gvrInstancetypes = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterinstancetypes"}
	gvrPreferences   = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterpreferences"}
	gvrDataSources   = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datasources"}
	gvrNADs          = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
)

// dyn lazily builds a dynamic client from the cluster client's config.
func (c *Client) dynamic() (dynamic.Interface, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	return c.dyn, nil
}

// ListOptions gathers all wizard/editor choices from the cluster. A failure in
// any single source is non-fatal: that list is left empty and the others fill in.
func (c *Client) ListOptions(ctx context.Context) (Options, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return Options{}, err
	}
	var opts Options

	if items, err := listAll(ctx, dyn, gvrInstancetypes); err == nil {
		for i := range items {
			cpu, _, _ := unstructured.NestedInt64(items[i].Object, "spec", "cpu", "guest")
			mem, _, _ := unstructured.NestedString(items[i].Object, "spec", "memory", "guest")
			opts.Instancetypes = append(opts.Instancetypes, Instancetype{Name: items[i].GetName(), CPU: cpu, Memory: mem})
		}
	}

	if items, err := listAll(ctx, dyn, gvrPreferences); err == nil {
		for i := range items {
			opts.Preferences = append(opts.Preferences, Preference{
				Name:        items[i].GetName(),
				DisplayName: items[i].GetAnnotations()["openshift.io/display-name"],
			})
		}
	}

	if items, err := listAllNS(ctx, dyn, gvrDataSources); err == nil {
		for i := range items {
			opts.OSImages = append(opts.OSImages, OSImage{
				Name:      items[i].GetName(),
				Namespace: items[i].GetNamespace(),
				Ready:     dataSourceReady(&items[i]),
			})
		}
	}

	if items, err := listAllNS(ctx, dyn, gvrNADs); err == nil {
		for i := range items {
			opts.Networks = append(opts.Networks, Network{Name: items[i].GetName(), Namespace: items[i].GetNamespace()})
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
