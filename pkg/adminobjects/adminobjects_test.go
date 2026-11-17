package adminobjects

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

var params = Params{
	Operator:       "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller",
	Namespace:      "dotvirt",
	ArgoNamespace:  "openshift-gitops",
	ArgoController: "openshift-gitops-argocd-application-controller",
	SpecImage:      "registry.example/team/dotvirt:custom",
	ConsoleHost:    "dotvirt-dotvirt.apps.example",
	ForgeSecret:    DefaultForgeSecret,
}

func policyOf(t *testing.T, o Object) admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	var p admissionregistrationv1.ValidatingAdmissionPolicy
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object.Object, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func exprs(p admissionregistrationv1.ValidatingAdmissionPolicy) []string {
	var out []string
	for _, v := range p.Spec.Validations {
		out = append(out, v.Expression)
	}
	return out
}

func holds(t *testing.T, where string, got []string, needle string) {
	t.Helper()
	if !slices.ContainsFunc(got, func(e string) bool { return strings.Contains(e, needle) }) {
		t.Errorf("%s: no rule holds %s", where, needle)
	}
}

// The render's shape: what each policy matches, the exact username first, and
// a rule for every pin. What the rules admit and refuse is the envtest's.
func TestGuardShape(t *testing.T) {
	objs := Render(params)
	if len(objs) != 2*len(GuardNames) {
		t.Fatalf("render = %d objects, want a policy and a binding per guard name", len(objs))
	}
	match := map[string][][2]string{
		GuardSecretsName:     {{"", "secrets"}, {"", "serviceaccounts"}},
		GuardDeploymentsName: {{"apps", "deployments"}, {"", "persistentvolumeclaims"}},
		GuardBindingsName:    {{"rbac.authorization.k8s.io", "clusterrolebindings"}, {"rbac.authorization.k8s.io", "rolebindings"}},
		GuardInstallName:     {{"dotvirt.io", "dotvirts"}},
		GuardFrontDoorName: {{"", "services"}, {"route.openshift.io", "routes"}, {"networking.k8s.io", "ingresses"},
			{"gateway.networking.k8s.io", "httproutes"}},
	}
	for i, name := range GuardNames {
		p := policyOf(t, objs[i])
		if p.Name != name || *p.Spec.FailurePolicy != admissionregistrationv1.Fail || objs[i].Purpose == "" {
			t.Errorf("policy %d = %s %q", i, p.Name, objs[i].Purpose)
		}
		var got [][2]string
		for _, r := range p.Spec.MatchConstraints.ResourceRules {
			ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
			if name == GuardInstallName {
				ops = ops[1:] // the operator never creates its install
			}
			if !slices.Equal(r.Operations, ops) {
				t.Errorf("%s operations = %v", name, r.Operations)
			}
			got = append(got, [2]string{r.APIGroups[0], r.Resources[0]})
		}
		if !slices.Equal(got, match[name]) {
			t.Errorf("%s matches %v, want %v", name, got, match[name])
		}
		if len(p.Spec.MatchConditions) != 1 || p.Spec.MatchConditions[0].Expression != `request.userInfo.username == "`+params.Operator+`"` {
			t.Errorf("%s condition = %+v, want the exact username", name, p.Spec.MatchConditions)
		}
	}
	sec, dep, bind, inst := exprs(policyOf(t, objs[0])), exprs(policyOf(t, objs[1])), exprs(policyOf(t, objs[2])), exprs(policyOf(t, objs[3]))
	holds(t, "secrets", sec, `request.namespace in ["dotvirt", "openshift-gitops"]`)
	holds(t, "deployments", dep, `request.namespace == "dotvirt"`)
	holds(t, "deployments", dep, `c.image.startsWith("quay.io/epheo/dotvirt@sha256:")`)
	holds(t, "deployments", dep, `c.image in ["registry.example/team/dotvirt:custom", ""]`)
	holds(t, "deployments", dep, `a.startsWith("-metrics-url=")`)
	holds(t, "deployments", dep, `e.name != "KUBECONFIG"`)
	holds(t, "deployments", dep, `"HTTPS_PROXY", "https_proxy"`)
	holds(t, "deployments", dep, `hostAliases`)
	holds(t, "deployments", dep, `a in ["-ui-origin=", "--ui-origin="]`)
	holds(t, "deployments", dep, `"-oauth-ca=`+IngressCAPath+`"`)
	holds(t, "install", inst, `object.spec == oldObject.spec`)
	holds(t, "secrets", sec, `object.metadata.name != "dotvirt" || !has(object.automountServiceAccountToken)`)
	holds(t, "deployments", dep, `m.mountPath == "/var/run/dotvirt/forge"`)
	holds(t, "deployments", dep, `v.secret.secretName == "dotvirt-forge"`)
	holds(t, "deployments", dep, `e.valueFrom.secretKeyRef.name == "dotvirt-forgejo-admin"`)
	holds(t, "deployments", dep, `"DOTVIRT_FORGE_URL/dotvirt-forge/url"`)
	holds(t, "deployments", dep, `v.persistentVolumeClaim.claimName == "dotvirt-forgejo-data") || has(v.emptyDir)`)
	holds(t, "deployments", dep, `e.name != "DOTVIRT_OIDC_ISSUER"`)
	holds(t, "deployments", dep, `request.resource.resource != "persistentvolumeclaims" || (!has(object.spec.selector)`)
	holds(t, "bindings", bind, `"ServiceAccount/openshift-gitops/openshift-gitops-argocd-application-controller"`)
	holds(t, "bindings", bind, `request.namespace == "openshift-gitops"`)
	for _, o := range objs {
		if _, ok := Allow(Allowed(params.Namespace), o.Object); !ok {
			t.Errorf("%s %s is rendered but not allowed", o.Object.GetKind(), o.Object.GetName())
		}
		if _, ok := diff[o.Object.GroupVersionKind().GroupKind()]; !ok {
			t.Errorf("%s has no comparer", o.Object.GetKind())
		}
	}
}

