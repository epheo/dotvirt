package install

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// The workload primitives shared by the operand and the managed Forgejo, so the
// two deployments cannot drift apart in posture (securityContext, probes) or in
// the boilerplate around SAs, PVCs and Services.

func serviceAccount(name string, dv *dotvirtv1alpha1.Dotvirt) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: objectMeta(name, dv.Namespace, dv.Name),
	}
}

func pvc(name string, dv *dotvirtv1alpha1.Dotvirt, size string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: objectMeta(name, dv.Namespace, dv.Name),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}

func service(name string, dv *dotvirtv1alpha1.Dotvirt, selector map[string]string, port int32) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(name, dv.Namespace, dv.Name),
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port)}},
		},
	}
}

// hardenedPodSecurityContext is dotvirt's standard restricted-v2-compatible pod
// context. On vanilla Kubernetes a fixed fsGroup makes a PVC group-writable for a
// non-root image UID. On OpenShift fsGroup MUST be omitted: restricted-v2 rejects
// any fsGroup outside the namespace's assigned range and injects its own, so the
// caller passes setFSGroup=false there (verified live: fsGroup:1000 fails
// admission with "1000 is not an allowed group").
func hardenedPodSecurityContext(setFSGroup bool) *corev1.PodSecurityContext {
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

// hardenedContainerSecurityContext drops all capabilities and forbids privilege
// escalation.
func hardenedContainerSecurityContext() *corev1.SecurityContext {
	noPrivilegeEscalation := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noPrivilegeEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// healthProbe probes /api/healthz (both the operand and Forgejo serve it) with a
// generous 5s timeout: the default 1s kills a merely-busy service (the
// SQLite-backed forge under clone bursts), and restarting it makes the overload
// worse. failureThreshold 0 keeps the API default.
func healthProbe(port, delay, period, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(port)},
		},
		InitialDelaySeconds: delay,
		PeriodSeconds:       period,
		TimeoutSeconds:      5,
		FailureThreshold:    failureThreshold,
	}
}
