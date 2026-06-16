package install

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	ForgejoImage       = "codeberg.org/forgejo/forgejo:11"
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

// forgejoEnv is the config shared by the init + main containers.
func forgejoEnv(dv *dotvirtv1alpha1.Dotvirt) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "FORGEJO__security__INSTALL_LOCK", Value: "true"},
		{Name: "FORGEJO__database__DB_TYPE", Value: "sqlite3"},
		{Name: "GITEA_CUSTOM", Value: "/data/gitea"},
		{Name: "FORGEJO__server__ROOT_URL", Value: ForgejoServiceURL(dv) + "/"},
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
