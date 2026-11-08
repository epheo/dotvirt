package manifest

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/epheo/dotvirt/internal/model"
)

// The layouts below each made the line editor damage or misplace what it did
// not edit, silently. Every one now either lands exactly or is refused.

func applyGuard(t *testing.T, in string, e VMEdit) ([]byte, error) {
	t.Helper()
	return ApplyEdit([]byte(in), "alpha", "alpha", "web", e)
}

func decoded(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return doc
}

func names(t *testing.T, list any) []string {
	t.Helper()
	items, _ := list.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, itemName(it))
	}
	return out
}

func TestEditRefusesFlowCollections(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		edit VMEdit
	}{
		"multi-line flow labels": {
			strings.Replace(guardVM, "  labels:\n    app: web\n", "  labels: {\n    app: web, tier: db\n  }\n", 1),
			VMEdit{SetLabels: map[string]string{"app": "api"}},
		},
		"multi-line flow memory": {
			strings.Replace(guardVM, "        memory:\n          guest: 1Gi\n", "        memory: {\n          guest: 1Gi, hugepages: {pageSize: 2Mi}\n        }\n", 1),
			VMEdit{Memory: ptr("2Gi")},
		},
		"flow labels removal": {
			strings.Replace(guardVM, "  labels:\n    app: web\n", "  labels: {app: web, tier: db}\n", 1),
			VMEdit{RemoveLabels: []string{"app"}},
		},
	} {
		if out, err := applyGuard(t, c.in, c.edit); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "flow style") {
			t.Errorf("%s: want ErrConflict naming flow style, got %v\n%s", name, err, out)
		}
	}
}

// A removed key takes its whole value: a folded scalar's continuation lines
// would otherwise stay behind as part of the previous entry.
func TestEditRemovesAMultiLineLabel(t *testing.T) {
	in := strings.Replace(guardVM, "    app: web\n", "    tier: db\n    app: >-\n      web\n", 1)
	out, err := applyGuard(t, in, VMEdit{RemoveLabels: []string{"app"}})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if got := lookup(decoded(t, out), "metadata", "labels"); !reflect.DeepEqual(got, map[string]any{"tier": "db"}) {
		t.Fatalf("labels = %#v\n%s", got, out)
	}
}

// A column-0 comment inside a block says nothing about the block's extent: an
// item, a mapping or a deleted key runs on past it.
func TestEditStepsOverColumnZeroComments(t *testing.T) {
	noCPU := strings.Replace(guardVM, "        cpu:\n          sockets: 1\n", "", 1)
	for name, c := range map[string]struct {
		in    string
		edit  VMEdit
		check func(doc map[string]any) bool
	}{
		"inside a disk item": {
			strings.Replace(guardVM, "              bus: virtio\n", "              bus: virtio\n# keep boot order\n            bootOrder: 1\n", 1),
			VMEdit{AddDisks: []model.DiskAdd{{Name: "extra", Size: "1Gi"}}},
			func(doc map[string]any) bool {
				disks := lookup(doc, "spec", "template", "spec", "domain", "devices", "disks").([]any)
				return reflect.DeepEqual(names(t, disks), []string{"data", "extra"}) && lookup(disks[0], "bootOrder") == 1
			},
		},
		"inside an interface item": {
			strings.Replace(guardVM, "          - name: default\n            masquerade: {}\n", "          - name: default\n#            macAddress: 02:00:00:00:00:01\n            masquerade: {}\n", 1),
			VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10"}}},
			func(doc map[string]any) bool {
				ifaces := lookup(doc, "spec", "template", "spec", "domain", "devices", "interfaces").([]any)
				return reflect.DeepEqual(names(t, ifaces), []string{"default", "vlan-10"}) && lookup(ifaces[0], "masquerade") != nil
			},
		},
		"inside devices, inserting cpu": {
			strings.Replace(noCPU, "          interfaces:\n", "# nics\n          interfaces:\n", 1),
			VMEdit{CPUCores: ptr(4)},
			func(doc map[string]any) bool {
				domain := lookup(doc, "spec", "template", "spec", "domain")
				return lookup(domain, "cpu", "sockets") == 4 && len(names(t, lookup(domain, "devices", "interfaces"))) == 1
			},
		},
		"inside labels, adding one": {
			strings.Replace(guardVM, "    app: web\n", "    app: web\n#   owner: bob\n    tier: db\n", 1),
			VMEdit{SetLabels: map[string]string{"env": "prod"}},
			func(doc map[string]any) bool {
				return reflect.DeepEqual(lookup(doc, "metadata", "labels"), map[string]any{"app": "web", "tier": "db", "env": "prod"})
			},
		},
		"inside labels, removing one": {
			strings.Replace(guardVM, "    app: web\n", "    app: web\n#   owner: bob\n    tier: db\n", 1),
			VMEdit{RemoveLabels: []string{"app"}},
			func(doc map[string]any) bool {
				return reflect.DeepEqual(lookup(doc, "metadata", "labels"), map[string]any{"tier": "db"})
			},
		},
	} {
		out, err := applyGuard(t, c.in, c.edit)
		if err != nil {
			t.Errorf("%s: ApplyEdit: %v", name, err)
			continue
		}
		if !c.check(decoded(t, out)) {
			t.Errorf("%s: edit misplaced:\n%s", name, out)
		}
	}
}

