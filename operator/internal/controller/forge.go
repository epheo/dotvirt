package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reconcileForge stands up + bootstraps the managed Forgejo (opt-in, eval-grade)
// before anything that needs the forge credential. Once dotvirt-forge exists, the
// rest of the install can't tell it from a BYO forge. For a wholly unconfigured forge
// it records NotConfigured (push-only) and returns; for a BYO forge it's a no-op.
// Requeues while Forgejo is coming up or its router host isn't assigned yet.
func (r *DotvirtReconciler) reconcileForge(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if !dv.Spec.Forge.Managed {
		// Derived status, so a switch off managed retires the stale bootstrap hint too.
		dv.Status.ForgeURL = dv.Spec.Forge.URL
		dv.Status.ForgeAdminHint = ""
		if !install.ForgeConfigured(dv) {
			if dv.Spec.Forge.URL != "" {
				// A URL alone is not a forge: the app reads URL and token from the
				// credentials secret. A generic reason beside a plainly set url is a lie.
				r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionFalse, "CredentialsRequired",
					"spec.forge.url is set but spec.forge.credentialsSecret is not; name a Secret with url/username/token keys (or set forge.managed)")
			} else {
				r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionFalse, "NotConfigured",
					"no forge configured; propose pushes a branch but opens no pull request")
			}
		}
		return nil, nil
	}
	// Trust anchors before the Deployment that mounts them (optional mounts, so
	// ordering is comfort not correctness).
	r.ensureTrustAnchors(ctx, dv)
	// Apply the base workload (incl. the exposure) first: on OpenShift with no explicit
	// URL that Route is hostless, so the router assigns a host we read back and fill into
	// the effective spec, before rendering the Deployment whose ROOT_URL needs it.
	if err := r.applyForgejoBase(ctx, dv); err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "ApplyFailed", err)
	}
	// Set here, not at Ready: the admin secret exists once the base is applied, so the
	// hint also shows while provisioning.
	dv.Status.ForgeAdminHint = forgeAdminHint(dv.Namespace)
	forgeURL, res, err := r.resolveForgeURL(ctx, dv)
	if err != nil || res != nil {
		return res, err
	}
	applyEffectiveForgeSpec(dv, forgeURL)
	dv.Status.ForgeURL = forgeURL
	if err := r.applyForgejoDeployment(ctx, dv); err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "ApplyFailed", err)
	}
	if r.DryRun {
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionForgeReady, "Forgejo bootstrap")
		return nil, nil
	}
	ready, err := r.bootstrapForgejo(ctx, dv)
	if errors.Is(err, forge.ErrUnauthorized) {
		// Forgejo rejected the operator's admin credential. The bootstrap initContainer
		// reconciles the DB password to the admin secret on every start, so this should
		// self-clear on the next pod restart; surface a legible reason (not a raw
		// "mint token: 401") and requeue rather than hot-looping on the error.
		return r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "AdminCredentialRejected",
			fmt.Sprintf("Forgejo rejected the operator's admin credential. Restart the managed forge to reconcile "+
				"its admin password: oc -n %s rollout restart deployment/%s", dv.Namespace, install.ForgejoServiceName),
			dotvirtv1alpha1.PhaseProvisioning, time.Minute)
	}
	if err != nil {
		return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "Error", err)
	}
	if !ready {
		return r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "Progressing",
			"waiting for Forgejo to come up", dotvirtv1alpha1.PhaseProvisioning, 15*time.Second)
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionTrue, "Ready", "managed Forgejo bootstrapped")
	return nil, nil
}

// managedForgeAPIBase is the base URL the operator uses for the managed Forgejo's
// admin API: its in-cluster Service (plain HTTP), reachable even when the external
// Route isn't routable from the operator pod. A test seam overrides it.
func (r *DotvirtReconciler) managedForgeAPIBase(dv *dotvirtv1alpha1.Dotvirt) string {
	if r.forgeAPIBase != nil {
		return r.forgeAPIBase(dv)
	}
	return install.ForgejoServiceURL(dv)
}

