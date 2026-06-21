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

// envValue returns the value of the named env var (ok=false if absent).
func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// DOTVIRT_WEBHOOK_URL points the forge at dotvirt's in-cluster Service ONLY for a managed
// (in-cluster) Forgejo, which can't hairpin to the external Route. A bring-your-own forge
// is typically off-cluster and can't reach that Service URL, so the var is left unset and
// the app falls back to its public URL — otherwise dotvirt registers an unreachable hook.
func TestWebhookURLGatedOnManagedForge(t *testing.T) {
	managed := testDotvirt()
	managed.Spec.Forge.Managed = true
	if got, ok := envValue(Deployment(managed).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_WEBHOOK_URL"); !ok || got != ServiceURL(managed) {
		t.Errorf("managed forge: DOTVIRT_WEBHOOK_URL = (%q, ok=%v), want %q", got, ok, ServiceURL(managed))
	}

	byo := testDotvirt() // Forge.Managed defaults to false
	if got, ok := envValue(Deployment(byo).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_WEBHOOK_URL"); ok {
		t.Errorf("BYO forge: DOTVIRT_WEBHOOK_URL must be unset (app falls back to public URL), got %q", got)
	}
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

// The managed Forgejo uses the rootless image under dotvirt's restricted-v2 posture
// (no anyuid): non-root, no privilege escalation, all caps dropped, an fsGroup for
// PVC writability — plus bounded, probed, and digest-pinned.
func TestForgejoDeploymentBoundedAndPinned(t *testing.T) {
	d := ForgejoDeployment(testDotvirt(), true)
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

	ps := d.Spec.Template.Spec.SecurityContext
	if ps == nil || ps.RunAsNonRoot == nil || !*ps.RunAsNonRoot {
		t.Error("forgejo pod securityContext must set runAsNonRoot=true (no anyuid)")
	}
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("forgejo container must set allowPrivilegeEscalation=false")
	}
	if sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("forgejo container must drop ALL capabilities")
	}
}

// fsGroup is set on vanilla K8s (PVC writability) but MUST be omitted on OpenShift,
// where restricted-v2 rejects an out-of-range fsGroup and injects its own.
func TestForgejoFSGroupIsPlatformConditional(t *testing.T) {
	if fg := ForgejoDeployment(testDotvirt(), true).Spec.Template.Spec.SecurityContext.FSGroup; fg == nil {
		t.Error("vanilla K8s (setFSGroup=true): fsGroup must be set")
	}
	if fg := ForgejoDeployment(testDotvirt(), false).Spec.Template.Spec.SecurityContext.FSGroup; fg != nil {
		t.Errorf("OpenShift (setFSGroup=false): fsGroup must be nil, got %d", *fg)
	}
}
