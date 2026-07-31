package hagen

import (
	"strings"
	"testing"
)

func TestManifestsDefaults(t *testing.T) {
	files, err := Manifests(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(files))
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = string(f.Content)
	}
	for _, path := range []string{NamespacePath, OperatorGroupPath, SubscriptionPath, CRPath} {
		if byPath[path] == "" {
			t.Errorf("missing file %s", path)
		}
	}
	cr := byPath[CRPath]
	for _, want := range []string{
		"kind: NodeHealthCheck",
		"name: " + CRName,
		"argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true",
		"minHealthy: 51%",
		"key: node-role.kubernetes.io/control-plane",
		"operator: DoesNotExist",
		"kind: SelfNodeRemediationTemplate",
		"name: self-node-remediation-resource-deletion-template",
		"namespace: " + Namespace,
		"duration: 300s",
		"status: Unknown",
	} {
		if !strings.Contains(cr, want) {
			t.Errorf("NodeHealthCheck missing %q:\n%s", want, cr)
		}
	}
	// Both unresponsive shapes must be covered: kubelet said NotReady, and
	// kubelet stopped talking (Unknown - the dead-host case HA exists for).
	if !strings.Contains(cr, `status: "False"`) {
		t.Errorf("NodeHealthCheck missing Ready=False condition:\n%s", cr)
	}
	sub := byPath[SubscriptionPath]
	for _, want := range []string{
		"kind: Subscription",
		"channel: stable",
		"name: node-healthcheck-operator",
		"source: redhat-operators",
		"installPlanApproval: Automatic",
	} {
		if !strings.Contains(sub, want) {
			t.Errorf("Subscription missing %q:\n%s", want, sub)
		}
	}
	// All-namespaces install mode: an OperatorGroup with no targetNamespaces.
	if og := byPath[OperatorGroupPath]; strings.Contains(og, "targetNamespaces") {
		t.Errorf("OperatorGroup must not scope targetNamespaces:\n%s", og)
	}
}

func TestManifestsCustom(t *testing.T) {
	files, err := Manifests(Spec{UnhealthySeconds: 120, MinHealthyPercent: 30})
	if err != nil {
		t.Fatal(err)
	}
	var cr string
	for _, f := range files {
		if f.Path == CRPath {
			cr = string(f.Content)
		}
	}
	for _, want := range []string{
		"minHealthy: 30%",
		"duration: 120s",
	} {
		if !strings.Contains(cr, want) {
			t.Errorf("NodeHealthCheck missing %q:\n%s", want, cr)
		}
	}
}

func TestManifestsValidate(t *testing.T) {
	for _, s := range []Spec{
		{UnhealthySeconds: 30},   // below the flap floor
		{UnhealthySeconds: 7200}, // above the ceiling
		{MinHealthyPercent: -1},  // bad percent
		{MinHealthyPercent: 101}, // bad percent
	} {
		if _, err := Manifests(s); err == nil {
			t.Errorf("expected error for spec %+v", s)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	in := Spec{UnhealthySeconds: 240, MinHealthyPercent: 40}
	files, err := Manifests(in)
	if err != nil {
		t.Fatal(err)
	}
	var cr []byte
	for _, f := range files {
		if f.Path == CRPath {
			cr = f.Content
		}
	}
	got, err := Parse(cr)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