// Adding a disk or NIC the VM already has would duplicate the device (and a
// disk's volume and DataVolume): KubeVirt rejects the VM at sync.
func TestEditRefusesAddingWhatExists(t *testing.T) {
	for name, c := range map[string]struct {
		edit VMEdit
		msg  string
	}{
		"disk":             {VMEdit{AddDisks: []model.DiskAdd{{Name: "data", Size: "1Gi"}}}, "disk named data"},
		"disk twice":       {VMEdit{AddDisks: []model.DiskAdd{{Name: "x", Size: "1Gi"}, {Name: "x", Size: "1Gi"}}}, "disk named x"},
		"interface":        {VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10", Interface: "default"}}}, "interface named default"},
		"pod network":      {VMEdit{AddNetworks: []model.NetworkAdd{{Name: model.PodNetwork, Interface: "nic-1"}}}, "pod network"},
		"interface twice":  {VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10"}, {Name: "other/vlan-10"}}}, "interface named vlan-10"},
		"derived iface":    {VMEdit{AddNetworks: []model.NetworkAdd{{Name: "alpha/default"}}}, "interface named default"},
		"pod via its name": {VMEdit{AddNetworks: []model.NetworkAdd{{Name: model.PodNetwork}}}, "pod network"},
	} {
		if out, err := applyGuard(t, guardVM, c.edit); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: want ErrConflict naming %q, got %v\n%s", name, c.msg, err, out)
		}
	}
}

// A dataVolumeTemplates key with no list has no item to align an append with:
// it is written anew, so the added disk keeps its DataVolume.
func TestEditAddsADiskUnderANullTemplateList(t *testing.T) {
	in := strings.Replace(guardVM, "  dataVolumeTemplates:\n  - metadata:\n      name: web-data\n    spec:\n      storage:\n        resources:\n          requests:\n            storage: 10Gi\n", "  dataVolumeTemplates:\n", 1)
	out, err := applyGuard(t, in, VMEdit{AddDisks: []model.DiskAdd{{Name: "extra", Size: "5Gi", StorageClass: "fast"}}})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	vm := parseOne(t, string(out))
	for _, d := range vm.Disks {
		if d.Name == "extra" && d.Type == "dataVolume" && d.Size == "5Gi" && d.StorageClass == "fast" {
			return
		}
	}
	t.Fatalf("disk extra not provisioned as asked: %+v\n%s", vm.Disks, out)
}

// A value an anchor shares, or an alias, changes more than its own line.
func TestEditRefusesSharedValues(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		edit VMEdit
	}{
		"anchored label": {
			strings.Replace(guardVM, "    app: web\n", "    app: &a web\n    tier: *a\n", 1),
			VMEdit{SetLabels: map[string]string{"app": "api"}},
		},
		"aliased label": {
			strings.Replace(guardVM, "    app: web\n", "    app: &a web\n    tier: *a\n", 1),
			VMEdit{SetLabels: map[string]string{"tier": "db"}},
		},
		"anchored memory": {
			strings.Replace(guardVM, "          guest: 1Gi\n", "          guest: &m 1Gi\n        resources:\n          requests:\n            memory: *m\n", 1),
			VMEdit{Memory: ptr("2Gi")},
		},
	} {
		if out, err := applyGuard(t, c.in, c.edit); !errors.Is(err, model.ErrConflict) {
			t.Errorf("%s: want ErrConflict, got %v\n%s", name, err, out)
		}
	}
}