func TestParamsValid(t *testing.T) {
	if !params.Valid() || params.OperatorNamespace() != "dotvirt-operator" {
		t.Fatalf("%+v: valid %v, operator namespace %q", params, params.Valid(), params.OperatorNamespace())
	}
	for name, edit := range map[string]func(*Params){
		"a user, not an account":  func(p *Params) { p.Operator = "alice" },
		"an extra colon":          func(p *Params) { p.Operator = "system:serviceaccount:a:b:c" },
		"a bad namespace":         func(p *Params) { p.Operator = "system:serviceaccount:A_B:op" },
		"no install namespace":    func(p *Params) { p.Namespace = "" },
		"a quote in a namespace":  func(p *Params) { p.ArgoNamespace = `argo"cd` },
		"no controller":           func(p *Params) { p.ArgoController = "" },
		"a quote in a controller": func(p *Params) { p.ArgoController = `a"b` },
		"no forge Secret":         func(p *Params) { p.ForgeSecret = "" },
		"a quote in a ConfigMap":  func(p *Params) { p.BrandingConfigMap = `a"b` },
		"a wildcard host":         func(p *Params) { p.ConsoleHost = "*.apps.example" },
		"an http issuer":          func(p *Params) { p.OIDCIssuer = "http://idp.example" },
	} {
		p := params
		edit(&p)
		if p.Valid() || p.Malformed() == "" {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestAllowRefusesAnythingElse(t *testing.T) {
	allowed := Allowed("dotvirt")
	ok := Render(params)[0].Object
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"another name":    func(o *unstructured.Unstructured) { o.SetName("cluster-admin") },
		"another kind":    func(o *unstructured.Unstructured) { o.SetKind("ClusterRoleBinding") },
		"another group":   func(o *unstructured.Unstructured) { o.SetAPIVersion("rbac.authorization.k8s.io/v1") },
		"another version": func(o *unstructured.Unstructured) { o.SetAPIVersion("admissionregistration.k8s.io/v1beta1") },
		"a namespace":     func(o *unstructured.Unstructured) { o.SetNamespace("kube-system") },
	} {
		o := ok.DeepCopy()
		edit(o)
		if _, allowed := Allow(allowed, o); allowed {
			t.Errorf("%s: allowed", name)
		}
	}
}

func TestCompareSaysWhatDiffers(t *testing.T) {
	want := Render(params)
	moved := params
	moved.ArgoNamespace = "argocd"
	other := params
	other.Operator = "system:serviceaccount:x:y"
	for name, c := range map[string]struct {
		live   func() *unstructured.Unstructured
		which  int
		state  State
		detail string
	}{
		"as rendered":         {func() *unstructured.Unstructured { return want[0].Object.DeepCopy() }, 0, Installed, ""},
		"missing":             {func() *unstructured.Unstructured { return nil }, 5, NotInstalled, "is missing"},
		"for another ArgoCD":  {func() *unstructured.Unstructured { return Render(moved)[0].Object }, 0, Outdated, "lacks the rule"},
		"for another account": {func() *unstructured.Unstructured { return Render(other)[1].Object }, 1, Outdated, "applies to another account"},
		"the install rule off": {func() *unstructured.Unstructured {
			o := want[3].Object.DeepCopy()
			_ = unstructured.SetNestedSlice(o.Object, nil, "spec", "validations")
			return o
		}, 3, Outdated, "lacks the rule"},
		"an extra deny-all rule": {func() *unstructured.Unstructured { return withValidations(want[1].Object, "false") }, 1, Outdated, "holds rules the render does not"},
		"a rule dropped": {func() *unstructured.Unstructured {
			o := want[1].Object.DeepCopy()
			v, _, _ := unstructured.NestedSlice(o.Object, "spec", "validations")
			_ = unstructured.SetNestedSlice(o.Object, v[1:], "spec", "validations")
			return o
		}, 1, Outdated, "lacks the rule"},
		"a warn-only binding": {func() *unstructured.Unstructured {
			o := want[len(GuardNames)].Object.DeepCopy()
			_ = unstructured.SetNestedStringSlice(o.Object, []string{"Warn"}, "spec", "validationActions")
			return o
		}, len(GuardNames), Outdated, "does not only deny"},
	} {
		got, detail := Compare(want[c.which].Object, c.live())
		if got != c.state || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: %s (%s), want %s with %q", name, got, detail, c.state, c.detail)
		}
	}
}

func withValidations(o *unstructured.Unstructured, extra string) *unstructured.Unstructured {
	o = o.DeepCopy()
	v, _, _ := unstructured.NestedSlice(o.Object, "spec", "validations")
	v = append(v, map[string]any{"expression": extra})
	_ = unstructured.SetNestedSlice(o.Object, v, "spec", "validations")
	return o
}

var update = flag.Bool("update", false, "rewrite the release template")

// The release template is the render with placeholders; filled for an
// install, it is what the console installs.
func TestReleaseTemplate(t *testing.T) {
	const path = "../../operator/install/guard.yaml"
	got, err := Template()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, got) {
		t.Fatalf("%s is stale: go test ./pkg/adminobjects -run TestReleaseTemplate -update", path)
	}
	filled := string(committed)
	for k, v := range map[string]string{
		"${OPERATOR_USERNAME}": params.Operator, "${INSTALL_NAMESPACE}": params.Namespace, "${ARGO_NAMESPACE}": params.ArgoNamespace,
		"${ARGO_CONTROLLER}": params.ArgoController, "${SPEC_IMAGE}": params.SpecImage, "${RELATED_IMAGE_DOTVIRT}": "", "${RELATED_IMAGE_FORGEJO}": "",
		"${FORGE_INSECURE_TLS}": "false", "${METRICS_URL}": "", "${METRICS_TENANCY_URL}": "", "${METRICS_CA}": "",
		"${CONSOLE_HOST}": params.ConsoleHost, "${BRANDING_CONFIGMAP}": params.BrandingConfigMap,
		"${METRICS_CA_CONFIGMAP}": params.MetricsCAConfigMap, "${FORGE_SECRET}": params.ForgeSecret,
		"${FORGE_MANAGED}": "false", "${OIDC_SECRET}": params.OIDCSecret, "${OIDC_SECRET_KEY}": params.OIDCSecretKey,
		"${OIDC_ISSUER}": params.OIDCIssuer,
	} {
		filled = strings.ReplaceAll(filled, k, v)
	}
	docs := strings.Split(filled, "\n---\n")[1:]
	want := Render(params)
	if len(docs) != len(want) {
		t.Fatalf("template holds %d objects, want %d", len(docs), len(want))
	}
	for i, doc := range docs {
		var got map[string]any
		if err := yaml.Unmarshal([]byte(doc), &got); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want[i].Object.Object)
		if string(a) != string(b) {
			t.Errorf("filled template object %d differs from the render:\n%s\n%s", i, a, b)
		}
	}
}

