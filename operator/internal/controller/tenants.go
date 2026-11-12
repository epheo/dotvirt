package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// tenantProject is one project as its labeled namespaces describe it.
type tenantProject struct {
	namespaces []string
	// repos are the resolved repo refs its namespaces annotate; "" is a
	// relative ref with no forge to resolve it against.
	repos map[string]bool
}

// deriveTenantProjects groups the project-labeled namespaces the way the app's
// resolver does (internal/project), relative repo refs resolved against
// forgeBase with the same pkg/forge rule, so each AppProject admits exactly
// the repoURL the plugin hands the ApplicationSet.
func deriveTenantProjects(namespaces []corev1.Namespace, forgeBase string) map[string]*tenantProject {
	out := map[string]*tenantProject{}
	for _, ns := range namespaces {
		name := ns.Labels[install.ProjectLabel]
		if name == "" {
			continue
		}
		p := out[name]
		if p == nil {
			p = &tenantProject{repos: map[string]bool{}}
			out[name] = p
		}
		p.namespaces = append(p.namespaces, ns.Name)
		if ref := ns.Annotations[install.RepoAnnotation]; ref != "" {
			p.repos[forge.ResolveRef(forgeBase, ref)] = true
		}
	}
	for _, p := range out {
		sort.Strings(p.namespaces)
	}
	return out
}

// sourceRepo is the project's one repo; ok=false when its namespaces name none,
// several, or one that does not resolve. Argo matches sourceRepos as globs, so
// a URL carrying glob syntax would admit other repos and does not count.
func (p *tenantProject) sourceRepo() (string, bool) {
	if len(p.repos) != 1 {
		return "", false
	}
	for repo := range p.repos {
		return repo, repo != "" && !strings.ContainsAny(repo, "*?[]{}\\") && !strings.HasPrefix(repo, "!")
	}
	return "", false
}

// tenantTier is what a pass renders for tenant Applications and what it could
// not: the AppProjects, the projects kept out of the generator because no
// AppProject of this install's confines them, and the notes for ArgoReady,
// degraded ones (a project not served as labeled) apart from the rest.
type tenantTier struct {
	objs       []client.Object
	unconfined []string
	degraded   []string
	notes      []string
	// accounts are the live projects' sync accounts, each with the namespaces
	// its project syncs into; sharedInUse says an Application still uses the
	// shared tier, which names no sync account.
	accounts    map[string][]string
	sharedInUse bool
	// controllerApplies says the shared Argo controller still applies
	// dotvirt's tiers (controllerApplies).
	controllerApplies bool
}

