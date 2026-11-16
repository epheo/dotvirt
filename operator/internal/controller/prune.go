package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// pruneWhere says which namespaces prune reads a kind in.
type pruneWhere int

const (
	// installNS: the install namespace only.
	installNS pruneWhere = iota
	// cluster: cluster-scoped kinds.
	cluster
	// writtenNS: the ArgoCD namespaces this install wrote (the current one and
	// the one status records), never the install namespace. Reading anywhere
	// else would take an admin's labelled copy.
	writtenNS
)

// pruneScope is one kind prune sweeps.
type pruneScope struct {
	list  func() client.ObjectList
	where pruneWhere
	// names are the names the install renders of the kind; prune reads each by
	// name, the only read the operator's RBAC allows. Empty only for the tenant
	// AppProjects, named after their projects, which prune lists.
	names []string
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

// pruneScopes are the rendered kinds and the names the install renders of each.
// Left out on purpose, in the install namespace: Secrets and PVCs are state, not
// render (create-once keys, the minted forge token, the forge's git data); the
// CA ConfigMaps are best-effort copies a transient read failure must not delete.
func pruneScopes() []pruneScope {
	operand := []string{install.AppName, install.ForgejoServiceName}
	return []pruneScope{
		{list: typedList(&appsv1.DeploymentList{}), where: installNS, names: operand},
		{list: typedList(&corev1.ServiceList{}), where: installNS, names: operand},
		{list: typedList(&corev1.ServiceAccountList{}), where: installNS, names: operand},
		{list: typedList(&networkingv1.NetworkPolicyList{}), where: installNS, names: operand},
		{list: typedList(&networkingv1.IngressList{}), where: installNS, names: operand},
		{list: unstructuredList(install.RouteGVK), where: installNS, names: operand},
		{list: unstructuredList(install.HTTPRouteGVK), where: installNS, names: operand},
		{list: typedList(&rbacv1.ClusterRoleBindingList{}), where: cluster, names: install.ClusterRoleBindingNames},
		{list: unstructuredList(install.ArgoGVK("AppProject")), where: writtenNS},
		{list: unstructuredList(install.ArgoGVK("Application")), where: writtenNS, names: []string{install.PlatformApplicationName}},
		{list: unstructuredList(install.ArgoGVK("ApplicationSet")), where: writtenNS, names: []string{install.ApplicationSetName}},
		{list: typedList(&corev1.SecretList{}), where: writtenNS, names: []string{install.AppsetSecretName, install.RepoCredsName}},
		{list: typedList(&corev1.ConfigMapList{}), where: writtenNS, names: []string{install.AppsetConfigMapName}},
		{list: typedList(&rbacv1.RoleBindingList{}), where: writtenNS, names: []string{install.SyncRequestName}},
	}
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

// finalize handles deletion: a prune with nothing rendered takes every object
// this install owns, including the cluster-scoped and ArgoCD-namespace ones a
// namespaced CR cannot ownerRef, then the finalizer comes off.
func (r *DotvirtReconciler) finalize(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	if r.DryRun || !controllerutil.ContainsFinalizer(dv, dotvirtFinalizer) {
		return nil
	}
	// Before prune takes dotvirt's AppProjects and their destinationServiceAccounts:
	// left on, the setting would stop every Application on the instance that
	// names no other account.
	for _, instance := range r.syncInstances(dv) {
		if err := r.releaseSyncImpersonation(ctx, instance); err != nil {
			return err
		}
	}
	// argocd-secret is Argo's: prune never takes it, so the key comes out here.
	argoNS, _ := r.argoTarget(dv)
	if err := r.releaseArgoWebhookKey(ctx, dv, argoNS); err != nil {
		return err
	}
	if err := r.prune(ctx, dv, renderSet{}); err != nil {
		return err
	}
	r.markPruned(client.ObjectKeyFromObject(dv), "")
	controllerutil.RemoveFinalizer(dv, dotvirtFinalizer)
	return r.Update(ctx, dv)
}

// syncInstances are the ArgoCD resources the operator may have set sync
// impersonation on: the one status records and the one spec.argocd names.
func (r *DotvirtReconciler) syncInstances(dv *dotvirtv1alpha1.Dotvirt) []string {
	argoNS, _ := r.argoTarget(dv)
	out := []string{argoNS + "/" + argoInstance(argoNS)}
	if prev := dv.Status.SyncImpersonationInstance; prev != "" && prev != out[0] {
		out = append(out, prev)
	}
	return out
}

// prune deletes this install's objects that the completed pass did not render:
// an exposure whose type changed, the platform tier after platformRepo is
// cleared, the ArgoCD-namespace objects after the namespace moves, a managed
// forge switched off. Only objects install.OwnedBy marks as this install's are
// candidates. Reads go to the API server directly: the cache holds none of
// these kinds.
func (r *DotvirtReconciler) prune(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rendered renderSet) error {
	// This pass's namespaces, the current ArgoCD namespace, which finalize must
	// sweep though it renders nothing there, and the one status records, which
	// the objects of a moved spec.argocd.namespace are left in.
	written := renderedNamespaces(dv, rendered)
	argoNS, _ := r.argoTarget(dv)
	for _, ns := range []string{argoNS, dv.Status.ArgoNamespace} {
		if ns != "" && ns != dv.Namespace {
			written[ns] = true
		}
	}
	for _, sc := range pruneScopes() {
		var namespaces []string
		switch sc.where {
		case installNS:
			namespaces = []string{dv.Namespace}
		case cluster:
			namespaces = []string{""}
		case writtenNS:
			namespaces = slices.Sorted(maps.Keys(written))
		}
		for _, ns := range namespaces {
			if err := r.pruneList(ctx, dv, sc, ns, rendered); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *DotvirtReconciler) pruneList(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, sc pruneScope, ns string, rendered renderSet) error {
	items, err := r.pruneCandidates(ctx, dv, sc, ns)
	if err != nil {
		return err
	}
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || !install.OwnedBy(obj.GetLabels(), dv) {
			continue
		}
		gvk, err := apiutil.GVKForObject(obj, r.Scheme)
		if err != nil {
			gvk = obj.GetObjectKind().GroupVersionKind()
		}
		if _, keep := rendered[objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()}]; keep {
			continue
		}
		log := logf.FromContext(ctx).WithValues("kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
		log.Info("pruning object the install no longer renders")
		err = r.Delete(ctx, obj)
		if apierrors.IsForbidden(err) {
			// The delete grant names what this version renders (rbac_markers.go); a
			// refusal, say an older role mid-upgrade, must not wedge prune or deletion.
			log.Info("left in place: the operator may delete only names it renders; delete it by hand")
			continue
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// pruneCandidates are the objects of sc's kind in ns ("" for a cluster-scoped
// kind) that carry this install's instance label: each rendered name read, or,
// for the tenant AppProjects, listed in ns.
func (r *DotvirtReconciler) pruneCandidates(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, sc pruneScope, ns string) ([]runtime.Object, error) {
	list := sc.list()
	if len(sc.names) > 0 {
		gvk, err := apiutil.GVKForObject(list, r.Scheme)
		if err != nil {
			return nil, err
		}
		var found []runtime.Object
		for _, name := range sc.names {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk.GroupVersion().WithKind(strings.TrimSuffix(gvk.Kind, "List")))
			err = r.reader().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj)
			if meta.IsNoMatchError(err) {
				return nil, nil
			}
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if obj.GetLabels()[install.InstanceLabel] == dv.Name {
				found = append(found, obj)
			}
		}
		return found, nil
	}
	if err := r.reader().List(ctx, list, client.InNamespace(ns), client.MatchingLabels{install.InstanceLabel: dv.Name}); err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return meta.ExtractList(list)
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
