package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

type objKey struct {
	gk        schema.GroupKind
	namespace string
	name      string
}

// renderSet is every object one reconcile pass handed to apply: what prune keeps.
type renderSet map[objKey]struct{}

type renderSetKey struct{}

// withRenderSet scopes a renderSet to one pass. Carried on the context so every
// apply records itself without threading the set through each phase helper.
func withRenderSet(ctx context.Context) (context.Context, renderSet) {
	s := renderSet{}
	return context.WithValue(ctx, renderSetKey{}, s), s
}

// recordRender runs before the apply, so a best-effort apply that failed this
// pass still counts as rendered and is never pruned for it.
func (r *DotvirtReconciler) recordRender(ctx context.Context, obj client.Object) {
	s, ok := ctx.Value(renderSetKey{}).(renderSet)
	if !ok {
		return
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Empty() {
		var err error
		if gvk, err = apiutil.GVKForObject(obj, r.Scheme); err != nil {
			return
		}
	}
	s[objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()}] = struct{}{}
}

// pruneScope is one kind prune sweeps. ownNamespace kinds are listed in the
// install namespace only; the rest everywhere except it.
type pruneScope struct {
	list         func() client.ObjectList
	ownNamespace bool
}

func typedList(l client.ObjectList) func() client.ObjectList {
	return func() client.ObjectList { return l.DeepCopyObject().(client.ObjectList) }
}

func unstructuredList(gvk schema.GroupVersionKind) func() client.ObjectList {
	return func() client.ObjectList {
		l := &unstructured.UnstructuredList{}
		l.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		return l
	}
}

// pruneScopes are the rendered kinds. Left out on purpose, in the install
// namespace: Secrets and PVCs are state, not render (create-once keys, the
// minted forge token, the forge's git data); the CA ConfigMaps are best-effort
// copies a transient read failure must not delete.
func pruneScopes() []pruneScope {
	scopes := []pruneScope{
		{typedList(&appsv1.DeploymentList{}), true},
		{typedList(&corev1.ServiceList{}), true},
		{typedList(&corev1.ServiceAccountList{}), true},
		{typedList(&networkingv1.NetworkPolicyList{}), true},
		{typedList(&networkingv1.IngressList{}), true},
		{unstructuredList(install.RouteGVK), true},
		{typedList(&rbacv1.ClusterRoleBindingList{}), false},
		{typedList(&corev1.SecretList{}), false},
		{typedList(&corev1.ConfigMapList{}), false},
	}
	for _, kind := range []string{"AppProject", "Application", "ApplicationSet"} {
		scopes = append(scopes, pruneScope{unstructuredList(install.ArgoGVK(kind)), false})
	}
	return scopes
}

// prune deletes this install's objects that the completed pass did not render:
// an exposure whose type changed, the platform tier after platformRepo is
// cleared, the ArgoCD-namespace objects after the namespace moves, a managed
// forge switched off, a name a newer version no longer renders. Only objects
// install.OwnedBy marks as this install's are candidates. Listing goes to the
// API server directly: the cache holds none of these kinds cluster-wide.
func (r *DotvirtReconciler) prune(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rendered renderSet) error {
	log := logf.FromContext(ctx)
	for _, sc := range pruneScopes() {
		list := sc.list()
		opts := []client.ListOption{client.MatchingLabels{install.InstanceLabel: dv.Name}}
		if sc.ownNamespace {
			opts = append(opts, client.InNamespace(dv.Namespace))
		}
		if err := r.reader().List(ctx, list, opts...); err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			return err
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return err
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok {
				continue
			}
			if !sc.ownNamespace && obj.GetNamespace() == dv.Namespace {
				continue
			}
			if !install.OwnedBy(obj.GetLabels(), dv) {
				continue
			}
			gvk, err := apiutil.GVKForObject(obj, r.Scheme)
			if err != nil {
				gvk = obj.GetObjectKind().GroupVersionKind()
			}
			if _, keep := rendered[objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()}]; keep {
				continue
			}
			log.Info("pruning object the install no longer renders", "kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
			if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// reader is the uncached API reader; tests leave APIReader nil and read the fake.
func (r *DotvirtReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
