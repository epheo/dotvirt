// Package typecheck checks the operator guard's policies as the
// controller-manager does, against a real apiserver's schemas: its own module,
// outside the workspace, so the apiserver library it needs moves no version
// the app or the operator builds with.
package typecheck

import (
	"path/filepath"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// Each policy type-checks clean against the one kind it matches: a rule
// naming another kind's fields leaves a warning per rule and kind in the
// policy's status, which the controller-manager fills (envtest runs none).
func TestGuardTypeChecksAgainstItsKind(t *testing.T) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases"), filepath.Join("..", "..", "internal", "controller", "testdata", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	checker := &validating.TypeChecker{
		SchemaResolver: &resolver.ClientDiscoveryResolver{Discovery: discovery.NewDiscoveryClientForConfigOrDie(cfg)},
		RestMapper:     mapper,
	}
	// Every pin set, Delegated on, so every rule is in the render.
	p := adminobjects.Params{
		Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt",
		ArgoNamespace: "openshift-gitops", ArgoController: "openshift-gitops-argocd-application-controller",
		SpecImage: "registry.example/mirror/dotvirt:1", ConsoleHost: "console.example", ForgeSecret: "creds",
		Metrics:           adminobjects.MetricsFor("https://thanos-querier.openshift-monitoring.svc:9091", "", true),
		BrandingConfigMap: "brand", MetricsCAConfigMap: "metrics-ca", OIDCIssuer: "https://idp.example", OIDCSecret: "oidc", OIDCSecretKey: "client-secret",
		ForgeDelegated: true, ForgeURL: "https://forge.example", ForgeClientID: "dotvirt", ForgeAuthorizeURL: "https://forge.example/login/oauth/authorize",
		ForgeClientSecret: "forge-app", ForgeClientSecretKey: "client-secret",
	}
	checked := 0
	for _, o := range adminobjects.Guard(p) {
		if o.Object.GetKind() != "ValidatingAdmissionPolicy" {
			continue
		}
		var pol admissionregistrationv1.ValidatingAdmissionPolicy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object.Object, &pol); err != nil {
			t.Fatal(err)
		}
		checked++
		if w := checker.Check(&pol); len(w) > 0 {
			t.Errorf("%s type-checks with %d warnings, the first: %s", pol.Name, len(w), w[0].Warning)
		}
	}
	if checked != len(adminobjects.GuardNames) {
		t.Errorf("checked %d policies, want %d", checked, len(adminobjects.GuardNames))
	}
	mixed := &admissionregistrationv1.ValidatingAdmissionPolicy{Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
			RuleWithOperations: admissionregistrationv1.RuleWithOperations{Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"}, Resources: []string{"services", "deployments"}}},
		}}},
		Validations: []admissionregistrationv1.Validation{{Expression: `has(object.spec.template.spec.hostNetwork)`}},
	}}
	if len(checker.Check(mixed)) == 0 {
		t.Error("the type checker passes a rule naming a Deployment's field on Services; the check above proves nothing")
	}
}