// tenantAppProjects renders one AppProject per project from the live namespace
// labels: its namespaces outside platform space as the only destinations, its
// repo as the only source. It also keeps the owned AppProjects Applications
// still reference though no live project derives them: the shared tier while
// the ApplicationSet moves its Applications off it, and a vanished project's,
// admitting nothing, until its Application is removed. Unreferenced, they
// leave the render and prune deletes them.
func (r *DotvirtReconciler) tenantAppProjects(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, platformNS []string) (tenantTier, error) {
	argoNS := rc.argoNS
	t := tenantTier{accounts: map[string][]string{}}
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.HasLabels{install.ProjectLabel}); err != nil {
		return t, fmt.Errorf("list project namespaces: %w", err)
	}
	forgeBase, err := r.appForgeBase(ctx, dv)
	if err != nil {
		return t, err
	}
	held := unstructuredList(install.ArgoGVK("AppProject"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, held, client.InNamespace(argoNS)); err != nil {
		return t, fmt.Errorf("list AppProjects: %w", err)
	}
	foreign := map[string]bool{}
	for _, o := range held.Items {
		if !install.OwnedBy(o.GetLabels(), dv) {
			foreign[o.GetName()] = true
		}
	}
	allowed := r.tenantKinds(ctx, dv, rc, held.Items)
	t.controllerApplies = r.controllerApplies(ctx, dv, rc, held.Items)
	want, refused := install.NamedTenantKinds(dv)
	if len(refused) > 0 {
		t.degraded = append(t.degraded, "spec.argocd.tenantKindsAllowed names kinds no tenant repo may apply, left out: "+tenancy.FormatGroupKinds(refused))
	}
	if len(want) > 0 && allowed == nil {
		t.notes = append(t.notes, "tenant repos keep the listed kinds until the ArgoCD instance impersonates: argocd-cm sets "+
			install.SyncImpersonationKey+", its AppProjects keep destinationServiceAccounts (Argo CD 2.13 or later) and its ArgoCD resource pins no image or version")
	}

	projects := deriveTenantProjects(nsList.Items, forgeBase)
	rendered := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(projects)) {
		if name == tenancy.PlatformProject {
			continue
		}
		p := projects[name]
		apName, ok := tenancy.AppProjectName(name)
		switch {
		case !ok:
			t.unconfined = append(t.unconfined, name)
			t.degraded = append(t.degraded, fmt.Sprintf("project %q has no valid AppProject name; no Application is generated for it", name))
			continue
		case foreign[apName]:
			t.unconfined = append(t.unconfined, name)
			t.degraded = append(t.degraded, fmt.Sprintf("AppProject %s is not this install's; no Application is generated for project %s", apName, name))
			continue
		}
		var destinations []string
		for _, ns := range p.namespaces {
			if tenancy.Reserved(ns, platformNS) {
				t.degraded = append(t.degraded, fmt.Sprintf("namespace %s is platform space, not a destination of project %s", ns, name))
				continue
			}
			destinations = append(destinations, ns)
		}
		var sources []string
		if repo, ok := p.sourceRepo(); ok {
			sources = []string{repo}
		} else if len(p.repos) > 0 {
			t.notes = append(t.notes, fmt.Sprintf("project %s names no single resolvable repo; its AppProject admits no source", name))
		}
		t.objs = append(t.objs, install.TenantAppProject(dv, argoNS, apName, name, destinations, sources, allowed))
		t.accounts[apName] = destinations
		rendered[apName] = true
	}

	var stale []unstructured.Unstructured
	for _, o := range held.Items {
		_, tenant := o.GetLabels()[install.ProjectLabel]
		if install.OwnedBy(o.GetLabels(), dv) && !rendered[o.GetName()] && (tenant || o.GetName() == install.TenantsProjectName) {
			stale = append(stale, o)
		}
	}
	if len(stale) == 0 {
		return t, nil
	}
	refs, err := r.projectReferences(ctx, argoNS)
	if err != nil {
		return t, err
	}
	for _, o := range stale {
		n := refs[o.GetName()]
		if n == 0 {
			continue
		}
		if o.GetName() == install.TenantsProjectName {
			t.objs = append(t.objs, install.TenantsAppProject(dv, argoNS, dv.Spec.Forge.PlatformRepo, dv.Namespace))
			t.notes = append(t.notes, fmt.Sprintf("%s kept while %d Applications still reference it", install.TenantsProjectName, n))
			t.sharedInUse = true
			continue
		}
		t.objs = append(t.objs, install.TenantAppProject(dv, argoNS, o.GetName(), o.GetLabels()[install.ProjectLabel], nil, nil, allowed))
	}
	return t, nil
}

