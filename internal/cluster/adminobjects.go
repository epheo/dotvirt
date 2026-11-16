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

// InstallImage reads, under THIS client's identity, the image an admin chose
// in the install namespace/name (its spec.image), "" when none.
func (c *Client) InstallImage(ctx context.Context, namespace, name string) (string, error) {
	u, err := c.dyn.Resource(dotvirtsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	image, _, err := unstructured.NestedString(u.Object, "spec", "image")
	if err != nil {
		return "", fmt.Errorf("spec.image: %w", err)
	}
	return image, nil
}

// OperatorImages reads, under THIS client's identity, the operand images an
// admin chose on the operator's own Deployment namespace/name: its
// RELATED_IMAGE_DOTVIRT and RELATED_IMAGE_FORGEJO, "" when unset.
func (c *Client) OperatorImages(ctx context.Context, namespace, name string) (dotvirt, forgejo string, err error) {
	d, err := c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		for _, e := range ctr.Env {
			switch e.Name {
			case "RELATED_IMAGE_DOTVIRT":
				dotvirt = e.Value
			case "RELATED_IMAGE_FORGEJO":
				forgejo = e.Value
			}
		}
	}
	return dotvirt, forgejo, nil
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
// operator hold no right to it. Forced: the console's Install exists to
// restore the render, a field someone changed since included.
func (c *Client) ApplyAdminObject(ctx context.Context, ref adminobjects.Ref, obj *unstructured.Unstructured) error {
	_, err := c.dyn.Resource(ref.GVR()).Namespace(ref.Namespace).Apply(ctx, ref.Name, obj, metav1.ApplyOptions{FieldManager: "dotvirt", Force: true})
	return err
}
