package install

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// DefaultImage is deployed when the Dotvirt spec doesn't pin one.
const DefaultImage = "registry.desku.be/dotvirt:8780d07"

// Secret names the operator generates (session, appset) or expects (the forge
// credential — overridable via spec.forge.credentialsSecret).
const (
	SessionSecretName     = "dotvirt-session"
	AppsetSecretName      = "dotvirt-appset-plugin"
	WebhookSecretName     = "dotvirt-webhook"
	ArgoWebhookSecretName = "dotvirt-argo-webhook"
	DefaultForgeSecret    = "dotvirt-forge"
)

func secretEnv(name, secret, key string, optional bool) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
			Optional:             &optional,
		},
	}}
}

// selectorLabels is the immutable Deployment/Service selector. It uses the legacy
// `app: dotvirt` label so the operator ADOPTS an existing (hand-installed)
// Deployment in place rather than colliding with its immutable selector.
var selectorLabels = map[string]string{"app": AppName}

func objectMeta(name, namespace, instance string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: Labels(instance)}
}

// podLabels merges the recommended labels with the immutable selector label, so the
// pods carry app.kubernetes.io/* AND match the Deployment/Service selector.
func podLabels(instance string) map[string]string {
	m := Labels(instance)
	for k, v := range selectorLabels {
		m[k] = v
	}
	return m
}

// ServiceAccount is dotvirt's runtime identity (TokenReview, SA reads, Argo re-sync).
func ServiceAccount(dv *dotvirtv1alpha1.Dotvirt) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: objectMeta(AppName, dv.Namespace, dv.Name),
	}
}

// DraftsPVC persists per-(user,project) drafts across restarts (single replica).
func DraftsPVC(dv *dotvirtv1alpha1.Dotvirt) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: objectMeta(AppName+"-drafts", dv.Namespace, dv.Name),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
}

// Service exposes dotvirt's HTTP port (the UI + API at one origin).
func Service(dv *dotvirtv1alpha1.Dotvirt) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(AppName, dv.Namespace, dv.Name),
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels,
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	}
}

// Deployment runs the dotvirt binary (which also serves the SPA). The image, args,
// platform-repo + metrics config, and the drafts volume are wired here; the
// secret-backed env (git/forge/session/appset credentials) and the metrics-CA mount
// land in the follow-up chunk alongside the secret generation.
func Deployment(dv *dotvirtv1alpha1.Dotvirt) *appsv1.Deployment {
	image := dv.Spec.Image
	if image == "" {
		image = DefaultImage
	}

	env := []corev1.EnvVar{}
	if dv.Spec.Forge.PlatformRepo != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_PLATFORM_REPO", Value: dv.Spec.Forge.PlatformRepo})
	}
	if dv.Spec.Ingress.Host != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_PUBLIC_URL", Value: "https://" + dv.Spec.Ingress.Host})
	}
	if dv.Spec.Metrics.URL != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_METRICS_URL", Value: dv.Spec.Metrics.URL})
	}

	forgeSecret := dv.Spec.Forge.CredentialsSecret
	if forgeSecret == "" {
		forgeSecret = DefaultForgeSecret
	}
	env = append(env,
		secretEnv("DOTVIRT_SESSION_SECRET", SessionSecretName, "secret", false),
		secretEnv("DOTVIRT_APPSET_PLUGIN_TOKEN", AppsetSecretName, "token", true),
		secretEnv("DOTVIRT_GIT_USERNAME", forgeSecret, "username", false),
		secretEnv("DOTVIRT_GIT_TOKEN", forgeSecret, "token", false),
		secretEnv("DOTVIRT_FORGE_URL", forgeSecret, "url", false),
		secretEnv("DOTVIRT_FORGE_TOKEN", forgeSecret, "token", false),
		// With a public URL + this secret, dotvirt self-registers its webhook on each
		// project repo (forge -> dotvirt: instant inventory updates vs polling).
		secretEnv("DOTVIRT_WEBHOOK_SECRET", WebhookSecretName, "secret", true),
	)

	replicas := int32(1)
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(AppName, dv.Namespace, dv.Name),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// The RWO drafts PVC can't be mounted by two pods at once.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(dv.Name)},
				Spec: corev1.PodSpec{
					ServiceAccountName: AppName,
					Containers: []corev1.Container{{
						Name:  AppName,
						Image: image,
						Args: []string{
							"-addr=:8080",
							"-ui-origin=", // same-origin: the binary serves the SPA
							"-argo=true",
							"-draft-dir=/var/lib/dotvirt/drafts",
						},
						Env:   env,
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "drafts", MountPath: "/var/lib/dotvirt/drafts"},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(8080)},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "drafts",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: AppName + "-drafts"},
						},
					}},
				},
			},
		},
	}
}
