package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// syncTier is what reconcileArgo rendered for sync impersonation.
type syncTier struct {
	// accounts maps each live project's sync account to the namespaces its
	// project syncs into.
	accounts map[string][]string
	// platform says the platform tier, and so its sync account, is rendered.
	platform bool
	// sharedInUse says an Application still uses the shared tenant tier,
	// which names no sync account.
	sharedInUse bool
	// appProjects are the AppProjects the pass rendered: an Application in
	// any other is not dotvirt's.
	appProjects map[string]bool
}

// syncProbeAccount is no project's sync account (tenancy.SyncAccount names
// carry its prefix). Asking what it may do in a project namespace asks what
// that namespace grants every ServiceAccount of the ArgoCD namespace, so every
// project's sync account.
const syncProbeAccount = "dotvirt-sync-probe"

// accessProbeLimit bounds the SubjectAccessReviews in flight at once.
const accessProbeLimit = 8

// argoInstance names the ArgoCD resource the operator sets
// install.SyncImpersonationKey on: the GitOps operator's default instance on
// OpenShift, the argocd-operator's conventional one elsewhere. The operator
// may get and patch these two names only.
func (r *DotvirtReconciler) argoInstance() string {
	if r.Platform == platform.OpenShift {
		return "openshift-gitops"
	}
	return "argocd"
}

// reconcileSyncImpersonation turns Argo CD's sync impersonation on for the
// instance once spec.argocd.syncImpersonation asks and nothing would stop
// syncing for it, and off when it no longer asks. Best-effort like the
// webhooks: the operand serves either way, so a failure is recorded, never a
// halt. Once on it stays on while asked: a project whose grants go missing
// fails to sync rather than falling back to the controller's rights.
func (r *DotvirtReconciler) reconcileSyncImpersonation(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.syncImpersonation(ctx, dv, rc)
	r.setCondition(dv, dotvirtv1alpha1.ConditionSyncImpersonationReady, status, reason, msg)
	return nil, nil
}

