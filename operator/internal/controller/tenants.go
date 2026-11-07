package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reservedProject is the app's platform tier name (internal/project.PlatformName):
// the app never generates an Application for it.
const reservedProject = "platform"

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

// tenantAppProjects renders one AppProject per project from the live namespace
// labels: its namespaces as the only destinations, its repo as the only
// source. It also keeps the owned AppProjects Applications still reference
// though no live project derives them: the shared tier while the ApplicationSet
// moves its Applications off it, and a vanished project's, admitting nothing,
// until its Application is removed. Unreferenced, they leave the render and
// prune deletes them. notes say what could not be confined as derived.
func (r *DotvirtReconciler) tenantAppProjects(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (objs []client.Object, notes []string, err error) {
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.HasLabels{install.ProjectLabel}); err != nil {
		return nil, nil, fmt.Errorf("list project namespaces: %w", err)
	}
	forgeBase, err := r.appForgeBase(ctx, dv)
	if err != nil {
		return nil, nil, err
	}
	held := unstructuredList(install.ArgoGVK("AppProject"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, held, client.InNamespace(rc.argoNS)); err != nil {
		return nil, nil, fmt.Errorf("list AppProjects: %w", err)
	}
	foreign := map[string]bool{}
	for _, o := range held.Items {
		if !install.OwnedBy(o.GetLabels(), dv) {
			foreign[o.GetName()] = true
		}
	}

	projects := deriveTenantProjects(nsList.Items, forgeBase)
	fallback := install.TenantRepoFallback(dv.Spec.Forge.PlatformRepo)
	reserved := func(ns string) bool {
		return strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-") ||
			ns == dv.Namespace || ns == rc.argoNS || ns == r.OperatorNamespace
	}
	rendered := map[string]bool{}
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, apName := projects[name], install.TenantProjectName(name)
		switch {
		case name == reservedProject:
			continue
		case len(validation.IsDNS1123Subdomain(apName)) > 0:
			notes = append(notes, fmt.Sprintf("project %q has no valid AppProject name", name))
			continue
		case foreign[apName]:
			notes = append(notes, fmt.Sprintf("AppProject %s belongs to someone else; project %s is not confined", apName, name))
			continue
		}
		var destinations []string
		for _, ns := range p.namespaces {
			if reserved(ns) {
				notes = append(notes, fmt.Sprintf("namespace %s is platform space, not a destination of project %s", ns, name))
				continue
			}
			destinations = append(destinations, ns)
		}
		repo, ok := p.sourceRepo()
		if !ok {
			repo = fallback
			if len(p.repos) > 0 {
				notes = append(notes, fmt.Sprintf("project %s: no single resolvable repo, sources limited to %s", name, fallback))
			}
		}
		objs = append(objs, install.TenantAppProject(dv, rc.argoNS, name, destinations, []string{repo}))
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
		return objs, notes, nil
	}
	refs, err := r.projectReferences(ctx, rc.argoNS)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range stale {
		n := refs[o.GetName()]
		if n == 0 {
			continue
		}
		if o.GetName() == install.TenantsProjectName {
			objs = append(objs, install.TenantsAppProject(dv, rc.argoNS, dv.Spec.Forge.PlatformRepo, dv.Namespace))
			notes = append(notes, fmt.Sprintf("%s kept while %d Applications still reference it", install.TenantsProjectName, n))
			continue
		}
		objs = append(objs, install.TenantAppProject(dv, rc.argoNS, o.GetLabels()[install.ProjectLabel], nil, nil))
	}
	return objs, notes, nil
}

// projectReferences counts the Applications in argoNS per AppProject name.
func (r *DotvirtReconciler) projectReferences(ctx context.Context, argoNS string) (map[string]int, error) {
	apps := unstructuredList(install.ArgoGVK("Application"))().(*unstructured.UnstructuredList)
	if err := r.reader().List(ctx, apps, client.InNamespace(argoNS)); err != nil {
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

// argoNote joins the tenant notes into the ArgoReady message.
func argoNote(notes []string) string {
	msg := "argo resources applied"
	if len(notes) > 0 {
		msg += "; " + strings.Join(notes, "; ")
	}
	return msg
}
