// Package install renders the resources that make up a dotvirt install as typed
// objects from the Dotvirt spec + detected platform (compile-checked rendering,
// trivial parameterization), then server-side-applies them with a stable field
// manager so re-reconciles converge and drift is corrected.
//
// GC has two regimes, because a namespaced Dotvirt CR cannot own cluster-scoped or
// cross-namespace resources via ownerReferences (k8s forbids it):
//   - dotvirt-namespace resources (Deployment, Service, SA, PVC) get an ownerRef to
//     the CR — automatic garbage collection.
//   - cluster-scoped + ArgoCD-namespace resources (ClusterRoles, AppProjects, the
//     ApplicationSet/Application) carry the managed-by labels below and are cleaned
//     up by the CR's finalizer on delete (added with those resources).
package install

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldManager is the server-side-apply field owner for everything the operator
// renders.
const FieldManager = "dotvirt-operator"

// AppName is the common name/label for the dotvirt workload.
const AppName = "dotvirt"

// HTTPPort is dotvirt's single HTTP port — the one source for the Service, the
// container port and probes, the `-addr` flag, and every in-cluster URL built to it.
const HTTPPort int32 = 8080

// Labels are the recommended labels stamped on every rendered resource, plus a
// per-instance label so cluster-scoped / cross-namespace resources (which can't
// carry an ownerRef to a namespaced CR) can be found for finalizer cleanup.
func Labels(instance string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       AppName,
		"app.kubernetes.io/managed-by": FieldManager,
		"dotvirt.io/instance":          instance,
	}
}

// Apply server-side-applies obj with the operator's field manager, force-owning the
// fields it sets. Typed objects must carry their GVK (the builders set TypeMeta).
// When dryRun is set, the API server validates the object (schema, admission, RBAC)
// but persists nothing — used to validate the full render against a real cluster.
func Apply(ctx context.Context, c client.Client, obj client.Object, dryRun bool) error {
	opts := []client.PatchOption{client.FieldOwner(FieldManager), client.ForceOwnership}
	if dryRun {
		opts = append(opts, client.DryRunAll)
	}
	return c.Patch(ctx, obj, client.Apply, opts...)
}
