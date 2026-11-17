package cluster

import (
	"context"
	"fmt"

	authzv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var dotvirtsGVR = schema.GroupVersionResource{Group: "dotvirt.io", Version: "v1alpha1", Resource: "dotvirts"}

// InstallSpec is what an admin chose on an install: its image, its ArgoCD
// namespace and controller, whether forge TLS goes unverified, and where
// metrics queries go.
type InstallSpec struct {
	Image, ArgoNamespace, ArgoController string
	InsecureTLS                          bool
	MetricsURL, MetricsTenancyURL        string
	MetricsCA                            bool
	IngressHost                          string
	// The console's volumes an admin chose: the branding and metrics CA
	// ConfigMaps, and the forge credential Secret (empty: the default).
	BrandingConfigMap, MetricsCAConfigMap, ForgeCredentials string
	ForgeManaged                                            bool
	// OIDCSecret and OIDCSecretKey are the OIDC client secret an admin chose,
	// OIDCIssuer the issuer it is sent to.
	OIDCSecret, OIDCSecretKey, OIDCIssuer string
}

// InstallIn reads, under THIS client's identity, the one install in
// namespace: two are refused, as neither can be told the console's.
func (c *Client) InstallIn(ctx context.Context, namespace string) (InstallSpec, error) {
	list, err := c.dyn.Resource(dotvirtsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return InstallSpec{}, err
	}
	if len(list.Items) != 1 {
		return InstallSpec{}, fmt.Errorf("%d Dotvirt resources in %s, want exactly one", len(list.Items), namespace)
	}
	o := list.Items[0].Object
	var out InstallSpec
	out.Image, _, _ = unstructured.NestedString(o, "spec", "image")
	out.ArgoNamespace, _, _ = unstructured.NestedString(o, "spec", "argocd", "namespace")
	out.ArgoController, _, _ = unstructured.NestedString(o, "spec", "argocd", "controllerServiceAccount")
	out.InsecureTLS, _, _ = unstructured.NestedBool(o, "spec", "forge", "insecureTLS")
	out.MetricsURL, _, _ = unstructured.NestedString(o, "spec", "metrics", "url")
	out.MetricsTenancyURL, _, _ = unstructured.NestedString(o, "spec", "metrics", "tenancyURL")
	_, out.MetricsCA, _ = unstructured.NestedMap(o, "spec", "metrics", "ca")
	out.IngressHost, _, _ = unstructured.NestedString(o, "spec", "ingress", "host")
	out.BrandingConfigMap, _, _ = unstructured.NestedString(o, "spec", "branding", "configMap")
	out.MetricsCAConfigMap, _, _ = unstructured.NestedString(o, "spec", "metrics", "ca", "name")
	out.ForgeCredentials, _, _ = unstructured.NestedString(o, "spec", "forge", "credentialsSecret")
	out.ForgeManaged, _, _ = unstructured.NestedBool(o, "spec", "forge", "managed")
	out.OIDCSecret, _, _ = unstructured.NestedString(o, "spec", "auth", "oidc", "clientSecret", "name")
	out.OIDCSecretKey, _, _ = unstructured.NestedString(o, "spec", "auth", "oidc", "clientSecret", "key")
	out.OIDCIssuer, _, _ = unstructured.NestedString(o, "spec", "auth", "oidc", "issuerURL")
	return out, nil
}

// OperatorInfo is the operator's own Deployment as an admin's token reads it:
// the account it runs as and the operand images an admin chose there.
type OperatorInfo struct {
	Namespace, ServiceAccount  string
	DotvirtImage, ForgejoImage string
	// InstallNamespace is the install's namespace an admin names there, if any.
	InstallNamespace string
}

// OperatorDeployment finds, under THIS client's identity, the operator of the
// install in installNS: among the Deployments named name, the one whose
// account may patch Dotvirt status there. Any tenant may name a Deployment
// so, and none's account holds that right; two that do are refused.
func (c *Client) OperatorDeployment(ctx context.Context, name, installNS string) (OperatorInfo, error) {
	list, err := c.kube.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name})
	if err != nil {
		return OperatorInfo{}, err
	}
	var found []OperatorInfo
	for _, d := range list.Items {
		sa := d.Spec.Template.Spec.ServiceAccountName
		if sa == "" {
			sa = "default"
		}
		review, err := c.kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
			User:   "system:serviceaccount:" + d.Namespace + ":" + sa,
			Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + d.Namespace, "system:authenticated"},
			ResourceAttributes: &authzv1.ResourceAttributes{
				Verb: "patch", Group: "dotvirt.io", Resource: "dotvirts", Subresource: "status", Namespace: installNS,
			},
		}}, metav1.CreateOptions{})
		if err != nil {
			return OperatorInfo{}, err
		}
		if !review.Status.Allowed {
			continue
		}
		info := OperatorInfo{Namespace: d.Namespace, ServiceAccount: sa}
		for _, ctr := range d.Spec.Template.Spec.Containers {
			for _, e := range ctr.Env {
				switch e.Name {
				case "RELATED_IMAGE_DOTVIRT":
					info.DotvirtImage = e.Value
				case "RELATED_IMAGE_FORGEJO":
					info.ForgejoImage = e.Value
				case adminobjects.InstallNamespaceEnv:
					info.InstallNamespace = e.Value
				}
			}
		}
		found = append(found, info)
	}
	if len(found) != 1 {
		return OperatorInfo{}, fmt.Errorf("%d Deployments named %s whose account may patch Dotvirt status in %s, want exactly one", len(found), name, installNS)
	}
	return found[0], nil
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

