package manifest

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// drsVM has a template WITH metadata (the demo-repo shape); realisticVM (in
// edit_test.go) has a template WITHOUT one — the two shapes the annotation
// edit must handle.
const drsVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/domain: web
    spec:
      evictionStrategy: LiveMigrate
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1Gi
`

func TestApplyEditDRSExcludeAddsAnnotation(t *testing.T) {
	out, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{DRSExclude: ptr(true)})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"`+PreferNoEvictionAnnotation+`": "true"`) {
		t.Fatalf("annotation not added:\n%s", got)
	}
	// The annotation value must survive a YAML round trip as a STRING (a bare
	// true would be a bool, which the API rejects for annotations).
	var doc struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Annotations map[string]string `yaml:"annotations"`
				} `yaml:"metadata"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("edited manifest no longer parses: %v", err)
	}
	if doc.Spec.Template.Metadata.Annotations[PreferNoEvictionAnnotation] != "true" {
		t.Fatalf("annotation not a string 'true': %+v", doc.Spec.Template.Metadata.Annotations)
	}
	if !strings.Contains(got, "kubevirt.io/domain: web") {
		t.Error("edit disturbed the template labels")
	}
}

func TestApplyEditDRSExcludeRemovesAnnotation(t *testing.T) {
	withAnnotation, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{DRSExclude: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyEdit(withAnnotation, "alpha", "web", VMEdit{DRSExclude: ptr(false)})
	if err != nil {
		t.Fatalf("ApplyEdit remove: %v", err)
	}
	if strings.Contains(string(out), PreferNoEvictionAnnotation) {
		t.Fatalf("annotation not removed:\n%s", out)
	}
}

func TestApplyEditDRSExcludeCreatesTemplateMetadata(t *testing.T) {
	// realisticVM's template has no metadata block at all.
	out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", VMEdit{DRSExclude: ptr(true)})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"`+PreferNoEvictionAnnotation+`": "true"`) {
		t.Fatalf("annotation not added:\n%s", got)
	}
	// The created block must land under template, not under template.spec.
	vms, err := ParseVMs("vm.yaml", out, "vm-health-gitops")
	if err != nil || len(vms) != 1 {
		t.Fatalf("edited manifest no longer parses: %v (%d VMs)", err, len(vms))
	}
	if !vms[0].DRSExclude {
		t.Fatalf("parser doesn't see the exclusion:\n%s", got)
	}
}

func TestApplyEditEvictionStrategy(t *testing.T) {
	// Change an existing strategy in place.
	out, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{EvictionStrategy: ptr("None")})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if !strings.Contains(string(out), "evictionStrategy: None") {
		t.Fatalf("strategy not changed:\n%s", out)
	}
	if n := changedLines(drsVM, string(out)); n != 1 {
		t.Errorf("expected 1 changed line, got %d:\n%s", n, unifiedish(drsVM, string(out)))
	}

	// Empty string removes the line (cluster default).
	out, err = ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{EvictionStrategy: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "evictionStrategy") {
		t.Fatalf("strategy not removed:\n%s", out)
	}

	// Insert when absent.
	out, err = ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", VMEdit{EvictionStrategy: ptr("LiveMigrate")})
	if err != nil {
		t.Fatal(err)
	}
	vms, err := ParseVMs("vm.yaml", out, "vm-health-gitops")
	if err != nil || len(vms) != 1 {
		t.Fatalf("edited manifest no longer parses: %v", err)
	}
	if vms[0].EvictionStrategy != "LiveMigrate" {
		t.Fatalf("strategy not inserted where the parser reads it:\n%s", out)
	}
}