// Only the old value is replaced: a '#' inside a quoted value is not a
// comment, and a real comment after it stays.
func TestEditReplacesOnlyTheOldValue(t *testing.T) {
	for old, want := range map[string]string{
		`app: "web #1"`:            "    app: api\n",
		`app: 'it''s #x' # real`:   "    app: api # real\n",
		`app: "a \" #b" # c`:       "    app: api # c\n",
		`app: !!str web # c`:       "    app: api # c\n",
		"app: web   # spaced":      "    app: api   # spaced\n",
		"app: web#not-a-comment":   "    app: api\n",
		`app: "web"` + "\t# tab":   "    app: api\t# tab\n",
		`app: 'web'`:               "    app: api\n",
		"app: web # one # two":     "    app: api # one # two\n",
		`app: !!str "w #1" # tail`: "    app: api # tail\n",
	} {
		in := strings.Replace(guardVM, "    app: web\n", "    "+old+"\n", 1)
		out, err := applyGuard(t, in, VMEdit{SetLabels: map[string]string{"app": "api"}})
		if err != nil || !strings.Contains(string(out), want) {
			t.Errorf("%s: want %q, got %v\n%s", old, want, err, out)
		}
	}
	in := strings.Replace(guardVM, "    app: web\n", "    app: \"web\n      #1\"\n", 1)
	if out, err := applyGuard(t, in, VMEdit{SetLabels: map[string]string{"app": "api"}}); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a quoted value spanning lines: want ErrConflict, got %v\n%s", err, out)
	}
}

// The scope check owns nothing beyond what the edit names: a sibling of an
// edited label, or another document of the file, may not move.
func TestVerifyScopeRefusesCollateralDamage(t *testing.T) {
	edit := VMEdit{SetLabels: map[string]string{"env": "prod"}}
	out, err := applyGuard(t, guardVM, edit)
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	for name, damaged := range map[string]string{
		"a sibling label":            strings.Replace(string(out), "    app: web\n", "", 1),
		"a label's type":             strings.Replace(string(out), "    app: web\n", "    app: true\n", 1),
		"a disk beside no disk edit": strings.Replace(string(out), "              bus: virtio\n", "              bus: sata\n", 1),
	} {
		if err := verifyScope([]byte(guardVM), []byte(damaged), "alpha", "alpha", "web", edit); !errors.Is(err, model.ErrConflict) {
			t.Errorf("%s: want ErrConflict, got %v", name, err)
		}
	}
	other := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  k: v\n"
	before := guardVM + "---\n" + other
	after := string(out) + "---\n" + strings.Replace(other, "k: v", "k: w", 1)
	if err := verifyScope([]byte(before), []byte(after), "alpha", "alpha", "web", edit); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "document 2") {
		t.Errorf("another document: want ErrConflict naming it, got %v", err)
	}
	if err := verifyScope([]byte(before), []byte(string(out)+"---\n"+other), "alpha", "alpha", "web", edit); err != nil {
		t.Errorf("the edit alone must pass: %v", err)
	}
}

