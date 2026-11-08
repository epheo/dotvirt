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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	// appNamespaces are the namespace patterns the instance reads
	// Applications from (instanceAppNamespaces).
	appNamespaces []string
}

// syncProbeAccount is no project's sync account (tenancy.SyncAccount names
// carry its prefix). Asking what it may do asks what is granted to every
// ServiceAccount of the ArgoCD namespace, so to every project's sync account.
const syncProbeAccount = "dotvirt-sync-probe"

// accessProbeLimit bounds the API calls one verdict has in flight at once.
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
	target := rc.argoNS + "/" + r.argoInstance()
	// The setting stays with the instance it was set on: one spec.argocd no
	// longer names gets it back off.
	if prev := dv.Status.SyncImpersonationInstance; prev != "" && prev != target {
		if err := r.releaseSyncImpersonation(ctx, prev); err != nil {
			return failed(err)
		}
		dv.Status.SyncImpersonationInstance = ""
	}
	inConfig, err := r.impersonationConfigured(ctx, rc.argoNS)
	if err != nil {
		return failed(err)
	}
	inst, owned, err := r.argoInstanceState(ctx, target)
	if err != nil {
		return failed(err)
	}
	if owned {
		dv.Status.SyncImpersonationInstance = target
	}
	where := "ArgoCD " + target
	opt := dv.Spec.ArgoCD.SyncImpersonation
	if !opt.Enabled {
		if owned {
			if err := r.applyShared(ctx, install.ArgoCDSyncImpersonation(inst.GetName(), inst.GetNamespace(), false)); err != nil {
				return failed(fmt.Errorf("remove %s from %s: %w", install.SyncImpersonationKey, where, err))
			}
			dv.Status.SyncImpersonationInstance = ""
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
			return metav1.ConditionFalse, "CloneAnywhere", "off, and " + cloneWarning(rc)
		}
		return metav1.ConditionUnknown, "Disabled", "off: Applications sync as the ArgoCD controller's ServiceAccount"
	}

	if rc.sync.appNamespaces, err = r.instanceAppNamespaces(ctx, rc.argoNS, inst); err != nil {
		return failed(err)
	}
	v, err := r.syncVerdict(ctx, dv, rc, scopeFull)
	if err != nil {
		return failed(err)
	}
	if inConfig {
		if problems := slices.Concat(v.missing, v.leaks, foreignProblem(v.foreign)); len(problems) > 0 {
			return metav1.ConditionFalse, "Degraded", "on; " + strings.Join(problems, "; ")
		}
		return metav1.ConditionTrue, "Enabled", fmt.Sprintf("on (%s); each Application syncs as its tier's own ServiceAccount", where)
	}
	// Until argocd-cm carries the setting, the controller's account is still
	// the one syncing.
	meanwhile := ""
	if v.cloneAnywhere {
		meanwhile = "; meanwhile " + cloneWarning(rc)
	}
	pending := fmt.Sprintf("set %s on %s; waiting for %s/argocd-cm to carry it%s", install.SyncImpersonationKey, where, rc.argoNS, meanwhile)
	if owned {
		return metav1.ConditionFalse, "Pending", pending
	}
	var blockers []string
	if rc.sync.sharedInUse {
		blockers = append(blockers, fmt.Sprintf("Applications still use the shared %s AppProject, which names no sync account", install.TenantsProjectName))
	}
	if !opt.IgnoreForeignApplications {
		blockers = append(blockers, foreignProblem(v.foreign)...)
	}
	blockers = append(append(blockers, v.missing...), v.leaks...)
	if len(blockers) > 0 {
		return metav1.ConditionFalse, "NotReady", strings.Join(blockers, "; ") + meanwhile
	}
	if inst == nil {
		return metav1.ConditionFalse, "ManualStep", fmt.Sprintf("ready, but there is no %s to set it on: set %s: \"true\" in %s/argocd-cm%s", where, install.SyncImpersonationKey, rc.argoNS, meanwhile)
	}
	if err := r.applyShared(ctx, install.ArgoCDSyncImpersonation(inst.GetName(), inst.GetNamespace(), true)); apierrors.IsConflict(err) {
		return metav1.ConditionFalse, "Conflict", fmt.Sprintf("%s on %s is held by %s; dotvirt never takes it: set it to \"true\" there, or release it",
			install.SyncImpersonationKey, where, strings.Join(install.SyncImpersonationManagers(inst), ", "))
	} else if err != nil {
		return failed(fmt.Errorf("set %s on %s: %w", install.SyncImpersonationKey, where, err))
	}
	dv.Status.SyncImpersonationInstance = target
	return metav1.ConditionFalse, "Pending", pending
}

