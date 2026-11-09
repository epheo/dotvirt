package install

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

const (
	applicationSetName  = "dotvirt-projects"
	appsetConfigMapName = "dotvirt-appset-plugin"
)

// ApplicationSet generates one Argo Application per dotvirt project - each in its
// project's own AppProject (TenantAppProject), from dotvirt's plugin generator - so
// labeling a namespace provisions its tenant app. Unstructured (no argo-cd module
// dep). The {{.project}}/{{.repo}}/{{.path}}/{{.namespace}} templates are evaluated by Argo,
// so they stay literal strings here.
//
// platformNS rides the generator input: the plugin picks each destination
// namespace outside it, as the AppProjects admit (tenancy.Destination).
// unconfined are the projects with no AppProject of this install's under their
// name: Argo only checks that an Application's project exists, so the
// post-selector keeps them out rather than let one land in a foreign
// AppProject's permissions.
func ApplicationSet(dv *dotvirtv1alpha1.Dotvirt, argoNS string, platformNS, unconfined []string) *unstructured.Unstructured {
	template := map[string]any{
		"metadata": map[string]any{
			"name":   "dotvirt-{{.project}}",
			"labels": map[string]any{ProjectLabel: "{{.project}}"},
		},
		"spec": argoAppSpec(tenancy.AppProjectPrefix+"{{.project}}", "{{.repo}}", "{{.path}}", "{{.namespace}}"),
	}
	generator := map[string]any{"plugin": map[string]any{
		"configMapRef":        map[string]any{"name": appsetConfigMapName},
		"input":               map[string]any{"parameters": map[string]any{"reservedNamespaces": toAny(platformNS)}},
		"requeueAfterSeconds": int64(60),
	}}
	if len(unconfined) > 0 {
		generator["selector"] = map[string]any{"matchExpressions": []any{
			map[string]any{"key": "project", "operator": "NotIn", "values": toAny(unconfined)},
		}}
	}
	return argoObject("ApplicationSet", applicationSetName, argoNS, dv, map[string]any{
		"goTemplate":        true,
		"goTemplateOptions": []any{"missingkey=error"},
		// Renaming generated apps prunes the old ones; preserve their resources so a
		// rename adopts the running VMs instead of cascade-deleting them.
		"syncPolicy": map[string]any{"preserveResourcesOnDeletion": true},
		"generators": []any{generator},
		"template":   template,
	})
}

func toAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

// AppsetPluginConfigMap tells the plugin generator where to reach dotvirt's
// /api/v1/getparams.execute and which token to present (Argo resolves the token
// from the mirrored dotvirt-appset-plugin Secret in the ArgoCD namespace).
func AppsetPluginConfigMap(dv *dotvirtv1alpha1.Dotvirt, argoNS, dotvirtNS string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: appsetConfigMapName, Namespace: argoNS, Labels: Labels(dv)},
		Data: map[string]string{
			"baseUrl": svcURL(AppName, dotvirtNS, HTTPPort),
			"token":   "$" + appsetConfigMapName + ":token",
		},
	}
}
