package install

import (
	"encoding/json"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// repoCredsName is the Argo repo-credentials template secret (in the ArgoCD ns).
const repoCredsName = "dotvirt-repo-creds"

const (
	argoGroup   = "argoproj.io"
	argoVersion = "v1alpha1"

	// TenantsProjectName is the shared tenant tier (TenantsAppProject);
	// projectPlatform is the privileged tier. Per-project AppProjects are named
	// by tenancy.AppProjectName.
	TenantsProjectName = "dotvirt-tenants"
	projectPlatform    = PlatformApplicationName

	// appTargetRevision is the branch every dotvirt Application syncs, spelled once so
	// the tenant template and the platform app cannot drift apart.
	appTargetRevision = "main"

	// InClusterServer is the cluster every dotvirt Application deploys to.
	InClusterServer = "https://kubernetes.default.svc"

	// PlatformApplicationName names the platform tier's Application and
	// AppProject.
	PlatformApplicationName = "dotvirt-platform"
)

// ArgoGVK is the GroupVersionKind for an argoproj.io resource (used by the builders
// here and by the operator's prune). Rendered as unstructured so the
// operator needn't vendor the heavy argo-cd module.
func ArgoGVK(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: argoGroup, Version: argoVersion, Kind: kind}
}

func argoObject(kind, name, namespace string, dv *dotvirtv1alpha1.Dotvirt, spec map[string]any) *unstructured.Unstructured {
	return unstructuredObject(ArgoGVK(kind), name, namespace, dv, spec)
}

// repoPrefix is the owner-path glob (".../<owner>/*") for the tenant AppProject's
// sourceRepos. Falls back to "*" when it can't be derived.
func repoPrefix(platformRepo string) string {
	if platformRepo == "" {
		return "*"
	}
	if p := forge.OwnerPrefixURL(platformRepo); p != platformRepo {
		return p + "/*"
	}
	return "*"
}

// ArgoWebhookSecret is a PARTIAL argocd-secret carrying ONLY the Gitea webhook
// secret key - server-side-applied so the operator owns just that key and coexists
// with the gitops-operator-managed keys. Deliberately NOT instance-labeled: the
// operator owns the key, not the Secret, so the finalizer must never delete
// argocd-secret. (The key is left behind on uninstall - a harmless orphan.)
func ArgoWebhookSecret(argoNS, value string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-secret", Namespace: argoNS},
		StringData: map[string]string{"webhook.gitea.secret": value},
	}
}

// RepoCredsSecret is an Argo repo-credentials TEMPLATE (type repo-creds) covering
// every repo under the forge owner prefix, so Argo can clone the tenant + platform
// repos - including private ones. Lives in the ArgoCD namespace; label-tracked for
// finalizer cleanup (a namespaced CR can't ownerRef it).
//
// No `insecure` key: repo-creds templates silently ignore it (repository-secret-
// only field, per the declarative-setup docs). Trust is real instead:
// ensureForgeTLSTrust puts the ingress CA in argocd-tls-certs-cm.
func RepoCredsSecret(dv *dotvirtv1alpha1.Dotvirt, argoNS, urlPrefix, username, token string) *corev1.Secret {
	labels := Labels(dv)
	labels["argocd.argoproj.io/secret-type"] = "repo-creds"
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: repoCredsName, Namespace: argoNS, Labels: labels},
		StringData: map[string]string{
			"type":     "git",
			"url":      urlPrefix,
			"username": username,
			"password": token,
		},
	}
}

// tenantWhitelist is what a tenant repo may hold: tenancy.TenantKinds, the
// list dotvirt stages by, so neither side admits what the other refuses.
// clusterResourceWhitelist stays empty, so a tenant repo cannot reconcile a
// CUDN/NNCP/Namespace even though the shared Argo controller SA could.
func tenantWhitelist(spec map[string]any) map[string]any {
	kinds := make([]any, 0, len(tenancy.TenantKinds))
	for _, k := range tenancy.TenantKinds {
		kinds = append(kinds, map[string]any{"group": k.Group, "kind": k.Kind})
	}
	spec["clusterResourceWhitelist"] = []any{}
	spec["namespaceResourceWhitelist"] = kinds
	return spec
}

