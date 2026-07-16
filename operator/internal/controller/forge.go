package controller

import (
	"context"
	"net/url"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reconcileForge stands up + bootstraps the managed Forgejo (opt-in, eval-grade)
// before anything that needs the forge credential. Once dotvirt-forge exists, the
// rest of the install can't tell it from a BYO forge — for which this phase is a
// no-op. Requeues while Forgejo is still coming up.
func (r *DotvirtReconciler) reconcileForge(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if !dv.Spec.Forge.Managed {
		return nil, nil
	}
	if err := r.applyForgejo(ctx, dv); err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "ApplyFailed", err)
	}
	if r.DryRun {
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionUnknown, "DryRun", "skipped Forgejo bootstrap in dry-run")
		return nil, nil
	}
	ready, err := r.bootstrapForgejo(ctx, dv)
	if err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "Error", err)
	}
	if !ready {
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionFalse, "Progressing", "waiting for Forgejo to come up")
		dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
		if uerr := r.Status().Update(ctx, dv); uerr != nil {
			return nil, uerr
		}
		return &ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionTrue, "Ready", "managed Forgejo bootstrapped")
	return nil, nil
}

// reconcilePlatformRepo ensures the platform repo exists — the imperative
// bootstrap pure declarative installers can't do. Skipped in dry-run (a real forge
// mutation server-side dry-run can't model). A bootstrap failure is recorded on
// the condition but doesn't halt the pipeline.
func (r *DotvirtReconciler) reconcilePlatformRepo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	switch {
	case dv.Spec.Forge.PlatformRepo == "":
		// No platform tier configured; nothing to bootstrap.
	case r.DryRun:
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionUnknown, "DryRun", "skipped platform-repo bootstrap in dry-run")
	default:
		if err := r.ensurePlatformRepo(ctx, dv); err != nil {
			r.setCondition(dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionFalse, "Error", err.Error())
		} else {
			r.setCondition(dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionTrue, "Ready", "platform repo present")
		}
	}
	return nil, nil
}

// ensurePlatformRepo creates the platform repo on the forge if absent — the
// install-time step a Helm/Kustomize/ArgoCD-app installer structurally can't do
// (it's a forge API call, not a kubectl apply).
func (r *DotvirtReconciler) ensurePlatformRepo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	client, err := r.forgeClient(ctx, dv)
	if err != nil {
		return err
	}
	created, err := client.EnsureRepo()
	if err != nil {
		return err
	}
	if created {
		logf.FromContext(ctx).Info("created platform repo", "repo", dv.Spec.Forge.PlatformRepo)
	}
	return nil
}

// forgejoExposure exposes the managed Forgejo on the host derived from
// spec.forge.url, so its UI + PRs are reviewable off-cluster. nil when no external
// forge URL is set (internal-only).
func (r *DotvirtReconciler) forgejoExposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	host := install.ForgejoHost(dv)
	if host == "" {
		return nil
	}
	return r.exposureFor(dv, install.ForgejoServiceName, install.ForgejoHTTPPort, host)
}

// applyForgejo renders the managed Forgejo workload (SA, Deployment with the verified
// bootstrap initContainer, Service) and the data PVC. The rootless image runs under
// dotvirt's standard non-root securityContext, so no SCC binding is needed. Everything
// but the PVC is owner-referenced for auto-cleanup; the PVC is orphaned so the git
// data survives uninstall.
func (r *DotvirtReconciler) applyForgejo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	if !r.DryRun {
		if err := r.ensureSecret(ctx, dv, install.ForgejoAdminSecret, "password"); err != nil {
			return err
		}
	}
	// PVC first (orphan: no ownerRef, so the git data survives uninstall) — applied
	// before the Deployment that mounts it, so a retry of any later resource can't
	// strand the pod Pending on a missing volume.
	if err := install.Apply(ctx, r.Client, install.ForgejoPVC(dv), r.DryRun); err != nil {
		return err
	}
	// The webhook allowlist needs ArgoCD's externally-visible host (see forgejoEnv),
	// resolved the same way webhook registration does. Empty (no Argo URL yet)
	// renders the baseline allowlist; the reconcile that registers the webhook then
	// re-renders the Deployment with the host in place.
	argoNS, _ := r.argoTarget(dv)
	argoHost := ""
	if u, err := url.Parse(r.argoServerURL(ctx, dv, argoNS)); err == nil {
		argoHost = u.Hostname()
	}
	owned := []client.Object{
		install.ForgejoServiceAccount(dv),
		install.ForgejoService(dv),
		// fsGroup only on vanilla K8s; OpenShift's restricted-v2 injects its own.
		install.ForgejoDeployment(dv, r.Platform != platform.OpenShift, argoHost),
	}
	if exp := r.forgejoExposure(dv); exp != nil {
		owned = append(owned, exp)
	}
	for _, o := range owned {
		if err := controllerutil.SetControllerReference(dv, o, r.Scheme); err != nil {
			return err
		}
		if err := install.Apply(ctx, r.Client, o, r.DryRun); err != nil {
			return err
		}
	}
	return nil
}

