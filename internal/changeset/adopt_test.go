package changeset

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

func liveVMYAML(name, labels string) string {
	return "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: " + name +
		"\n  namespace: alpha\n" + labels + "spec:\n  runStrategy: Always\n"
}

// fakeLive stands in for the SA snapshot: the VMs the cluster is running, already
// serialized the way the repo holds them.
type fakeLive map[string]string // repo path -> manifest

func (f fakeLive) Ready() bool { return true }

func (f fakeLive) VMManifests([]string) []LiveManifest {
	out := make([]LiveManifest, 0, len(f))
	for path, content := range f {
		out = append(out, LiveManifest{Path: path, Content: []byte(content)})
	}
	return out
}

// seedBareWithLive creates a bare repo holding alpha/web on main, and returns the live
// state beside it: web has drifted (an extra label) and alpha/copy is running without
// ever being declared (an out-of-band create, e.g. a clone target).
func seedBareWithLive(t *testing.T) (string, fakeLive) {
	t.Helper()
	bare, _ := seedWork(t, map[string][]byte{"alpha/web.yaml": []byte(liveVMYAML("web", ""))})
	return bare, fakeLive{
		"alpha/web.yaml":  liveVMYAML("web", "  labels:\n    env: prod\n"),
		"alpha/copy.yaml": liveVMYAML("copy", ""),
	}
}

// A VM running but not on main (a clone target) adopts as a CREATE carrying
// the live manifest verbatim, and proposes to the same path.
func TestAdoptStagesCreateForClusterOnlyVM(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t)
	c.live = live
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.Adopt(id, proj, "alpha", "copy")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if view.Count != 1 || len(view.Items) != 1 {
		t.Fatalf("want 1 staged item, got count=%d items=%d", view.Count, len(view.Items))
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindCreate) || it.Namespace != "alpha" || it.Name != "copy" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if !strings.Contains(it.YAML, "name: copy") {
		t.Errorf("item YAML should carry the live manifest, got:\n%s", it.YAML)
	}
	if len(it.Changes) != 1 || it.Changes[0].Action != "add" {
		t.Errorf("want one add change, got %+v", it.Changes)
	}

	// The changeset item must land the manifest at its serialized live-state path.
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	items, err := c.toChangesetItems(entries)
	if err != nil {
		t.Fatalf("toChangesetItems: %v", err)
	}
	if len(items) != 1 || items[0].Path != "alpha/copy.yaml" || items[0].NewContent == nil {
		t.Fatalf("want a create at alpha/copy.yaml, got %+v", items)
	}
	if !strings.Contains(string(items[0].NewContent), "name: copy") {
		t.Errorf("NewContent should be the live manifest")
	}
}

// A VM on both branches keeps the edit-adopt path: the staged entry is a
// KindEdit making base match running.
func TestAdoptStagesEditForDriftedVM(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t)
	c.live = live
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.Adopt(id, proj, "alpha", "web")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != string(draft.KindEdit) {
		t.Fatalf("want one edit item, got %+v", view.Items)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].Edit == nil || entries[0].Edit.SetLabels["env"] != "prod" {
		t.Fatalf("want an edit staging the drifted label, got %+v", entries)
	}
}

// AdoptNamespace stages every captured object whatever its kind, so a namespace comes
// under git whole rather than VM by VM. What is worth capturing is decided by the
// caller (cluster.AdoptableObjects); this only stages.
func TestAdoptNamespaceStagesEveryKind(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	objs := []Adoptable{
		{Namespace: "alpha", Name: "copy", Kind: "VirtualMachine",
			Path: "alpha/copy.yaml", Manifest: []byte(liveVMYAML("copy", ""))},
		{Namespace: "alpha", Name: "deny", Kind: "NetworkPolicy",
			Path:     "alpha/networkpolicies/deny.yaml",
			Manifest: []byte("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: deny\n  namespace: alpha\nspec: {}\n")},
	}
	view, err := c.AdoptNamespace(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptNamespace: %v", err)
	}
	if view.Count != 2 {
		t.Fatalf("want both objects staged, got count=%d", view.Count)
	}

	// Idempotent: re-staging replaces each entry rather than duplicating it.
	view, err = c.AdoptNamespace(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptNamespace (rerun): %v", err)
	}
	if view.Count != 2 {
		t.Fatalf("re-adopt should be idempotent, got count=%d", view.Count)
	}
}

// Nothing left to adopt is a clear ErrInvalid, so the UI says so rather than opening
// an empty PR.
func TestAdoptNamespaceNothingToAdopt(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t)
	_, err := c.AdoptNamespace(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "beta", nil)
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid when there is nothing to adopt, got %v", err)
	}
}

// Git decides what git already describes. ArgoCD's tracking annotation only records
// what it has applied, so it misses an object committed but not yet synced, one whose
// Application is broken, and every object on a cluster tracking by label. Restating one
// would overwrite the hand-authored manifest with the live defaulted copy.
func TestAdoptNamespaceSkipsWhatBaseAlreadyDeclares(t *testing.T) {
	bare, _ := seedBareWithLive(t) // main holds alpha/web.yaml
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	// web is declared on main; the capture still offers it (no tracking-id on the object).
	// A different path must not defeat the check: identity is what is declared, not layout.
	objs := []Adoptable{
		{Namespace: "alpha", Name: "web", Kind: "VirtualMachine",
			Path: "alpha/elsewhere/web.yaml", Manifest: []byte(liveVMYAML("web", ""))},
		{Namespace: "alpha", Name: "copy", Kind: "VirtualMachine",
			Path: "alpha/copy.yaml", Manifest: []byte(liveVMYAML("copy", ""))},
	}
	view, err := c.AdoptNamespace(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptNamespace: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want only the undeclared VM staged, got count=%d", view.Count)
	}
	if view.Items[0].Name != "copy" {
		t.Fatalf("staged the wrong object: %s", view.Items[0].Name)
	}
}