func (r *DotvirtReconciler) syncImpersonation(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (metav1.ConditionStatus, string, string) {
	failed := func(err error) (metav1.ConditionStatus, string, string) {
		return metav1.ConditionFalse, "Error", err.Error()
	}
	inConfig, err := r.impersonationConfigured(ctx, rc.argoNS)
	if err != nil {
		return failed(err)
	}
	inst, owned, err := r.argoInstanceState(ctx, rc.argoNS)
	if err != nil {
		return failed(err)
	}
	where := fmt.Sprintf("ArgoCD %s/%s", rc.argoNS, r.argoInstance())
	opt := dv.Spec.ArgoCD.SyncImpersonation
	if !opt.Enabled {
		if owned {
			if err := r.apply(ctx, install.ArgoCDSyncImpersonation(inst.GetName(), inst.GetNamespace(), false)); err != nil {
				return failed(fmt.Errorf("remove %s from %s: %w", install.SyncImpersonationKey, where, err))
			}
			return metav1.ConditionUnknown, "Disabled", fmt.Sprintf("removed %s from %s; Applications sync as the ArgoCD controller's ServiceAccount again", install.SyncImpersonationKey, where)
		}
		if inConfig {
			return metav1.ConditionTrue, "Enabled", fmt.Sprintf("set outside dotvirt in %s/argocd-cm; dotvirt's Applications sync as their tiers' own ServiceAccounts", rc.argoNS)
		}
		v, err := r.syncVerdict(ctx, dv, rc, scopeOff)
		if err != nil {
			return failed(err)
		}
		if v.cloneAnywhere {
			return metav1.ConditionFalse, "CloneAnywhere", fmt.Sprintf("off, and Applications sync as %s, which may create datavolumes/source in every namespace: a DataVolume a tenant repo declares can clone a disk out of any namespace; spec.argocd.syncImpersonation.enabled confines each repo to its project's rights", rc.argoSA)
		}
		return metav1.ConditionUnknown, "Disabled", "off: Applications sync as the ArgoCD controller's ServiceAccount"
	}

	if inConfig || owned {
		v, err := r.syncVerdict(ctx, dv, rc, scopeOn)
		if err != nil {
			return failed(err)
		}
		if problems := slices.Concat(v.missing, v.leaks); len(problems) > 0 {
			return metav1.ConditionFalse, "Degraded", "on; " + strings.Join(problems, "; ")
		}
		return metav1.ConditionTrue, "Enabled", fmt.Sprintf("on (%s); each Application syncs as its tier's own ServiceAccount", where)
	}
	v, err := r.syncVerdict(ctx, dv, rc, scopeGate)
	if err != nil {
		return failed(err)
	}
	var blockers []string
	if rc.sync.sharedInUse {
		blockers = append(blockers, fmt.Sprintf("Applications still use the shared %s AppProject, which names no sync account", install.TenantsProjectName))
	}
	if len(v.foreign) > 0 && !opt.IgnoreForeignApplications {
		blockers = append(blockers, fmt.Sprintf("the instance also syncs Applications outside dotvirt's AppProjects (%s), which would stop syncing; set spec.argocd.syncImpersonation.ignoreForeignApplications to proceed", listSome(v.foreign)))
	}
	blockers = append(append(blockers, v.missing...), v.leaks...)
	if len(blockers) > 0 {
		return metav1.ConditionFalse, "NotReady", strings.Join(blockers, "; ")
	}
	if inst == nil {
		return metav1.ConditionFalse, "ManualStep", fmt.Sprintf("ready, but there is no %s to set it on: set %s: \"true\" in %s/argocd-cm", where, install.SyncImpersonationKey, rc.argoNS)
	}
	if err := r.apply(ctx, install.ArgoCDSyncImpersonation(inst.GetName(), inst.GetNamespace(), true)); err != nil {
		return failed(fmt.Errorf("set %s on %s: %w", install.SyncImpersonationKey, where, err))
	}
	return metav1.ConditionTrue, "Enabled", fmt.Sprintf("set %s on %s; each Application syncs as its tier's own ServiceAccount", install.SyncImpersonationKey, where)
}

// impersonationConfigured reports whether argocd-cm, what Argo reads, turns
// sync impersonation on.
func (r *DotvirtReconciler) impersonationConfigured(ctx context.Context, argoNS string) (bool, error) {
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: argoNS, Name: "argocd-cm"}, &cm)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s/argocd-cm: %w", argoNS, err)
	}
	return cm.Data[install.SyncImpersonationKey] == "true", nil
}

// argoInstanceState reads the ArgoCD resource the setting goes on, nil when
// there is none (an instance the argocd-operator does not run), and whether
// the operator's apply holds the setting there.
func (r *DotvirtReconciler) argoInstanceState(ctx context.Context, argoNS string) (*unstructured.Unstructured, bool, error) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(install.ArgoCDGVK)
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: argoNS, Name: r.argoInstance()}, inst)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read ArgoCD %s/%s: %w", argoNS, r.argoInstance(), err)
	}
	return inst, install.OwnsSyncImpersonation(inst), nil
}

// syncVerdict is what stands between the instance and sync impersonation.
type syncVerdict struct {
	// missing are grants a sync account lacks: its tier's Applications fail
	// to sync under impersonation.
	missing []string
	// leaks are grants reaching a project's sync account outside its own
	// namespaces.
	leaks []string
	// foreign are the Applications outside dotvirt's AppProjects, as ns/name.
	foreign []string
	// cloneAnywhere says the controller's own account may create
	// datavolumes/source cluster-wide.
	cloneAnywhere bool
}

// syncCache memoizes the verdict: it costs SubjectAccessReviews in proportion
// to the project namespaces plus a list of every Application, while passes
// come in bursts (a rollout's Deployment events). One verdict serves until its
// inputs or the CR's spec change, or driftResync/2 elapses; RBAC moves are not
// watched, so that timer, or a spec edit, is how a merged grant is seen.
type syncCache struct {
	syncMu      sync.Mutex
	syncKey     string
	syncAt      time.Time
	syncResults syncVerdict
}

// syncScope is what a verdict covers.
type syncScope int

