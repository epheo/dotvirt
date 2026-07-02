package install

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

const (
	ApplicationSetName  = "dotvirt-projects"
	AppsetConfigMapName = "dotvirt-appset-plugin"
)

// ApplicationSet generates one Argo Application per dotvirt project — in the
// dotvirt-tenants AppProject (restricted), from dotvirt's plugin generator — so
// labeling a namespace provisions its tenant app. Unstructured (no argo-cd module
// dep). The {{.project}}/{{.repo}} templates are evaluated by Argo, so they stay
// literal strings here.
func ApplicationSet(dv *dotvirtv1alpha1.Dotvirt, argoNS string) *unstructured.Unstructured {
	template := map[string]any{
		"metadata": map[string]any{
			"name":   "dotvirt-{{.project}}",
			"labels": map[string]any{"dotvirt.io/project": "{{.project}}"},
		},
		"spec": map[string]any{
			"project": ProjectTenants,
			"source": map[string]any{
				"repoURL":        "{{.repo}}",
				"targetRevision": "main",
				"path":           ".",
				"directory":      map[string]any{"recurse": true, "include": "*.yaml"},
			},
			"destination": map[string]any{"server": inClusterServer, "namespace": "default"},
			"syncPolicy": map[string]any{
				"automated":   map[string]any{"prune": true, "selfHeal": true},
				"syncOptions": []any{"CreateNamespace=false"},
			},
		},
	}
	return argoObject("ApplicationSet", ApplicationSetName, argoNS, dv.Name, map[string]any{
		"goTemplate":        true,
		"goTemplateOptions": []any{"missingkey=error"},
		// Renaming generated apps prunes the old ones; preserve their resources so a
		// rename adopts the running VMs instead of cascade-deleting them.
		"syncPolicy": map[string]any{"preserveResourcesOnDeletion": true},
		"generators": []any{
			map[string]any{"plugin": map[string]any{
				"configMapRef":        map[string]any{"name": AppsetConfigMapName},
				"input":               map[string]any{"parameters": map[string]any{}},
				"requeueAfterSeconds": int64(60),
			}},
		},
		"template": template,
	})
}

// AppsetPluginConfigMap tells the plugin generator where to reach dotvirt's
// /api/v1/getparams.execute and which token to present (Argo resolves the token
// from the mirrored dotvirt-appset-plugin Secret in the ArgoCD namespace).
func AppsetPluginConfigMap(dv *dotvirtv1alpha1.Dotvirt, argoNS, dotvirtNS string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: AppsetConfigMapName, Namespace: argoNS, Labels: Labels(dv.Name)},
		Data: map[string]string{
			"baseUrl": fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", AppName, dotvirtNS, HTTPPort),
			"token":   "$" + AppsetConfigMapName + ":token",
		},
	}
}
