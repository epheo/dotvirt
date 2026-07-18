package manifest

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// reparse runs the edited manifest back through ParseVMs: every scheduling
// edit must round-trip through the same reader the inventory uses.
func reparse(t *testing.T, content []byte, ns, name string) model.VM {
	t.Helper()
	vms, err := ParseVMs("vms/test.yaml", content, ns)
	if err != nil {
		t.Fatalf("edited manifest no longer parses: %v", err)
	}
	for _, vm := range vms {
		if vm.Name == name {
			return vm
		}
	}
	t.Fatalf("VM %s not found after edit:\n%s", name, content)
	return model.VM{}
}

func TestApplyEditAddPlacementGroup(t *testing.T) {
	out, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{
		AddGroups: []model.PlacementGroup{{Name: "web-tier", Mode: "together", Strict: true}},
	})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"group.scheduling.dotvirt.io/web-tier": together`) {
		t.Fatalf("membership label not added:\n%s", got)
	}
	if !strings.Contains(got, "kubevirt.io/domain: web") {
		t.Error("edit disturbed the existing template labels")
	}

	vm := reparse(t, out, "alpha", "web")
	if vm.Scheduling == nil || len(vm.Scheduling.Groups) != 1 {
		t.Fatalf("Scheduling = %+v, want one group", vm.Scheduling)
	}
	g := vm.Scheduling.Groups[0]
	if g.Name != "web-tier" || g.Mode != "together" || !g.Strict {
		t.Errorf("group = %+v, want web-tier/together/strict", g)
	}
	if vm.Scheduling.Custom {
		t.Error("own affinity encoding must not read back as custom")
	}
}

func TestApplyEditGroupsPreferredAndApart(t *testing.T) {
	out, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{
		AddGroups: []model.PlacementGroup{
			{Name: "web-tier", Mode: "together"},
			{Name: "db", Mode: "apart", Strict: true},
		},
	})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "preferredDuringSchedulingIgnoredDuringExecution") ||
		!strings.Contains(got, "weight: 100") {
		t.Fatalf("non-strict group must render a preferred term:\n%s", got)
	}
	if !strings.Contains(got, "podAntiAffinity") {
		t.Fatalf("apart group must render podAntiAffinity:\n%s", got)
	}

	vm := reparse(t, out, "alpha", "web")
	if vm.Scheduling == nil || len(vm.Scheduling.Groups) != 2 {
		t.Fatalf("Scheduling = %+v, want two groups", vm.Scheduling)
	}
	byName := map[string]model.PlacementGroup{}
	for _, g := range vm.Scheduling.Groups {
		byName[g.Name] = g
	}
	if g := byName["web-tier"]; g.Mode != "together" || g.Strict {
		t.Errorf("web-tier = %+v, want together/preferred", g)
	}
	if g := byName["db"]; g.Mode != "apart" || !g.Strict {
		t.Errorf("db = %+v, want apart/strict", g)
	}
}

func TestApplyEditRemoveGroupClearsAffinity(t *testing.T) {
	withGroup, err := ApplyEdit([]byte(drsVM), "alpha", "web", VMEdit{
		AddGroups: []model.PlacementGroup{{Name: "web-tier", Mode: "together", Strict: true}},
	})
	if err != nil {
		t.Fatalf("ApplyEdit add: %v", err)
	}
	out, err := ApplyEdit(withGroup, "alpha", "web", VMEdit{RemoveGroups: []string{"web-tier"}})
	if err != nil {
		t.Fatalf("ApplyEdit remove: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "affinity:") || strings.Contains(got, "group.scheduling.dotvirt.io") {
		t.Fatalf("last group removed must clear affinity and label:\n%s", got)
	}
	if vm := reparse(t, out, "alpha", "web"); vm.Scheduling != nil {
		t.Errorf("Scheduling = %+v, want nil after removal", vm.Scheduling)
	}
}

func TestApplyEditPinReplaceAndClear(t *testing.T) {
	pin := func(hosts ...string) VMEdit { return VMEdit{Pin: &hosts} }

	out, err := ApplyEdit([]byte(drsVM), "alpha", "web", pin("w1", "w2"))
	if err != nil {
		t.Fatalf("ApplyEdit pin: %v", err)
	}
	vm := reparse(t, out, "alpha", "web")
	if vm.Scheduling == nil || strings.Join(vm.Scheduling.Pin, ",") != "w1,w2" {
		t.Fatalf("Scheduling = %+v, want pin w1,w2", vm.Scheduling)
	}

	out, err = ApplyEdit(out, "alpha", "web", pin("w3"))
	if err != nil {
		t.Fatalf("ApplyEdit repin: %v", err)
	}
	if got := string(out); strings.Contains(got, "- w1") || !strings.Contains(got, "- w3") {
		t.Fatalf("repin must replace the host list:\n%s", got)
	}

	out, err = ApplyEdit(out, "alpha", "web", pin())
	if err != nil {
		t.Fatalf("ApplyEdit unpin: %v", err)
	}
	if strings.Contains(string(out), "affinity:") {
		t.Fatalf("empty pin must remove the affinity block:\n%s", out)
	}
	if vm := reparse(t, out, "alpha", "web"); vm.Scheduling != nil {
		t.Errorf("Scheduling = %+v, want nil after unpin", vm.Scheduling)
	}
}

const nodeSelectorVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: pinned
  namespace: alpha
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/domain: pinned
    spec:
      nodeSelector:
        kubernetes.io/hostname: w0
        feature.node.kubernetes.io/gpu: "true"
      domain:
        cpu:
          cores: 1
`

