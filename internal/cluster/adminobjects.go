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

// InstallSpec is what an admin chose on an install: its image, its ArgoCD
// namespace and controller, and whether forge TLS goes unverified.
type InstallSpec struct {
	Image, ArgoNamespace, ArgoController string
	InsecureTLS                          bool
}

// InstallSpec reads, under THIS client's identity, the spec of the install
// namespace/name.
func (c *Client) InstallSpec(ctx context.Context, namespace, name string) (InstallSpec, error) {
	u, err := c.dyn.Resource(dotvirtsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return InstallSpec{}, err
	}
	var out InstallSpec
	out.Image, _, _ = unstructured.NestedString(u.Object, "spec", "image")
	out.ArgoNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "argocd", "namespace")
	out.ArgoController, _, _ = unstructured.NestedString(u.Object, "spec", "argocd", "controllerServiceAccount")
	out.InsecureTLS, _, _ = unstructured.NestedBool(u.Object, "spec", "forge", "insecureTLS")
	return out, nil
}

// OperatorInfo is the operator's own Deployment as an admin's token reads it:
// the account it runs as and the operand images an admin chose there.
type OperatorInfo struct {
	Namespace, ServiceAccount  string
	DotvirtImage, ForgejoImage string
}

// OperatorDeployment finds, under THIS client's identity, the one Deployment
// named name in the cluster. The operator may create no Deployment of that
// name, so OLM or GitOps wrote it; two are refused, as neither can be told the
// operator.
func (c *Client) OperatorDeployment(ctx context.Context, name string) (OperatorInfo, error) {
	list, err := c.kube.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name})
	if err != nil {
		return OperatorInfo{}, err
	}
	if len(list.Items) != 1 {
		return OperatorInfo{}, fmt.Errorf("%d Deployments named %s, want exactly one", len(list.Items), name)
	}
	d := list.Items[0]
	out := OperatorInfo{Namespace: d.Namespace, ServiceAccount: d.Spec.Template.Spec.ServiceAccountName}
	if out.ServiceAccount == "" {
		out.ServiceAccount = "default"
	}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		for _, e := range ctr.Env {
			switch e.Name {
			case "RELATED_IMAGE_DOTVIRT":
				out.DotvirtImage = e.Value
			case "RELATED_IMAGE_FORGEJO":
				out.ForgejoImage = e.Value
			}
		}
	}
	return out, nil
}

// ServesRoutes reports whether the cluster serves route.openshift.io, which
// the operator reads as OpenShift (MicroShift included).
func (c *Client) ServesRoutes() (bool, error) {
	return c.ServesAPIResource(schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"})
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
