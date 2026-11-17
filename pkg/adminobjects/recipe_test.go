package adminobjects

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// stubKubectl answers the recipe's reads from STUB_* env and writes what it
// is asked to apply to $STUB_APPLIED.
const stubKubectl = `#!/bin/sh
case "$*" in
"get deploy -A "*) printf 'dotvirt-operator/dotvirt-operator-controller\n' ;;
"auth can-i "*) exit 0 ;;
*"get deploy dotvirt-operator-controller "*)
	name=$(printf '%s' "$*" | sed 's/.*@.name=="\([A-Z_]*\)".*/\1/')
	printenv "STUB_OPERATOR_$name" | tr -d '\n' ;;
*"get dotvirt -o name") ;;
"get clusterrolebinding dotvirt "*) printf dotvirt ;;
*"get dotvirt -o jsonpath="*)
	field=$(printf '%s' "$*" | sed 's/.*{.items\[0\].spec.\([A-Za-z.]*\)}.*/\1/' | tr . _)
	printenv "STUB_SPEC_$field" || true ;;
"api-resources "*) printf '%s' "$STUB_ROUTES" ;;
"get ingresses.config.openshift.io cluster "*) [ -n "$STUB_DOMAIN" ] && printf '%s' "$STUB_DOMAIN" || exit 1 ;;
*"get route.route.openshift.io dotvirt "*) exit 1 ;;
"apply "*) cat > "$STUB_APPLIED" ;;
*) echo "stub kubectl: unexpected $*" >&2; exit 2 ;;
esac
`

// recipe is the template's header as a script, with the fill it ends with:
// envsubst, else the sed form the header gives beside it.
func recipe(t *testing.T, template []byte, fill string) string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(string(template), "\n") {
		if l, ok := strings.CutPrefix(l, "#   "); ok {
			lines = append(lines, l)
		}
	}
	script := strings.Join(lines, "\n")
	envsubst := strings.Index(script, "envsubst < guard.yaml")
	sedFill := strings.Index(script, "sed -e \"s|\\${OPERATOR_USERNAME}")
	if envsubst < 0 || sedFill < 0 {
		t.Fatal("the header lost its fill lines")
	}
	if fill == "envsubst" {
		return script[:envsubst] + script[envsubst:sedFill]
	}
	return script[:envsubst] + script[sedFill:]
}

// runRecipe runs the recipe against the stub with spec and operator env,
// returning what it applied (nil when it applied nothing) and its stderr.
func runRecipe(t *testing.T, fill string, env map[string]string) ([]map[string]any, string, error) {
	t.Helper()
	dir := t.TempDir()
	template, err := Template()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(stubKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guard.yaml"), template, 0o644); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(dir, "applied.yaml")
	cmd := exec.Command("sh", "-c", recipe(t, template, fill))
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "STUB_APPLIED=" + applied}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out, err := os.ReadFile(applied)
	if os.IsNotExist(err) {
		return nil, stderr.String(), runErr
	}
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	for _, doc := range strings.Split(string(out), "\n---\n")[1:] {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("applied YAML: %v\n%s", err, doc)
		}
		docs = append(docs, o)
	}
	return docs, stderr.String(), runErr
}

// fills are the fills an admin has here: sed always, envsubst where installed,
// and always in CI, which must run the header's first form.
func fills(t *testing.T) []string {
	out := []string{"sed"}
	if _, err := exec.LookPath("envsubst"); err == nil {
		out = append(out, "envsubst")
	} else if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal("no envsubst on this runner: the recipe's envsubst fill would go untested")
	}
	return out
}

