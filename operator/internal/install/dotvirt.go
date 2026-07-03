package install

import (
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// DefaultImage is deployed when the Dotvirt spec doesn't pin one.
const DefaultImage = "quay.io/epheo/dotvirt@sha256:e66cbb324cff5516d4cf379db31f3aac6b822de50e19a0a2baa5b97a3a498ae2"

// imageFromEnv returns the operand image pinned in the operator's RELATED_IMAGE_* env (set
// from the CSV by OLM, and overridable per-install), falling back to the digest compiled in
// at build time when the env is unset (e.g. `make run`, non-OLM installs).
func imageFromEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Secret names the operator generates (session, appset) or expects (the forge
// credential — overridable via spec.forge.credentialsSecret).
const (
	SessionSecretName     = "dotvirt-session"
	AppsetSecretName      = "dotvirt-appset-plugin"
	WebhookSecretName     = "dotvirt-webhook"
	ArgoWebhookSecretName = "dotvirt-argo-webhook"
	DefaultForgeSecret    = "dotvirt-forge"
)

// ForgeSecretName is the forge-credential Secret for this install: the spec override,
// else the default the managed-Forgejo bootstrap writes.
func ForgeSecretName(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.CredentialsSecret != "" {
		return dv.Spec.Forge.CredentialsSecret
	}
	return DefaultForgeSecret
}

// forgeTokenMountPath is where the forge credential secret's "token" key is
// projected into the app container (read per call → rotation-safe).
const forgeTokenMountPath = "/var/run/dotvirt/forge/token"

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
			Ports:    []corev1.ServicePort{{Name: "http", Port: HTTPPort, TargetPort: intstr.FromInt32(HTTPPort)}},
		},
	}
}

// ServiceHost is dotvirt's in-cluster DNS host and ServiceURL its base URL. A managed
// forge delivers webhooks here, not to the external Route: an in-cluster Forgejo can't
// hairpin to the Route and doesn't trust its CA. dotvirt serves plain HTTP; the delivery
// is still authenticated by HMAC.
func ServiceHost(dv *dotvirtv1alpha1.Dotvirt) string { return svcHost(AppName, dv.Namespace) }
func ServiceURL(dv *dotvirtv1alpha1.Dotvirt) string  { return svcURL(AppName, dv.Namespace, HTTPPort) }

// svcHost and svcURL build the in-cluster DNS host / base URL for a Service —
// `<name>.<ns>.svc[:port]` — so the template lives in one place.
func svcHost(name, namespace string) string { return name + "." + namespace + ".svc" }
func svcURL(name, namespace string, port int32) string {
	return fmt.Sprintf("http://%s:%d", svcHost(name, namespace), port)
}

// Deployment runs the dotvirt binary (which also serves the SPA): the image, args,
// platform-repo + metrics config, the drafts volume, and the secret-backed env
// (git/forge/session/appset credentials, with the forge token mounted so a re-mint
// reaches the app without a restart).
func Deployment(dv *dotvirtv1alpha1.Dotvirt) *appsv1.Deployment {
	image := dv.Spec.Image
	if image == "" {
		image = imageFromEnv("RELATED_IMAGE_DOTVIRT", DefaultImage)
	}

	env := []corev1.EnvVar{}
	if dv.Spec.Forge.PlatformRepo != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_PLATFORM_REPO", Value: dv.Spec.Forge.PlatformRepo})
	}
	if dv.Spec.Ingress.Host != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_PUBLIC_URL", Value: "https://" + dv.Spec.Ingress.Host})
	}
	// A managed (in-cluster) Forgejo delivers webhooks to dotvirt's in-cluster Service,
	// not the external Route (which it can't hairpin to and whose CA it doesn't trust).
	// A bring-your-own forge is typically off-cluster and can't reach that Service URL, so
	// leave this unset for it — the app then falls back to DOTVIRT_PUBLIC_URL (the
	// external host the forge can reach), or skips self-registration if there's no public
	// URL either.
	if dv.Spec.Forge.Managed {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_WEBHOOK_URL", Value: ServiceURL(dv)})
	}
	if dv.Spec.Metrics.URL != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_METRICS_URL", Value: dv.Spec.Metrics.URL})
	}

	forgeSecret := ForgeSecretName(dv)
	env = append(env,
		secretEnv("DOTVIRT_SESSION_SECRET", SessionSecretName, "secret", false),
		secretEnv("DOTVIRT_APPSET_PLUGIN_TOKEN", AppsetSecretName, "token", true),
		secretEnv("DOTVIRT_GIT_USERNAME", forgeSecret, "username", false),
		secretEnv("DOTVIRT_FORGE_URL", forgeSecret, "url", false),
		// The forge token (git https + API, one credential) is MOUNTED, not injected
		// as env: kubelet updates the file in place, so an operator re-mint/rotation
		// reaches the app without a restart (env vars freeze at pod start).
		corev1.EnvVar{Name: "DOTVIRT_FORGE_TOKEN_FILE", Value: forgeTokenMountPath},
		// With a public URL + this secret, dotvirt self-registers its webhook on each
		// project repo (forge -> dotvirt: instant inventory updates vs polling).
		secretEnv("DOTVIRT_WEBHOOK_SECRET", WebhookSecretName, "secret", true),
	)

	args := []string{
		fmt.Sprintf("-addr=:%d", HTTPPort),
		"-ui-origin=", // same-origin: the binary serves the SPA
		"-argo=true",
		"-draft-dir=/var/lib/dotvirt/drafts",
		// Webhooks (self-registered above) are the primary trigger for inventory
		// updates; the git poll is only the missed-event backstop, so keep it slow
		// to spare the forge — the managed Forgejo is a single SQLite-backed pod.
		"-git-poll-interval=5m",
	}
	if dv.Spec.Forge.InsecureTLS {
		// The managed/eval forge Route is self-signed, so the app must skip TLS
		// verification for its forge API calls + git clones — the same flag the
		// manual deploy carried. Metrics stays verified (its own CA env).
		args = append(args, "-insecure-tls")
	}

	replicas := int32(1)
	runAsNonRoot := true
	noPrivilegeEscalation := false
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
					// Restricted-v2 compatible (the app image is distroless-nonroot). No
					// readOnlyRootFilesystem: the app writes git clones + temp under $HOME,
					// and restricted-v2 doesn't require a read-only root.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:  AppName,
						Image: image,
						Args:  args,
						Env:   env,
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: HTTPPort}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "drafts", MountPath: "/var/lib/dotvirt/drafts"},
							{Name: "forge-token", MountPath: "/var/run/dotvirt/forge", ReadOnly: true},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &noPrivilegeEscalation,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(HTTPPort)},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(HTTPPort)},
							},
							InitialDelaySeconds: 15,
							PeriodSeconds:       20,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "drafts",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: AppName + "-drafts"},
						},
					}, {
						Name: "forge-token",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: forgeSecret,
								Items:      []corev1.KeyToPath{{Key: "token", Path: "token"}},
							},
						},
					}},
				},
			},
		},
	}
}
