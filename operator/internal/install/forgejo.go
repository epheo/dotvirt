package install

import (
	"net/url"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// Managed (eval) Forgejo: deployed in the dotvirt namespace, reachable in-cluster
// via its Service (no Route — git + API are cluster-internal; eval-grade, single
// replica). The bootstrap was verified live against a real Forgejo (see the
// initContainer below). For production, bring your own forge instead.
const (
	// Pinned by digest (the codeberg.org/forgejo/forgejo:11 tag) so the eval forge is
	// reproducible and declarable in the CSV's relatedImages. Re-pin manually when the
	// upstream :11 tag moves. The s6 image starts as root, so this pod keeps the anyuid
	// SCC by design (see ForgejoAnyuidBinding) — it is NOT hardened to non-root.
	ForgejoImage       = "codeberg.org/forgejo/forgejo@sha256:d98d860ea64fd36cb0aabf0b46bbe1a37566b498eee4af0a6b246d5a45759d6d"
	ForgejoSAName      = "dotvirt-forgejo"
	ForgejoAdminSecret = "dotvirt-forgejo-admin" // generated admin password (key "password")
	ForgejoPVCName     = "dotvirt-forgejo-data"
	ForgejoServiceName = "dotvirt-forgejo"
	ForgejoBotUser     = "dotvirt-bot" // the service user the operator mints a token for
)

var forgejoSelector = map[string]string{"app": ForgejoServiceName}

// ForgejoServiceURL is the in-cluster base URL of the managed Forgejo.
func ForgejoServiceURL(dv *dotvirtv1alpha1.Dotvirt) string {
	return "http://" + ForgejoServiceName + "." + dv.Namespace + ".svc:3000"
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

// ForgejoServiceAccount runs the Forgejo pod; on OpenShift it's bound to anyuid.
func ForgejoServiceAccount(dv *dotvirtv1alpha1.Dotvirt) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: objectMeta(ForgejoSAName, dv.Namespace, dv.Name),
	}
}

// ForgejoAnyuidBinding grants the Forgejo SA the anyuid SCC (OpenShift only) — the
// s6 image starts as root, which the restricted SCC forbids (verified: it
// CrashLoops otherwise). A no-op concept on vanilla Kubernetes (caller skips it).
func ForgejoAnyuidBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: objectMeta(ForgejoSAName+"-anyuid", dv.Namespace, dv.Name),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:openshift:scc:anyuid"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: ForgejoSAName, Namespace: dv.Namespace}},
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
			Ports:    []corev1.ServicePort{{Name: "http", Port: 3000, TargetPort: intstr.FromInt32(3000)}},
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

// forgejoEnv is the config shared by the init + main containers.
func forgejoEnv(dv *dotvirtv1alpha1.Dotvirt) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "FORGEJO__security__INSTALL_LOCK", Value: "true"},
		{Name: "FORGEJO__database__DB_TYPE", Value: "sqlite3"},
		{Name: "GITEA_CUSTOM", Value: "/data/gitea"},
		{Name: "FORGEJO__server__ROOT_URL", Value: ForgejoExternalURL(dv) + "/"},
	}
}

// ForgejoDeployment runs Forgejo with a one-shot bootstrap initContainer that, on a
// fresh volume, generates the config, migrates the DB, and creates the admin service
// user — the exact sequence verified live. The main container then serves on the
// prepared data. The operator mints the API token afterward (it can't exec).
func ForgejoDeployment(dv *dotvirtv1alpha1.Dotvirt) *appsv1.Deployment {
	replicas := int32(1)
	dataMount := corev1.VolumeMount{Name: "data", MountPath: "/data"}
	adminPW := corev1.EnvVar{Name: "ADMIN_PW", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ForgejoAdminSecret}, Key: "password",
		},
	}}
	bootstrap := `set -e
mkdir -p /data/gitea/conf
chown -R git:git /data
su-exec git environment-to-ini
su-exec git forgejo migrate
su-exec git forgejo admin user create --admin --username ` + ForgejoBotUser +
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
					InitContainers: []corev1.Container{{
						Name:         "bootstrap",
						Image:        ForgejoImage,
						Command:      []string{"sh", "-c", bootstrap},
						Env:          append(forgejoEnv(dv), adminPW),
						VolumeMounts: []corev1.VolumeMount{dataMount},
						Resources:    forgejoResources(),
					}},
					Containers: []corev1.Container{{
						Name:         "forgejo",
						Image:        ForgejoImage,
						Env:          forgejoEnv(dv),
						Ports:        []corev1.ContainerPort{{Name: "http", ContainerPort: 3000}},
						VolumeMounts: []corev1.VolumeMount{dataMount},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(3000)}},
							InitialDelaySeconds: 8,
							PeriodSeconds:       5,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(3000)}},
							InitialDelaySeconds: 30,
							PeriodSeconds:       20,
						},
						Resources: forgejoResources(),
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ForgejoPVCName}},
					}},
				},
			},
		},
	}
}

// ForgejoRoute exposes the managed Forgejo externally on OpenShift (edge TLS), so
// the forge UI + PRs are reviewable in a browser and the repos are clonable
// off-cluster. Mirrors the app Route; targets Forgejo's http port.
func ForgejoRoute(dv *dotvirtv1alpha1.Dotvirt, host string) *unstructured.Unstructured {
	spec := map[string]any{
		"to":   map[string]any{"kind": "Service", "name": ForgejoServiceName},
		"port": map[string]any{"targetPort": "http"},
		"tls":  map[string]any{"termination": "edge", "insecureEdgeTerminationPolicy": "Redirect"},
	}
	if host != "" {
		spec["host"] = host
	}
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	u.SetName(ForgejoServiceName)
	u.SetNamespace(dv.Namespace)
	u.SetLabels(Labels(dv.Name))
	u.Object["spec"] = spec
	return u
}

// ForgejoIngress exposes the managed Forgejo on vanilla Kubernetes. TLS is left to
// the cluster's ingress controller / cert-manager.
func ForgejoIngress(dv *dotvirtv1alpha1.Dotvirt, host string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: objectMeta(ForgejoServiceName, dv.Namespace, dv.Name),
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: ForgejoServiceName,
									Port: networkingv1.ServiceBackendPort{Number: 3000},
								},
							},
						}},
					},
				},
			}},
		},
	}
}
