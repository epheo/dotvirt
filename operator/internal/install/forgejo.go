package install

import (
	"net/url"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// Managed (eval) Forgejo: deployed in the dotvirt namespace, reachable in-cluster
// via its Service (no Route — git + API are cluster-internal; eval-grade, single
// replica). The bootstrap was verified live against a real Forgejo (see the
// initContainer below). For production, bring your own forge instead.
const (
	// Pinned by digest (the codeberg.org/forgejo/forgejo:11-rootless tag) so the eval
	// forge is reproducible and declarable in the CSV's relatedImages. Re-pin manually
	// when the upstream :11-rootless tag moves. The ROOTLESS image runs non-root and
	// admits to OpenShift's restricted-v2 SCC with no anyuid grant (verified live: an
	// arbitrary injected UID with gid 0 completes migrate + admin-create + serve), so
	// it carries dotvirt's standard hardened securityContext like the operand does.
	ForgejoImage       = "codeberg.org/forgejo/forgejo:11-rootless@sha256:5135f11de848bea6d59c0a96688e90c361380ba102bdc08dbd5aa52cca2b179b"
	ForgejoSAName      = "dotvirt-forgejo"
	ForgejoAdminSecret = "dotvirt-forgejo-admin" // generated admin password (key "password")
	ForgejoPVCName     = "dotvirt-forgejo-data"
	ForgejoServiceName = "dotvirt-forgejo"
	ForgejoBotUser     = "dotvirt-bot" // the service user the operator mints a token for
)

// ForgejoHTTPPort is the managed Forgejo's single HTTP port — the one source for
// its Service, the container port and probes, and every URL/exposure built to it.
const ForgejoHTTPPort int32 = 3000

var forgejoSelector = map[string]string{"app": ForgejoServiceName}

// ForgejoServiceURL is the in-cluster base URL of the managed Forgejo.
func ForgejoServiceURL(dv *dotvirtv1alpha1.Dotvirt) string {
	return svcURL(ForgejoServiceName, dv.Namespace, ForgejoHTTPPort)
}

// ForgejoExternalURL is the browser/clone-facing base URL: the configured
// spec.forge.url when set (exposed via Route/Ingress, so the forge UI and PRs are
// reviewable off-cluster), else the in-cluster Service URL (internal-only eval).
func ForgejoExternalURL(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.URL != "" {
		return strings.TrimRight(dv.Spec.Forge.URL, "/")
	}
	return ForgejoServiceURL(dv)
}

// ForgejoHost is the external hostname to expose the managed Forgejo on, derived
// from spec.forge.url. Empty when no external URL is set (internal-only).
func ForgejoHost(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.URL == "" {
		return ""
	}
	if u, err := url.Parse(dv.Spec.Forge.URL); err == nil {
		return u.Host
	}
	return ""
}

// ForgejoServiceAccount runs the Forgejo pod under dotvirt's hardened, non-root
// securityContext — no SCC grant required (the rootless image needs none).
func ForgejoServiceAccount(dv *dotvirtv1alpha1.Dotvirt) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: objectMeta(ForgejoSAName, dv.Namespace, dv.Name),
	}
}

// ForgejoPVC holds Forgejo's data. NOT owner-referenced by the caller — orphaned on
// uninstall so the platform repo's git data survives (the operator owns Forgejo's
// lifecycle, not its data).
func ForgejoPVC(dv *dotvirtv1alpha1.Dotvirt) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: objectMeta(ForgejoPVCName, dv.Namespace, dv.Name),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
			},
		},
	}
}

// ForgejoService exposes Forgejo's HTTP port in-cluster.
func ForgejoService(dv *dotvirtv1alpha1.Dotvirt) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(ForgejoServiceName, dv.Namespace, dv.Name),
		Spec: corev1.ServiceSpec{
			Selector: forgejoSelector,
			Ports:    []corev1.ServicePort{{Name: "http", Port: ForgejoHTTPPort, TargetPort: intstr.FromInt32(ForgejoHTTPPort)}},
		},
	}
}

// forgejoResources bounds the eval forge — modest single-replica sizing, shared by
// the bootstrap init and main containers.
func forgejoResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
}

// forgejoEnv is the config shared by the init + main containers. It does NOT override
// GITEA_CUSTOM/GITEA_WORK_DIR: the rootless image's defaults all live under
// /var/lib/gitea (the one PVC mount). Overriding to a custom path is what breaks the
// rootless image's arbitrary-UID writability — keep the defaults.
func forgejoEnv(dv *dotvirtv1alpha1.Dotvirt) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "FORGEJO__security__INSTALL_LOCK", Value: "true"},
		{Name: "FORGEJO__database__DB_TYPE", Value: "sqlite3"},
		{Name: "FORGEJO__server__ROOT_URL", Value: ForgejoExternalURL(dv) + "/"},
		// dotvirt's webhook is delivered to its in-cluster Service; Forgejo's SSRF guard
		// blocks private targets by default, so allow that host — keeping `external` so
		// delivery to any public webhook still works.
		{Name: "FORGEJO__webhook__ALLOWED_HOST_LIST", Value: ServiceHost(dv) + ",external"},
		// Skip webhook TLS verification globally for this managed Forgejo. It's required by
		// the ArgoCD-direct backstop (a fallback to dotvirt's RefreshForRepo), which targets
		// ArgoCD's EXTERNAL Route — off-cluster, served by an ingress CA Forgejo doesn't
		// trust. Being global, it also relaxes verification for any tenant-added external
		// webhook on this forge: an accepted trade-off for the eval-grade managed forge, not
		// the bounded in-cluster exposure the name might suggest. (dotvirt's own webhook
		// needs no exemption — it's delivered to the in-cluster Service over plain HTTP.)
		{Name: "FORGEJO__webhook__SKIP_TLS_VERIFY", Value: "true"},
	}
}