func TestApplyEditPinClearsHostnameNodeSelector(t *testing.T) {
	hosts := []string{"w2"}
	out, err := ApplyEdit([]byte(nodeSelectorVM), "alpha", "pinned", VMEdit{Pin: &hosts})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "kubernetes.io/hostname: w0") {
		t.Fatalf("hostname nodeSelector must be dropped when pinning:\n%s", got)
	}
	if !strings.Contains(got, `feature.node.kubernetes.io/gpu: "true"`) {
		t.Fatalf("unrelated nodeSelector keys must survive:\n%s", got)
	}
	vm := reparse(t, out, "alpha", "pinned")
	if vm.Scheduling == nil || strings.Join(vm.Scheduling.Pin, ",") != "w2" {
		t.Fatalf("Scheduling = %+v, want pin w2", vm.Scheduling)
	}
}

func TestParseVMsNodeSelectorPin(t *testing.T) {
	vms, err := ParseVMs("vms/pinned.yaml", []byte(nodeSelectorVM), "alpha")
	if err != nil {
		t.Fatalf("ParseVMs: %v", err)
	}
	s := vms[0].Scheduling
	if s == nil || strings.Join(s.Pin, ",") != "w0" || s.Custom {
		t.Fatalf("Scheduling = %+v, want non-custom pin w0 from nodeSelector", s)
	}
}

// Groups must survive an untouched nodeSelector pin: the pin stays in
// nodeSelector (not duplicated into the affinity block) and reads back.
func TestApplyEditGroupKeepsNodeSelectorPin(t *testing.T) {
	out, err := ApplyEdit([]byte(nodeSelectorVM), "alpha", "pinned", VMEdit{
		AddGroups: []model.PlacementGroup{{Name: "g1", Mode: "apart", Strict: true}},
	})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "kubernetes.io/hostname: w0") {
		t.Fatalf("an unedited nodeSelector pin must stay:\n%s", got)
	}
	if strings.Contains(got, "nodeAffinity") {
		t.Fatalf("nodeSelector pin must not be duplicated into node affinity:\n%s", got)
	}
	vm := reparse(t, out, "alpha", "pinned")
	if vm.Scheduling == nil || strings.Join(vm.Scheduling.Pin, ",") != "w0" || len(vm.Scheduling.Groups) != 1 {
		t.Fatalf("Scheduling = %+v, want pin w0 + one group", vm.Scheduling)
	}
}

const customAffinityVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: tuned
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      affinity:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels:
                  app: db
              topologyKey: topology.kubernetes.io/zone
      domain:
        cpu:
          cores: 1
