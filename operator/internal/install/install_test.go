package install

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
	d := ForgejoDeployment(testDotvirt(), true, "")
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
	if fg := ForgejoDeployment(testDotvirt(), true, "").Spec.Template.Spec.SecurityContext.FSGroup; fg == nil {
		t.Error("vanilla K8s (setFSGroup=true): fsGroup must be set")
	}
	if fg := ForgejoDeployment(testDotvirt(), false, "").Spec.Template.Spec.SecurityContext.FSGroup; fg != nil {
		t.Errorf("OpenShift (setFSGroup=false): fsGroup must be nil, got %d", *fg)
	}
}

// The forge→ArgoCD webhook targets the Argo Route by name, which often resolves
// to a PRIVATE ingress VIP; Forgejo's `external` allowlist entry matches public
// resolved IPs only, so the host must be allowed by name or every delivery is
// silently denied by the SSRF guard. Both containers share the env (the init
// renders app.ini from it).
func TestForgejoWebhookAllowlistIncludesArgoHost(t *testing.T) {
	const argo = "openshift-gitops-server-openshift-gitops.apps.example.com"
	d := ForgejoDeployment(testDotvirt(), false, argo)
	for _, env := range [][]corev1.EnvVar{
		d.Spec.Template.Spec.InitContainers[0].Env,
		d.Spec.Template.Spec.Containers[0].Env,
	} {
		got, ok := envValue(env, "FORGEJO__webhook__ALLOWED_HOST_LIST")
		if !ok || got != ServiceHost(testDotvirt())+",external,"+argo {
			t.Errorf("ALLOWED_HOST_LIST = (%q, ok=%v), want service host + external + argo host", got, ok)
		}
	}

	// No Argo URL resolvable yet: the baseline list, no trailing separator.
	got, _ := envValue(ForgejoDeployment(testDotvirt(), false, "").Spec.Template.Spec.Containers[0].Env,
		"FORGEJO__webhook__ALLOWED_HOST_LIST")
	if got != ServiceHost(testDotvirt())+",external" {
		t.Errorf("ALLOWED_HOST_LIST without an Argo host = %q", got)
	}
}

// This is the operand half of the CSV's `capabilities: Seamless Upgrades` claim: an
// OLM operator upgrade must roll the dotvirt app WITHOUT a CR edit. The mechanism is
// the image precedence here — when the CR doesn't pin spec.image, the operand image
// comes from RELATED_IMAGE_DOTVIRT (which OLM sets on the manager from the new CSV on
// every upgrade) and falls back to the compile-time-pinned DefaultImage. So a newer
// operator (new digest in both) re-reconciles an existing unpinned CR into a rolled
// operand. The explicit spec.image override must still win for a user who pins.
func TestOperandImageRollsWithOperatorUpgrade(t *testing.T) {
	// DefaultImage is the build-time fallback used by `make run` / non-OLM installs;
	// a release repins it (hack/release.sh) so it tracks the operator version.
	if !strings.Contains(DefaultImage, "@sha256:") {
		t.Errorf("DefaultImage must be digest-pinned so an upgrade is reproducible, got %q", DefaultImage)
	}

	// Unpinned CR, no env (e.g. `make run`): the operand is the compiled-in DefaultImage.
	if got := Deployment(testDotvirt()).Spec.Template.Spec.Containers[0].Image; got != DefaultImage {
		t.Errorf("unpinned CR, no env: operand image = %q, want DefaultImage %q", got, DefaultImage)
	}

	// Unpinned CR under OLM: RELATED_IMAGE_DOTVIRT (set on the manager from the CSV) wins
	// over DefaultImage — this is the edge OLM bumps on upgrade to roll the operand.
	const upgraded = "quay.io/epheo/dotvirt@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	t.Setenv("RELATED_IMAGE_DOTVIRT", upgraded)
	if got := Deployment(testDotvirt()).Spec.Template.Spec.Containers[0].Image; got != upgraded {
		t.Errorf("unpinned CR + RELATED_IMAGE_DOTVIRT: operand image = %q, want %q (operand must follow the operator's pinned env)", got, upgraded)
	}

	// An explicit spec.image pin always wins, even over the upgraded env — a user who
	// pins opts out of the auto-roll.
	pinned := testDotvirt()
	pinned.Spec.Image = "quay.io/example/custom@sha256:" + strings.Repeat("a", 64)
	if got := Deployment(pinned).Spec.Template.Spec.Containers[0].Image; got != pinned.Spec.Image {
		t.Errorf("spec.image override: operand image = %q, want %q", got, pinned.Spec.Image)
	}
}

// Both Argo source definitions must exclude templates/ — the repo's VM-template
// library. Its manifests' CRD (template.kubevirt.io) need not exist on-cluster;
// a missing exclude makes every generated app degrade on unknown kinds.
func TestArgoSourcesExcludeTemplateLibrary(t *testing.T) {
	dv := testDotvirt()

	appset := ApplicationSet(dv, "argocd")
	dir, ok, _ := unstructured.NestedMap(appset.Object, "spec", "template", "spec", "source", "directory")
	if !ok {
		t.Fatal("ApplicationSet template has no source.directory")
	}
	if dir["exclude"] != "templates/*" {
		t.Fatalf("ApplicationSet exclude = %v", dir["exclude"])
	}

	app := PlatformApplication(dv, "argocd", "https://forge/x/platform.git")
	dir, ok, _ = unstructured.NestedMap(app.Object, "spec", "source", "directory")
	if !ok {
		t.Fatal("PlatformApplication has no source.directory")
	}
	if dir["exclude"] != "templates/*" {
		t.Fatalf("PlatformApplication exclude = %v", dir["exclude"])
	}
}