func cloneWarning(rc *reconcileCtx) string {
	return fmt.Sprintf("Applications sync as %s, which may create datavolumes/source in every namespace: a DataVolume a tenant repo declares can clone a disk out of any namespace; spec.argocd.syncImpersonation.enabled confines each repo to its project's rights", rc.argoSA)
}

func foreignProblem(foreign []string) []string {
	if len(foreign) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("Applications outside dotvirt's AppProjects whose AppProject maps no ServiceAccount for them stop syncing (%s); map one in its destinationServiceAccounts, the controller's own account to keep its rights, or set spec.argocd.syncImpersonation.ignoreForeignApplications",
		listSome(foreign))}
}

// releaseSyncImpersonation removes the setting from the ArgoCD resource
// instance (namespace/name) if the operator's apply holds it there. Nothing
// to do when the resource or its kind is gone.
func (r *DotvirtReconciler) releaseSyncImpersonation(ctx context.Context, instance string) error {
	inst, owned, err := r.argoInstanceState(ctx, instance)
	if err != nil || !owned {
		return err
	}
	if err := r.applyShared(ctx, install.ArgoCDSyncImpersonation(inst.GetName(), inst.GetNamespace(), false)); err != nil {
		return fmt.Errorf("remove %s from ArgoCD %s: %w", install.SyncImpersonationKey, instance, err)
	}
	return nil
}

// applyShared applies the operator's one field on the shared ArgoCD resource:
// never forced, and outside the render, which prune must never act on.
func (r *DotvirtReconciler) applyShared(ctx context.Context, obj client.Object) error {
	return install.ApplyShared(ctx, r.Client, obj, r.DryRun)
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

// argoInstanceState reads the ArgoCD resource instance (namespace/name), nil
// when there is none (an instance the argocd-operator does not run), and
// whether the operator's apply holds the setting there.
func (r *DotvirtReconciler) argoInstanceState(ctx context.Context, instance string) (*unstructured.Unstructured, bool, error) {
	ns, name, _ := strings.Cut(instance, "/")
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(install.ArgoCDGVK)
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, inst)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read ArgoCD %s: %w", instance, err)
	}
	return inst, install.OwnsSyncImpersonation(inst), nil
}

// instanceAppNamespaces are the namespace patterns the instance reads
// Applications from: argoNS, and its Applications-in-any-namespace list, from
// the ArgoCD resource's sourceNamespaces or, without one, argocd-cmd-params-cm.
// Applications elsewhere are another instance's, which the setting does not
// reach.
func (r *DotvirtReconciler) instanceAppNamespaces(ctx context.Context, argoNS string, inst *unstructured.Unstructured) ([]string, error) {
	out := []string{argoNS}
	if inst != nil {
		more, _, _ := unstructured.NestedStringSlice(inst.Object, "spec", "sourceNamespaces")
		return append(out, more...), nil
	}
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: argoNS, Name: "argocd-cmd-params-cm"}, &cm)
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s/argocd-cmd-params-cm: %w", argoNS, err)
	}
	for _, ns := range strings.Split(cm.Data["application.namespaces"], ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out, nil
}

