package changeset

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
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

func (f fakeLive) VMManifests([]string) []model.File {
	out := make([]model.File, 0, len(f))
	for path, content := range f {
		out = append(out, model.File{Path: path, Content: []byte(content)})
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
	c := newTestCoordinator(t, false)
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
	items := c.toChangesetItems(proj, entries)
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
	c := newTestCoordinator(t, false)
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

// Adopting drift onto a staged edit of the VM adds to it, as any field edit
// does; it never drops what the user staged.
func TestAdoptLaysDriftOverStagedEdit(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t, false)
	c.live = live
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	off := "Off"
	if _, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{SourceFile: "alpha/web.yaml", VMEdit: model.VMEdit{Power: &off}}); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	if _, err := c.Adopt(id, proj, "alpha", "web"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].Edit == nil || entries[0].Edit.Power == nil || entries[0].Edit.SetLabels["env"] != "prod" {
		t.Fatalf("want the staged power and the adopted label together, got %+v", entries)
	}
}

// AdoptObjects stages every captured object whatever its kind, so a namespace comes
// under git whole rather than VM by VM. What is worth capturing is decided by the
// caller (cluster.AdoptableObjects); this only stages.
func TestAdoptObjectsStagesEveryKind(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	objs := []model.Adoptable{
		{Namespace: "alpha", Name: "copy", Kind: "VirtualMachine",
			Path: "alpha/copy.yaml", Manifest: []byte(liveVMYAML("copy", ""))},
		{Namespace: "alpha", Name: "deny", Kind: "NetworkPolicy",
			Path:     "alpha/networkpolicies/deny.yaml",
			Manifest: []byte("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: deny\n  namespace: alpha\nspec: {}\n")},
	}
	view, err := c.AdoptObjects(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptObjects: %v", err)
	}
	if view.Count != 2 {
		t.Fatalf("want both objects staged, got count=%d", view.Count)
	}

	// Idempotent: re-staging replaces each entry rather than duplicating it.
	view, err = c.AdoptObjects(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptObjects (rerun): %v", err)
	}
	if view.Count != 2 {
		t.Fatalf("re-adopt should be idempotent, got count=%d", view.Count)
	}
}

// Nothing left to adopt is a clear ErrInvalid, so the UI says so rather than opening
// an empty PR.
func TestAdoptObjectsNothingToAdopt(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t, false)
	_, err := c.AdoptObjects(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "beta", nil)
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid when there is nothing to adopt, got %v", err)
	}
}

