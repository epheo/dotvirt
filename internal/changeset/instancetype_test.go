package changeset

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// fakeInstancetypes serves instancetype objects by name and records each
// read; a name it does not hold fails, as a forbidden GET would.
type fakeInstancetypes struct {
	objs  map[string]map[string]any
	reads []string
}

func (f *fakeInstancetypes) Instancetype(id auth.Identity, namespace, kind, name string) (map[string]any, error) {
	f.reads = append(f.reads, strings.Join([]string{id.Username, namespace, kind, name}, " "))
	if o, ok := f.objs[name]; ok {
		return o, nil
	}
	return nil, errors.New("forbidden")
}

// itWeb keeps a CPU model and hugepages beside its instancetype.
const itWeb = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  instancetype:
    name: u1.small
  template:
    spec:
      domain:
        cpu:
          model: host-passthrough
        memory:
          hugepages:
            pageSize: 2Mi
        devices: {}
`

type itStage struct {
	t    *testing.T
	c    *Coordinator
	its  *fakeInstancetypes
	id   auth.Identity
	proj project.ProjectInfo
}

func newITStage(t *testing.T, its *fakeInstancetypes) itStage {
	c := newTestCoordinator(t, true)
	if its != nil {
		c.UseInstancetypes(its)
	}
	bare := seedBareFiles(t, map[string][]byte{"alpha/web.yaml": []byte(itWeb)})
	return itStage{t, c, its, auth.Identity{Username: "alice", Token: "tok"}, project.ProjectInfo{Name: "p", Repo: bare}}
}

// stage stages edit on alpha/web and returns the item's rows as
// "Field: action from".
func (s itStage) stage(edit model.VMEdit) []string {
	s.t.Helper()
	view, err := s.c.StageEdit(s.id, s.proj, "alpha", "web", model.EditRequest{VMEdit: edit})
	if err != nil {
		s.t.Fatalf("StageEdit: %v", err)
	}
	var rows []string
	for _, c := range view.Items[0].Changes {
		rows = append(rows, c.Field+": "+c.Action+" "+c.From)
	}
	return rows
}

// proposed is alpha/web.yaml as the proposal commits it.
func (s itStage) proposed() string {
	s.t.Helper()
	res, err := s.c.Propose(s.id, s.proj, model.ProposeRequest{Title: "size web"})
	if err != nil {
		s.t.Fatalf("Propose: %v", err)
	}
	out, err := exec.Command("git", "-C", s.proj.Repo, "cat-file", "-p", res.Branch+":alpha/web.yaml").Output()
	if err != nil {
		s.t.Fatalf("read the proposal: %v", err)
	}
	return string(out)
}

// A switch reads its target once, under the stager's token, and strips only
// what that instancetype sets: the read rides with the entry, so a later edit
// and the propose apply it without asking again, and an edit keeping the
// instancetype never asks.
func TestInstancetypeSwitchReadsItsTargetOnce(t *testing.T) {
	its := &fakeInstancetypes{objs: map[string]map[string]any{
		"u1.large": {"spec": map[string]any{"memory": map[string]any{"guest": "8Gi", "hugepages": map[string]any{"pageSize": "1Gi"}}}},
	}}
	s := newITStage(t, its)
	s.stage(model.VMEdit{SetLabels: map[string]string{"env": "prod"}})
	if len(its.reads) != 0 {
		t.Fatalf("an edit keeping the instancetype read %q", its.reads)
	}
	rows := s.stage(model.VMEdit{Instancetype: ptr("u1.large")})
	if want := []string{"alice alpha  u1.large"}; !slices.Equal(its.reads, want) {
		t.Fatalf("reads = %q, want %q", its.reads, want)
	}
	if !slices.Contains(rows, "Hugepages: remove pageSize: 2Mi") || slices.ContainsFunc(rows, func(r string) bool { return strings.HasPrefix(r, "CPU model") }) {
		t.Errorf("rows = %q: want hugepages removed, the CPU model kept", rows)
	}
	s.stage(model.VMEdit{Power: ptr("Off")})
	if len(its.reads) != 1 {
		t.Errorf("a later edit read again: %q", its.reads)
	}
	got := s.proposed()
	if !strings.Contains(got, "name: u1.large") || !strings.Contains(got, "model: host-passthrough") || strings.Contains(got, "hugepages") {
		t.Errorf("proposal does not apply the read:\n%s", got)
	}
}

// A target that cannot be read never fails the edit: the switch strips every
// field an instancetype may take over, and the stage result says why.
func TestInstancetypeSwitchFallsBackWhenUnread(t *testing.T) {
	for name, its := range map[string]*fakeInstancetypes{"read fails": {}, "no reader": nil} {
		s := newITStage(t, its)
		rows := s.stage(model.VMEdit{Instancetype: ptr("u1.secret")})
		for _, want := range []string{
			"CPU model: remove host-passthrough (could not read u1.secret)",
			"Hugepages: remove pageSize: 2Mi (could not read u1.secret)",
		} {
			if !slices.Contains(rows, want) {
				t.Errorf("%s: rows = %q, want %q", name, rows, want)
			}
		}
		if got := s.proposed(); strings.Contains(got, "model:") || strings.Contains(got, "hugepages") {
			t.Errorf("%s: proposal keeps what u1.secret may set:\n%s", name, got)
		}
	}
}

// An edit's preview reads the instancetype it switches to as staging does, so
// it lists what staging lists: the shared fields that instancetype sets, or,
// read refused, every one and why.
func TestPreviewEditListsWhatStagingLists(t *testing.T) {
	for name, it := range map[string]string{"shared field": "u1.large", "unread": "u1.secret"} {
		its := &fakeInstancetypes{objs: map[string]map[string]any{
			"u1.large": {"spec": map[string]any{"memory": map[string]any{"hugepages": map[string]any{"pageSize": "1Gi"}}}},
		}}
		s := newITStage(t, its)
		edit := model.VMEdit{Instancetype: ptr(it)}
		preview, err := s.c.PreviewEdit(s.id, s.proj, "alpha", "web", edit)
		if err != nil {
			t.Fatalf("%s: PreviewEdit: %v", name, err)
		}
		if want := []string{"alice alpha  " + it}; !slices.Equal(its.reads, want) {
			t.Errorf("%s: preview reads = %q, want %q", name, its.reads, want)
		}
		view, err := s.c.StageEdit(s.id, s.proj, "alpha", "web", model.EditRequest{VMEdit: edit})
		if err != nil {
			t.Fatalf("%s: StageEdit: %v", name, err)
		}
		if staged := view.Items[0].Changes; !slices.Equal(preview, staged) {
			t.Errorf("%s: preview = %+v\nstaging lists %+v", name, preview, staged)
		}
	}
}

// A re-propose lays the draft over what the open PR carries: the PR's read of
// the instancetype it switches to rides into the combined edit, so a later
// power toggle does not turn it into a strip of every shared field.
func TestReproposeKeepsTheCarriedInstancetypeRead(t *testing.T) {
	s := newITStage(t, nil)
	read, err := s.c.read(s.proj)
	if err != nil {
		t.Fatal(err)
	}
	larger := manifest.Target{Name: "u1.large", Read: true, Sets: []string{"memory.hugepages"}}
	entry := func(edit model.VMEdit, it *manifest.Target) draft.Entry {
		return draft.Entry{Kind: draft.KindEdit, Namespace: "alpha", Name: "web", SourceFile: "alpha/web.yaml", Edit: &edit, Instancetype: it}
	}
	carried := []draft.Entry{entry(model.VMEdit{Instancetype: ptr("u1.large")}, &larger)}
	all, err := s.c.combine(read, 1, carried, []draft.Entry{entry(model.VMEdit{Power: ptr("Off")}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Instancetype == nil || !all[0].Instancetype.Answers(larger) {
		t.Errorf("combined = %+v, want the carried read of u1.large", all)
	}
}