// resolveForgeURL determines the effective external forge URL for a managed install:
// the explicit spec.forge.url when set, else (OpenShift) the host the router assigned
// to the hostless Forgejo Route applyForgejoBase just created. It returns a requeue
// while the host isn't assigned yet, and halts on vanilla Kubernetes with an actionable
// reason: there is no router to name the forge, so the user must set spec.forge.url.
func (r *DotvirtReconciler) resolveForgeURL(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (string, *ctrl.Result, error) {
	if dv.Spec.Forge.URL != "" {
		return strings.TrimRight(dv.Spec.Forge.URL, "/"), nil, nil
	}
	if r.DryRun {
		// Nothing is persisted, so no Route exists to read; the in-cluster Service URL lets
		// the render validate.
		return install.ForgejoServiceURL(dv), nil, nil
	}
	// A router-assigned hostname only exists when forgejoExposure creates a Route, which
	// it does not on vanilla Kubernetes NOR when spec.ingress.type names another exposure.
	// Reading routeHost in those cases would wait for a Route nothing creates, requeueing
	// forever, so halt on the same predicate the exposure uses.
	if r.Platform != platform.OpenShift || r.resolveExposureType(dv) != "route" {
		res, err := r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "ForgeURLRequired",
			"set spec.forge.url: the operator can only discover a managed forge's hostname from an OpenShift Route, which this install does not create",
			dotvirtv1alpha1.PhaseProvisioning, 0)
		return "", res, err
	}
	host := r.routeHost(ctx, dv.Namespace, install.ForgejoServiceName)
	if host == "" {
		res, err := r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeReady, "Progressing",
			"waiting for the router to assign the forge hostname", dotvirtv1alpha1.PhaseProvisioning, 5*time.Second)
		return "", res, err
	}
	return "https://" + host, nil, nil
}

// applyEffectiveForgeSpec fills the resolved forge URL (and a default platform repo)
// into the IN-MEMORY spec, so every later phase and the rendered workload consume it
// unchanged. Never persisted: the reconcile writes only /status after the finalizer add,
// so this mutation stays in-process (a regression test pins that the stored spec keeps
// its empty url).
func applyEffectiveForgeSpec(dv *dotvirtv1alpha1.Dotvirt, forgeURL string) {
	dv.Spec.Forge.URL = forgeURL
	if dv.Spec.Forge.PlatformRepo == "" {
		dv.Spec.Forge.PlatformRepo = strings.TrimRight(forgeURL, "/") + "/dotvirt/platform.git"
	}
}

func forgeAdminHint(ns string) string {
	return fmt.Sprintf("Forgejo admin user %q; reveal the password: "+
		"oc extract secret/%s -n %s --keys=password --to=-",
		install.ForgejoBotUser, install.ForgejoAdminSecret, ns)
}

// reconcilePlatformRepo ensures the platform repo exists — the imperative
// bootstrap pure declarative installers can't do. Skipped in dry-run (a real forge
// mutation server-side dry-run can't model). Runs LAST in the pipeline: nothing
// below it depends on the repo existing, so its retry never delays another phase.
// A failure keeps Available and Phase untouched (the operand still serves; the
// condition plus the app's recover-repo UI carry the signal) but requeues, since
// no watch event fires when the forge comes back.
func (r *DotvirtReconciler) reconcilePlatformRepo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	switch {
	case dv.Spec.Forge.PlatformRepo == "":
		// No platform tier configured; nothing to bootstrap.
	case r.DryRun:
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionForgeRepoReady, "platform-repo bootstrap")
	default:
		if err := r.ensurePlatformRepo(ctx, dv); err != nil {
			// phase "": the operand still serves, so the retry must not regress
			// a Ready install.
			return r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionForgeRepoReady, "Error",
				err.Error(), "", time.Minute)
		}
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionTrue, "Ready", "platform repo present")
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

