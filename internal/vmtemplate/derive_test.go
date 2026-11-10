package vmtemplate

import (
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

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
            - name: vm-net
              bridge: {}
            - name: shared
              bridge: {}
      networks:
        - name: default
          pod: {}
        - name: vm-net
          multus:
            networkName: tenant-a/vm-net
        - name: shared
          multus:
            networkName: openshift-cnv/shared
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

// A disk cloning from the source VM's own namespace clones the same-named
// source in the deploy's namespace; another namespace's (a golden image) is
// kept.
func TestDeriveUnanchorsOwnNamespaceSources(t *testing.T) {
	own := strings.Replace(sourceVM, "          name: fedora\n          namespace: images\n",
		"          name: fedora\n          namespace: tenant-a\n", 1)
	own = strings.Replace(own, "  template:\n", "    - metadata:\n        name: web-01-data\n      spec:\n        source:\n          pvc:\n            name: golden\n            namespace: tenant-a\n        storage: {}\n"+
		"    - metadata:\n        name: web-01-snap\n      spec:\n        source:\n          snapshot:\n            name: golden-snap\n            namespace: tenant-a\n        storage: {}\n  template:\n", 1)
	if n := strings.Count(own, "namespace: tenant-a"); n != 4 {
		t.Fatalf("fixture names tenant-a %d times, want the VM, the DataSource, the PVC and the snapshot", n)
	}
	out, err := Derive([]byte(own), "web-template", "")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if strings.Contains(string(out), "tenant-a") {
		t.Errorf("the source namespace survived:\n%s", out)
	}
	// Deployed, the PVC and the snapshot name the deploy's namespace, as CDI
	// requires; the DataSource stays bare, which CDI reads as the same.
	r, err := EngineRenderer{}.Render(out, nil, "tenant-b")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var vm struct {
		Spec struct {
			DataVolumeTemplates []struct {
				Spec struct {
					Source *struct {
						PVC, Snapshot *struct{ Namespace string }
					}
					SourceRef *struct{ Namespace *string }
				}
			} `json:"dataVolumeTemplates"`
		}
	}
	if err := yaml.Unmarshal(r.Manifest, &vm); err != nil || len(vm.Spec.DataVolumeTemplates) != 3 {
		t.Fatalf("rendered: %v\n%s", err, r.Manifest)
	}
	dvts := vm.Spec.DataVolumeTemplates
	if ns := dvts[0].Spec.SourceRef; ns == nil || ns.Namespace != nil {
		t.Errorf("DataSource ref = %+v, want bare", ns)
	}
	if got := dvts[1].Spec.Source.PVC.Namespace; got != "tenant-b" {
		t.Errorf("PVC source namespace = %q, want tenant-b", got)
	}
	if got := dvts[2].Spec.Source.Snapshot.Namespace; got != "tenant-b" {
		t.Errorf("snapshot source namespace = %q, want tenant-b", got)
	}
	kept, err := Derive([]byte(sourceVM), "web-template", "")
	if err != nil || !strings.Contains(string(kept), "namespace: images") {
		t.Errorf("another namespace's source must stay: %v\n%s", err, kept)
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
	// The source namespace's network is the target's same-named one; another
	// namespace's (a global one) is kept.
	if strings.Contains(m, "tenant-a/") || !strings.Contains(m, "networkName: vm-net") || !strings.Contains(m, "networkName: openshift-cnv/shared") {
		t.Fatalf("network references wrong:\n%s", m)
	}
}

func TestDeriveRejectsNonVM(t *testing.T) {
	if _, err := Derive([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), "t", ""); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}
