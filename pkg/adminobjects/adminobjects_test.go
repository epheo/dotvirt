package adminobjects

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"strconv"
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
		GuardSecretsName:     {{"", "secrets"}},
		GuardAccountsName:    {{"", "serviceaccounts"}},
		GuardDeploymentsName: {{"apps", "deployments"}},
		GuardClaimsName:      {{"", "persistentvolumeclaims"}},
		GuardBindingsName:    {{"rbac.authorization.k8s.io", "clusterrolebindings"}, {"rbac.authorization.k8s.io", "rolebindings"}},
		GuardInstallName:     {{"dotvirt.io", "dotvirts"}},
		GuardFrontDoorName:   {{"", "services"}},
		GuardRoutesName:      {{"route.openshift.io", "routes"}},
		GuardIngressesName:   {{"networking.k8s.io", "ingresses"}},
		GuardHTTPRoutesName:  {{"gateway.networking.k8s.io", "httproutes"}},
	}
	for _, name := range GuardNames {
		i := index(objs, "ValidatingAdmissionPolicy", name)
		if i < 0 || index(objs, "ValidatingAdmissionPolicyBinding", name) != i+1 {
			t.Fatalf("%s: policy at %d, want its binding right after", name, i)
		}
		p := policyOf(t, objs[i])
		if *p.Spec.FailurePolicy != admissionregistrationv1.Fail || objs[i].Purpose == "" {
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
	named := func(name string) []string {
		return exprs(policyOf(t, objs[index(objs, "ValidatingAdmissionPolicy", name)]))
	}
	sec, acc, dep, claims := named(GuardSecretsName), named(GuardAccountsName), named(GuardDeploymentsName), named(GuardClaimsName)
	bind, inst := named(GuardBindingsName), named(GuardInstallName)
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
	holds(t, "accounts", acc, `object.metadata.name != "dotvirt" || !has(object.automountServiceAccountToken)`)
	holds(t, "deployments", dep, `m.mountPath == "/var/run/dotvirt/forge"`)
	holds(t, "deployments", dep, `v.secret.secretName == "dotvirt-forge"`)
	holds(t, "deployments", dep, `e.valueFrom.secretKeyRef.name == "dotvirt-forgejo-admin"`)
	holds(t, "deployments", dep, `"DOTVIRT_FORGE_URL/dotvirt-forge/url"`)
	holds(t, "deployments", dep, `v.persistentVolumeClaim.claimName == "dotvirt-forgejo-data") || has(v.emptyDir)`)
	holds(t, "deployments", dep, `e.name != "DOTVIRT_OIDC_ISSUER"`)
	holds(t, "claims", claims, `!has(object.spec.selector) && !has(object.spec.dataSource)`)
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
	secrets, deployments := index(want, "ValidatingAdmissionPolicy", GuardSecretsName), index(want, "ValidatingAdmissionPolicy", GuardDeploymentsName)
	install, binding := index(want, "ValidatingAdmissionPolicy", GuardInstallName), index(want, "ValidatingAdmissionPolicyBinding", GuardSecretsName)
	for name, c := range map[string]struct {
		live   func() *unstructured.Unstructured
		which  int
		state  State
		detail string
	}{
		"as rendered":         {func() *unstructured.Unstructured { return want[secrets].Object.DeepCopy() }, secrets, Installed, ""},
		"missing":             {func() *unstructured.Unstructured { return nil }, binding, NotInstalled, "is missing"},
		"for another ArgoCD":  {func() *unstructured.Unstructured { return Render(moved)[secrets].Object }, secrets, Outdated, "lacks the rule"},
		"for another account": {func() *unstructured.Unstructured { return Render(other)[deployments].Object }, deployments, Outdated, "applies to another account"},
		"the install rule off": {func() *unstructured.Unstructured {
			o := want[install].Object.DeepCopy()
			_ = unstructured.SetNestedSlice(o.Object, nil, "spec", "validations")
			return o
		}, install, Outdated, "lacks the rule"},
		"an extra deny-all rule": {func() *unstructured.Unstructured { return withValidations(want[deployments].Object, "false") }, deployments, Outdated, "holds rules the render does not"},
		"a rule dropped": {func() *unstructured.Unstructured {
			o := want[deployments].Object.DeepCopy()
			v, _, _ := unstructured.NestedSlice(o.Object, "spec", "validations")
			_ = unstructured.SetNestedSlice(o.Object, v[1:], "spec", "validations")
			return o
		}, deployments, Outdated, "lacks the rule"},
		"a warn-only binding": {func() *unstructured.Unstructured {
			o := want[binding].Object.DeepCopy()
			_ = unstructured.SetNestedStringSlice(o.Object, []string{"Warn"}, "spec", "validationActions")
			return o
		}, binding, Outdated, "does not only deny"},
	} {
		got, detail := Compare(want[c.which].Object, c.live())
		if got != c.state || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: %s (%s), want %s with %q", name, got, detail, c.state, c.detail)
		}
	}
}