// forgejoExposure exposes the managed Forgejo off-cluster so its UI + PRs are reviewable.
// With an explicit spec.forge.url it uses that host; with no URL on a Route platform,
// exposureFor returns a HOSTLESS Route for the router to name (resolveForgeURL reads
// that host back). nil only where there's nothing to expose: no URL on vanilla
// Kubernetes (Ingress needs an explicit host; that path halts with ForgeURLRequired).
func (r *DotvirtReconciler) forgejoExposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	return r.exposureFor(dv, install.ForgejoServiceName, install.ForgejoHTTPPort, install.ForgejoHost(dv))
}

// applyForgejoBase renders everything the managed Forgejo needs BEFORE its URL is known:
// the admin secret, the data PVC, the ServiceAccount, the Service, and the exposure (a
// hostless Route on OpenShift when no URL is set). The Deployment is applied separately,
// after resolveForgeURL, because its ROOT_URL depends on the resolved host. The rootless
// image runs under dotvirt's non-root securityContext (no SCC binding); the PVC is
// orphaned (no ownerRef) so the git data survives uninstall.
func (r *DotvirtReconciler) applyForgejoBase(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	if !r.DryRun {
		if err := r.ensureSecret(ctx, dv, install.ForgejoAdminSecret, "password"); err != nil {
			return err
		}
	}
	// PVC first (orphan: no ownerRef, so the git data survives uninstall) — applied
	// before the Deployment that mounts it, so a retry of any later resource can't
	// strand the pod Pending on a missing volume.
	if err := r.apply(ctx, install.ForgejoPVC(dv)); err != nil {
		return err
	}
	owned := []client.Object{
		install.ForgejoServiceAccount(dv),
		install.ForgejoService(dv),
	}
	if exp := r.forgejoExposure(dv); exp != nil {
		owned = append(owned, exp)
	}
	return r.applyOwned(ctx, dv, owned...)
}

// applyForgejoDeployment renders the Forgejo Deployment once the effective URL is filled,
// so its ROOT_URL is the browser-facing host. Kept separate from applyForgejoBase for
// that ordering.
func (r *DotvirtReconciler) applyForgejoDeployment(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	// The webhook allowlist needs ArgoCD's externally-visible host (see forgejoEnv),
	// resolved the same way webhook registration does. Empty (no Argo URL yet) renders
	// the baseline allowlist; the reconcile that registers the webhook then re-renders
	// the Deployment with the host in place.
	argoNS, _ := r.argoTarget(dv)
	argoHost := ""
	if u, err := url.Parse(r.argoServerURL(ctx, dv, argoNS)); err == nil {
		argoHost = u.Hostname()
	}
	// fsGroup only on vanilla K8s; OpenShift's restricted-v2 injects its own.
	return r.applyOwned(ctx, dv, install.ForgejoDeployment(dv, r.Platform != platform.OpenShift, argoHost, r.adminSecretHash(ctx, dv)))
}

// adminSecretHash rides the pod template so a rotated secret rolls the pod and
// the initContainer converges the DB password with no human runbook. Empty in
// dry-run and on read failure; the 401 path stays the legible fallback.
func (r *DotvirtReconciler) adminSecretHash(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) string {
	if r.DryRun {
		return ""
	}
	s, err := r.secret(ctx, dv.Namespace, install.ForgejoAdminSecret)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(s.Data["password"])
	return hex.EncodeToString(sum[:8])
}