`

func TestApplyEditSchedulingRefusesCustomAffinity(t *testing.T) {
	_, err := ApplyEdit([]byte(customAffinityVM), "alpha", "tuned", VMEdit{
		AddGroups: []model.PlacementGroup{{Name: "g1", Mode: "together"}},
	})
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict for hand-written affinity", err)
	}

	// A non-scheduling edit must still work and leave the affinity untouched.
	power := "Off"
	out, err := ApplyEdit([]byte(customAffinityVM), "alpha", "tuned", VMEdit{Power: &power})
	if err != nil {
		t.Fatalf("ApplyEdit power: %v", err)
	}
	if !strings.Contains(string(out), "topology.kubernetes.io/zone") {
		t.Fatalf("non-scheduling edit disturbed the affinity:\n%s", out)
	}

	vm := reparse(t, []byte(customAffinityVM), "alpha", "tuned")
	if vm.Scheduling == nil || !vm.Scheduling.Custom {
		t.Fatalf("Scheduling = %+v, want Custom", vm.Scheduling)
	}
}

func TestApplyEditSchedulingValidation(t *testing.T) {
	for _, edit := range []VMEdit{
		{AddGroups: []model.PlacementGroup{{Name: "Bad_Name", Mode: "together"}}},
		{AddGroups: []model.PlacementGroup{{Name: "ok", Mode: "sideways"}}},
		{Pin: &[]string{" "}},
	} {
		if _, err := ApplyEdit([]byte(drsVM), "alpha", "web", edit); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("edit %+v: err = %v, want ErrInvalid", edit, err)
		}
	}
}

// A template without metadata plus a combined group + DRS edit must create ONE
// metadata block carrying both the label and the annotation.
func TestApplyEditSchedulingCreatesTemplateMetadataOnce(t *testing.T) {
	exclude := true
	out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", VMEdit{
		AddGroups:  []model.PlacementGroup{{Name: "g1", Mode: "together", Strict: true}},
		DRSExclude: &exclude,
	})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if strings.Count(got, "\n    metadata:") != 1 {
		t.Fatalf("template metadata must be created exactly once:\n%s", got)
	}
	vm := reparse(t, out, "vm-health-gitops", "vm-health")
	if vm.Scheduling == nil || len(vm.Scheduling.Groups) != 1 {
		t.Fatalf("Scheduling = %+v, want one group", vm.Scheduling)
	}
	if !vm.DRSExclude {
		t.Error("DRS exclude annotation lost in the combined metadata create")
	}
}

func TestChangesForEditScheduling(t *testing.T) {
	current := model.VM{
		Power: model.PowerOn,
		Scheduling: &model.VMScheduling{
			Pin:    []string{"w1"},
			Groups: []model.PlacementGroup{{Name: "web", Mode: "together", Strict: true}},
		},
	}
	pin := []string{}
	changes := ChangesForEdit(current, VMEdit{
		Pin: &pin,
		AddGroups: []model.PlacementGroup{
			{Name: "web", Mode: "apart", Strict: true}, // mode change
			{Name: "db", Mode: "together"},             // new
		},
		RemoveGroups: []string{"web", "gone"}, // "gone" doesn't exist: no entry
	})
	byField := map[string]model.Change{}
	for _, c := range changes {
		byField[c.Field+"/"+c.Action] = c
	}
	if c := byField["Placement group web/change"]; c.From != "keep together, strict" || c.To != "keep apart, strict" {
		t.Errorf("web change = %+v", c)
	}
	if c := byField["Placement group db/add"]; c.To != "keep together, preferred" {
		t.Errorf("db add = %+v", c)
	}
	if c := byField["Placement group web/remove"]; c.From != "keep together, strict" {
		t.Errorf("web remove = %+v", c)
	}
	if _, ok := byField["Placement group gone/remove"]; ok {
		t.Error("removing a non-member group must not render a change")
	}
	if c := byField["Host pinning/remove"]; c.From != "w1" {
		t.Errorf("pin remove = %+v", c)
	}
}