// index is where objs holds the kind named name, -1 if nowhere.
func index(objs []Object, kind, name string) int {
	return slices.IndexFunc(objs, func(o Object) bool { return o.Object.GetKind() == kind && o.Object.GetName() == name })
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
	docs := filledTemplate(t, committed, params)
	want := Render(params)
	if len(docs) != len(want) {
		t.Fatalf("template holds %d objects, want %d", len(docs), len(want))
	}
	for i, doc := range docs {
		a, _ := json.Marshal(doc.Object)
		b, _ := json.Marshal(want[i].Object.Object)
		if string(a) != string(b) {
			t.Errorf("filled template object %d differs from the render:\n%s\n%s", i, a, b)
		}
	}
}

// filledTemplate is template filled with p as its recipe fills it: an image
// in its official repository left empty.
func filledTemplate(t *testing.T, template []byte, p Params) []*unstructured.Unstructured {
	t.Helper()
	outside := func(repository, img string) string {
		if InRepository(repository, img) {
			return ""
		}
		return img
	}
	if !p.ForgeDelegated {
		p.ForgeURL, p.ForgeClientID, p.ForgeAuthorizeURL, p.ForgeTokenURL, p.ForgeClientSecret, p.ForgeClientSecretKey = "", "", "", "", "", ""
	}
	filled := string(template)
	for k, v := range map[string]string{
		"${OPERATOR_USERNAME}": p.Operator, "${INSTALL_NAMESPACE}": p.Namespace, "${ARGO_NAMESPACE}": p.ArgoNamespace,
		"${ARGO_CONTROLLER}": p.ArgoController, "${SPEC_IMAGE}": outside(Repositories[0], p.SpecImage),
		"${RELATED_IMAGE_DOTVIRT}": outside(Repositories[0], p.DotvirtImage), "${RELATED_IMAGE_FORGEJO}": outside(Repositories[1], p.ForgejoImage),
		"${FORGE_INSECURE_TLS}": strconv.FormatBool(p.InsecureTLS), "${METRICS_URL}": p.Metrics.URL,
		"${METRICS_TENANCY_URL}": p.Metrics.TenancyURL, "${METRICS_CA}": p.Metrics.CA,
		"${CONSOLE_HOST}": p.ConsoleHost, "${BRANDING_CONFIGMAP}": p.BrandingConfigMap,
		"${METRICS_CA_CONFIGMAP}": p.MetricsCAConfigMap, "${FORGE_SECRET}": p.ForgeSecret,
		"${FORGE_MANAGED}": strconv.FormatBool(p.ForgeManaged), "${OIDC_SECRET}": p.OIDCSecret, "${OIDC_SECRET_KEY}": p.OIDCSecretKey,
		"${OIDC_ISSUER}": p.OIDCIssuer, "${FORGE_DELEGATED}": strconv.FormatBool(p.ForgeDelegated),
		"${FORGE_URL}": p.ForgeURL, "${FORGE_CLIENT_ID}": p.ForgeClientID, "${FORGE_AUTHORIZE_URL}": p.ForgeAuthorizeURL,
		"${FORGE_TOKEN_URL}": p.ForgeTokenURL, "${FORGE_CLIENT_SECRET}": p.ForgeClientSecret, "${FORGE_CLIENT_SECRET_KEY}": p.ForgeClientSecretKey,
	} {
		filled = strings.ReplaceAll(filled, k, v)
	}
	if m := placeholder.FindString(filled); m != "" {
		t.Fatalf("the fill leaves %s", m)
	}
	var out []*unstructured.Unstructured
	for _, doc := range strings.Split(filled, "\n---\n")[1:] {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatal(err)
		}
		out = append(out, &unstructured.Unstructured{Object: o})
	}
	return out
}

// A release that only moves its images' digests inside the official
// repositories leaves an installed guard Installed, whether the console or
// the release template rendered it: upgrades stay automatic. An image an
// admin chose outside them stays pinned.
func TestDigestOnlyReleasesStayInstalled(t *testing.T) {
	release := func(digest, specImage string) Params {
		p := params
		p.SpecImage = specImage
		p.DotvirtImage = Repositories[0] + "@sha256:" + strings.Repeat(digest, 64)
		p.ForgejoImage = Repositories[1] + ":11-rootless@sha256:" + strings.Repeat(digest, 64)
		return p
	}
	template, err := Template()
	if err != nil {
		t.Fatal(err)
	}
	for _, specImage := range []string{"", Repositories[0] + ":pinned-by-the-admin"} {
		prev, next := release("a", specImage), release("b", specImage)
		for source, installed := range map[string][]*unstructured.Unstructured{
			"the console's render":        objects(Render(prev)),
			"the filled release template": filledTemplate(t, template, prev),
		} {
			for i, want := range Render(next) {
				if state, detail := Compare(want.Object, installed[i]); state != Installed {
					t.Errorf("spec.image %q, installed from %s: %s %s", specImage, source, state, detail)
				}
			}
		}
	}
	// A mirror (a disconnected install) is no official repository: its
	// release image stays pinned, and a new one reads Outdated.
	prev, next := release("a", ""), release("b", "")
	prev.DotvirtImage = "registry.example/mirror/dotvirt@sha256:" + strings.Repeat("a", 64)
	next.DotvirtImage = "registry.example/mirror/dotvirt@sha256:" + strings.Repeat("b", 64)
	outdated := false
	for i, want := range Render(next) {
		if state, _ := Compare(want.Object, Render(prev)[i].Object); state == Outdated {
			outdated = true
		}
	}
	if !outdated {
		t.Error("a mirrored release image moved, and the installed guard still reads Installed")
	}
}