// projectReferences counts the generated Applications in argoNS per AppProject
// name: the ApplicationSet labels each with its project.
func (r *DotvirtReconciler) projectReferences(ctx context.Context, argoNS string) (map[string]int, error) {
	apps := unstructuredList(install.ArgoGVK("Application"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, apps, client.InNamespace(argoNS), client.HasLabels{install.ProjectLabel}); err != nil {
		return nil, fmt.Errorf("list Applications: %w", err)
	}
	refs := map[string]int{}
	for _, a := range apps.Items {
		if p, _, _ := unstructured.NestedString(a.Object, "spec", "project"); p != "" {
			refs[p]++
		}
	}
	return refs, nil
}

// platformNamespaces are the namespaces dotvirt, its operator and ArgoCD run
// in, in name order: never a tenant destination (tenancy.Reserved).
func (r *DotvirtReconciler) platformNamespaces(dv *dotvirtv1alpha1.Dotvirt, argoNS string) []string {
	var out []string
	for _, ns := range []string{dv.Namespace, argoNS, r.OperatorNamespace} {
		if ns != "" {
			out = append(out, ns)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// appForgeBase is the forge URL the app resolves relative repo refs against,
// the DOTVIRT_FORGE_URL dotvirtEnv hands it: a managed forge's resolved URL, a
// BYO forge's from its credentials secret, none without a forge.
func (r *DotvirtReconciler) appForgeBase(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (string, error) {
	if dv.Spec.Forge.Managed {
		return dv.Spec.Forge.URL, nil
	}
	if !install.ForgeConfigured(dv) {
		return "", nil
	}
	s, err := r.secret(ctx, dv.Namespace, install.ForgeSecretName(dv))
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read forge credentials: %w", err)
	}
	return string(s.Data["url"]), nil
}

// setArgoReady records the tier's state: Degraded while a project is not
// served as labeled, Unknown while the ApplicationSet waits on the dotvirt
// pod, Ready otherwise, the notes said either way.
func (r *DotvirtReconciler) setArgoReady(dv *dotvirtv1alpha1.Dotvirt, t tenantTier, a appsetState) {
	degraded := t.degraded
	if a.degraded != "" {
		degraded = append(degraded, a.degraded)
	}
	msg := strings.Join(append(append([]string{"argo resources applied"}, degraded...), t.notes...), "; ")
	switch {
	case len(degraded) > 0:
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoReady, metav1.ConditionFalse, "Degraded", msg)
	case a.waiting != "":
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoReady, metav1.ConditionUnknown, a.waiting,
			strings.Join(append([]string{"argo resources applied", a.msg}, t.notes...), "; "))
	default:
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoReady, metav1.ConditionTrue, "Ready", msg)
	}
}

// tenantKinds is install.TenantAllowed for this pass, decided once, so the
// dotvirt pod (the workload phase) and the AppProjects (the Argo phase, after
// it) read the same answer. held are the ArgoCD namespace's AppProjects,
// listed here when nil. A failed read is unknown, so no: the listed kinds.
func (r *DotvirtReconciler) tenantKinds(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, held []unstructured.Unstructured) []tenancy.GroupKind {
	if rc.tenantKindsRead {
		return rc.tenantAllowed
	}
	rc.tenantKindsRead = true
	if allowed, _ := install.NamedTenantKinds(dv); len(allowed) == 0 {
		return nil
	}
	if held == nil {
		list := unstructuredList(install.ArgoGVK("AppProject"))().(*unstructured.UnstructuredList)
		if err := r.reader().List(ctx, list, client.InNamespace(rc.argoNS)); err != nil {
			return nil
		}
		held = list.Items
	}
	rc.tenantAllowed = install.TenantAllowed(dv, r.impersonatingOnce(ctx, dv, rc, held))
	return rc.tenantAllowed
}

// impersonatingOnce is impersonating, asked once per pass.
func (r *DotvirtReconciler) impersonatingOnce(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, held []unstructured.Unstructured) bool {
	if rc.impersonation == nil {
		rc.impersonation = new(r.impersonating(ctx, dv, rc.argoNS, held))
	}
	return *rc.impersonation
}

// controllerApplies reports whether the shared Argo controller still applies
// dotvirt's tiers, so needs dotvirt-argocd-apply. It stops only once the
// spec asks for impersonation, Argo is impersonating, and an earlier pass
// already saw argocd-cm carry the setting (SyncImpersonationReady Enabled, or
// Degraded naming what is missing), so Argo read it before the role goes. A
// pass asked off keeps the controller bound: the Argo phase runs before the
// sync phase removes the setting, so the role is back before Argo applies as
// the controller again.
func (r *DotvirtReconciler) controllerApplies(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx, held []unstructured.Unstructured) bool {
	if !dv.Spec.ArgoCD.SyncImpersonation.On() {
		return true
	}
	seen := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionSyncImpersonationReady)
	if seen == nil || (seen.Reason != "Enabled" && seen.Reason != "Degraded") {
		return true
	}
	return !r.impersonatingOnce(ctx, dv, rc, held)
}

// impersonating reports whether the instance syncs dotvirt's Applications as
// their projects' own accounts: argocd-cm sets the key, and an AppProject this
// install applied kept destinationServiceAccounts, which an Argo CD older than
// 2.13 neither knows nor honors (its schema prunes the field). The field proves
// the CRD, not the controller: an ArgoCD resource pinning its own image or
// version may run an older one under newer CRDs, so it proves nothing. Read
// where Argo reads it; unknown is no.
func (r *DotvirtReconciler) impersonating(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string, held []unstructured.Unstructured) bool {
	if set, err := r.impersonationConfigured(ctx, argoNS); err != nil || !set {
		return false
	}
	inst, _, err := r.argoInstanceState(ctx, argoNS+"/"+argoInstance(argoNS))
	if err != nil {
		return false
	}
	if inst != nil {
		version, _, _ := unstructured.NestedString(inst.Object, "spec", "version")
		image, _, _ := unstructured.NestedString(inst.Object, "spec", "image")
		if version != "" || image != "" {
			return false
		}
	}
	return slices.ContainsFunc(held, func(o unstructured.Unstructured) bool {
		accounts, _, _ := unstructured.NestedSlice(o.Object, "spec", "destinationServiceAccounts")
		return install.OwnedBy(o.GetLabels(), dv) && len(accounts) > 0
	})
}