// TenantsAppProject is the shared tenant tier: any namespace outside platform
// space, any repo of the forge owner. That is wide enough for one tenant's repo
// to write into another's namespace, so it is rendered only while an
// Application still references it, which keeps those Applications syncing
// until the ApplicationSet moves them to their own TenantAppProject.
func TenantsAppProject(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo, operatorNS string) *unstructured.Unstructured {
	return argoObject("AppProject", TenantsProjectName, argoNS, dv, tenantWhitelist(map[string]any{
		"description": "dotvirt tenant projects — namespaced workloads only.",
		"sourceRepos": []any{repoPrefix(platformRepo)},
		"destinations": []any{
			map[string]any{"server": InClusterServer, "namespace": "*"},
			map[string]any{"server": InClusterServer, "namespace": "!openshift-*"},
			map[string]any{"server": InClusterServer, "namespace": "!kube-*"},
			map[string]any{"server": InClusterServer, "namespace": "!" + operatorNS},
			map[string]any{"server": InClusterServer, "namespace": "!" + argoNS},
		},
	}))
}

// ProjectLabel and RepoAnnotation are the namespace keys naming a project and
// its repo. The app is started with them and the tenant AppProjects derive
// from them, so both read the same tenancy.
const (
	ProjectLabel   = "dotvirt.io/project"
	RepoAnnotation = "dotvirt.io/repo"
)

// TenantAppProject, named name (tenancy.AppProjectName), confines one project's
// Application to exactly its own namespaces and repos. Argo checks the
// Application's destination and every object's namespace against
// destinations, so a manifest merged into this project's repo cannot land in
// another tenant's namespace. No namespaces or repos permit nothing: a project
// whose namespaces are gone while its Application is still being removed, or
// whose repo does not resolve. Labeled with the project, which is how a pass
// finds the ones it no longer derives.
//
// Its sync account (tenancy.SyncAccount, named like the AppProject) is the
// ServiceAccount Argo syncs the project's Application as once sync
// impersonation is on (syncAccounts).
func TenantAppProject(dv *dotvirtv1alpha1.Dotvirt, argoNS, name, project string, namespaces, sourceRepos []string) *unstructured.Unstructured {
	destinations := make([]any, 0, len(namespaces))
	for _, ns := range namespaces {
		destinations = append(destinations, map[string]any{"server": InClusterServer, "namespace": ns})
	}
	repos := toAny(sourceRepos)
	u := argoObject("AppProject", name, argoNS, dv, tenantWhitelist(map[string]any{
		"description":                "dotvirt project " + project + ": its own namespaces and repo only.",
		"sourceRepos":                repos,
		"destinations":               destinations,
		"destinationServiceAccounts": syncAccounts(argoNS, name),
	}))
	labels := u.GetLabels()
	labels[ProjectLabel] = project
	u.SetLabels(labels)
	return u
}

// syncAccounts names the one ServiceAccount, in argoNS, Argo syncs an
// AppProject's Applications as while sync impersonation is on: any
// destination, since the AppProject's destinations already bound where they
// go. Inert while it is off, so it is rendered either way: the setting can
// then flip with no window in which an Application names no account, and an
// instance switched on by hand syncs dotvirt's tiers as their own accounts.
// The account is a name only, never created: Argo impersonates it by username
// and RBAC binds it by name, so the platform repo's grants are all it is.
func syncAccounts(argoNS, account string) []any {
	return []any{map[string]any{
		"server":                InClusterServer,
		"namespace":             "*",
		"defaultServiceAccount": argoNS + ":" + account,
	}}
}

// PlatformAppProject is the only tier allowed cluster-scoped + tenancy objects.
func PlatformAppProject(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo string) *unstructured.Unstructured {
	return argoObject("AppProject", projectPlatform, argoNS, dv, map[string]any{
		"description": "dotvirt platform tier — tenancy + cluster-scoped network infrastructure.",
		"sourceRepos": []any{platformRepo},
		"destinations": []any{
			map[string]any{"server": InClusterServer, "namespace": "*"},
		},
		"destinationServiceAccounts": syncAccounts(argoNS, tenancy.PlatformSyncAccount),
		"clusterResourceWhitelist": []any{
			map[string]any{"group": "", "kind": "Namespace"},
			map[string]any{"group": "k8s.ovn.org", "kind": "ClusterUserDefinedNetwork"},
			// The cluster edge - cluster-scoped fixed outbound IPs + external routes.
			map[string]any{"group": "k8s.ovn.org", "kind": "EgressIP"},
			map[string]any{"group": "k8s.ovn.org", "kind": "AdminPolicyBasedExternalRoute"},
			// The cluster rule tiers.
			map[string]any{"group": "policy.networking.k8s.io", "kind": "AdminNetworkPolicy"},
			map[string]any{"group": "policy.networking.k8s.io", "kind": "BaselineAdminNetworkPolicy"},
			map[string]any{"group": "nmstate.io", "kind": "NodeNetworkConfigurationPolicy"},
			// DRS: the worker PSI kernel-arg the load-aware descheduler profile needs.
			map[string]any{"group": "machineconfiguration.openshift.io", "kind": "MachineConfig"},
		},
		"namespaceResourceWhitelist": []any{
			map[string]any{"group": "k8s.ovn.org", "kind": "UserDefinedNetwork"},
			map[string]any{"group": "k8s.cni.cncf.io", "kind": "NetworkAttachmentDefinition"},
			// Tenant access delegation: a project's owners -> namespace-admin grant.
			map[string]any{"group": "rbac.authorization.k8s.io", "kind": "RoleBinding"},
			// DRS: the descheduler operator install + its configuration CR.
			map[string]any{"group": "operators.coreos.com", "kind": "Subscription"},
			map[string]any{"group": "operators.coreos.com", "kind": "OperatorGroup"},
			map[string]any{"group": "operator.openshift.io", "kind": "KubeDescheduler"},
		},
	})
}

