package install

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/forge"
)

// RepoCredsName is the Argo repo-credentials template secret (in the ArgoCD ns).
const RepoCredsName = "dotvirt-repo-creds"

const (
	argoGroup   = "argoproj.io"
	argoVersion = "v1alpha1"

	// ProjectTenants / ProjectPlatform are the two AppProject tiers.
	ProjectTenants  = "dotvirt-tenants"
	ProjectPlatform = "dotvirt-platform"

	inClusterServer = "https://kubernetes.default.svc"
)

// ArgoGVK is the GroupVersionKind for an argoproj.io resource (used by the builders
// here and by the operator's finalizer cleanup). Rendered as unstructured so the
// operator needn't vendor the heavy argo-cd module.
func ArgoGVK(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: argoGroup, Version: argoVersion, Kind: kind}
}

func argoObject(kind, name, namespace, instance string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(ArgoGVK(kind))
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(Labels(instance))
	u.Object["spec"] = spec
	return u
}

// repoPrefix is the owner-path glob ("…/<owner>/*") for the tenant AppProject's
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
// secret key — server-side-applied so the operator owns just that key and coexists
// with the gitops-operator-managed keys. Deliberately NOT instance-labeled: the
// operator owns the key, not the Secret, so the finalizer must never delete
// argocd-secret. (The key is left behind on uninstall — a harmless orphan.)
func ArgoWebhookSecret(argoNS, value string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-secret", Namespace: argoNS},
		StringData: map[string]string{"webhook.gitea.secret": value},
	}
}

// RepoCredsSecret is an Argo repo-credentials TEMPLATE (type repo-creds) covering
// every repo under the forge owner prefix, so Argo can clone the tenant + platform
// repos — including private ones. Lives in the ArgoCD namespace; label-tracked for
// finalizer cleanup (a namespaced CR can't ownerRef it).
func RepoCredsSecret(dv *dotvirtv1alpha1.Dotvirt, argoNS, urlPrefix, username, token string) *corev1.Secret {
	labels := Labels(dv.Name)
	labels["argocd.argoproj.io/secret-type"] = "repo-creds"
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: RepoCredsName, Namespace: argoNS, Labels: labels},
		StringData: map[string]string{
			"type":     "git",
			"url":      urlPrefix,
			"username": username,
			"password": token,
		},
	}
}

// TenantsAppProject permits namespaced workloads ONLY — clusterResourceWhitelist is
// empty, so a tenant repo cannot reconcile a CUDN/NNCP/Namespace even though the
// shared Argo controller SA could. This is the enforcement boundary. operatorNS +
// argoNS are denied as destinations so a tenant can't target platform space.
func TenantsAppProject(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo, operatorNS string) *unstructured.Unstructured {
	return argoObject("AppProject", ProjectTenants, argoNS, dv.Name, map[string]any{
		"description": "dotvirt tenant projects — namespaced workloads only.",
		"sourceRepos": []any{repoPrefix(platformRepo)},
		"destinations": []any{
			map[string]any{"server": inClusterServer, "namespace": "*"},
			map[string]any{"server": inClusterServer, "namespace": "!openshift-*"},
			map[string]any{"server": inClusterServer, "namespace": "!kube-*"},
			map[string]any{"server": inClusterServer, "namespace": "!" + operatorNS},
			map[string]any{"server": inClusterServer, "namespace": "!" + argoNS},
		},
		"clusterResourceWhitelist": []any{},
		"namespaceResourceWhitelist": []any{
			map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine"},
			map[string]any{"group": "cdi.kubevirt.io", "kind": "DataVolume"},
			map[string]any{"group": "k8s.ovn.org", "kind": "UserDefinedNetwork"},
			map[string]any{"group": "k8s.cni.cncf.io", "kind": "NetworkAttachmentDefinition"},
		},
	})
}

// PlatformAppProject is the only tier allowed cluster-scoped + tenancy objects.
func PlatformAppProject(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo string) *unstructured.Unstructured {
	return argoObject("AppProject", ProjectPlatform, argoNS, dv.Name, map[string]any{
		"description": "dotvirt platform tier — tenancy + cluster-scoped network infrastructure.",
		"sourceRepos": []any{platformRepo},
		"destinations": []any{
			map[string]any{"server": inClusterServer, "namespace": "*"},
		},
		"clusterResourceWhitelist": []any{
			map[string]any{"group": "", "kind": "Namespace"},
			map[string]any{"group": "k8s.ovn.org", "kind": "ClusterUserDefinedNetwork"},
			map[string]any{"group": "nmstate.io", "kind": "NodeNetworkConfigurationPolicy"},
		},
		"namespaceResourceWhitelist": []any{
			map[string]any{"group": "k8s.ovn.org", "kind": "UserDefinedNetwork"},
			map[string]any{"group": "k8s.cni.cncf.io", "kind": "NetworkAttachmentDefinition"},
			// Tenant access delegation: a project's owners → namespace-admin grant.
			map[string]any{"group": "rbac.authorization.k8s.io", "kind": "RoleBinding"},
		},
	})
}

// PlatformApplication is the single STATIC Argo app over the platform repo — the
// privileged tier, deliberately NOT generated by the ApplicationSet (so the
// privileged app's existence never flows through dotvirt's dynamic project list).
func PlatformApplication(dv *dotvirtv1alpha1.Dotvirt, argoNS, platformRepo string) *unstructured.Unstructured {
	return argoObject("Application", "dotvirt-platform", argoNS, dv.Name, map[string]any{
		"project": ProjectPlatform,
		"source": map[string]any{
			"repoURL":        platformRepo,
			"targetRevision": "main",
			"path":           ".",
			"directory":      map[string]any{"recurse": true, "include": "*.yaml"},
		},
		"destination": map[string]any{"server": inClusterServer, "namespace": "default"},
		"syncPolicy": map[string]any{
			"automated":   map[string]any{"prune": true, "selfHeal": true},
			"syncOptions": []any{"CreateNamespace=false"},
		},
	})
}