// Git decides what git already describes. ArgoCD's tracking annotation only records
// what it has applied, so it misses an object committed but not yet synced, one whose
// Application is broken, and every object on a cluster tracking by label. Restating one
// would overwrite the hand-authored manifest with the live defaulted copy.
func TestAdoptObjectsSkipsWhatBaseAlreadyDeclares(t *testing.T) {
	bare, _ := seedBareWithLive(t) // main holds alpha/web.yaml
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	// web is declared on main; the capture still offers it (no tracking-id on the object).
	// A different path must not defeat the check: identity is what is declared, not layout.
	objs := []model.Adoptable{
		{Namespace: "alpha", Name: "web", Kind: "VirtualMachine",
			Path: "alpha/elsewhere/web.yaml", Manifest: []byte(liveVMYAML("web", ""))},
		{Namespace: "alpha", Name: "copy", Kind: "VirtualMachine",
			Path: "alpha/copy.yaml", Manifest: []byte(liveVMYAML("copy", ""))},
	}
	view, err := c.AdoptObjects(id, proj, "alpha", objs)
	if err != nil {
		t.Fatalf("AdoptObjects: %v", err)
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
func TestAdoptObjectsAllDeclaredIsInvalid(t *testing.T) {
	bare, _ := seedBareWithLive(t)
	c := newTestCoordinator(t, false)
	_, err := c.AdoptObjects(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha",
		[]model.Adoptable{{Namespace: "alpha", Name: "web", Kind: "VirtualMachine",
			Path: "alpha/web.yaml", Manifest: []byte(liveVMYAML("web", ""))}})
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid when base declares everything captured, got %v", err)
	}
}

// projectAdoption stamps each of a project's namespaces with the dotvirt.io/repo
// annotation (+ an owners RoleBinding) for the platform draft - the staging core of
// AdoptProject, exercised without a forge.
func TestProjectAdoptionStampsRepoOnEveryNamespace(t *testing.T) {
	target := project.ProjectInfo{Name: "team-a", Namespaces: []string{"team-a", "team-a-db"}}
	repoURL := "https://forge.example/acme/team-a.git"

	specs := tenantNamespaces(target, repoURL, make([]*netgen.LiveNamespace, len(target.Namespaces)))
	entries, err := projectAdoption(specs, []string{"alice", "bob"}, grantSource{})
	if err != nil {
		t.Fatalf("projectAdoption: %v", err)
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
	c := newTestCoordinator(t, false)
	c.live = live

	_, err := c.Adopt(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}

// fakePrune stands in for the argo snapshot's requiresPruning relay.
type fakePrune []model.ObjectRef

func (f fakePrune) PrunePending(string, ...string) ([]model.ObjectRef, bool) { return f, true }

// held is a project whose app ArgoCD holds at an emptied source, and what
// that means.
type held model.Hold

func (h held) EmptyHold(project.ProjectInfo) model.Hold { return model.Hold(h) }

// settledPrune is Argo about to prune refs from the root folder's app, which
// a settled root leaves out.
type settledPrune []model.ObjectRef

func (f settledPrune) PrunePending(_ string, settled ...string) ([]model.ObjectRef, bool) {
	if slices.Contains(settled, "") {
		return nil, true
	}
	return f, true
}

// A deliberate removal of a project's last objects applies whatever merges
// next: it neither warns nor asks for a yes. Git that lost what runs still
// does both.
func TestHeldDeletionNeitherWarnsNorAsks(t *testing.T) {
	stray := model.ObjectRef{Kind: "VirtualMachine", Namespace: "alpha", Name: "web"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	for _, c := range []struct {
		hold model.Hold
		warn bool
	}{{model.HoldDeleting, false}, {model.HoldReplaced, true}, {model.HoldPending, true}} {
		co := newTestCoordinator(t, false)
		co.prune = settledPrune{stray}
		co.UseHolds(held(c.hold))
		view, err := co.Get(auth.Identity{Username: "alice"}, proj)
		if err != nil {
			t.Fatalf("%s: Get: %v", c.hold, err)
		}
		if got := view.Warning != "" && len(view.Prune) == 1; got != c.warn {
			t.Errorf("%s: warning %q prune %v, want warned=%v", c.hold, view.Warning, view.Prune, c.warn)
		}
	}
}

// unloaded is an Argo view that has not loaded yet.
type unloaded struct{}

func (unloaded) PrunePending(string, ...string) ([]model.ObjectRef, bool) { return nil, false }

// tellsIn names only the objects in its namespaces: a caller who may list
// there and nowhere else.
type tellsIn struct {
	allowAll
	ns []string
}

func (t tellsIn) Tellable(_ auth.Identity, refs []model.ObjectRef) []model.ObjectRef {
	var out []model.ObjectRef
	for _, r := range refs {
		if slices.Contains(t.ns, r.Namespace) {
			out = append(out, r)
		}
	}
	return out
}

// A tenant's warning covers the whole project, whatever part of it the caller
// sees: the merge prunes the rest too. What they see is named; the rest is
// only counted, so the warning never names what their RBAC hides.
func TestPruneWarningCoversTheWholeProject(t *testing.T) {
	c := newTestCoordinator(t, false)
	c.UseAuthorizer(tellsIn{ns: []string{"alpha"}})
	c.prune = fakePrune{
		{Kind: "VirtualMachine", Namespace: "alpha", Name: "web"},
		{Kind: "VirtualMachine", Namespace: "beta", Name: "secret"},
	}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	view, err := c.Get(auth.Identity{Username: "alice"}, proj)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := []model.ObjectRef{{Kind: "VirtualMachine", Namespace: "alpha", Name: "web"}}; !reflect.DeepEqual(view.Prune, want) || view.PruneHidden != 1 {
		t.Fatalf("Prune = %v hidden %d, want %v hidden 1", view.Prune, view.PruneHidden, want)
	}
	if !strings.Contains(view.Warning, "VirtualMachine alpha/web, and 1 more not shown here") {
		t.Errorf("warning must name the visible object and count the hidden one, got %q", view.Warning)
	}
	if strings.Contains(view.Warning, "beta") || strings.Contains(view.Warning, "secret") {
		t.Errorf("warning names an object the caller cannot see: %q", view.Warning)
	}
}

// The platform tier's warning holds every object its app would prune,
// cluster-scoped ones included: named to a platform reader, only counted to a
// caller who merely holds a draft there.
func TestPlatformPruneWarningNamesOnlyToReaders(t *testing.T) {
	pending := fakePrune{
		{Kind: "Namespace", Name: "team-b"},
		{Kind: "RoleBinding", Namespace: "team-b", Name: "admins"},
	}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: seedBare(t)}
	for _, reads := range []bool{true, false} {
		c := newTestCoordinator(t, false)
		c.prune = pending
		if !reads {
			c.UseAuthorizer(&rbac{})
		}
		view, err := c.Get(auth.Identity{Username: "alice"}, plat)
		if err != nil {
			t.Fatalf("reads=%v: Get: %v", reads, err)
		}
		switch {
		case reads && (!reflect.DeepEqual(view.Prune, []model.ObjectRef(pending)) || view.PruneHidden != 0):
			t.Errorf("reader: Prune = %v hidden %d, want every object named", view.Prune, view.PruneHidden)
		case reads && !strings.Contains(view.Warning, "Namespace team-b, RoleBinding team-b/admins."):
			t.Errorf("reader: warning %q, want both objects named", view.Warning)
		case !reads && (len(view.Prune) != 0 || view.PruneHidden != 2):
			t.Errorf("holder: Prune = %v hidden %d, want 2 counted, none named", view.Prune, view.PruneHidden)
		case !reads && (strings.Contains(view.Warning, "team-b") || !strings.Contains(view.Warning, "2 objects not shown here")):
			t.Errorf("holder: warning %q, want a count and no name", view.Warning)
		}
	}
}

// The warning derives every view: shows on an EMPTY draft, shrinks as adoption
// stages. Nothing stored, so reopening never shows partial as complete.
func TestDraftWarningDerivedNotStored(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t, false)
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
	if view.DefaultTitle != "Create copy" || !strings.Contains(view.DefaultBody, "## Changes") {
		t.Fatalf("view must carry the propose defaults, got title %q body %q", view.DefaultTitle, view.DefaultBody)
	}
	if strings.Contains(view.Warning, "alpha/copy") {
		t.Errorf("adopting copy must remove it from the warning, got %q", view.Warning)
	}
	if !strings.Contains(view.Warning, "UserDefinedNetwork alpha/backend") {
		t.Errorf("the unadopted network must keep warning, got %q", view.Warning)
	}
	// The review lists the same objects one by one.
	if want := []model.ObjectRef{{Kind: "UserDefinedNetwork", Namespace: "alpha", Name: "backend"}}; !reflect.DeepEqual(view.Prune, want) {
		t.Errorf("Prune = %v, want %v", view.Prune, want)
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