// argoAppSpec is the Application spec every dotvirt app shares; the platform app
// and the ApplicationSet's tenant template must not drift apart.
//
// prune+selfHeal is safe against a repo that lost its content because ArgoCD's
// automated.allowEmpty defaults to false: a source that yields zero resources is
// refused rather than applied, so an emptied or unclonable repo cannot wipe a tier.
func argoAppSpec(project, repoURL, namespace string) map[string]any {
	return map[string]any{
		"project": project,
		"source": map[string]any{
			"repoURL":        repoURL,
			"targetRevision": appTargetRevision,
			"path":           ".",
			// templates/ is the repo's VM-template library (VirtualMachineTemplate
			// manifests dotvirt renders itself) - its CRD need not exist on-cluster,
			// so Argo must never try to apply it. Argo compiles these globs without
			// a separator, so the * crosses "/" (the same reason include matches
			// nested <ns>/<vm>.yaml).
			"directory": map[string]any{"recurse": true, "include": "*.yaml", "exclude": "templates/*"},
		},
		"destination": map[string]any{"server": InClusterServer, "namespace": namespace},
		"syncPolicy": map[string]any{
			"automated":   map[string]any{"prune": true, "selfHeal": true},
			"syncOptions": []any{"CreateNamespace=false"},
		},
	}
}

// PlatformApplication is the single STATIC Argo app over the platform repo - the
// privileged tier, deliberately NOT generated by the ApplicationSet (so the
// privileged app's existence never flows through dotvirt's dynamic project list).
func PlatformApplication(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo string) *unstructured.Unstructured {
	return argoObject("Application", PlatformApplicationName, argoNS, dv, argoAppSpec(projectPlatform, platformRepo, "default"))
}

// SyncImpersonationKey is the argocd-cm setting that makes Argo sync each
// Application as the ServiceAccount its AppProject names.
const SyncImpersonationKey = "application.sync.impersonation.enabled"

// ArgoCDGVK is the argocd-operator's instance kind; its spec.extraConfig is
// copied into argocd-cm, which the argocd-operator rewrites from the CR.
var ArgoCDGVK = schema.GroupVersionKind{Group: argoGroup, Version: "v1beta1", Kind: "ArgoCD"}

// ArgoCDSyncImpersonation is the operator's whole apply on the shared ArgoCD
// instance: SyncImpersonationKey alone, or nothing, which releases the key.
// extraConfig is a granular map, so the operator owns that one key and no
// other field; unlabeled, since the instance is not the install's.
func ArgoCDSyncImpersonation(name, namespace string, on bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(ArgoCDGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	if on {
		u.Object["spec"] = map[string]any{"extraConfig": map[string]any{SyncImpersonationKey: "true"}}
	}
	return u
}

// OwnsSyncImpersonation reports whether the operator's apply holds
// SyncImpersonationKey on inst: only then is removing it the operator's call.
func OwnsSyncImpersonation(inst *unstructured.Unstructured) bool {
	return slices.Contains(SyncImpersonationManagers(inst), fieldManager)
}

// SyncImpersonationManagers are the field managers holding
// SyncImpersonationKey on inst, the operator's apply as fieldManager.
func SyncImpersonationManagers(inst *unstructured.Unstructured) []string {
	var out []string
	for _, mf := range inst.GetManagedFields() {
		if mf.FieldsV1 == nil || (mf.Manager == fieldManager && mf.Operation != metav1.ManagedFieldsOperationApply) {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(mf.FieldsV1.Raw, &fields) != nil {
			continue
		}
		spec, _ := fields["f:spec"].(map[string]any)
		extra, _ := spec["f:extraConfig"].(map[string]any)
		if _, ok := extra["f:"+SyncImpersonationKey]; ok && !slices.Contains(out, mf.Manager) {
			out = append(out, mf.Manager)
		}
	}
	return out
}
