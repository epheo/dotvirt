// Package install renders the resources that make up a dotvirt install as typed
// objects from the Dotvirt spec + detected platform (compile-checked rendering,
// trivial parameterization), then server-side-applies them with a stable field
// manager so re-reconciles converge and drift is corrected.
//
// GC has two regimes, because a namespaced Dotvirt CR cannot own cluster-scoped or
// cross-namespace resources via ownerReferences (k8s forbids it):
//   - dotvirt-namespace resources (Deployment, Service, SA, PVC) get an ownerRef to
//     the CR - automatic garbage collection.
//   - cluster-scoped + ArgoCD-namespace resources (ClusterRoleBindings, AppProjects,
//     the ApplicationSet/Application, the plugin ConfigMap and Secrets) carry the
//     instance labels below and are cleaned up by the CR's finalizer on delete.
package install

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// fieldManager is the server-side-apply field owner for everything the operator
// renders.
const fieldManager = "dotvirt-operator"

// AppName is the common name/label for the dotvirt workload.
const AppName = "dotvirt"

// HTTPPort is dotvirt's single HTTP port - the one source for the Service, the
// container port and probes, the `-addr` flag, and every in-cluster URL built to it.
const HTTPPort int32 = 8080

// InstanceLabel and InstanceNamespaceLabel together tie a rendered object to the
// install that owns it; prune and the finalizer's cleanup select on both. Two
// labels, not one "<ns>.<name>" value: a label value caps at 63 characters and a
// CR name alone may be longer.
const (
	InstanceLabel          = "dotvirt.io/instance"
	InstanceNamespaceLabel = "dotvirt.io/instance-namespace"
)

// Labels are the recommended labels plus the instance labels, stamped on every
// rendered resource.
func Labels(dv *dotvirtv1alpha1.Dotvirt) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       AppName,
		"app.kubernetes.io/managed-by": fieldManager,
		InstanceLabel:                  dv.Name,
		InstanceNamespaceLabel:         dv.Namespace,
	}
}

// OwnedBy reports whether labels mark an object as dv's. An object carrying only
// InstanceLabel was rendered before InstanceNamespaceLabel existed; it counts as
// dv's when the name matches, since one install per cluster is enforced.
func OwnedBy(labels map[string]string, dv *dotvirtv1alpha1.Dotvirt) bool {
	if labels[InstanceLabel] != dv.Name {
		return false
	}
	ns, ok := labels[InstanceNamespaceLabel]
	return !ok || ns == dv.Namespace
}

// InstanceSelectors match dv's objects: the current label pair, and the legacy
// name-only label (OwnedBy explains why that one is still dv's).
func InstanceSelectors(dv *dotvirtv1alpha1.Dotvirt) []labels.Selector {
	name, _ := labels.NewRequirement(InstanceLabel, selection.Equals, []string{dv.Name})
	ns, _ := labels.NewRequirement(InstanceNamespaceLabel, selection.Equals, []string{dv.Namespace})
	noNS, _ := labels.NewRequirement(InstanceNamespaceLabel, selection.DoesNotExist, nil)
	return []labels.Selector{
		labels.NewSelector().Add(*name, *ns),
		labels.NewSelector().Add(*name, *noNS),
	}
}

// unstructuredObject renders a labeled unstructured object with the given spec -
// the shared shape for the kinds the operator applies without importing their API
// module (the argoproj.io resources, the OpenShift Route).
func unstructuredObject(gvk schema.GroupVersionKind, name, namespace string, dv *dotvirtv1alpha1.Dotvirt, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(Labels(dv))
	u.Object["spec"] = spec
	return u
}

// Apply server-side-applies obj with the operator's field manager, force-owning the
// fields it sets. Typed objects must carry their GVK (the builders set TypeMeta).
// When dryRun is set, the API server validates the object (schema, admission, RBAC)
// but persists nothing - used to validate the full render against a real cluster.
func Apply(ctx context.Context, c client.Client, obj client.Object, dryRun bool) error {
	opts := []client.PatchOption{client.FieldOwner(fieldManager), client.ForceOwnership}
	if dryRun {
		opts = append(opts, client.DryRunAll)
	}
	// Client.Apply takes ApplyConfigurations, which the typed builders do not render.
	return c.Patch(ctx, obj, client.Apply, opts...) //nolint:staticcheck // SA1019, see above
}