// bootstrapForgejo mints the scoped token + ensures the owner org once the managed
// Forgejo is up, then writes the dotvirt-forge secret. Idempotent: a no-op once
// dotvirt-forge exists; returns ready=false (caller requeues) while Forgejo isn't up.
func (r *DotvirtReconciler) bootstrapForgejo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (bool, error) {
	credName := install.ForgeSecretName(dv)
	// The operator reaches the managed Forgejo over its in-cluster Service, not the
	// external Route: the Route may be unroutable from the operator pod (hairpin/egress),
	// and during the placeholder-URL wedge it doesn't resolve at all. The secret's url
	// stays the EXTERNAL URL (what the app, Argo, and browsers use). Every factory
	// below passes insecureTLS=false because this base is plain HTTP; TLS (and so
	// spec.forge.insecureTLS) never applies to the bootstrap path.
	apiBase := r.managedForgeAPIBase(dv)
	// Already bootstrapped AND the stored token still works? Trusting mere existence
	// leaves a dead token in place forever after a Forgejo data reset or out-of-band
	// rotation (Argo + the app then fail auth). Validate it; only short-circuit when
	// it's genuinely valid, else fall through to re-mint. A forge blip surfaces as
	// err (requeue) rather than a needless re-mint.
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: credName}, &existing); err == nil {
		valid, err := forge.NewFactory(apiBase, "unused", false).
			ValidateToken(string(existing.Data["token"]))
		if err != nil {
			return false, err
		}
		if valid {
			// The token still authenticates, but spec.forge.url may have changed since it was
			// minted (the secret's url/username are written only at mint time). Upsert them so
			// the app + Argo repo creds track the external URL without a needless re-mint.
			if string(existing.Data["url"]) != dv.Spec.Forge.URL || string(existing.Data["username"]) != install.ForgejoBotUser {
				if err := r.writeForgeSecret(ctx, dv, credName, dv.Spec.Forge.URL, install.ForgejoBotUser, string(existing.Data["token"])); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		logf.FromContext(ctx).Info("stored forge token rejected — re-minting", "secret", credName)
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	// Forgejo up yet? Not-found reads as not-ready, not an error: the Deployment
	// was applied earlier this reconcile, and the cache can lag the apply.
	var d appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoServiceName}, &d); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if d.Status.AvailableReplicas < 1 {
		return false, nil
	}
	// Mint the scoped token via basic auth as the bootstrapped admin.
	admin, err := r.secret(ctx, dv.Namespace, install.ForgejoAdminSecret)
	if err != nil {
		return false, err
	}
	// read:user lets the token read /api/v1/user — which is what ValidateToken probes.
	// Under Forgejo's granular token scopes a write:* token can't reach /api/v1/user, so
	// without this the freshly minted token validates as "rejected" and the operator
	// re-mints every reconcile forever. write:organization/write:repository cover the org
	// + repo webhook and PR operations.
	token, err := forge.NewFactory(apiBase, "unused", false).
		MintToken(install.ForgejoBotUser, string(admin.Data["password"]), "dotvirt-operator", []string{"read:user", "write:organization", "write:repository"})
	if err != nil {
		return false, err
	}
	if err := r.writeForgeSecret(ctx, dv, credName, dv.Spec.Forge.URL, install.ForgejoBotUser, token); err != nil {
		return false, err
	}
	// Ensure the owner org exists (repos live under it for the org-level webhook).
	if dv.Spec.Forge.PlatformRepo != "" {
		if c := forge.NewFactory(apiBase, token, false).For(dv.Spec.Forge.PlatformRepo); c != nil {
			if err := c.EnsureOrg(); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// writeForgeSecret upserts the dotvirt-forge credential from the managed Forgejo's
// minted token, so the rest of the install treats it like a BYO forge. Applied (not
// create-once) so a re-mint of a rejected token overwrites the stale value in place.
func (r *DotvirtReconciler) writeForgeSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, url, username, token string) error {
	s := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace, Labels: install.Labels(dv.Name)},
		// Data (not StringData): all three keys are rewritten every time, so no stale key
		// lingers, and the value is readable without the API server's StringData merge.
		Data: map[string][]byte{"url": []byte(url), "username": []byte(username), "token": []byte(token)},
	}
	return r.applyOwned(ctx, dv, s)
}