const (
	// scopeOff: the controller's clone rights, all that matters while not asked.
	scopeOff syncScope = iota
	// scopeOn: the sync accounts' grants, all that matters once on.
	scopeOn
	// scopeGate: the grants and the foreign Applications, before turning it on.
	scopeGate
)

// syncVerdict evaluates, or reuses, scope's verdict for rc's tier.
func (r *DotvirtReconciler) syncVerdict(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, scope syncScope) (syncVerdict, error) {
	key := syncInputs(dv.Generation, rc, scope)
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if key == r.syncKey && r.clock().Sub(r.syncAt) < driftResync/2 {
		return r.syncResults, nil
	}
	v, err := r.evaluateSync(ctx, rc, scope)
	if err != nil {
		return v, err
	}
	r.syncKey, r.syncAt, r.syncResults = key, r.clock(), v
	return v, nil
}

func syncInputs(generation int64, rc *reconcileCtx, scope syncScope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n%s\n%s\n%d\n%v\n", generation, rc.argoNS, rc.argoSA, scope, rc.sync.platform)
	for _, account := range slices.Sorted(maps.Keys(rc.sync.accounts)) {
		fmt.Fprintf(&b, "%s=%s\n", account, strings.Join(rc.sync.accounts[account], ","))
	}
	fmt.Fprintf(&b, "%s\n", strings.Join(slices.Sorted(maps.Keys(rc.sync.appProjects)), ","))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// accessCheck is one SubjectAccessReview: account (a ServiceAccount of the
// ArgoCD namespace) doing attrs, and the problem a verdict other than want is.
type accessCheck struct {
	account string
	attrs   authzv1.ResourceAttributes
	want    bool
	problem string
	// set receives the verdict instead of a problem.
	set *bool
}

func createAttrs(group, resource, ns string) authzv1.ResourceAttributes {
	return authzv1.ResourceAttributes{Verb: "create", Group: group, Resource: resource, Namespace: ns}
}

// evaluateSync runs the checks. Per project: its sync account may create
// VirtualMachines and EgressFirewalls in each of its namespaces, and holds no
// cluster-wide VirtualMachine grant. Per project namespace: it grants nothing
// to every ServiceAccount of the ArgoCD namespace, so no other project's sync
// account. A grant naming one project's account in another's namespace is
// left to that namespace's own admins, who may grant anyone anything there.
// The platform account may create Namespaces, ClusterUserDefinedNetworks and
// RoleBindings, and bind the roles project grants name. That is 3 reviews per
// project namespace plus one per project and 5, bounded to accessProbeLimit in
// flight; before turning it on, plus one list of every Application. While not
// asked, the one review of the controller's clone rights.
func (r *DotvirtReconciler) evaluateSync(ctx context.Context, rc *reconcileCtx, scope syncScope) (syncVerdict, error) {
	var v syncVerdict
	var checks []accessCheck
	if scope == scopeOff {
		checks = []accessCheck{{
			account: rc.argoSA,
			attrs:   authzv1.ResourceAttributes{Verb: "create", Group: "cdi.kubevirt.io", Resource: "datavolumes", Subresource: "source"},
			set:     &v.cloneAnywhere,
		}}
	} else {
		checks = syncChecks(rc)
	}
	if scope == scopeGate {
		foreign, err := r.foreignApplications(ctx, rc.sync.appProjects)
		if err != nil {
			return v, err
		}
		v.foreign = foreign
	}
	verdicts := make([]bool, len(checks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(accessProbeLimit)
	for i, c := range checks {
		g.Go(func() error {
			ok, err := r.allowed(gctx, rc.argoNS, c.account, c.attrs)
			verdicts[i] = ok
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return v, fmt.Errorf("review sync account grants: %w", err)
	}
	for i, c := range checks {
		switch {
		case c.set != nil:
			*c.set = verdicts[i]
		case verdicts[i] != c.want && c.want:
			v.missing = append(v.missing, c.problem)
		case verdicts[i] != c.want:
			v.leaks = append(v.leaks, c.problem)
		}
	}
	return v, nil
}

// syncChecks are the grant checks for rc's tier, in a stable order.
func syncChecks(rc *reconcileCtx) []accessCheck {
	var checks []accessCheck
	projectNS := map[string]bool{}
	for _, account := range slices.Sorted(maps.Keys(rc.sync.accounts)) {
		project := strings.TrimPrefix(account, tenancy.AppProjectPrefix)
		grant := fmt.Sprintf("the platform repo grants it none there (dotvirt stages them: POST /api/projects/%s/sync-access)", project)
		for _, ns := range rc.sync.accounts[account] {
			projectNS[ns] = true
			checks = append(checks,
				accessCheck{account: account, attrs: createAttrs("kubevirt.io", "virtualmachines", ns), want: true,
					problem: fmt.Sprintf("project %s cannot sync VirtualMachines into %s: %s", project, ns, grant)},
				accessCheck{account: account, attrs: createAttrs("k8s.ovn.org", "egressfirewalls", ns), want: true,
					problem: fmt.Sprintf("project %s cannot sync EgressFirewalls into %s: %s", project, ns, grant)},
			)
		}
		checks = append(checks, accessCheck{account: account, attrs: createAttrs("kubevirt.io", "virtualmachines", ""),
			problem: fmt.Sprintf("project %s's sync account %s may create VirtualMachines in every namespace", project, account)})
	}
	for _, ns := range slices.Sorted(maps.Keys(projectNS)) {
		checks = append(checks, accessCheck{account: syncProbeAccount, attrs: createAttrs("kubevirt.io", "virtualmachines", ns),
			problem: fmt.Sprintf("namespace %s lets every ServiceAccount of %s create VirtualMachines, other projects' sync accounts included", ns, rc.argoNS)})
	}
	if rc.sync.platform {
		missing := func(what string) string {
			return fmt.Sprintf("the platform sync account %s cannot %s (its operand ClusterRoles are not bound)", tenancy.PlatformSyncAccount, what)
		}
		for _, c := range []struct {
			attrs authzv1.ResourceAttributes
			what  string
		}{
			{createAttrs("", "namespaces", ""), "create Namespaces"},
			{createAttrs("k8s.ovn.org", "clusteruserdefinednetworks", ""), "create ClusterUserDefinedNetworks"},
			{createAttrs("rbac.authorization.k8s.io", "rolebindings", ""), "create RoleBindings"},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "admin"}, "bind the admin role"},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: tenancy.TenantEgressRole}, "bind " + tenancy.TenantEgressRole},
		} {
			checks = append(checks, accessCheck{account: tenancy.PlatformSyncAccount, attrs: c.attrs, want: true, problem: missing(c.what)})
		}
	}
	return checks
}

// allowed asks whether account, a ServiceAccount of saNS, may do attrs. Its
// groups ride along, as the apiserver adds them to an impersonated account.
func (r *DotvirtReconciler) allowed(ctx context.Context, saNS, account string, attrs authzv1.ResourceAttributes) (bool, error) {
	sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
		User:               "system:serviceaccount:" + saNS + ":" + account,
		Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + saNS, "system:authenticated"},
		ResourceAttributes: &attrs,
	}}
	if err := r.Create(ctx, sar); err != nil {
		return false, err
	}
	return sar.Status.Allowed, nil
}

// foreignApplications are the Applications, in any namespace, whose
// AppProject the pass did not render, as ns/name in name order.
func (r *DotvirtReconciler) foreignApplications(ctx context.Context, appProjects map[string]bool) ([]string, error) {
	apps := unstructuredList(install.ArgoGVK("Application"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, apps); err != nil {
		return nil, fmt.Errorf("list Applications: %w", err)
	}
	var out []string
	for _, a := range apps.Items {
		if p, _, _ := unstructured.NestedString(a.Object, "spec", "project"); !appProjects[p] {
			out = append(out, a.GetNamespace()+"/"+a.GetName())
		}
	}
	slices.Sort(out)
	return out, nil
}

// listSome names up to three of items, and how many there are beyond.
func listSome(items []string) string {
	if len(items) <= 3 {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:3], ", "), len(items)-3)
}
