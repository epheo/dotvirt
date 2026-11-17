package install

import (
	"slices"
	"strings"
	"testing"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// The guard admits the official repositories; the operator's built-in
// defaults must come from them, or an install on defaults is refused.
func TestDefaultImagesAreInTheGuardRepositories(t *testing.T) {
	for _, img := range []string{defaultImage, forgejoImage} {
		repo, _, _ := strings.Cut(img, "@")
		repo, _, _ = strings.Cut(repo, ":")
		if !slices.Contains(adminobjects.Repositories, repo) {
			t.Errorf("default image %s is outside the guard's repositories %v", img, adminobjects.Repositories)
		}
	}
}

// The guard pins what the operator renders by the shared package's values:
// the names and accounts of its Deployments, and the metrics CA paths.
func TestRenderMatchesTheGuardsPins(t *testing.T) {
	for name, c := range map[string][2]string{
		"console Deployment": {AppName, adminobjects.ConsoleDeployment},
		"console account":    {AppName, adminobjects.ConsoleAccount},
		"forge Deployment":   {ForgejoServiceName, adminobjects.ForgeDeployment},
		"forge account":      {forgejoSAName, adminobjects.ForgeAccount},
		"metrics CA":         {metricsCAMountPath + "/" + metricsCAFile, adminobjects.MetricsCAPath},
		"service CA":         {serviceCAMountPath + "/" + ServiceCAKey, adminobjects.ServiceCAPath},
		"router CA":          {ingressCAMountPath + "/" + IngressCAKey, adminobjects.IngressCAPath},
		"router CA map":      {IngressCAConfigMap, adminobjects.IngressCAConfigMap},
		"service CA map":     {ServiceCAConfigMap, adminobjects.ServiceCAConfigMap},
		"drafts claim":       {DraftsPVC(&dotvirtv1alpha1.Dotvirt{}).Name, adminobjects.DraftsClaim},
		"forge Secret":       {defaultForgeSecret, adminobjects.DefaultForgeSecret},
		"forge admin Secret": {ForgejoAdminSecret, adminobjects.ForgeAdminSecret},
		"branding path":      {brandingMountPath, adminobjects.BrandingMountPath},
		"forge claim":        {forgejoPVCName, adminobjects.ForgeClaim},
		"session Secret":     {SessionSecretName, adminobjects.SessionSecret},
		"OAuth Secret":       {OAuthSecretName, adminobjects.OAuthSecret},
		"appset Secret":      {AppsetSecretName, adminobjects.AppsetSecret},
		"webhook Secret":     {WebhookSecretName, adminobjects.WebhookSecret},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: install %q, guard %q", name, c[0], c[1])
		}
	}
}

// The console's volumes and mounts, every one an install may render, are the
// guard's table; the forge reads no Secret but its admin password.
func TestRenderMatchesTheGuardsVolumes(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{}
	dv.Name, dv.Namespace = "x", "dotvirt"
	dv.Spec.Branding.ConfigMap = "brand"
	dv.Spec.Metrics.URL = "https://thanos.example:9091"
	dv.Spec.Metrics.CA = &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: "ca.crt"}
	dv.Spec.Forge.CredentialsSecret = "creds"
	want := map[string]adminobjects.ConsoleVolume{}
	for _, v := range adminobjects.ConsoleVolumes("brand", "metrics-ca", "creds") {
		want[v.Name] = v
	}
	dv.Spec.Auth.OIDC = &dotvirtv1alpha1.OIDCSpec{IssuerURL: "https://idp.example", ClientID: "dotvirt",
		ClientSecret: &dotvirtv1alpha1.SecretKeyRef{Name: "oidc", Key: "client-secret"}}
	pod := Deployment(dv, "argocd", nil).Spec.Template.Spec
	secretEnv := adminobjects.ConsoleSecretEnv("creds", "oidc", "client-secret")
	for _, sso := range []bool{false, true} {
		sd := dv.DeepCopy()
		if sso {
			sd.Spec.Auth.OIDC = nil
			sd.Spec.Auth.OpenShiftSSO = new(true)
		}
		for _, e := range Deployment(sd, "argocd", nil).Spec.Template.Spec.Containers[0].Env {
			if r := e.ValueFrom; r != nil && r.SecretKeyRef != nil && !slices.Contains(secretEnv, [3]string{e.Name, r.SecretKeyRef.Name, r.SecretKeyRef.Key}) {
				t.Errorf("console env %s from %s/%s is not in the guard's %v", e.Name, r.SecretKeyRef.Name, r.SecretKeyRef.Key, secretEnv)
			}
		}
	}
	if len(pod.Volumes) != len(want) {
		t.Errorf("rendered %d volumes, the guard knows %d", len(pod.Volumes), len(want))
	}
	for _, v := range pod.Volumes {
		w, ok := want[v.Name]
		var kind, source string
		switch {
		case v.PersistentVolumeClaim != nil:
			kind, source = "persistentVolumeClaim", v.PersistentVolumeClaim.ClaimName
		case v.ConfigMap != nil:
			kind, source = "configMap", v.ConfigMap.Name
		case v.Secret != nil:
			kind, source = "secret", v.Secret.SecretName
		}
		if !ok || kind != w.Kind || source != w.Source {
			t.Errorf("volume %s from %s %q; the guard wants %+v", v.Name, kind, source, w)
		}
	}
	for _, m := range pod.Containers[0].VolumeMounts {
		if w := want[m.Name]; m.MountPath != w.MountPath || m.SubPath != "" {
			t.Errorf("mount %s at %s (subPath %q); the guard wants %s", m.Name, m.MountPath, m.SubPath, w.MountPath)
		}
	}

	forge := ForgejoDeployment(dv, false, "", "").Spec.Template.Spec
	for _, c := range append(forge.InitContainers, forge.Containers...) {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name != adminobjects.ForgeAdminSecret {
				t.Errorf("forge %s reads Secret %s", c.Name, e.ValueFrom.SecretKeyRef.Name)
			}
		}
		if len(c.EnvFrom) > 0 {
			t.Errorf("forge %s takes envFrom", c.Name)
		}
	}
	for _, v := range forge.Volumes {
		if v.Secret != nil || v.Projected != nil || v.CSI != nil {
			t.Errorf("forge volume %s reads a Secret", v.Name)
		}
	}
}
