package install

import (
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// defaultImage is deployed when the Dotvirt spec doesn't pin one.
const defaultImage = "quay.io/epheo/dotvirt@sha256:ba55ef5d0b9894d7b09b8961a630c6b0bfe208dc1293259c7ae8fbbd9ac497b2"

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
// credential - overridable via spec.forge.credentialsSecret).
const (
	SessionSecretName     = "dotvirt-session"
	AppsetSecretName      = "dotvirt-appset-plugin"
	WebhookSecretName     = "dotvirt-webhook"
	ArgoWebhookSecretName = "dotvirt-argo-webhook"
	defaultForgeSecret    = "dotvirt-forge"
	// OAuthSecretName holds the operator-generated OAuth client secret (key clientSecret).
	OAuthSecretName   = "dotvirt-oauth"
	oauthClientPrefix = "dotvirt"

	// IngressCAConfigMap: in-namespace copy of the default ingress CA, mounted so
	// the app and the managed Forgejo VERIFY router-served TLS.
	IngressCAConfigMap = "dotvirt-ingress-ca"
	// ServiceCAConfigMap is injector-filled (inject-cabundle) and verifies
	// in-cluster serving certs (the sample's thanos-querier).
	ServiceCAConfigMap = "dotvirt-service-ca"

	ingressCAMountPath = "/var/run/dotvirt/tls-ingress"
	serviceCAMountPath = "/var/run/dotvirt/tls-service"
	// Keys mirror default-ingress-cert and the injector respectively.
	IngressCAKey = "ca-bundle.crt"
	ServiceCAKey = "service-ca.crt"
)

// OAuthClientName is the OpenShift OAuthClient this install registers as. An OAuthClient
// is CLUSTER-scoped, so the name carries the install's namespace: a shared constant would
// have a second install overwrite the first's redirect URI and secret, breaking its SSO
// with nothing to say why.
func OAuthClientName(namespace string) string {
	return oauthClientPrefix + "-" + namespace
}

// ForgeSecretName is the forge-credential Secret for this install: the spec override,
// else the default the managed-Forgejo bootstrap writes.
func ForgeSecretName(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.CredentialsSecret != "" {
		return dv.Spec.Forge.CredentialsSecret
	}
	return defaultForgeSecret
}

// ForgeConfigured reports whether the install has any forge to wire the app to: a
// managed Forgejo, an explicit URL, or a BYO credentials secret. When false the app
// runs push-only, and the Deployment omits the forge credential env + mount so the pod
// doesn't wedge on a dotvirt-forge secret that will never be written.
func ForgeConfigured(dv *dotvirtv1alpha1.Dotvirt) bool {
	// A credential, not a URL: the app reads BOTH the forge URL and the token from this
	// secret for a BYO forge, and only a managed forge or an explicit credentialsSecret
	// guarantees one exists. Counting a bare spec.forge.url would mount a Secret nothing
	// writes, wedging the pod in CreateContainerConfigError.
	return dv.Spec.Forge.Managed || dv.Spec.Forge.CredentialsSecret != ""
}

// forgeTokenMountPath is where the forge credential secret's "token" key is
// projected into the app container (read per call -> rotation-safe).
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
	return serviceAccount(AppName, dv)
}

// DraftsPVC persists per-(user,project) drafts across restarts (single replica).
func DraftsPVC(dv *dotvirtv1alpha1.Dotvirt) *corev1.PersistentVolumeClaim {
	return pvc(AppName+"-drafts", dv, "1Gi")
}

// Service exposes dotvirt's HTTP port (the UI + API at one origin).
func Service(dv *dotvirtv1alpha1.Dotvirt) *corev1.Service {
	return service(AppName, dv, selectorLabels, HTTPPort)
}

// serviceHost is dotvirt's in-cluster DNS host and ServiceURL its base URL. A managed
// forge delivers webhooks here, not to the external Route: an in-cluster Forgejo can't
// hairpin to the Route and doesn't trust its CA. dotvirt serves plain HTTP; the delivery
// is still authenticated by HMAC.
func serviceHost(dv *dotvirtv1alpha1.Dotvirt) string { return svcHost(AppName, dv.Namespace) }
func ServiceURL(dv *dotvirtv1alpha1.Dotvirt) string  { return svcURL(AppName, dv.Namespace, HTTPPort) }

