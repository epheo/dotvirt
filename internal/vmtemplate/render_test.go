package vmtemplate

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

const testTemplate = `apiVersion: template.kubevirt.io/v1beta1
kind: VirtualMachineTemplate
metadata:
  name: t
spec:
  parameters:
    - name: NAME
      generate: expression
      from: "vm-[a-z0-9]{5}"
    - name: CPU_MODEL
      value: host-model
    - name: PASSWORD
      required: true
  virtualMachine:
    apiVersion: kubevirt.io/v1
    kind: VirtualMachine
    metadata:
      name: ${NAME}
      namespace: hardcoded
    spec:
      runStrategy: Halted
      template:
        spec:
          domain:
            cpu:
              model: ${CPU_MODEL}
            devices: {}
            firmware:
              serial: ${PASSWORD}
`

func TestRenderSubstitutesAndStampsNamespace(t *testing.T) {
	r, err := EngineRenderer{}.Render([]byte(testTemplate),
		map[string]string{"NAME": "web-01", "PASSWORD": "s3cret"}, "tenant-a")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if r.Name != "web-01" || r.Namespace != "tenant-a" {
		t.Fatalf("got name=%q namespace=%q", r.Name, r.Namespace)
	}
	out := string(r.Manifest)
	for _, want := range []string{"name: web-01", "namespace: tenant-a", "model: host-model", "serial: s3cret"} {
		if !strings.Contains(out, want) {
			t.Errorf("manifest missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{"${", "hardcoded", "status:", "creationTimestamp"} {
		if strings.Contains(out, reject) {
			t.Errorf("manifest still contains %q:\n%s", reject, out)
		}
	}
}

func TestRenderGeneratesNameFromExpression(t *testing.T) {
	r, err := EngineRenderer{}.Render([]byte(testTemplate),
		map[string]string{"PASSWORD": "s3cret"}, "tenant-a")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !regexp.MustCompile(`^vm-[a-z0-9]{5}$`).MatchString(r.Name) {
		t.Fatalf("generated name %q does not match the from pattern", r.Name)
	}
}

func TestRenderErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		params map[string]string
	}{
		{"required parameter missing", testTemplate, map[string]string{}},
		{"unknown parameter", testTemplate, map[string]string{"PASSWORD": "x", "NOPE": "y"}},
		{"invalid template YAML", "{not yaml", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EngineRenderer{}.Render([]byte(tc.raw), tc.params, "ns")
			if !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}