// ForgejoDeployment runs the rootless Forgejo with a one-shot bootstrap initContainer
// that, on a fresh volume, migrates the DB and creates the admin service user — the
// exact sequence verified live as an arbitrary OpenShift-injected UID. The main
// container then serves on the prepared data. The operator mints the API token
// afterward (it can't exec).
//
// No chown/su-exec: the container already runs as the unprivileged user, and the data
// dir is group-writable (gid 0 on OpenShift via the SCC; fsGroup on vanilla K8s). The
// PVC mounts at the image's default GITEA_WORK_DIR (/var/lib/gitea); /etc/gitea is the
// image's other declared volume, backed by an emptyDir.
func ForgejoDeployment(dv *dotvirtv1alpha1.Dotvirt, setFSGroup bool) *appsv1.Deployment {
	replicas := int32(1)
	forgejoImg := imageFromEnv("RELATED_IMAGE_FORGEJO", ForgejoImage)
	dataMount := corev1.VolumeMount{Name: "data", MountPath: "/var/lib/gitea"}
	etcMount := corev1.VolumeMount{Name: "etc", MountPath: "/etc/gitea"}
	adminPW := corev1.EnvVar{Name: "ADMIN_PW", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ForgejoAdminSecret}, Key: "password",
		},
	}}
	// environment-to-ini renders app.ini from the FORGEJO__* env (the rootless image's
	// entrypoint normally does this; we override the command, so run it ourselves).
	// migrate then has a config to load. No chown/su-exec: already the non-root user.
	bootstrap := `set -e
mkdir -p "$(dirname "$GITEA_APP_INI")"
environment-to-ini
forgejo migrate
forgejo admin user create --admin --username ` + ForgejoBotUser +
		` --password "$ADMIN_PW" --email ` + ForgejoBotUser + `@dotvirt.local --must-change-password=false || true`

	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(ForgejoServiceName, dv.Namespace, dv.Name),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, // RWO data
			Selector: &metav1.LabelSelector{MatchLabels: forgejoSelector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: forgejoSelector},
				Spec: corev1.PodSpec{
					ServiceAccountName: ForgejoSAName,
					SecurityContext:    forgejoPodSecurityContext(setFSGroup),
					InitContainers: []corev1.Container{{
						Name:            "bootstrap",
						Image:           forgejoImg,
						Command:         []string{"sh", "-c", bootstrap},
						Env:             append(forgejoEnv(dv), adminPW),
						VolumeMounts:    []corev1.VolumeMount{dataMount, etcMount},
						Resources:       forgejoResources(),
						SecurityContext: forgejoContainerSecurityContext(),
					}},
					Containers: []corev1.Container{{
						Name:         "forgejo",
						Image:        forgejoImg,
						Env:          forgejoEnv(dv),
						Ports:        []corev1.ContainerPort{{Name: "http", ContainerPort: ForgejoHTTPPort}},
						VolumeMounts: []corev1.VolumeMount{dataMount, etcMount},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(ForgejoHTTPPort)}},
							InitialDelaySeconds: 8,
							PeriodSeconds:       5,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(ForgejoHTTPPort)}},
							InitialDelaySeconds: 30,
							PeriodSeconds:       20,
						},
						Resources:       forgejoResources(),
						SecurityContext: forgejoContainerSecurityContext(),
					}},
					Volumes: []corev1.Volume{
						{
							Name:         "data",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ForgejoPVCName}},
						},
						{Name: "etc", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

// forgejoPodSecurityContext is dotvirt's standard restricted-v2-compatible pod
// context. On vanilla Kubernetes a fixed fsGroup makes the PVC group-writable for the
// image's non-root UID. On OpenShift fsGroup MUST be omitted — restricted-v2 rejects
// any fsGroup outside the namespace's assigned range and injects its own, so the
// caller passes setFSGroup=false there (verified live: fsGroup:1000 → admission
// "1000 is not an allowed group").
func forgejoPodSecurityContext(setFSGroup bool) *corev1.PodSecurityContext {
	runAsNonRoot := true
	sc := &corev1.PodSecurityContext{
		RunAsNonRoot:   &runAsNonRoot,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if setFSGroup {
		fsGroup := int64(1000)
		sc.FSGroup = &fsGroup
	}
	return sc
}

// forgejoContainerSecurityContext drops all capabilities and forbids privilege
// escalation — the operand's posture, now shared by the rootless forge.
func forgejoContainerSecurityContext() *corev1.SecurityContext {
	noPrivilegeEscalation := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noPrivilegeEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}
