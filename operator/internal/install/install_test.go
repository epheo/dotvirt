package install

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

func testDotvirt() *dotvirtv1alpha1.Dotvirt {
	return &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}}
}

// The operand must render restricted-v2 compatible — non-root, no privilege
// escalation, all capabilities dropped, bounded resources, and a liveness probe — so
// OpenShift's restricted SCC admits it WITHOUT the anyuid grant.
func TestDeploymentSecurityHardening(t *testing.T) {
	d := Deployment(testDotvirt())

	ps := d.Spec.Template.Spec.SecurityContext
	if ps == nil || ps.RunAsNonRoot == nil || !*ps.RunAsNonRoot {
		t.Error("pod securityContext must set runAsNonRoot=true")
	}
	if ps == nil || ps.SeccompProfile == nil || ps.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("pod securityContext must set seccompProfile=RuntimeDefault")
	}

	c := d.Spec.Template.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("container must set allowPrivilegeEscalation=false")
	}
	if sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("container must drop ALL capabilities")
	}
	if _, ok := c.Resources.Requests[corev1.ResourceMemory]; !ok {
		t.Error("container must set a memory request")
	}
	if _, ok := c.Resources.Limits[corev1.ResourceMemory]; !ok {
		t.Error("container must set a memory limit")
	}
	if c.LivenessProbe == nil {
		t.Error("container must set a liveness probe")
	}
}

// The managed Forgejo keeps its root/anyuid posture by design (the s6 image starts as
// root), but it must still be bounded, probed, and pinned by digest.
func TestForgejoDeploymentBoundedAndPinned(t *testing.T) {
	d := ForgejoDeployment(testDotvirt())
	c := d.Spec.Template.Spec.Containers[0]
	if c.LivenessProbe == nil {
		t.Error("forgejo must set a liveness probe")
	}
	if _, ok := c.Resources.Limits[corev1.ResourceMemory]; !ok {
		t.Error("forgejo must set a memory limit")
	}
	if !strings.Contains(ForgejoImage, "@sha256:") {
		t.Errorf("ForgejoImage must be digest-pinned, got %q", ForgejoImage)
	}
}
