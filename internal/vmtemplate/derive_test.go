package vmtemplate

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

const sourceVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web-01
  namespace: tenant-a
spec:
  runStrategy: Halted
  instancetype:
    name: u1.medium
  preference:
    name: fedora
  dataVolumeTemplates:
    - metadata:
        name: web-01-rootdisk
      spec:
        sourceRef:
          kind: DataSource
          name: fedora
          namespace: images
        storage:
          resources:
            requests:
              storage: 30Gi
  template:
    spec:
      domain:
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: virtio
          interfaces:
            - name: default
              masquerade: {}
      networks:
        - name: default
          pod: {}
      volumes:
        - name: rootdisk
          dataVolume:
            name: web-01-rootdisk
status:
  ready: true
`

func TestDeriveParameterizesIdentity(t *testing.T) {
	out, err := Derive([]byte(sourceVM), "web-template", "golden web tier")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"kind: VirtualMachineTemplate",
		"name: web-template",
		"description: golden web tier",
		"from: web-01-[a-z0-9]{5}",
		"name: ${NAME}-rootdisk",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("derived template missing %q:\n%s", want, s)
		}
	}
	for _, reject := range []string{"namespace: tenant-a", "status:", "name: web-01\n"} {
		if strings.Contains(s, reject) {
			t.Errorf("derived template still contains %q:\n%s", reject, s)
		}
	}
	// The blueprint's own name must be the parameter reference.
	if !strings.Contains(s, "name: ${NAME}\n") {
		t.Errorf("blueprint name not parameterized:\n%s", s)
	}
}

func TestDeriveRenderRoundTrip(t *testing.T) {
	out, err := Derive([]byte(sourceVM), "web-template", "")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	tpl := Parse("templates/web-template.yaml", out, "acme")
	if tpl.Error != "" {
		t.Fatalf("derived template does not parse: %s", tpl.Error)
	}
	r, err := EngineRenderer{}.Render(out, nil, "tenant-b")
	if err != nil {
		t.Fatalf("derived template does not render: %v", err)
	}
	if !strings.HasPrefix(r.Name, "web-01-") {
		t.Fatalf("generated name %q lost the source base", r.Name)
	}
	m := string(r.Manifest)
	if !strings.Contains(m, "namespace: tenant-b") || !strings.Contains(m, r.Name+"-rootdisk") {
		t.Fatalf("round-tripped manifest wrong:\n%s", m)
	}
}

func TestDeriveRejectsNonVM(t *testing.T) {
	if _, err := Derive([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), "t", ""); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}
