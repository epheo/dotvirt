package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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

// pruneWhere says which namespaces prune lists a kind in.
type pruneWhere int

const (
	// installNS: the install namespace only.
	installNS pruneWhere = iota
	// anywhere: cluster-scoped kinds, and the argoproj kinds, which only this
	// operator labels; where they sit tells prune which namespaces it wrote.
	anywhere
	// writtenNS: the namespaces this install wrote, never the install namespace.
	// A cluster-wide sweep would delete an admin's labelled copy elsewhere.
	writtenNS
)

// pruneScope is one kind prune sweeps.
type pruneScope struct {
	list  func() client.ObjectList
	where pruneWhere
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

// pruneScopes are the rendered kinds, the anywhere ones before the writtenNS
// ones they discover namespaces for. Left out on purpose, in the install
// namespace: Secrets and PVCs are state, not render (create-once keys, the
// minted forge token, the forge's git data); the CA ConfigMaps are best-effort
// copies a transient read failure must not delete.
func pruneScopes() []pruneScope {
	scopes := []pruneScope{
		{typedList(&appsv1.DeploymentList{}), installNS},
		{typedList(&corev1.ServiceList{}), installNS},
		{typedList(&corev1.ServiceAccountList{}), installNS},
		{typedList(&networkingv1.NetworkPolicyList{}), installNS},
		{typedList(&networkingv1.IngressList{}), installNS},
		{unstructuredList(install.RouteGVK), installNS},
		{typedList(&rbacv1.ClusterRoleBindingList{}), anywhere},
	}
	for _, kind := range []string{"AppProject", "Application", "ApplicationSet"} {
		scopes = append(scopes, pruneScope{unstructuredList(install.ArgoGVK(kind)), anywhere})
	}
	return append(scopes,
		pruneScope{typedList(&corev1.SecretList{}), writtenNS},
		pruneScope{typedList(&corev1.ConfigMapList{}), writtenNS},
	)
}

// renderedNamespaces are the namespaces other than the install's that the pass
// wrote to.
func renderedNamespaces(dv *dotvirtv1alpha1.Dotvirt, rendered renderSet) map[string]bool {
	ns := map[string]bool{}
	for k := range rendered {
		if k.namespace != "" && k.namespace != dv.Namespace {
			ns[k.namespace] = true
		}
	}
	return ns
}

// prune deletes this install's objects that the completed pass did not render:
// an exposure whose type changed, the platform tier after platformRepo is
// cleared, the ArgoCD-namespace objects after the namespace moves, a managed
// forge switched off, a name a newer version no longer renders. Only objects
// install.OwnedBy marks as this install's are candidates. Listing goes to the
// API server directly: the cache holds none of these kinds cluster-wide.
func (r *DotvirtReconciler) prune(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rendered renderSet) error {
	// Seeded with this pass's namespaces; a former ArgoCD namespace joins when
	// an argoproj object of this install is found there.
	written := renderedNamespaces(dv, rendered)
	for _, sc := range pruneScopes() {
		var namespaces []string
		switch sc.where {
		case installNS:
			namespaces = []string{dv.Namespace}
		case anywhere:
			namespaces = []string{""}
		case writtenNS:
			for ns := range written {
				namespaces = append(namespaces, ns)
			}
		}
		for _, ns := range namespaces {
			if err := r.pruneList(ctx, dv, sc, ns, rendered, written); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *DotvirtReconciler) pruneList(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, sc pruneScope, ns string, rendered renderSet, written map[string]bool) error {
	list := sc.list()
	opts := []client.ListOption{client.MatchingLabels{install.InstanceLabel: dv.Name}}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := r.reader().List(ctx, list, opts...); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || !install.OwnedBy(obj.GetLabels(), dv) {
			continue
		}
		if sc.where != installNS && obj.GetNamespace() == dv.Namespace {
			continue
		}
		if sc.where == anywhere && obj.GetNamespace() != "" {
			written[obj.GetNamespace()] = true
		}
		gvk, err := apiutil.GVKForObject(obj, r.Scheme)
		if err != nil {
			gvk = obj.GetObjectKind().GroupVersionKind()
		}
		if _, keep := rendered[objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()}]; keep {
			continue
		}
		logf.FromContext(ctx).Info("pruning object the install no longer renders",
			"kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
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

// pruneState is the reconciler's memory of past prunes.
type pruneState struct {
	pruneMu sync.Mutex
	pruned  map[types.NamespacedName]pruneMark
	// now is a test seam; nil = time.Now.
	now func() time.Time
}

// pruneMark is the last successful prune of one install.
type pruneMark struct {
	digest string
	at     time.Time
}

// digest fingerprints a render set independent of map order.
func (s renderSet) digest() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k.gk.String()+"/"+k.namespace+"/"+k.name)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

// pruneDue spares the uncached LISTs on the event-driven passes: prune runs when
// the render changed since the last successful prune, or once the drift timer
// is due. Out-of-band leftovers wait at most that long.
func (r *DotvirtReconciler) pruneDue(key types.NamespacedName, digest string) bool {
	r.pruneMu.Lock()
	defer r.pruneMu.Unlock()
	m, ok := r.pruned[key]
	return !ok || m.digest != digest || r.clock().Sub(m.at) >= driftResync/2
}

func (r *DotvirtReconciler) markPruned(key types.NamespacedName, digest string) {
	r.pruneMu.Lock()
	defer r.pruneMu.Unlock()
	if r.pruned == nil {
		r.pruned = map[types.NamespacedName]pruneMark{}
	}
	if digest == "" {
		delete(r.pruned, key)
		return
	}
	r.pruned[key] = pruneMark{digest: digest, at: r.clock()}
}

func (r *DotvirtReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}
