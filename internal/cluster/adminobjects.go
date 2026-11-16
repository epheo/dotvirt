package cluster

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var dotvirtsGVR = schema.GroupVersionResource{Group: "dotvirt.io", Version: "v1alpha1", Resource: "dotvirts"}

// InstallAdminObjects reads, under THIS client's identity, the objects the
// install namespace/name asks a cluster admin to apply (its
// status.adminObjects), as the operator rendered them. They are unchecked:
// the caller weighs each against adminobjects.Allowed.
func (c *Client) InstallAdminObjects(ctx context.Context, namespace, name string) ([]adminobjects.Object, error) {
	u, err := c.dyn.Resource(dotvirtsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	entries, _, err := unstructured.NestedSlice(u.Object, "status", "adminObjects")
	if err != nil {
		return nil, fmt.Errorf("status.adminObjects: %w", err)
	}
	var out []adminobjects.Object
	for _, e := range entries {
		m, _ := e.(map[string]any)
		obj, _ := m["object"].(map[string]any)
		purpose, _ := m["purpose"].(string)
		if obj == nil {
			continue
		}
		out = append(out, adminobjects.Object{Purpose: purpose, Object: &unstructured.Unstructured{Object: obj}})
	}
	return out, nil
}

// AdminObject reads ref under THIS client's identity: nil when absent.
func (c *Client) AdminObject(ctx context.Context, ref adminobjects.Ref) (*unstructured.Unstructured, error) {
	o, err := c.dyn.Resource(ref.GVR()).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return o, err
}

// ApplyAdminObject server-side applies obj as ref under THIS client's
// identity: the caller's RBAC is the gate, so dotvirt's ServiceAccount and the
// operator hold no right to it.
func (c *Client) ApplyAdminObject(ctx context.Context, ref adminobjects.Ref, obj *unstructured.Unstructured) error {
	_, err := c.dyn.Resource(ref.GVR()).Namespace(ref.Namespace).Apply(ctx, ref.Name, obj, metav1.ApplyOptions{FieldManager: "dotvirt", Force: true})
	return err
}