// Everything running is already declared, so there is nothing to propose: the caller
// must be told that, not handed an empty draft that looks like work.
func TestAdoptNamespaceAllDeclaredIsInvalid(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t)
	_, err := c.AdoptNamespace(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha",
		[]Adoptable{{Namespace: "alpha", Name: "web", Kind: "VirtualMachine",
			Path: "alpha/web.yaml", Manifest: []byte(liveVMYAML("web", ""))}})
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid when base declares everything captured, got %v", err)
	}
}

// stageProjectAdoption stamps each of a project's namespaces with the dotvirt.io/repo
// annotation (+ an owners RoleBinding) into the platform draft - the staging core of
// AdoptProject, exercised without a forge.
func TestStageProjectAdoptionStampsRepoOnEveryNamespace(t *testing.T) {
	c := newTestCoordinator(t)
	id := "alice"
	const platform = "platform"
	target := project.ProjectInfo{Name: "team-a", Namespaces: []string{"team-a", "team-a-db"}}
	repoURL := "https://forge.example/acme/team-a.git"

	if err := c.stageProjectAdoption(id, platform, target, repoURL, []string{"alice", "bob"}); err != nil {
		t.Fatalf("stageProjectAdoption: %v", err)
	}
	entries, err := c.store.List(id, platform)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}

	nsByName := map[string]draft.Entry{}
	rbByName := map[string]draft.Entry{}
	for _, e := range entries {
		switch e.Resource {
		case draft.ResourceNamespace:
			nsByName[e.Name] = e
		case draft.ResourceRoleBinding:
			rbByName[e.Name] = e
		}
	}
	for _, ns := range target.Namespaces {
		e, ok := nsByName[ns]
		if !ok {
			t.Fatalf("no namespace entry staged for %q", ns)
		}
		if e.Kind != draft.KindCreate {
			t.Errorf("namespace %q: want KindCreate, got %q", ns, e.Kind)
		}
		// Stamped HOST-FREE: the forge's identity lives in the install config, so a
		// forge-host change re-resolves rather than strands.
		if !strings.Contains(e.Manifest, "dotvirt.io/repo: acme/team-a.git") {
			t.Errorf("namespace %q manifest must carry a host-free dotvirt.io/repo ref, got:\n%s", ns, e.Manifest)
		}
		if !strings.Contains(e.Manifest, "dotvirt.io/project") || !strings.Contains(e.Manifest, "team-a") {
			t.Errorf("namespace %q manifest must carry the dotvirt.io/project label, got:\n%s", ns, e.Manifest)
		}
		if _, ok := rbByName[ns+"-admins"]; !ok {
			t.Errorf("no owners RoleBinding staged for namespace %q", ns)
		}
	}
}

func TestAdoptAbsentFromRunningNotFound(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t)
	c.live = live

	_, err := c.Adopt(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}

// fakePrune stands in for the argo snapshot's requiresPruning relay.
type fakePrune []model.ObjectRef

func (f fakePrune) PrunePending(string, []string) []model.ObjectRef { return f }

// The warning derives every view: shows on an EMPTY draft, shrinks as adoption
// stages. Nothing stored, so reopening never shows partial as complete.
func TestDraftWarningDerivedNotStored(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t)
	c.live = live
	c.prune = fakePrune{
		{Kind: "VirtualMachine", Namespace: "alpha", Name: "copy"},
		{Kind: "UserDefinedNetwork", Namespace: "alpha", Name: "backend"},
	}
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}

	view, err := c.Get(id, proj)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, want := range []string{"VirtualMachine alpha/copy", "UserDefinedNetwork alpha/backend"} {
		if !strings.Contains(view.Warning, want) {
			t.Fatalf("empty draft must warn about %q, got %q", want, view.Warning)
		}
	}

	if _, err := c.Adopt(id, proj, "alpha", "copy"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	view, err = c.Get(id, proj)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(view.Warning, "alpha/copy") {
		t.Errorf("adopting copy must remove it from the warning, got %q", view.Warning)
	}
	if !strings.Contains(view.Warning, "UserDefinedNetwork alpha/backend") {
		t.Errorf("the unadopted network must keep warning, got %q", view.Warning)
	}
}

// Manifest entries speak for their documents; a VM delete for the prune it
// intends; every other resource carries a manifest and never hits the fallback.
func TestEntryRefsSpeakForDraftEntries(t *testing.T) {
	refs := entryRefs(draft.Entry{Kind: draft.KindCreate, Namespace: "alpha", Name: "web",
		Manifest: "kind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\n"})
	if len(refs) != 1 || refs[0] != (model.ObjectRef{Kind: "VirtualMachine", Namespace: "alpha", Name: "web"}) {
		t.Fatalf("manifest entry: %v", refs)
	}
	refs = entryRefs(draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "web"})
	if len(refs) != 1 || refs[0] != (model.ObjectRef{Kind: "VirtualMachine", Namespace: "alpha", Name: "web"}) {
		t.Fatalf("vm delete entry: %v", refs)
	}
	if refs = entryRefs(draft.Entry{Kind: draft.KindCreate, Resource: draft.ResourceTemplate, Name: "gold"}); refs != nil {
		t.Fatalf("a template speaks for no prunable object, got %v", refs)
	}
}