// syncVerdict is what stands between the instance and sync impersonation.
type syncVerdict struct {
	// missing are grants an account lacks: its tier's Applications fail to
	// sync under impersonation.
	missing []string
	// leaks are grants reaching a project's sync account outside its own
	// namespaces.
	leaks []string
	// foreign are the instance's Applications, as ns/name, outside dotvirt's
	// AppProjects and with no ServiceAccount their AppProject maps.
	foreign []string
	// cloneAnywhere says the controller's own account may create
	// datavolumes/source cluster-wide.
	cloneAnywhere bool
}

func (v syncVerdict) clean() bool {
	return len(v.missing)+len(v.leaks)+len(v.foreign) == 0
}

// syncCache memoizes the verdict: it costs SubjectAccessReviews and reads in
// proportion to the project namespaces, while passes come in bursts (a
// rollout's Deployment events). A clean verdict serves until its inputs or
// the CR's spec change, or driftResync/2 elapses; one naming a problem only
// for recheck, since RBAC moves are not watched and a grant merged since, or
// still reaching the authorizer, must be seen soon.
type syncCache struct {
	syncMu      sync.Mutex
	syncKey     string
	syncAt      time.Time
	syncResults syncVerdict
	// syncRecheck is a test seam; 0 = time.Minute.
	syncRecheck time.Duration
}

// recheck is how long a verdict naming a problem serves, and how soon a pass
// waiting on sync impersonation comes back.
func (c *syncCache) recheck() time.Duration {
	if c.syncRecheck > 0 {
		return c.syncRecheck
	}
	return time.Minute
}

// syncScope is what a verdict covers.
type syncScope int

const (
	// scopeOff: the controller's clone rights, all that matters while not asked.
	scopeOff syncScope = iota
	// scopeFull: those, every account's grants and the foreign Applications.
	scopeFull
)