// AppsDomain reads, under THIS client's identity, the cluster's ingress domain
// (config.openshift.io Ingress cluster): where the router names a hostless
// Route.
func (c *Client) AppsDomain(ctx context.Context) (string, error) {
	gvr := schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "ingresses"}
	u, err := c.dyn.Resource(gvr).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	domain, _, _ := unstructured.NestedString(u.Object, "spec", "domain")
	if domain == "" {
		return "", fmt.Errorf("the cluster's Ingress names no domain")
	}
	return domain, nil
}

// SSOClient is the OpenShift OAuthClient the console in namespace registers
// and signs users in with, as THIS client reads it: named for the namespace,
// redirecting to the install's spec.ingress.host, else the router's host for
// the console's Route. Never the env the operator writes.
func (c *Client) SSOClient(ctx context.Context, namespace string) (id, redirect string, err error) {
	spec, err := c.InstallIn(ctx, namespace)
	if err != nil {
		return "", "", fmt.Errorf("the install: %w", err)
	}
	domain := ""
	if spec.IngressHost == "" {
		if domain, err = c.AppsDomain(ctx); err != nil {
			return "", "", fmt.Errorf("the cluster's ingress domain: %w", err)
		}
	}
	return adminobjects.SSOClientName(namespace), adminobjects.SSORedirect(spec.IngressHost, namespace, domain), nil
}

// OperatorInstall is the namespace whose Dotvirt the operator op acts on, as
// the operator resolves it, read as THIS client: the one an admin names on its
// Deployment, else its own while a Dotvirt is there, else the one
// ClusterRoleBinding dotvirt grants the console's ServiceAccount in, while a
// Dotvirt there predates the binding; "" for none.
func (c *Client) OperatorInstall(ctx context.Context, op OperatorInfo) (string, error) {
	if op.InstallNamespace != "" {
		return op.InstallNamespace, nil
	}
	own, err := c.dyn.Resource(dotvirtsGVR).Namespace(op.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return "", err
	}
	if len(own.Items) > 0 {
		return op.Namespace, nil
	}
	crb, err := c.kube.RbacV1().ClusterRoleBindings().Get(ctx, adminobjects.ConsoleAccount, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if crb.RoleRef.Kind != "ClusterRole" || crb.RoleRef.Name != adminobjects.ConsoleAccount {
		return "", nil
	}
	bound := ""
	for _, s := range crb.Subjects {
		if s.Kind != rbacv1.ServiceAccountKind || s.Name != adminobjects.ConsoleAccount {
			continue
		}
		if s.Namespace == "" || (bound != "" && s.Namespace != bound) {
			return "", nil
		}
		bound = s.Namespace
	}
	if bound == "" {
		return "", nil
	}
	there, err := c.dyn.Resource(dotvirtsGVR).Namespace(bound).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for _, d := range there.Items {
		created := d.GetCreationTimestamp()
		if !crb.CreationTimestamp.Before(&created) {
			return bound, nil
		}
	}
	return "", nil
}

// RouterHost is the host routers report serving the Route named name in
// namespace under, as THIS client reads it (adminobjects.RouterHost).
func (c *Client) RouterHost(ctx context.Context, namespace, name string) (string, error) {
	r, err := c.dyn.Resource(schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}).
		Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return adminobjects.RouterHost(r.Object), nil
}