func TestArgoTargetDefaults(t *testing.T) {
	for _, c := range []struct {
		ns, sa    string
		openShift bool
		wantNS    string
		wantSA    string
	}{
		{"", "", true, "openshift-gitops", "openshift-gitops-argocd-application-controller"},
		{"", "", false, "argocd", "argocd-application-controller"},
		{"gitops", "", true, "gitops", "argocd-application-controller"},
		{"openshift-gitops", "custom", false, "openshift-gitops", "custom"},
	} {
		if ns, sa := ArgoTarget(c.ns, c.sa, c.openShift); ns != c.wantNS || sa != c.wantSA {
			t.Errorf("ArgoTarget(%q, %q, %v) = %s, %s; want %s, %s", c.ns, c.sa, c.openShift, ns, sa, c.wantNS, c.wantSA)
		}
	}
}

func TestSSORedirect(t *testing.T) {
	if got := SSORedirect("", "dotvirt", "apps.example"); got != "https://dotvirt-dotvirt.apps.example/api/auth/callback" {
		t.Errorf("router host: %s", got)
	}
	if got := SSORedirect("console.example", "dotvirt", "apps.example"); got != "https://console.example/api/auth/callback" {
		t.Errorf("spec host: %s", got)
	}
	if SSOClientName("dotvirt") != "dotvirt-dotvirt" {
		t.Errorf("client name %s", SSOClientName("dotvirt"))
	}
}

func TestMetricsFor(t *testing.T) {
	const thanos = "https://thanos-querier.openshift-monitoring.svc:9091"
	for _, c := range []struct {
		url, tenancy string
		ca           bool
		want         Metrics
	}{
		{"", "", true, Metrics{}},
		{thanos, "", false, Metrics{thanos, "https://thanos-querier.openshift-monitoring.svc:9092", ServiceCAPath}},
		{thanos, "https://t.example", true, Metrics{thanos, "https://t.example", MetricsCAPath}},
		{"http://prom.example:9090", "", false, Metrics{URL: "http://prom.example:9090"}},
	} {
		if got := MetricsFor(c.url, c.tenancy, c.ca); got != c.want {
			t.Errorf("MetricsFor(%q, %q, %v) = %+v, want %+v", c.url, c.tenancy, c.ca, got, c.want)
		}
	}
}