func objects(os []Object) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range os {
		out = append(out, o.Object)
	}
	return out
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

// Turning Delegated on changes what the console may be given (its forge
// application and the URLs users' tokens go to), so a guard installed before
// reads Outdated until an admin re-installs it.
func TestDelegatedReadsOutdated(t *testing.T) {
	delegated := params
	delegated.ForgeDelegated, delegated.ForgeURL = true, "https://forge.example"
	delegated.ForgeClientID, delegated.ForgeAuthorizeURL = "dotvirt", "https://forge.example/login/oauth/authorize"
	delegated.ForgeClientSecret, delegated.ForgeClientSecretKey = "forge-app", "client-secret"
	installed := Render(params)
	outdated := false
	for i, want := range Render(delegated) {
		if state, _ := Compare(want.Object, installed[i].Object); state == Outdated {
			outdated = true
		}
	}
	if !outdated {
		t.Error("Delegated turned on, and the guard installed before reads Installed")
	}
}

// Installing over the previous release's guard, object by object in the
// render's order, never leaves a kind it guarded unguarded: an install that
// stops partway keeps every kind the previous guard covered.
func TestInstallKeepsEveryKindGuarded(t *testing.T) {
	previous, err := os.ReadFile("../../operator/internal/controller/testdata/guard-previous.yaml")
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]*unstructured.Unstructured{}
	key := func(o *unstructured.Unstructured) string { return o.GetKind() + "/" + o.GetName() }
	for _, doc := range strings.Split(string(previous), "\n---\n")[1:] {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: o}
		live[key(u)] = u
	}
	// guarded are the kinds a bound policy matches.
	guarded := func() map[string]bool {
		out := map[string]bool{}
		for _, b := range live {
			if b.GetKind() != "ValidatingAdmissionPolicyBinding" {
				continue
			}
			name, _, _ := unstructured.NestedString(b.Object, "spec", "policyName")
			p := live["ValidatingAdmissionPolicy/"+name]
			if p == nil {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(p.Object, "spec", "matchConstraints", "resourceRules")
			for _, r := range rules {
				groups, _, _ := unstructured.NestedStringSlice(r.(map[string]any), "apiGroups")
				resources, _, _ := unstructured.NestedStringSlice(r.(map[string]any), "resources")
				for _, g := range groups {
					for _, res := range resources {
						out[g+"/"+res] = true
					}
				}
			}
		}
		return out
	}
	before := guarded()
	if len(before) == 0 {
		t.Fatal("the previous guard guards nothing; the check would prove nothing")
	}
	for i, o := range Render(params) {
		live[key(o.Object)] = o.Object
		now := guarded()
		for kind := range before {
			if !now[kind] {
				t.Errorf("after %d objects (up to %s), %s is unguarded", i+1, key(o.Object), kind)
			}
		}
	}
}

// An admin's userAuth set while pull requests are not Delegated renders the
// same guard as none: it pins nothing, so it admits nothing extra.
func TestUserAuthWithoutDelegatedPinsNothing(t *testing.T) {
	ahead := params
	ahead.ForgeURL, ahead.ForgeClientID, ahead.ForgeAuthorizeURL = "https://forge.example", "dotvirt", "https://forge.example/login/oauth/authorize"
	ahead.ForgeClientSecret, ahead.ForgeClientSecretKey = "forge-app", "client-secret"
	a, _ := json.Marshal(objects(Render(ahead)))
	b, _ := json.Marshal(objects(Render(params)))
	if string(a) != string(b) {
		t.Error("userAuth without Delegated changes the guard")
	}
}

// A lookalike of an official repository is no official repository: an
// admin's image there stays pinned.
func TestLookalikeImagesStayPinned(t *testing.T) {
	for _, img := range []string{Repositories[0] + "-mirror:x", Repositories[0] + "x@sha256:" + strings.Repeat("a", 64), Repositories[1] + "-fork:12"} {
		p := params
		p.SpecImage, p.DotvirtImage, p.ForgejoImage = img, img, img
		deployments := Render(p)[index(Render(p), "ValidatingAdmissionPolicy", GuardDeploymentsName)].Object
		body, _ := json.Marshal(deployments.Object)
		if !strings.Contains(string(body), `\"`+img+`\"`) {
			t.Errorf("%s is left out of the admin's images", img)
		}
	}
}