// syncVerdict evaluates, or reuses, scope's verdict for rc's tier.
func (r *DotvirtReconciler) syncVerdict(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, scope syncScope) (syncVerdict, error) {
	key := syncInputs(dv.Generation, rc, scope)
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	ttl := driftResync / 2
	if !r.syncResults.clean() {
		ttl = r.recheck()
	}
	if key == r.syncKey && r.clock().Sub(r.syncAt) < ttl {
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
	fmt.Fprintf(&b, "%s\n%s\n", strings.Join(slices.Sorted(maps.Keys(rc.sync.appProjects)), ","), strings.Join(rc.sync.appNamespaces, ","))
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

func bindAttrs(kind, name, ns string) authzv1.ResourceAttributes {
	resource := "clusterroles"
	if kind == "Role" {
		resource = "roles"
	}
	return authzv1.ResourceAttributes{Verb: "bind", Group: rbacv1.GroupName, Resource: resource, Name: name, Namespace: ns}
}

// cloneSource is creating datavolumes/source cluster-wide: cloning a disk out
// of any namespace, what CDI authorizes a DataVolume's source against.
var cloneSource = authzv1.ResourceAttributes{Verb: "create", Group: "cdi.kubevirt.io", Resource: "datavolumes", Subresource: "source"}

// evaluateSync runs the checks (syncChecks, platformRoleChecks) and, for the
// full scope, finds the unmapped foreign Applications: 3 reviews per project
// namespace, 2 per project, 3 more, 5 for the platform tier and one per
// further role its RoleBindings bind, at most accessProbeLimit in flight, plus
// a list of the Applications and AppProjects and a read of each platform
// RoleBinding. While not asked, the one review of the controller's clone
// rights.
func (r *DotvirtReconciler) evaluateSync(ctx context.Context, rc *reconcileCtx, scope syncScope) (syncVerdict, error) {
	var v syncVerdict
	checks := []accessCheck{{account: rc.argoSA, attrs: cloneSource, set: &v.cloneAnywhere}}
	if scope == scopeFull {
		apps := unstructuredList(install.ArgoGVK("Application"))().(*unstructured.UnstructuredList)
		if err := r.reader().List(ctx, apps); err != nil {
			return v, fmt.Errorf("list Applications: %w", err)
		}
		foreign, err := r.unmappedApplications(ctx, rc, apps.Items)
		if err != nil {
			return v, err
		}
		v.foreign = foreign
		checks = append(checks, syncChecks(rc)...)
		if rc.sync.platform {
			roles, err := r.platformRoleChecks(ctx, rc, apps.Items)
			if err != nil {
				return v, err
			}
			checks = append(checks, roles...)
		}
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

// syncChecks are the grant checks for rc's tier, in a stable order:
//   - the controller may impersonate the accounts;
//   - per project, its sync account may create VirtualMachines and
//     EgressFirewalls in each of its namespaces, and may neither create
//     VirtualMachines nor clone disks cluster-wide;
//   - every ServiceAccount of the ArgoCD namespace, so every other project's
//     sync account, may neither write VirtualMachines in a project namespace
//     nor clone disks cluster-wide. A grant naming one project's account in
//     another's namespace is left to that namespace's own admins, who may
//     grant anyone anything there;
//   - the platform account may create Namespaces, ClusterUserDefinedNetworks
//     and RoleBindings, and bind the roles project grants name.
func syncChecks(rc *reconcileCtx) []accessCheck {
	checks := []accessCheck{
		{account: rc.argoSA, attrs: authzv1.ResourceAttributes{Verb: "impersonate", Resource: "serviceaccounts", Namespace: rc.argoNS}, want: true,
			problem: fmt.Sprintf("the ArgoCD controller %s cannot impersonate ServiceAccounts in %s", rc.argoSA, rc.argoNS)},
		{account: syncProbeAccount, attrs: cloneSource,
			problem: fmt.Sprintf("every ServiceAccount of %s may clone disks out of any namespace (datavolumes/source cluster-wide), every project's sync account included", rc.argoNS)},
	}
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
		checks = append(checks,
			accessCheck{account: account, attrs: createAttrs("kubevirt.io", "virtualmachines", ""),
				problem: fmt.Sprintf("project %s's sync account %s may create VirtualMachines in every namespace", project, account)},
			accessCheck{account: account, attrs: cloneSource,
				problem: fmt.Sprintf("project %s's sync account %s may clone disks out of any namespace (datavolumes/source cluster-wide)", project, account)},
		)
	}
	for _, ns := range slices.Sorted(maps.Keys(projectNS)) {
		checks = append(checks, accessCheck{account: syncProbeAccount, attrs: createAttrs("kubevirt.io", "virtualmachines", ns),
			problem: fmt.Sprintf("namespace %s lets every ServiceAccount of %s create VirtualMachines, other projects' sync accounts included", ns, rc.argoNS)})
	}
	if rc.sync.platform {
		for _, c := range []struct {
			attrs authzv1.ResourceAttributes
			what  string
		}{
			{createAttrs("", "namespaces", ""), "create Namespaces"},
			{createAttrs("k8s.ovn.org", "clusteruserdefinednetworks", ""), "create ClusterUserDefinedNetworks"},
			{createAttrs(rbacv1.GroupName, "rolebindings", ""), "create RoleBindings"},
			{bindAttrs("ClusterRole", "admin", ""), "bind the admin role"},
			{bindAttrs("ClusterRole", tenancy.TenantEgressRole, ""), "bind " + tenancy.TenantEgressRole},
		} {
			checks = append(checks, accessCheck{account: tenancy.PlatformSyncAccount, attrs: c.attrs, want: true,
				problem: fmt.Sprintf("the platform sync account %s cannot %s (its operand ClusterRoles are not bound)", tenancy.PlatformSyncAccount, c.what)})
		}
	}
	return checks
}

// platformRoleChecks ask that the platform account may bind every role the
// platform tier's RoleBindings bind beyond the ones project grants name, as
// the platform Application reports managing them: once impersonation is on,
// Argo applies a RoleBinding as that account, and the apiserver refuses one
// binding a role the account may neither bind nor wholly hold. An account
// holding every right of such a role reads as missing here: a review can only
// ask for bind.
func (r *DotvirtReconciler) platformRoleChecks(ctx context.Context, rc *reconcileCtx, apps []unstructured.Unstructured) ([]accessCheck, error) {
	var refs []types.NamespacedName
	for _, a := range apps {
		if a.GetNamespace() != rc.argoNS || a.GetName() != install.PlatformApplicationName {
			continue
		}
		resources, _, _ := unstructured.NestedSlice(a.Object, "status", "resources")
		for _, res := range resources {
			m, _ := res.(map[string]any)
			if m["group"] == rbacv1.GroupName && m["kind"] == "RoleBinding" {
				ns, _ := m["namespace"].(string)
				name, _ := m["name"].(string)
				refs = append(refs, types.NamespacedName{Namespace: ns, Name: name})
			}
		}
	}
	bindings := make([]*rbacv1.RoleBinding, len(refs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(accessProbeLimit)
	for i, ref := range refs {
		g.Go(func() error {
			rb := &rbacv1.RoleBinding{}
			// Uncached: a cached read would start a cluster-wide RoleBinding informer.
			err := r.reader().Get(gctx, ref, rb)
			if apierrors.IsNotFound(err) {
				return nil
			}
			bindings[i] = rb
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("read the platform tier's RoleBindings: %w", err)
	}
	seen := map[string]bool{"ClusterRole//admin": true, "ClusterRole//" + tenancy.TenantEgressRole: true}
	var checks []accessCheck
	for _, rb := range bindings {
		if rb == nil {
			continue
		}
		ns := ""
		if rb.RoleRef.Kind == "Role" {
			ns = rb.Namespace
		}
		if key := rb.RoleRef.Kind + "/" + ns + "/" + rb.RoleRef.Name; !seen[key] {
			seen[key] = true
			checks = append(checks, accessCheck{account: tenancy.PlatformSyncAccount, attrs: bindAttrs(rb.RoleRef.Kind, rb.RoleRef.Name, ns), want: true,
				problem: fmt.Sprintf("the platform sync account %s cannot bind %s %s, which RoleBinding %s/%s in the platform repo binds: grant it bind on that role, or that RoleBinding stops syncing",
					tenancy.PlatformSyncAccount, rb.RoleRef.Kind, rb.RoleRef.Name, rb.Namespace, rb.Name)})
		}
	}
	return checks, nil
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

// unmappedApplications are the instance's Applications (rc's appNamespaces)
// outside dotvirt's AppProjects whose AppProject maps no ServiceAccount for
// their destination, as ns/name in name order: the ones impersonation stops.
func (r *DotvirtReconciler) unmappedApplications(ctx context.Context, rc *reconcileCtx, apps []unstructured.Unstructured) ([]string, error) {
	projects := unstructuredList(install.ArgoGVK("AppProject"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, projects, client.InNamespace(rc.argoNS)); err != nil {
		return nil, fmt.Errorf("list AppProjects: %w", err)
	}
	mapped := map[string][]any{}
	for _, p := range projects.Items {
		mapped[p.GetName()], _, _ = unstructured.NestedSlice(p.Object, "spec", "destinationServiceAccounts")
	}
	var out []string
	for _, a := range apps {
		ours := slices.ContainsFunc(rc.sync.appNamespaces, func(pattern string) bool { return argoMatch(pattern, a.GetNamespace(), true) })
		project, _, _ := unstructured.NestedString(a.Object, "spec", "project")
		if !ours || rc.sync.appProjects[project] {
			continue
		}
		server, _, _ := unstructured.NestedString(a.Object, "spec", "destination", "server")
		if name, _, _ := unstructured.NestedString(a.Object, "spec", "destination", "name"); server == "" && name == "in-cluster" {
			server = install.InClusterServer
		}
		ns, _, _ := unstructured.NestedString(a.Object, "spec", "destination", "namespace")
		if !slices.ContainsFunc(mapped[project], func(e any) bool {
			entry, _ := e.(map[string]any)
			s, _ := entry["server"].(string)
			n, _ := entry["namespace"].(string)
			return argoMatch(s, server, false) && argoMatch(n, ns, false)
		}) {
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