// bootstrapForgejo mints the scoped token + ensures the owner org once the managed
// Forgejo is up, then writes the dotvirt-forge secret. Idempotent: a no-op once
// dotvirt-forge exists; returns ready=false (caller requeues) while Forgejo isn't up.
func (r *DotvirtReconciler) bootstrapForgejo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (bool, error) {
	credName := install.ForgeSecretName(dv)
	// Already bootstrapped AND the stored token still works? Trusting mere existence
	// leaves a dead token in place forever after a Forgejo data reset or out-of-band
	// rotation (Argo + the app then fail auth). Validate it; only short-circuit when
	// it's genuinely valid, else fall through to re-mint. A forge blip surfaces as
	// err (requeue) rather than a needless re-mint.
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: credName}, &existing); err == nil {
		valid, err := forge.NewFactory(dv.Spec.Forge.URL, "unused", dv.Spec.Forge.InsecureTLS).
			ValidateToken(string(existing.Data["token"]))
		if err != nil {
			return false, err
		}
		if valid {
			return true, nil
		}
		logf.FromContext(ctx).Info("stored forge token rejected — re-minting", "secret", credName)
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	// Forgejo up yet?
	var d appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoServiceName}, &d); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if d.Status.AvailableReplicas < 1 {
		return false, nil
	}
	// Mint the scoped token via basic auth as the bootstrapped admin.
	var admin corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoAdminSecret}, &admin); err != nil {
		return false, err
	}
	url := dv.Spec.Forge.URL
	// read:user lets the token read /api/v1/user — which is what ValidateToken probes.
	// Under Forgejo's granular token scopes a write:* token can't reach /api/v1/user, so
	// without this the freshly minted token validates as "rejected" and the operator
	// re-mints every reconcile forever. write:organization/write:repository cover the org
	// + repo webhook and PR operations.
	token, err := forge.NewFactory(url, "unused", dv.Spec.Forge.InsecureTLS).
		MintToken(install.ForgejoBotUser, string(admin.Data["password"]), "dotvirt-operator", []string{"read:user", "write:organization", "write:repository"})
	if err != nil {
		return false, err
	}
	if err := r.writeForgeSecret(ctx, dv, credName, url, install.ForgejoBotUser, token); err != nil {
		return false, err
	}
	// Ensure the owner org exists (repos live under it for the org-level webhook).
	if dv.Spec.Forge.PlatformRepo != "" {
		if c := forge.NewFactory(url, token, dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo); c != nil {
			if err := c.EnsureOrg(); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// writeForgeSecret upserts the dotvirt-forge credential from the managed Forgejo's
// minted token, so the rest of the install treats it like a BYO forge. Upsert (not
// create-once) so a re-mint of a rejected token overwrites the stale value in place.
func (r *DotvirtReconciler) writeForgeSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, url, username, token string) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, s, func() error {
		s.Labels = install.Labels(dv.Name)
		s.StringData = map[string]string{"url": url, "username": username, "token": token}
		return controllerutil.SetControllerReference(dv, s, r.Scheme)
	})
	return err
}