// The recipe, run as an admin runs it, derives every pin as the console does
// and applies exactly the console's render of the guard.
func TestRecipeAppliesTheConsolesRender(t *testing.T) {
	operator := map[string]string{
		"STUB_OPERATOR_RELATED_IMAGE_DOTVIRT": Repositories[0] + "@sha256:" + strings.Repeat("a", 64),
		"STUB_OPERATOR_RELATED_IMAGE_FORGEJO": Repositories[1] + ":12-rootless@sha256:" + strings.Repeat("a", 64),
	}
	for name, c := range map[string]struct {
		env  map[string]string
		want Params
	}{
		"vanilla, with metrics, OIDC and Delegated": {
			env: map[string]string{
				"STUB_SPEC_image": "registry.example/mirror/dotvirt:1", "STUB_SPEC_ingress_host": "console.example",
				"STUB_SPEC_metrics_url":         "https://thanos-querier.openshift-monitoring.svc:9091",
				"STUB_SPEC_auth_oidc_issuerURL": "https://idp.example/realms/x", "STUB_SPEC_auth_oidc_clientSecret_name": "oidc",
				"STUB_SPEC_auth_oidc_clientSecret_key": "client-secret", "STUB_SPEC_forge_credentialsSecret": "creds",
				"STUB_SPEC_forge_pullRequests": "Delegated", "STUB_SPEC_forge_url": "https://forge.example/",
				"STUB_SPEC_forge_userAuth_clientID":          "6d1a4f0e-11b2-4a0c-9d3e-2f7c5b8a9e01",
				"STUB_SPEC_forge_userAuth_authorizeURL":      "https://forge.example/login/oauth/authorize",
				"STUB_SPEC_forge_userAuth_clientSecret_name": "forge-app", "STUB_SPEC_forge_userAuth_clientSecret_key": "client-secret",
			},
			want: Params{
				Namespace: "dotvirt", ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
				SpecImage: "registry.example/mirror/dotvirt:1", ConsoleHost: "console.example",
				Metrics:    MetricsFor("https://thanos-querier.openshift-monitoring.svc:9091", "", false),
				OIDCIssuer: "https://idp.example/realms/x", OIDCSecret: "oidc", OIDCSecretKey: "client-secret", ForgeSecret: "creds",
				ForgeDelegated: true, ForgeURL: "https://forge.example/", ForgeClientID: "6d1a4f0e-11b2-4a0c-9d3e-2f7c5b8a9e01",
				ForgeAuthorizeURL: "https://forge.example/login/oauth/authorize", ForgeClientSecret: "forge-app", ForgeClientSecretKey: "client-secret",
			},
		},
		"OpenShift, on the router's host, userAuth set but not Delegated": {
			env: map[string]string{
				"STUB_ROUTES": "routes.route.openshift.io", "STUB_DOMAIN": "apps.example",
				"STUB_SPEC_image": Repositories[0] + ":0.2.4", "STUB_SPEC_branding_configMap": "brand",
				"STUB_SPEC_forge_managed": "true", "STUB_SPEC_forge_insecureTLS": "true",
				"STUB_SPEC_forge_userAuth_clientID": "ahead-of-time",
			},
			want: Params{
				Namespace: "dotvirt", ArgoNamespace: "openshift-gitops", ArgoController: "openshift-gitops-argocd-application-controller",
				SpecImage: Repositories[0] + ":0.2.4", ConsoleHost: "dotvirt-dotvirt.apps.example", BrandingConfigMap: "brand",
				ForgeManaged: true, InsecureTLS: true, ForgeSecret: DefaultForgeSecret, ForgeClientID: "ahead-of-time",
			},
		},
	} {
		c.want.Operator = "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller"
		c.want.DotvirtImage, c.want.ForgejoImage = operator["STUB_OPERATOR_RELATED_IMAGE_DOTVIRT"], operator["STUB_OPERATOR_RELATED_IMAGE_FORGEJO"]
		env := map[string]string{}
		for _, m := range []map[string]string{operator, c.env} {
			for k, v := range m {
				env[k] = v
			}
		}
		for _, fill := range fills(t) {
			got, stderr, err := runRecipe(t, fill, env)
			if err != nil {
				t.Fatalf("%s, %s: %v\n%s", name, fill, err, stderr)
			}
			want := Guard(c.want)
			if len(got) != len(want) {
				t.Fatalf("%s, %s: applied %d objects, want %d", name, fill, len(got), len(want))
			}
			for i := range want {
				a, _ := json.Marshal(got[i])
				b, _ := json.Marshal(want[i].Object.Object)
				if string(a) != string(b) {
					t.Errorf("%s, %s: object %d differs from the console's render:\n%s\n%s", name, fill, i, a, b)
				}
			}
		}
	}
}

// A value the template cannot carry (a fresh YAML document after a line
// break, a quote ending its string) stops the recipe before anything is
// applied. CRD fields without a pattern (spec.image, spec.forge.url,
// userAuth.clientID) can hold any of them.
func TestRecipeRefusesWhatTheTemplateCannotCarry(t *testing.T) {
	inject := "x\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: injected\n  namespace: default\n---\n#"
	for name, value := range map[string]string{
		"a new document": inject, "a single quote": "a'b", "a double quote": `a"b`, "a backslash": `a\b`,
		"a carriage return": "a\rb", "a tab": "a\tb", "a control character": "a\x01b", "a space": "a b",
	} {
		for _, field := range []string{"STUB_SPEC_image", "STUB_SPEC_forge_url", "STUB_SPEC_forge_userAuth_clientID"} {
			env := map[string]string{field: value, "STUB_SPEC_forge_pullRequests": "Delegated"}
			got, stderr, err := runRecipe(t, "sed", env)
			if err == nil || got != nil || !strings.Contains(stderr, "cannot carry") {
				t.Errorf("%s in %s: err=%v, applied %d objects, stderr %q", name, field, err, len(got), stderr)
			}
		}
	}
}

// The recipe leaves out an image inside its official repository, as the
// render does, and keeps a lookalike or another repository's.
func TestRecipeLeavesOutRepositoryImages(t *testing.T) {
	for image, want := range map[string]string{
		Repositories[0] + ":0.2.4":                             "",
		Repositories[0] + "@sha256:" + strings.Repeat("b", 64): "",
		Repositories[0] + "-mirror:0.2.4":                      Repositories[0] + "-mirror:0.2.4",
		Repositories[1] + ":12-rootless":                       Repositories[1] + ":12-rootless",
	} {
		got, stderr, err := runRecipe(t, "sed", map[string]string{"STUB_SPEC_image": image})
		if err != nil {
			t.Fatalf("%s: %v\n%s", image, err, stderr)
		}
		p := Params{Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt",
			ArgoNamespace: "argocd", ArgoController: "argocd-application-controller", SpecImage: want, ForgeSecret: DefaultForgeSecret}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(objects(Guard(p)))
		if string(a) != string(b) {
			t.Errorf("spec.image %s: the recipe's guard differs from the render pinning %q", image, want)
		}
	}
}
