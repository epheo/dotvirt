package drsgen

import (
	"strings"
	"testing"
)

func TestManifestsDefaults(t *testing.T) {
	files, err := Manifests(Spec{Mode: ModeAutomatic})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("expected 4 files without PSI, got %d", len(files))
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
		"kind: KubeDescheduler",
		"name: cluster",
		"namespace: " + Namespace,
		"managementState: Managed",
		"mode: Automatic",
		"deschedulingIntervalSeconds: 60",
		"- KubeVirtRelieveAndMigrate",
		"devActualUtilizationProfile: PrometheusCPUCombined",
		"devDeviationThresholds: AsymmetricLow",
		"devEnableSoftTainter: true",
		"node: 2",
		"total: 5",
	} {
		if !strings.Contains(cr, want) {
			t.Errorf("KubeDescheduler missing %q:\n%s", want, cr)
		}
	}
	sub := byPath[SubscriptionPath]
	for _, want := range []string{
		"kind: Subscription",
		"channel: stable",
		"name: cluster-kube-descheduler-operator",
		"source: redhat-operators",
		"installPlanApproval: Automatic",
	} {
		if !strings.Contains(sub, want) {
			t.Errorf("Subscription missing %q:\n%s", want, sub)
		}
	}
	if ns := byPath[NamespacePath]; !strings.Contains(ns, `openshift.io/cluster-monitoring: "true"`) {
		t.Errorf("Namespace missing cluster-monitoring label:\n%s", ns)
	}
	if og := byPath[OperatorGroupPath]; !strings.Contains(og, "targetNamespaces") {
		t.Errorf("OperatorGroup missing targetNamespaces:\n%s", og)
	}
}

func TestManifestsPSI(t *testing.T) {
	files, err := Manifests(Spec{Mode: ModePredictive, InstallPSI: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("expected 5 files with PSI, got %d", len(files))
	}
	last := files[len(files)-1]
	if last.Path != PSIPath {
		t.Errorf("PSI path = %q", last.Path)
	}
	y := string(last.Content)
	for _, want := range []string{
		"kind: MachineConfig",
		"machineconfiguration.openshift.io/role: worker",
		"- psi=1",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("MachineConfig missing %q:\n%s", want, y)
		}
	}
}

func TestManifestsCustom(t *testing.T) {
	soft := false
	files, err := Manifests(Spec{
		Mode: ModePredictive, Threshold: "High", IntervalSeconds: 300,
		SoftTainter: &soft, EvictionNodeLimit: 1, EvictionTotalLimit: 3,
	})
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
		"mode: Predictive",
		"devDeviationThresholds: High",
		"deschedulingIntervalSeconds: 300",
		"devEnableSoftTainter: false",
		"node: 1",
		"total: 3",
	} {
		if !strings.Contains(cr, want) {
			t.Errorf("KubeDescheduler missing %q:\n%s", want, cr)
		}
	}
}

func TestManifestsValidate(t *testing.T) {
	for _, s := range []Spec{
		{},                                      // mode required
		{Mode: "Auto"},                          // bad mode
		{Mode: ModeAutomatic, Threshold: "Max"}, // bad threshold
		{Mode: ModeAutomatic, IntervalSeconds: 5},         // interval too short
		{Mode: ModeAutomatic, EvictionNodeLimit: -1},      // bad node limit
		{Mode: ModeAutomatic, EvictionTotalLimit: 100000}, // bad total limit
	} {
		if _, err := Manifests(s); err == nil {
			t.Errorf("expected error for spec %+v", s)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	soft := false
	in := Spec{Mode: ModeAutomatic, Threshold: "Medium", IntervalSeconds: 120,
		SoftTainter: &soft, EvictionNodeLimit: 3, EvictionTotalLimit: 7}
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
	if got.Mode != in.Mode || got.Threshold != in.Threshold ||
		got.IntervalSeconds != in.IntervalSeconds ||
		got.SoftTainter == nil || *got.SoftTainter != soft ||
		got.EvictionNodeLimit != in.EvictionNodeLimit ||
		got.EvictionTotalLimit != in.EvictionTotalLimit {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
