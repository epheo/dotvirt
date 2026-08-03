package cluster

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// trackingIDAnnotation is ArgoCD's resource-tracking key, formatted
// "<app>:<group>/<kind>:<namespace>/<name>" where <app> may be "<ns>/<name>".
const trackingIDAnnotation = "argocd.argoproj.io/tracking-id"

// adoptableKinds are the namespace-scoped kinds a tenant repo may declare. It mirrors
// the tenant AppProject's namespaceResourceWhitelist (operator/internal/install/
// argocd.go): capturing outside it stages manifests ArgoCD is configured to refuse,
// capturing less leaves the namespace half under git. Operator and app ship in one
// release, so the two are kept in step by hand rather than read back from the cluster.
var adoptableKinds = []schema.GroupVersionResource{
	{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"},
	{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"},
	{Group: "k8s.ovn.org", Version: "v1", Resource: "userdefinednetworks"},
	{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"},
	{Group: "k8s.ovn.org", Version: "v1", Resource: "egressfirewalls"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
}

// Adoptable is one live object serialized as the manifest a repo would hold.
type Adoptable struct {
	Namespace string
	Name      string
	Kind      string
	Path      string // repo-relative
	Manifest  []byte
}

// AdoptableObjects: everything running in namespaces that git does not describe,
// plus the kinds the caller could not read.
//
// Skips foreign-app claims: that repo declares the object; capturing it would give
// two sources. The OWN app is excluded from foreignApps: whether this repo
// declares an object is git's call, and after repo loss the own app survives
// while git declares nothing (the recovery case). A deleted app's annotation is
// residue; skipping on it would strand the object.
//
// Skips ownerReferences: derived objects (dataVolumeTemplates DVs, UDN-rendered
// NADs) must not be declared beside their owner.
//
// Forbidden kinds are named in unreadable, never an error: capture runs under the
// caller's token and standard admin roles miss some kinds; failing hard would put
// adoption out of their reach, silence would read partial as whole.
func (c *Client) AdoptableObjects(ctx context.Context, namespaces []string, foreignApps map[string]bool) (objs []Adoptable, unreadable []string, err error) {
	seen := map[string]bool{}
	for _, ns := range namespaces {
		for _, gvr := range adoptableKinds {
			list, err := c.dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
					continue
				}
				if apierrors.IsForbidden(err) {
					if !seen[gvr.Resource] {
						seen[gvr.Resource] = true
						unreadable = append(unreadable, gvr.Resource)
					}
					continue
				}
				return nil, nil, fmt.Errorf("read %s in %s: %w", gvr.Resource, ns, err)
			}
			for i := range list.Items {
				obj := &list.Items[i]
				if claimedByForeignApp(obj, foreignApps) || len(obj.GetOwnerReferences()) > 0 {
					continue
				}
				// A terminating object still LISTs while a finalizer runs. adoptManifest
				// strips deletionTimestamp and finalizers, so capturing one would produce a
				// healthy-looking manifest whose merge re-creates what was just deleted.
				if obj.GetDeletionTimestamp() != nil {
					continue
				}
				m, err := adoptManifest(obj)
				if err != nil {
					return nil, nil, fmt.Errorf("serialize %s %s/%s: %w", gvr.Resource, ns, obj.GetName(), err)
				}
				objs = append(objs, Adoptable{
					Namespace: ns,
					Name:      obj.GetName(),
					Kind:      obj.GetKind(),
					Path:      adoptPath(obj),
					Manifest:  m,
				})
			}
		}
	}
	return objs, unreadable, nil
}

// claimedByForeignApp: the tracking-id names a live app on another source. The
// bare annotation proves only that some app once declared the object.
func claimedByForeignApp(obj *unstructured.Unstructured, foreignApps map[string]bool) bool {
	id := obj.GetAnnotations()[trackingIDAnnotation]
	if id == "" {
		return false
	}
	app, _, ok := strings.Cut(id, ":")
	if !ok {
		return false
	}
	return foreignApps[app]
}

// adoptManifest strips a live object to desired state: status, apply-time metadata, and
// the volatile annotations the VM export already drops. Kind-agnostic, so one path
// serves every adoptable kind. Unstructured keeps removed keys gone, unlike the typed
// ExportManifest, which needs its empty status stripped afterwards.
func adoptManifest(obj *unstructured.Unstructured) ([]byte, error) {
	u := obj.DeepCopy()
	unstructured.RemoveNestedField(u.Object, "status")
	for _, f := range []string{
		"uid", "resourceVersion", "generation", "creationTimestamp",
		"deletionTimestamp", "deletionGracePeriodSeconds",
		"managedFields", "selfLink", "ownerReferences", "finalizers",
	} {
		unstructured.RemoveNestedField(u.Object, "metadata", f)
	}
	if ann := u.GetAnnotations(); len(ann) > 0 {
		if cleaned := stripAnnotations(ann); len(cleaned) == 0 {
			unstructured.RemoveNestedField(u.Object, "metadata", "annotations")
		} else {
			u.SetAnnotations(cleaned)
		}
	}
	// Older KubeVirt pins the matcher's ControllerRevision into spec; declared in
	// git it turns any later matcher edit into a webhook reject ("Name updated
	// without updating RevisionName"). KubeVirt's own GitOps guidance is to strip it.
	unstructured.RemoveNestedField(u.Object, "spec", "instancetype", "revisionName")
	unstructured.RemoveNestedField(u.Object, "spec", "preference", "revisionName")
	return yaml.Marshal(u.Object)
}

// adoptPath places a captured object where dotvirt's own generators would have written
// it (internal/netgen, internal/vmgen), so adopting then editing through the UI lands on
// ONE file. A layout of its own would leave the same object declared twice in the same
// Application source, which Argo reports as a duplicate and neither file's deletion
// resolves. A kind with no generator falls back to a kind-suffixed name, which cannot
// collide with a VM of the same name.
func adoptPath(obj *unstructured.Unstructured) string {
	ns, name := obj.GetNamespace(), obj.GetName()
	switch obj.GetKind() {
	case "VirtualMachine":
		return ns + "/" + name + ".yaml"
	case "UserDefinedNetwork":
		return ns + "/networks/" + name + ".yaml"
	case "NetworkPolicy":
		return ns + "/networkpolicies/" + name + ".yaml"
	case "EgressFirewall":
		// OVN allows one per namespace, always named "default", and netgen writes exactly
		// that file.
		return ns + "/egressfirewalls/default.yaml"
	}
	return ns + "/" + name + "." + strings.ToLower(obj.GetKind()) + ".yaml"
}

// ApplyOAuthClient converges the OAuthClient SSO needs under THIS client's
// identity: the caller's RBAC is the gate, so neither dotvirt's SA nor the
// operator ever holds oauthclients permissions.
func (c *Client) ApplyOAuthClient(ctx context.Context, id, secret, redirectURL string) error {
	gvr := schema.GroupVersionResource{Group: "oauth.openshift.io", Version: "v1", Resource: "oauthclients"}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":   "oauth.openshift.io/v1",
		"kind":         "OAuthClient",
		"metadata":     map[string]any{"name": id},
		"secret":       secret,
		"redirectURIs": []any{redirectURL},
		"grantMethod":  "auto",
	}}
	_, err := c.dyn.Resource(gvr).Apply(ctx, id, obj, metav1.ApplyOptions{FieldManager: "dotvirt", Force: true})
	return err
}