// svcHost and svcURL build the in-cluster DNS host / base URL for a Service -
// `<name>.<ns>.svc[:port]` - so the template lives in one place.
func svcHost(name, namespace string) string { return name + "." + namespace + ".svc" }
func svcURL(name, namespace string, port int32) string {
	return fmt.Sprintf("http://%s:%d", svcHost(name, namespace), port)
}

// dotvirtEnv assembles the app container's env from the effective spec: the
// platform-repo + metrics config and the secret-backed credentials.
func dotvirtEnv(dv *dotvirtv1alpha1.Dotvirt) []corev1.EnvVar {
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
	// leave this unset for it - the app then falls back to DOTVIRT_PUBLIC_URL (the
	// external host the forge can reach), or skips self-registration if there's no public
	// URL either.
	if dv.Spec.Forge.Managed {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_WEBHOOK_URL", Value: ServiceURL(dv)})
	}
	if dv.Spec.Metrics.URL != "" {
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_METRICS_URL", Value: dv.Spec.Metrics.URL})
	}
	// OpenShift SSO: the operator generates the client secret and wires it here; the admin
	// applies the OAuthClient (a cluster-admin act it deliberately doesn't perform; no
	// oauthclients grant), reported in status.ssoOAuthClient. optional=true: enabling SSO
	// before the OAuthClient is applied must not wedge the pod.
	if dv.Spec.Auth.OpenShiftSSO {
		env = append(env,
			corev1.EnvVar{Name: "DOTVIRT_OAUTH_CLIENT_ID", Value: OAuthClientName(dv.Namespace)},
			secretEnv("DOTVIRT_OAUTH_CLIENT_SECRET", OAuthSecretName, "clientSecret", true),
		)
	}

	env = append(env,
		secretEnv("DOTVIRT_SESSION_SECRET", SessionSecretName, "secret", false),
		secretEnv("DOTVIRT_APPSET_PLUGIN_TOKEN", AppsetSecretName, "token", true),
		// With a public URL + this secret, dotvirt self-registers its webhook on each
		// project repo (forge -> dotvirt: instant inventory updates vs polling).
		secretEnv("DOTVIRT_WEBHOOK_SECRET", WebhookSecretName, "secret", true),
	)
	forgeSecret := ForgeSecretName(dv)
	if ForgeConfigured(dv) {
		// A managed forge's credential secret is operator-written, so emit the resolved URL
		// as a LITERAL: a URL change (e.g. the first router host assignment) then rolls the
		// app; a BYO forge reads it from the admin-supplied secret. The token (git https +
		// API, one credential) is MOUNTED, not env: kubelet updates the file in place, so an
		// operator re-mint/rotation reaches the app without a restart (env freezes at start).
		env = append(env, secretEnv("DOTVIRT_GIT_USERNAME", forgeSecret, "username", false))
		if dv.Spec.Forge.Managed {
			env = append(env, corev1.EnvVar{Name: "DOTVIRT_FORGE_URL", Value: dv.Spec.Forge.URL})
		} else {
			env = append(env, secretEnv("DOTVIRT_FORGE_URL", forgeSecret, "url", false))
		}
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_FORGE_TOKEN_FILE", Value: forgeTokenMountPath})
		if dv.Spec.Forge.Managed {
			// Verified TLS instead of insecureTLS; an explicit insecureTLS still wins
			// inside the app, and CA loads are tolerant of a lagging mount.
			env = append(env, corev1.EnvVar{Name: "DOTVIRT_FORGE_CA", Value: ingressCAMountPath + "/" + IngressCAKey})
		}
	}
	if dv.Spec.Auth.OpenShiftSSO {
		// The oauth token endpoint is router-served: same ingress CA.
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_OAUTH_CA", Value: ingressCAMountPath + "/" + IngressCAKey})
	}
	if strings.HasPrefix(dv.Spec.Metrics.URL, "https://") && strings.Contains(dv.Spec.Metrics.URL, ".svc") {
		// In-cluster metrics serve the service-CA-signed cert.
		env = append(env, corev1.EnvVar{Name: "DOTVIRT_METRICS_CA", Value: serviceCAMountPath + "/" + ServiceCAKey})
	}
	return env
}