// Lines of a block scalar are its value even when they start with '#'
// (cloud-init's "#cloud-config"): an insert after a cloud-init volume listed
// last must land past the whole script, never inside it.
func TestEditTreatsBlockScalarLinesAsContent(t *testing.T) {
	userData := "#cloud-config\npassword: x\n# end\n"
	ci := strings.Replace(guardVM, "      volumes:\n      - name: data\n        dataVolume:\n          name: web-data\n",
		"      volumes:\n      - name: data\n        dataVolume:\n          name: web-data\n      - name: cloudinit\n        cloudInitNoCloud:\n          userData: |\n            #cloud-config\n            password: x\n            # end\n", 1)
	ci = strings.Replace(ci, "              bus: virtio\n", "              bus: virtio\n          - name: cloudinit\n            disk:\n              bus: virtio\n", 1)
	noEviction := strings.Replace(ci, "      evictionStrategy: LiveMigrate\n", "", 1)
	for name, c := range map[string]struct {
		in   string
		edit VMEdit
	}{
		"append a volume after it":   {ci, VMEdit{AddDisks: []model.DiskAdd{{Name: "extra", Size: "1Gi"}}}},
		"insert into the spec":       {noEviction, VMEdit{EvictionStrategy: ptr("None")}},
		"remove the volume above it": {ci, VMEdit{RemoveDisks: []string{"data"}}},
	} {
		out, err := applyGuard(t, c.in, c.edit)
		if err != nil {
			t.Errorf("%s: ApplyEdit: %v", name, err)
			continue
		}
		vols, _ := lookup(decoded(t, out), "spec", "template", "spec", "volumes").([]any)
		var got any
		for _, v := range vols {
			if itemName(v) == "cloudinit" {
				got = lookup(v, "cloudInitNoCloud", "userData")
			}
		}
		if got != userData {
			t.Errorf("%s: userData = %q, want %q\n%s", name, got, userData, out)
		}
	}
	out, err := applyGuard(t, ci, VMEdit{RemoveDisks: []string{"cloudinit"}})
	if err != nil || strings.Contains(string(out), "# end") || strings.Contains(string(out), "cloud-config") {
		t.Errorf("removing the cloud-init disk must take its whole script: %v\n%s", err, out)
	}
}

// KubeVirt reads meaning from empty maps (a binding's `masquerade: {}`, a
// network's `pod: {}`, `efi: {}` against `bios: {}`), from nulls, and from
// the order of devices (boot order without bootOrder). The scope check sees
// each: damage to any of them beside an edit is refused.
func TestVerifyScopeSeesEmptyValuesAndOrder(t *testing.T) {
	addNIC := VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10"}}}
	label := VMEdit{SetLabels: map[string]string{"env": "prod"}}
	withNIC, err := applyGuard(t, guardVM, addNIC)
	if err != nil {
		t.Fatalf("add NIC: %v", err)
	}
	firmware := strings.Replace(guardVM, "        devices:\n", "        firmware:\n          bootloader:\n            efi: {}\n        devices:\n", 1)
	twoDisks := strings.Replace(guardVM, "          - name: data\n            disk:\n              bus: virtio\n",
		"          - name: root\n            disk:\n              bus: virtio\n          - name: data\n            disk:\n              bus: virtio\n", 1)
	labeled := func(in string) string {
		out, err := applyGuard(t, in, label)
		if err != nil {
			t.Fatalf("label: %v", err)
		}
		return string(out)
	}
	moved := strings.Replace(string(withNIC), "          - name: default\n            masquerade: {}\n", "          - name: default\n", 1)
	moved = strings.Replace(moved, "            bridge: {}\n", "            bridge: {}\n            masquerade: {}\n", 1)
	for name, c := range map[string]struct {
		before, after string
		edit          VMEdit
	}{
		"a binding moved to the new NIC": {guardVM, moved, addNIC},
		"pod: {} dropped":                {guardVM, strings.Replace(string(withNIC), "      - name: default\n        pod: {}\n", "      - name: default\n", 1), addNIC},
		"efi swapped for bios":           {firmware, strings.Replace(labeled(firmware), "efi: {}", "bios: {}", 1), label},
		"disks reordered": {twoDisks, strings.Replace(labeled(twoDisks),
			"          - name: root\n            disk:\n              bus: virtio\n          - name: data\n",
			"          - name: data\n            disk:\n              bus: virtio\n          - name: root\n", 1), label},
		"a null key added": {guardVM, strings.Replace(labeled(guardVM), "  runStrategy: Always\n", "  runStrategy: Always\n  running:\n", 1), label},
	} {
		if c.after == c.before {
			t.Fatalf("%s: the damage did not apply", name)
		}
		if err := verifyScope([]byte(c.before), []byte(c.after), "alpha", "alpha", "web", c.edit); !errors.Is(err, model.ErrConflict) {
			t.Errorf("%s: want ErrConflict, got %v", name, err)
		}
	}

	// Recreating an empty or null container the edit fills stays allowed.
	empty := strings.Replace(guardVM, "  labels:\n    app: web\n", "  labels: {}\n", 1)
	if _, err := applyGuard(t, empty, label); err != nil {
		t.Errorf("labels: {} filled: %v", err)
	}
}
