package install

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
// the app falls back to its public URL - otherwise dotvirt registers an unreachable hook.
func TestWebhookURLGatedOnManagedForge(t *testing.T) {
	managed := testDotvirt()
	managed.Spec.Forge.Managed = true
	if got, ok := envValue(Deployment(managed, "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_WEBHOOK_URL"); !ok || got != ServiceURL(managed) {
		t.Errorf("managed forge: DOTVIRT_WEBHOOK_URL = (%q, ok=%v), want %q", got, ok, ServiceURL(managed))
	}

	byo := testDotvirt() // Forge.Managed defaults to false
	if got, ok := envValue(Deployment(byo, "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_WEBHOOK_URL"); ok {
		t.Errorf("BYO forge: DOTVIRT_WEBHOOK_URL must be unset (app falls back to public URL), got %q", got)
	}
}

// hasVolume reports whether the pod declares a volume of the given name.
func hasVolume(d *appsv1.Deployment, name string) bool {
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

// With no forge configured the app runs push-only: the Deployment must omit the forge
// credential env AND the forge-token secret mount, or the pod wedges on a dotvirt-forge
// secret that is never written (the `forge: {}` failure). A managed forge restores both.
func TestForgelessDeploymentOmitsForgeCredential(t *testing.T) {
	bare := Deployment(testDotvirt(), "argocd", nil) // no managed / url / credentialsSecret
	if _, ok := envValue(bare.Spec.Template.Spec.Containers[0].Env, "DOTVIRT_FORGE_URL"); ok {
		t.Error("forge-less: DOTVIRT_FORGE_URL must be unset")
	}
	if hasVolume(bare, "forge-token") {
		t.Error("forge-less: the forge-token volume must be omitted (else the pod wedges on a missing secret)")
	}

	managed := testDotvirt()
	managed.Spec.Forge.Managed = true
	managed.Spec.Forge.URL = "https://forgejo.apps.example"
	d := Deployment(managed, "argocd", nil)
	// Managed emits the URL as a literal (a URL change rolls the app), not a secret ref.
	if got, ok := envValue(d.Spec.Template.Spec.Containers[0].Env, "DOTVIRT_FORGE_URL"); !ok || got != managed.Spec.Forge.URL {
		t.Errorf("managed: DOTVIRT_FORGE_URL = (%q, ok=%v), want the literal resolved url", got, ok)
	}
	if !hasVolume(d, "forge-token") {
		t.Error("managed: the forge-token volume must be present")
	}
}

// OpenShift SSO wiring is gated on auth.openShiftSSO: the OAuth client id is a literal
// and the client secret refs the operator-generated dotvirt-oauth Secret; both absent otherwise.
func TestDeploymentSSOEnvGatedOnFlag(t *testing.T) {
	if _, ok := envValue(Deployment(testDotvirt(), "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_OAUTH_CLIENT_ID"); ok {
		t.Error("SSO off: DOTVIRT_OAUTH_CLIENT_ID must be unset")
	}
	sso := testDotvirt()
	sso.Spec.Auth.OpenShiftSSO = true
	env := Deployment(sso, "argocd", nil).Spec.Template.Spec.Containers[0].Env
	// The OAuthClient is cluster-scoped, so its name must be per-install: a shared one
	// lets a second install overwrite the first's redirect URI and secret.
	want := OAuthClientName(sso.Namespace)
	if got, ok := envValue(env, "DOTVIRT_OAUTH_CLIENT_ID"); !ok || got != want {
		t.Errorf("SSO on: DOTVIRT_OAUTH_CLIENT_ID = (%q, ok=%v), want %q", got, ok, want)
	}
	if other := OAuthClientName("other-ns"); other == want {
		t.Errorf("two installs share the cluster-scoped OAuthClient name %q", want)
	}
	var secretRef *corev1.SecretKeySelector
	for _, e := range env {
		if e.Name == "DOTVIRT_OAUTH_CLIENT_SECRET" && e.ValueFrom != nil {
			secretRef = e.ValueFrom.SecretKeyRef
		}
	}
	if secretRef == nil || secretRef.Name != OAuthSecretName || secretRef.Key != "clientSecret" {
		t.Errorf("DOTVIRT_OAUTH_CLIENT_SECRET must ref %s/clientSecret, got %+v", OAuthSecretName, secretRef)
	}
}

// The operand must render restricted-v2 compatible - non-root, no privilege
// escalation, all capabilities dropped, bounded resources, and a liveness probe - so
// OpenShift's restricted SCC admits it WITHOUT the anyuid grant.
func TestDeploymentSecurityHardening(t *testing.T) {
	d := Deployment(testDotvirt(), "argocd", nil)

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
// PVC writability - plus bounded, probed, and digest-pinned.
func TestForgejoDeploymentBoundedAndPinned(t *testing.T) {
	d := ForgejoDeployment(testDotvirt(), true, "", "h")
	c := d.Spec.Template.Spec.Containers[0]
	if c.LivenessProbe == nil {
		t.Error("forgejo must set a liveness probe")
	}
	if _, ok := c.Resources.Limits[corev1.ResourceMemory]; !ok {
		t.Error("forgejo must set a memory limit")
	}
	if !strings.Contains(forgejoImage, "@sha256:") {
		t.Errorf("forgejoImage must be digest-pinned, got %q", forgejoImage)
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

// The bootstrap reconciles the admin password on every start: `user create` seeds a
// fresh volume, and a change-password fallback resets it on an existing one. Without
// the fallback, a regenerated admin secret over a retained data PVC leaves the operator
// authenticating against a stale password and the mint 401s forever.
func TestForgejoBootstrapReconcilesAdminPassword(t *testing.T) {
	cmd := ForgejoDeployment(testDotvirt(), true, "", "h").Spec.Template.Spec.InitContainers[0].Command
	script := cmd[len(cmd)-1] // sh -c "<script>"
	if !strings.Contains(script, "user create") {
		t.Error("bootstrap must create the admin user on a fresh volume")
	}
	if !strings.Contains(script, "change-password") {
		t.Error("bootstrap must reset the admin password on an existing volume (create-or-reset)")
	}
	if strings.Contains(script, "|| true") {
		t.Error("bootstrap still swallows the create failure with `|| true`; the password never reconciles")
	}
}

// fsGroup is set on vanilla K8s (PVC writability) but MUST be omitted on OpenShift,
// where restricted-v2 rejects an out-of-range fsGroup and injects its own.
func TestForgejoFSGroupIsPlatformConditional(t *testing.T) {
	if fg := ForgejoDeployment(testDotvirt(), true, "", "h").Spec.Template.Spec.SecurityContext.FSGroup; fg == nil {
		t.Error("vanilla K8s (setFSGroup=true): fsGroup must be set")
	}
	if fg := ForgejoDeployment(testDotvirt(), false, "", "h").Spec.Template.Spec.SecurityContext.FSGroup; fg != nil {
		t.Errorf("OpenShift (setFSGroup=false): fsGroup must be nil, got %d", *fg)
	}
}

// The forge->ArgoCD webhook targets the Argo Route by name, which often resolves
// to a PRIVATE ingress VIP; Forgejo's `external` allowlist entry matches public
// resolved IPs only, so the host must be allowed by name or every delivery is
// silently denied by the SSRF guard. Both containers share the env (the init
// renders app.ini from it).
func TestForgejoWebhookAllowlistIncludesArgoHost(t *testing.T) {
	const argo = "openshift-gitops-server-openshift-gitops.apps.example.com"
	d := ForgejoDeployment(testDotvirt(), false, argo, "h")
	for _, env := range [][]corev1.EnvVar{
		d.Spec.Template.Spec.InitContainers[0].Env,
		d.Spec.Template.Spec.Containers[0].Env,
	} {
		got, ok := envValue(env, "FORGEJO__webhook__ALLOWED_HOST_LIST")
		if !ok || got != svcHost(AppName, testDotvirt().Namespace)+",external,"+argo {
			t.Errorf("ALLOWED_HOST_LIST = (%q, ok=%v), want service host + external + argo host", got, ok)
		}
	}

	// No Argo URL resolvable yet: the baseline list, no trailing separator.
	got, _ := envValue(ForgejoDeployment(testDotvirt(), false, "", "h").Spec.Template.Spec.Containers[0].Env,
		"FORGEJO__webhook__ALLOWED_HOST_LIST")
	if got != svcHost(AppName, testDotvirt().Namespace)+",external" {
		t.Errorf("ALLOWED_HOST_LIST without an Argo host = %q", got)
	}
}

// This is the operand half of the CSV's `capabilities: Seamless Upgrades` claim: an
// OLM operator upgrade must roll the dotvirt app WITHOUT a CR edit. The mechanism is
// the image precedence here - when the CR doesn't pin spec.image, the operand image
// comes from RELATED_IMAGE_DOTVIRT (which OLM sets on the manager from the new CSV on
// every upgrade) and falls back to the compile-time-pinned defaultImage. So a newer
// operator (new digest in both) re-reconciles an existing unpinned CR into a rolled
// operand. The explicit spec.image override must still win for a user who pins.
func TestOperandImageRollsWithOperatorUpgrade(t *testing.T) {
	// defaultImage is the build-time fallback used by `make run` / non-OLM installs;
	// a release repins it (hack/release.sh) so it tracks the operator version.
	if !strings.Contains(defaultImage, "@sha256:") {
		t.Errorf("defaultImage must be digest-pinned so an upgrade is reproducible, got %q", defaultImage)
	}

	// Unpinned CR, no env (e.g. `make run`): the operand is the compiled-in defaultImage.
	if got := Deployment(testDotvirt(), "argocd", nil).Spec.Template.Spec.Containers[0].Image; got != defaultImage {
		t.Errorf("unpinned CR, no env: operand image = %q, want defaultImage %q", got, defaultImage)
	}

	// Unpinned CR under OLM: RELATED_IMAGE_DOTVIRT (set on the manager from the CSV) wins
	// over defaultImage - this is the edge OLM bumps on upgrade to roll the operand.
	const upgraded = "quay.io/epheo/dotvirt@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	t.Setenv("RELATED_IMAGE_DOTVIRT", upgraded)
	if got := Deployment(testDotvirt(), "argocd", nil).Spec.Template.Spec.Containers[0].Image; got != upgraded {
		t.Errorf("unpinned CR + RELATED_IMAGE_DOTVIRT: operand image = %q, want %q (operand must follow the operator's pinned env)", got, upgraded)
	}

	// An explicit spec.image pin always wins, even over the upgraded env - a user who
	// pins opts out of the auto-roll.
	pinned := testDotvirt()
	pinned.Spec.Image = "quay.io/example/custom@sha256:" + strings.Repeat("a", 64)
	if got := Deployment(pinned, "argocd", nil).Spec.Template.Spec.Containers[0].Image; got != pinned.Spec.Image {
		t.Errorf("spec.image override: operand image = %q, want %q", got, pinned.Spec.Image)
	}
}

// Every Argo source (argoAppSpec is the one shared definition) must exclude
// templates/ - the repo's VM-template library. Its manifests' CRD
// (template.kubevirt.io) need not exist on-cluster; a missing exclude makes
// every generated app degrade on unknown kinds.
func TestArgoSourcesExcludeTemplateLibrary(t *testing.T) {
	app := PlatformApplication(testDotvirt(), "argocd", "https://forge/x/platform.git")
	dir, ok, _ := unstructured.NestedMap(app.Object, "spec", "source", "directory")
	if !ok {
		t.Fatal("PlatformApplication has no source.directory")
	}
	if dir["exclude"] != "templates/*" {
		t.Fatalf("exclude = %v", dir["exclude"])
	}
}

// repo-creds templates ignore `insecure`; emitting it reads as a fix while doing
// nothing. Trust is real instead (argocd-tls-certs-cm).
func TestRepoCredsNeverEmitsIgnoredInsecure(t *testing.T) {
	dv := testDotvirt()
	dv.Spec.Forge.InsecureTLS = true
	if _, ok := RepoCredsSecret(dv, "openshift-gitops", "https://forge.example/dotvirt", "bot", "tok").StringData["insecure"]; ok {
		t.Error("insecure is not a repo-creds template field; emitting it masks the real TLS gap")
	}
}

// The template URL ends at the org's slash, so it serves that org's repos and
// never an org whose name merely starts the same way.
func TestRepoCredsURLEndsAtTheOrg(t *testing.T) {
	for _, prefix := range []string{"https://forge.example/dotvirt", "https://forge.example/dotvirt/"} {
		if got := RepoCredsSecret(testDotvirt(), "argocd", prefix, "bot", "tok").StringData["url"]; got != "https://forge.example/dotvirt/" {
			t.Errorf("RepoCredsSecret(%q) url = %q, want https://forge.example/dotvirt/", prefix, got)
		}
	}
}

// A rotated admin secret must roll the pod (initContainer reconciles the DB
// password), never wait on a human runbook.
func TestForgejoRollsOnAdminSecretChange(t *testing.T) {
	dv := testDotvirt()
	a := ForgejoDeployment(dv, false, "", "hash-a").Spec.Template.Annotations["dotvirt.io/admin-secret-hash"]
	b := ForgejoDeployment(dv, false, "", "hash-b").Spec.Template.Annotations["dotvirt.io/admin-secret-hash"]
	if a == "" || a == b {
		t.Fatalf("pod template must carry the changing admin-secret hash, got %q then %q", a, b)
	}
}

// Zero-config posture is VERIFIED TLS: optional trust-anchor mounts, one CA env
// per client, -insecure-tls only on explicit opt-in.
func TestDeploymentVerifiedTLSByDefault(t *testing.T) {
	dv := testDotvirt()
	dv.Spec.Forge.Managed = true
	dv.Spec.Auth.OpenShiftSSO = true
	dv.Spec.Metrics.URL = "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"
	d := Deployment(dv, "argocd", nil)
	c := d.Spec.Template.Spec.Containers[0]
	for _, arg := range c.Args {
		if arg == "-insecure-tls" {
			t.Fatal("-insecure-tls must not render without the explicit spec opt-in")
		}
	}
	for _, want := range []string{"DOTVIRT_FORGE_CA", "DOTVIRT_OAUTH_CA", "DOTVIRT_METRICS_CA"} {
		if _, ok := envValue(c.Env, want); !ok {
			t.Errorf("%s missing: that client would silently skip CA verification", want)
		}
	}
	if !hasVolume(d, "ingress-ca") || !hasVolume(d, "service-ca") {
		t.Error("trust-anchor mounts missing")
	}
}

// The thanos-querier's cluster-wide port brings its tenancy port beside it, for
// callers who may only query their own namespaces; any other metrics endpoint
// has no known tenancy port.
func TestDeploymentMetricsTenancyURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://thanos-querier.openshift-monitoring.svc.cluster.local:9091": "https://thanos-querier.openshift-monitoring.svc.cluster.local:9092",
		"https://thanos-querier.openshift-monitoring.svc:9091/":              "https://thanos-querier.openshift-monitoring.svc:9092/",
		"https://thanos-querier-openshift-monitoring.apps.example.com":       "",
		"http://prometheus-operated.monitoring.svc.cluster.local:9090":       "",
		"https://prometheus-k8s.openshift-monitoring.svc.cluster.local:9091": "",
		"https://thanos-querier.example.com:9091":                            "",
	} {
		dv := testDotvirt()
		dv.Spec.Metrics.URL = url
		got, ok := envValue(Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_METRICS_TENANCY_URL")
		if got != want || ok != (want != "") {
			t.Errorf("%s: tenancy URL = %q (set %t), want %q", url, got, ok, want)
		}
	}
}

// Webhook TLS stays verified; the global SKIP_TLS_VERIFY (which also unverified
// tenant webhooks) must never come back.
func TestForgejoVerifiesWebhookTLS(t *testing.T) {
	d := ForgejoDeployment(testDotvirt(), false, "", "h")
	env := d.Spec.Template.Spec.Containers[0].Env
	if _, ok := envValue(env, "FORGEJO__webhook__SKIP_TLS_VERIFY"); ok {
		t.Fatal("webhook TLS verification must stay ON")
	}
	if v, ok := envValue(env, "SSL_CERT_DIR"); !ok || !strings.Contains(v, "/etc/ssl/certs") {
		t.Errorf("SSL_CERT_DIR must join the system pool with the mounted CA, got %q", v)
	}
	if !hasVolume(d, "ingress-ca") {
		t.Error("forgejo ingress-ca mount missing")
	}
}

// A tenant AppProject admits exactly its project's namespaces and repo, with
// the shared tier's workload whitelist, and the ApplicationSet template names
// it and a destination the plugin picks among those namespaces.
func TestTenantAppProjectConfinesOneProject(t *testing.T) {
	dv := testDotvirt()
	ap := TenantAppProject(dv, "argocd", "dotvirt-p-team-a", "team-a", []string{"a-dev", "a-prod"}, []string{"https://forge/dotvirt/team-a.git"}, nil)
	if ap.GetName() != "dotvirt-p-team-a" || ap.GetLabels()[ProjectLabel] != "team-a" || !OwnedBy(ap.GetLabels(), dv) {
		t.Fatalf("name %s labels %v", ap.GetName(), ap.GetLabels())
	}
	dests, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinations")
	var got []string
	for _, d := range dests {
		got = append(got, d.(map[string]any)["namespace"].(string))
	}
	if strings.Join(got, ",") != "a-dev,a-prod" {
		t.Errorf("destinations = %v", got)
	}
	if repos, _, _ := unstructured.NestedStringSlice(ap.Object, "spec", "sourceRepos"); len(repos) != 1 || repos[0] != "https://forge/dotvirt/team-a.git" {
		t.Errorf("sourceRepos = %v", repos)
	}
	shared := TenantsAppProject(dv, "argocd", "https://forge/dotvirt/platform.git", "ns")
	sharedDests, _, _ := unstructured.NestedSlice(shared.Object, "spec", "destinations")
	if !slices.ContainsFunc(sharedDests, func(d any) bool { return d.(map[string]any)["namespace"] == "!*-publish" }) {
		t.Errorf("the shared tier admits publish namespaces: %v", sharedDests)
	}
	for _, f := range []string{"clusterResourceWhitelist", "namespaceResourceWhitelist"} {
		if !reflect.DeepEqual(ap.Object["spec"].(map[string]any)[f], shared.Object["spec"].(map[string]any)[f]) {
			t.Errorf("%s differs from the shared tier's", f)
		}
	}
	empty := TenantAppProject(dv, "argocd", "dotvirt-p-gone", "gone", nil, nil, nil)
	for _, f := range []string{"destinations", "sourceRepos"} {
		if v, _, _ := unstructured.NestedSlice(empty.Object, "spec", f); v == nil || len(v) != 0 {
			t.Errorf("a project with nothing to admit must render an empty %s, got %v", f, v)
		}
	}

	set := ApplicationSet(dv, "argocd", []string{"argocd", "ns"}, nil)
	spec, _, _ := unstructured.NestedMap(set.Object, "spec", "template", "spec")
	if spec["project"] != "dotvirt-p-{{.project}}" || spec["destination"].(map[string]any)["namespace"] != "{{.namespace}}" {
		t.Errorf("template project %v destination %v", spec["project"], spec["destination"])
	}
	gen, _, _ := unstructured.NestedSlice(set.Object, "spec", "generators")
	g := gen[0].(map[string]any)
	if reserved, _, _ := unstructured.NestedSlice(g, "plugin", "input", "parameters", "reservedNamespaces"); !reflect.DeepEqual(reserved, []any{"argocd", "ns"}) {
		t.Errorf("generator input reservedNamespaces = %v", reserved)
	}
	if _, has := g["selector"]; has {
		t.Error("a selector rendered with nothing to exclude")
	}
	platform, _, _ := unstructured.NestedString(PlatformApplication(dv, "argocd", "https://forge/x/platform.git").Object, "spec", "project")
	if platform != projectPlatform {
		t.Errorf("platform Application project = %s", platform)
	}
}

// A project with no AppProject of this install's is kept out of the generator:
// Argo checks only that an Application's project exists.
func TestApplicationSetExcludesUnconfinedProjects(t *testing.T) {
	gen, _, _ := unstructured.NestedSlice(ApplicationSet(testDotvirt(), "argocd", nil, []string{"taken"}).Object, "spec", "generators")
	exprs, _, _ := unstructured.NestedSlice(gen[0].(map[string]any), "selector", "matchExpressions")
	want := []any{map[string]any{"key": "project", "operator": "NotIn", "values": []any{"taken"}}}
	if !reflect.DeepEqual(exprs, want) {
		t.Errorf("selector = %v, want %v", exprs, want)
	}
}

// The app reads tenancy from the keys the AppProjects are derived from.
func TestDeploymentPinsTenancyKeys(t *testing.T) {
	args := Deployment(testDotvirt(), "argocd", nil).Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{"-project-label=" + ProjectLabel, "-repo-annotation=" + RepoAnnotation} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %s", args, want)
		}
	}
}

// A tenant repo can never reach a NetworkAttachmentDefinition through Argo's
// rights: no tenant AppProject admits one, the platform tier keeps them.
func TestTenantAppProjectsAdmitNoNAD(t *testing.T) {
	dv := testDotvirt()
	nad := func(ap *unstructured.Unstructured) bool {
		kinds, _, _ := unstructured.NestedSlice(ap.Object, "spec", "namespaceResourceWhitelist")
		for _, k := range kinds {
			if k.(map[string]any)["kind"] == "NetworkAttachmentDefinition" {
				return true
			}
		}
		return false
	}
	if nad(TenantAppProject(dv, "argocd", "dotvirt-p-a", "a", []string{"a"}, nil, nil)) || nad(TenantsAppProject(dv, "argocd", "https://forge/dotvirt/platform.git", "ns")) {
		t.Error("a tenant AppProject admits NetworkAttachmentDefinitions")
	}
	if !nad(PlatformAppProject(dv, "argocd", "https://forge/dotvirt/platform.git")) {
		t.Error("the platform AppProject lost NetworkAttachmentDefinitions")
	}
}

func TestHTTPRouteAttachesToTheNamedGateway(t *testing.T) {
	dv := testDotvirt()
	dv.Spec.Ingress.Gateway = &dotvirtv1alpha1.GatewayRef{Name: "edge", Namespace: "gateway-system", SectionName: "https"}
	r := HTTPRoute(dv, AppName, HTTPPort, "dotvirt.example.com")
	if r.GetNamespace() != dv.Namespace || r.GetName() != AppName || r.GroupVersionKind() != HTTPRouteGVK {
		t.Errorf("rendered %s %s/%s", r.GroupVersionKind(), r.GetNamespace(), r.GetName())
	}
	parents, _, _ := unstructured.NestedSlice(r.Object, "spec", "parentRefs")
	want := map[string]any{"name": "edge", "namespace": "gateway-system", "sectionName": "https"}
	if len(parents) != 1 || !reflect.DeepEqual(parents[0], want) {
		t.Errorf("parentRefs = %v, want [%v]", parents, want)
	}
	hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames")
	if len(hosts) != 1 || hosts[0] != "dotvirt.example.com" {
		t.Errorf("hostnames = %v", hosts)
	}
	rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
	backend := rules[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
	if backend["name"] != AppName || backend["port"] != int64(HTTPPort) {
		t.Errorf("backend = %v", backend)
	}
}

// Without this peer the fence the operator draws on OpenShift-like clusters
// would cut the console off from the Gateway that serves it.
func TestNetworkPolicyAdmitsTheGatewayNamespace(t *testing.T) {
	admits := func(dv *dotvirtv1alpha1.Dotvirt, ns string) bool {
		for _, peer := range OperandNetworkPolicy(dv, "argocd", "").Spec.Ingress[0].From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] == ns {
				return true
			}
		}
		return false
	}
	dv := testDotvirt()
	if admits(dv, "gateway-system") {
		t.Error("admits a Gateway namespace nobody named")
	}
	dv.Spec.Ingress.Gateway = &dotvirtv1alpha1.GatewayRef{Name: "edge", Namespace: "gateway-system"}
	if !admits(dv, "gateway-system") {
		t.Error("the Gateway's namespace is not admitted")
	}
}

// A tenant's folder comes from the plugin; the platform repo is always whole.
func TestArgoSourcePaths(t *testing.T) {
	dv := testDotvirt()
	set := ApplicationSet(dv, "argocd", []string{"argocd"}, nil)
	if got, _, _ := unstructured.NestedString(set.Object, "spec", "template", "spec", "source", "path"); got != "{{.path}}" {
		t.Errorf("tenant source path = %q", got)
	}
	app := PlatformApplication(dv, "argocd", "https://forge/platform.git")
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "path"); got != "." {
		t.Errorf("platform source path = %q", got)
	}
}

// A project's repo applies the allowed kinds only while impersonation is
// asked for and the instance impersonates (whatever SyncImpersonationReady
// says: one project's missing grant degrades it); otherwise it keeps the
// listed kinds, and the shared tier always does. A kind no tenant repo may
// apply never reaches a whitelist.
func TestTenantKindsAllowedNeedImpersonation(t *testing.T) {
	whitelisted := func(ap *unstructured.Unstructured, group, kind string) bool {
		list, _, _ := unstructured.NestedSlice(ap.Object, "spec", "namespaceResourceWhitelist")
		want := map[string]any{"group": group, "kind": kind}
		return slices.ContainsFunc(list, func(e any) bool { return reflect.DeepEqual(e, want) })
	}
	allow := func(dv *dotvirtv1alpha1.Dotvirt) {
		dv.Spec.ArgoCD.TenantKindsAllowed = []dotvirtv1alpha1.GroupKind{{Group: "apps", Kind: "Deployment"}, {Group: "rbac.authorization.k8s.io", Kind: "Role"}}
	}
	degraded := metav1.Condition{Type: dotvirtv1alpha1.ConditionSyncImpersonationReady, Status: metav1.ConditionFalse, Reason: "Degraded"}
	off := false
	for _, c := range []struct {
		name          string
		set           func(dv *dotvirtv1alpha1.Dotvirt)
		impersonating bool
		allowed       bool
	}{
		{"none named", func(*dotvirtv1alpha1.Dotvirt) {}, true, false},
		{"not impersonating", allow, false, false},
		{"impersonating", allow, true, true},
		{"impersonating, one project degraded", func(dv *dotvirtv1alpha1.Dotvirt) {
			allow(dv)
			meta.SetStatusCondition(&dv.Status.Conditions, degraded)
		}, true, true},
		{"asked off while still impersonating", func(dv *dotvirtv1alpha1.Dotvirt) {
			allow(dv)
			dv.Spec.ArgoCD.SyncImpersonation.Enabled = &off
		}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dv := testDotvirt()
			c.set(dv)
			ap := TenantAppProject(dv, "argocd", "dotvirt-p-a", "a", []string{"a"}, nil, TenantAllowed(dv, c.impersonating))
			if got := whitelisted(ap, "apps", "Deployment"); got != c.allowed {
				t.Errorf("Deployment whitelisted = %v, want %v", got, c.allowed)
			}
			if !whitelisted(ap, "kubevirt.io", "VirtualMachine") {
				t.Error("the listed kinds are missing")
			}
			if whitelisted(ap, "rbac.authorization.k8s.io", "Role") {
				t.Error("a kind no tenant repo may apply is whitelisted")
			}
			if _, black := ap.Object["spec"].(map[string]any)["namespaceResourceBlacklist"]; black {
				t.Error("a tenant AppProject carries a deny list")
			}
			if whitelisted(TenantsAppProject(dv, "argocd", "https://forge/dotvirt/platform.git", "ns"), "apps", "Deployment") {
				t.Error("the shared tier, which reaches any namespace, applies an allowed kind")
			}
			if v, _, _ := unstructured.NestedSlice(ap.Object, "spec", "clusterResourceWhitelist"); v == nil || len(v) != 0 {
				t.Errorf("clusterResourceWhitelist = %v, want empty", v)
			}
		})
	}
}

// dotvirt reads what tenant repos may apply beyond the listed kinds from its
// env: nothing while they keep the listed kinds, the allowed ones else.
func TestDeploymentCarriesTheTenantKinds(t *testing.T) {
	dv := testDotvirt()
	env := func(allowed []tenancy.GroupKind) (string, bool) {
		return envValue(Deployment(dv, "argocd", allowed).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_TENANT_KINDS_ALLOWED")
	}
	if v, ok := env(nil); ok {
		t.Errorf("listed: %q", v)
	}
	dv.Spec.ArgoCD.TenantKindsAllowed = []dotvirtv1alpha1.GroupKind{{Group: "apps", Kind: "Deployment"}, {Kind: "Service"}, {Group: "rbac.authorization.k8s.io", Kind: "Role"}}
	if v, _ := env(TenantAllowed(dv, true)); v != "apps/Deployment,/Service" {
		t.Errorf("allowed: %q", v)
	}
}

// Publishing reaches dotvirt only with both its domain and class; the issuer
// rides along when set.
func TestDeploymentCarriesPublishing(t *testing.T) {
	dv := testDotvirt()
	env := func(name string) (string, bool) {
		return envValue(Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env, name)
	}
	dv.Spec.Publishing.Domain = "apps.example.com"
	if _, ok := env("DOTVIRT_PUBLISH_DOMAIN"); ok {
		t.Error("a domain without a class turned publishing on")
	}
	dv.Spec.Publishing.GatewayClassName = "portail"
	if d, _ := env("DOTVIRT_PUBLISH_DOMAIN"); d != "apps.example.com" {
		t.Errorf("domain = %q", d)
	}
	if c, _ := env("DOTVIRT_PUBLISH_GATEWAY_CLASS"); c != "portail" {
		t.Errorf("class = %q", c)
	}
	if _, ok := env("DOTVIRT_PUBLISH_ISSUER"); ok {
		t.Error("an issuer without one set")
	}
	dv.Spec.Publishing.ClusterIssuer = "letsencrypt"
	if i, _ := env("DOTVIRT_PUBLISH_ISSUER"); i != "letsencrypt" {
		t.Errorf("issuer = %q", i)
	}
	if _, ok := env("DOTVIRT_PUBLISH_PORTAIL_NAMESPACE"); ok {
		t.Error("portail's namespace without one set")
	}
	dv.Spec.Publishing.PortailNamespace = "edge"
	if n, _ := env("DOTVIRT_PUBLISH_PORTAIL_NAMESPACE"); n != "edge" {
		t.Errorf("portail namespace = %q", n)
	}
}

// A provider's branding reaches dotvirt only when set; its ConfigMap mounts
// optional, so a missing one leaves the console dotvirt's own.
func TestDeploymentCarriesTheBranding(t *testing.T) {
	dv := testDotvirt()
	pod := func() corev1.PodSpec { return Deployment(dv, "argocd", nil).Spec.Template.Spec }
	for _, name := range []string{"DOTVIRT_PRODUCT_NAME", "DOTVIRT_MASTHEAD_LINKS", "DOTVIRT_BRANDING_DIR"} {
		if v, ok := envValue(pod().Containers[0].Env, name); ok {
			t.Errorf("unset branding: %s = %q", name, v)
		}
	}
	dv.Spec.Branding = dotvirtv1alpha1.BrandingSpec{
		ProductName: "Uniwatt Cloud", ConfigMap: "uniwatt-theme",
		Links: []dotvirtv1alpha1.BrandingLink{{Label: "Account", URL: "https://c.example/account"}},
	}
	env := pod().Containers[0].Env
	if v, _ := envValue(env, "DOTVIRT_PRODUCT_NAME"); v != "Uniwatt Cloud" {
		t.Errorf("product name = %q", v)
	}
	if v, _ := envValue(env, "DOTVIRT_MASTHEAD_LINKS"); v != `[{"label":"Account","url":"https://c.example/account"}]` {
		t.Errorf("links = %q", v)
	}
	if v, _ := envValue(env, "DOTVIRT_BRANDING_DIR"); v != brandingMountPath {
		t.Errorf("branding dir = %q", v)
	}
	var mounted bool
	for _, vol := range pod().Volumes {
		if cm := vol.ConfigMap; vol.Name == "branding" && cm != nil && cm.Name == "uniwatt-theme" && cm.Optional != nil && *cm.Optional {
			mounted = true
		}
	}
	if !mounted {
		t.Error("the branding ConfigMap is not mounted optional")
	}
}

// The forge's web base reaches dotvirt only when set: unset, links keep the
// forge's URL.
func TestDeploymentCarriesTheForgeWebURL(t *testing.T) {
	dv := testDotvirt()
	dv.Spec.Forge.Managed = true
	web := func() (string, bool) {
		return envValue(Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_FORGE_WEB_URL")
	}
	if v, ok := web(); ok {
		t.Errorf("unset webURL: DOTVIRT_FORGE_WEB_URL = %q, want unset", v)
	}
	dv.Spec.Forge.WebURL = "https://c.example/forge"
	if v, _ := web(); v != "https://c.example/forge" {
		t.Errorf("webURL: DOTVIRT_FORGE_WEB_URL = %q", v)
	}
}

// Users opening their own pull requests reaches dotvirt as one env var; the
// default leaves it out.
func TestDeploymentCarriesWhoOpensPullRequests(t *testing.T) {
	dv := testDotvirt()
	dv.Spec.Forge.Managed = true
	users := func() (string, bool) {
		return envValue(Deployment(dv, "argocd", nil).Spec.Template.Spec.Containers[0].Env, "DOTVIRT_USERS_OPEN_PRS")
	}
	for _, who := range []string{"", "Dotvirt"} {
		dv.Spec.Forge.PullRequests = who
		if v, ok := users(); ok {
			t.Errorf("pullRequests %q: DOTVIRT_USERS_OPEN_PRS = %q, want unset", who, v)
		}
	}
	dv.Spec.Forge.PullRequests = "Users"
	if v, _ := users(); v != "true" {
		t.Errorf("pullRequests Users: DOTVIRT_USERS_OPEN_PRS = %q, want true", v)
	}
}
