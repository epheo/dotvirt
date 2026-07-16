package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// finalize handles deletion: clean up the label-tracked cluster/ArgoCD-namespace
// resources (the ones not owner-referenceable), then drop the finalizer.
func (r *DotvirtReconciler) finalize(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	if r.DryRun || !controllerutil.ContainsFinalizer(dv, dotvirtFinalizer) {
		return nil
	}
	if err := r.cleanupClusterResources(ctx, dv); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(dv, dotvirtFinalizer)
	return r.Update(ctx, dv)
}

// cleanupClusterResources deletes the label-selected cluster-scoped + ArgoCD-
// namespace resources provisioned for this instance (the ones a namespaced CR
// can't ownerRef). Not-found and missing-CRD are tolerated so cleanup is idempotent.
func (r *DotvirtReconciler) cleanupClusterResources(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	sel := client.MatchingLabels{"dotvirt.io/instance": dv.Name}
	// Only the ClusterRoleBindings are ours to delete — the operand ClusterRoles are
	// static, OLM/kustomize-owned (config/rbac/operand_roles.yaml) and shared.
	if err := r.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, sel); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	argoNS, _ := r.argoTarget(dv)
	for _, kind := range []string{"AppProject", "Application", "ApplicationSet"} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(install.ArgoGVK(kind))
		if err := r.DeleteAllOf(ctx, u, client.InNamespace(argoNS), sel); err != nil &&
			!apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
	}
	// The ArgoCD-namespace plugin ConfigMap + mirrored token Secret.
	for _, o := range []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}} {
		if err := r.DeleteAllOf(ctx, o, client.InNamespace(argoNS), sel); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