func dotvirtArgs(dv *dotvirtv1alpha1.Dotvirt) []string {
	args := []string{
		fmt.Sprintf("-addr=:%d", HTTPPort),
		"-ui-origin=", // same-origin: the binary serves the SPA
		"-argo=true",
		"-draft-dir=/var/lib/dotvirt/drafts",
		// Webhooks (self-registered above) are the primary trigger for inventory
		// updates; the git poll is only the missed-event backstop, so keep it slow
		// to spare the forge - the managed Forgejo is a single SQLite-backed pod.
		"-git-poll-interval=5m",
	}
	if dv.Spec.Forge.InsecureTLS {
		// A self-signed forge Route: skip TLS verification for forge API calls +
		// git clones. Metrics stays verified (its own CA env).
		args = append(args, "-insecure-tls")
	}
	return args
}

// dotvirtVolumes: the drafts PVC, the trust anchors, and (forge-configured) the
// mounted forge token. Trust-anchor mounts are always rendered and OPTIONAL:
// absent ConfigMaps (vanilla, or copy/injection lag) must never block the pod.
func dotvirtVolumes(dv *dotvirtv1alpha1.Dotvirt) ([]corev1.VolumeMount, []corev1.Volume) {
	caOptional := true
	volumeMounts := []corev1.VolumeMount{
		{Name: "drafts", MountPath: "/var/lib/dotvirt/drafts"},
		{Name: "ingress-ca", MountPath: ingressCAMountPath, ReadOnly: true},
		{Name: "service-ca", MountPath: serviceCAMountPath, ReadOnly: true},
	}
	volumes := []corev1.Volume{{
		Name:         "drafts",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: AppName + "-drafts"}},
	}, {
		Name: "ingress-ca",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: IngressCAConfigMap}, Optional: &caOptional,
		}},
	}, {
		Name: "service-ca",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: ServiceCAConfigMap}, Optional: &caOptional,
		}},
	}}
	if ForgeConfigured(dv) {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "forge-token", MountPath: "/var/run/dotvirt/forge", ReadOnly: true})
		volumes = append(volumes, corev1.Volume{
			Name: "forge-token",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: ForgeSecretName(dv),
				Items:      []corev1.KeyToPath{{Key: "token", Path: "token"}},
			}},
		})
	}
	return volumeMounts, volumes
}

// Deployment runs the dotvirt binary (which also serves the SPA), with the forge
// token mounted so a re-mint reaches the app without a restart.
func Deployment(dv *dotvirtv1alpha1.Dotvirt) *appsv1.Deployment {
	image := dv.Spec.Image
	if image == "" {
		image = imageFromEnv("RELATED_IMAGE_DOTVIRT", defaultImage)
	}
	volumeMounts, volumes := dotvirtVolumes(dv)
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
					// Restricted-v2 compatible (the app image is distroless-nonroot). No
					// readOnlyRootFilesystem: the app writes git clones + temp under $HOME,
					// and restricted-v2 doesn't require a read-only root.
					SecurityContext: hardenedPodSecurityContext(false),
					Containers: []corev1.Container{{
						Name:            AppName,
						Image:           image,
						Args:            dotvirtArgs(dv),
						Env:             dotvirtEnv(dv),
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: HTTPPort}},
						VolumeMounts:    volumeMounts,
						SecurityContext: hardenedContainerSecurityContext(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
						},
						ReadinessProbe: healthProbe(HTTPPort, 5, 10, 0),
						LivenessProbe:  healthProbe(HTTPPort, 15, 20, 0),
					}},
					Volumes: volumes,
				},
			},
		},
	}
}
