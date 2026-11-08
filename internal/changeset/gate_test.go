package changeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// allowAll grants every access: for tests of what a change does rather than
// who may make it.
type allowAll struct{}

func (allowAll) Authorize(auth.Identity, []Access) error { return nil }

// rbac grants every access deny does not, recording what it was asked.
type rbac struct {
	mu    sync.Mutex
	deny  func(Access) bool
	asked []Access
}

func (r *rbac) Authorize(_ auth.Identity, need []Access) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, need...)
	for _, a := range need {
		if r.deny != nil && r.deny(a) {
			return fmt.Errorf("%w: you may not %s", model.ErrForbidden, a)
		}
	}
	return nil
}

func (r *rbac) refuse(deny func(Access) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deny = deny
}

func (r *rbac) questions() []Access {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

// verbs is the questions without the objects a create carries.
func (r *rbac) verbs() []Access {
	out := r.questions()
	for i := range out {
		out[i].Object = ""
	}
	return out
}

func verbOn(verb, resource string) func(Access) bool {
	return func(a Access) bool { return a.Verb == verb && a.Resource == resource }
}

const (
	webPolicy = "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  podSelector: {}\n"
	netUDN    = "apiVersion: k8s.ovn.org/v1\nkind: UserDefinedNetwork\nmetadata:\n  name: net\n  namespace: alpha\nspec:\n  topology: Layer2\n"
	ownerRB   = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: own\n  namespace: alpha\nroleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: admin}\nsubjects: [{kind: User, name: mallory}]\n"
)

// The manifest editor stages only what Argo would apply as exactly the object
// edited. The gate's decoding refuses what Argo unpacks (items) or reads past
// dotvirt's own parser (a duplicate key ending a document early for one of
// them); its identity check refuses a second object; the placement guard
// refuses a namespace left to Argo and a NAD in a tenant repo.
func TestManifestEditorRefusesWhatArgoReadsOtherwise(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/networkpolicies/web.yaml": []byte(webPolicy),
		"alpha/networks/net.yaml":        []byte(netUDN),
	})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	edited := strings.Replace(webPolicy, "podSelector: {}", "podSelector: {matchLabels: {app: web}}", 1)

	for _, tc := range []struct{ name, resource, object, yaml string }{
		{"an items list", "networkpolicy", "web", edited +
			"items:\n- {apiVersion: rbac.authorization.k8s.io/v1, kind: RoleBinding, metadata: {name: own, namespace: alpha}, roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: admin}}\n"},
		{"a second document dotvirt's parser stops before", "networkpolicy", "web", edited + "---\n" +
			strings.Replace(ownerRB, "  name: own\n", "  name: own\n  name: own\n", 1)},
		{"a second document", "networkpolicy", "web", edited + "---\n" + ownerRB},
		{"a document without its namespace", "networkpolicy", "web", strings.Replace(edited, "  namespace: alpha\n", "", 1)},
		{"a UDN turned into a NAD", "network", "net",
			"apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: net\n  namespace: alpha\nspec:\n  config: '{}'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Discard(id, proj); err != nil {
				t.Fatal(err)
			}
			_, err := c.StageUpdateManifest(id, proj, tc.resource, "alpha", tc.object, model.UpdateManifestRequest{YAML: tc.yaml})
			if !errors.Is(err, model.ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
			if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
				t.Errorf("staged %+v", entries)
			}
		})
	}
	if _, err := c.StageUpdateManifest(id, proj, "networkpolicy", "alpha", "web", model.UpdateManifestRequest{YAML: edited}); err != nil {
		t.Errorf("the plain edit: %v", err)
	}
}

// Each object a staged write creates, changes or removes is asked of the
// caller with that verb, and a refusal stages nothing.
func TestStageAsksTheCallerForEachEffect(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"alpha/networks/net.yaml": []byte(netUDN)})
	c := newTestCoordinator(t, false)
	r := &rbac{}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}

	edited := strings.Replace(netUDN, "Layer2", "Layer3", 1)
	if _, err := c.StageUpdateManifest(id, proj, "network", "alpha", "net", model.UpdateManifestRequest{YAML: edited}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StageDelete(id, proj, "network", "alpha", "net"); err != nil {
		t.Fatal(err)
	}
	want := []Access{
		{Verb: "update", Group: "k8s.ovn.org", Resource: "userdefinednetworks", Namespace: "alpha", Name: "net"},
		{Verb: "delete", Group: "k8s.ovn.org", Resource: "userdefinednetworks", Namespace: "alpha", Name: "net"},
	}
	if got := r.questions(); !slices.Equal(got, want) {
		t.Errorf("asked %+v, want %+v", got, want)
	}

	r.refuse(verbOn("create", "networkpolicies"))
	raw, _ := json.Marshal(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha"})
	if _, err := c.StageCreate(id, proj, draft.ResourceNetworkPolicy, raw); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("a create the caller may not make: err = %v", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 1 {
		t.Errorf("a refused stage changed the draft: %+v", entries)
	}
}

// A RoleBinding's access carries the binding itself, for the authorizer to
// have the apiserver weigh it with the escalation rule Argo applying it skips;
// a refusal stages nothing.
func TestRoleBindingCarriesItself(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t, false)
	r := &rbac{deny: verbOn("create", "rolebindings")}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	entries, err := projectAdoption([]netgen.NamespaceSpec{{Name: "alpha", Project: "team"}}, []string{"bob"}, grantSource{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.stage(id, plat, entries...); !errors.Is(err, model.ErrForbidden) {
		t.Fatalf("a refused grant: err = %v, want ErrForbidden", err)
	}
	var grant *Access
	for _, a := range r.questions() {
		if a.Resource == "rolebindings" && a.Name == "alpha-admins" {
			grant = &a
		}
	}
	var rb struct {
		RoleRef struct{ Kind, Name string } `json:"roleRef"`
	}
	if grant == nil || grant.Verb != "create" || grant.Namespace != "alpha" || grant.Name != "alpha-admins" ||
		json.Unmarshal([]byte(grant.Object), &rb) != nil || rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != "admin" {
		t.Fatalf("asked %+v, want the binding carried", r.questions())
	}
	if entries, _ := c.store.List(id.Username, plat.Name); len(entries) != 0 {
		t.Errorf("staged %+v", entries)
	}
}

// Nothing is staged without an authorizer: an unwired coordinator fails closed.
func TestGateWithoutAnAuthorizerRefuses(t *testing.T) {
	c := newTestCoordinator(t, false)
	c.UseAuthorizer(nil)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	raw, _ := json.Marshal(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha"})
	if _, err := c.StageCreate(id, proj, draft.ResourceNetworkPolicy, raw); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// Files dotvirt's parser and Argo's would read apart are refused even when
// the identity dotvirt reads is the right one.
func TestArgoObjectsRefusesWhatTheParsersReadApart(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\n"
	two := vm + "---\n" + ownerRB
	utf16 := []byte{0xFF, 0xFE}
	for _, r := range two {
		utf16 = append(utf16, byte(r), 0)
	}
	for name, content := range map[string][]byte{
		"a merge key overriding the namespace": []byte(vm + "  <<: {namespace: beta}\n"),
		"carriage returns alone":               []byte(strings.ReplaceAll(two, "\n", "\r")),
		"a UTF-16 byte order mark":             utf16,
		"a document after a document end":      []byte(vm + "...\n" + ownerRB),
	} {
		if objs, err := argoObjects("alpha/web.yaml", content); err == nil {
			t.Errorf("%s: read as %+v", name, objs)
		}
	}
}

// A settings edit's result is read as Argo reads it, beside the editor's own
// checks: one declaring another object is refused, and so is an edit of a
// file whose other document Argo would place itself.
func TestSettingsEditKeepsWhatTheFileDeclares(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	proj := project.ProjectInfo{Name: "p", Namespaces: []string{"alpha"}}
	if err := keepsDeclared(proj, "web.yaml", []byte(vm), []byte(vm+"---\n"+ownerRB)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a result declaring a RoleBinding: err = %v, want ErrInvalid", err)
	}
	if err := keepsDeclared(proj, "web.yaml", []byte(vm), []byte(strings.Replace(vm, "Always", "Halted", 1))); err != nil {
		t.Errorf("a plain edit: %v", err)
	}

	loose := vm + "---\n" + strings.Replace(webPolicy, "  namespace: alpha\n", "", 1)
	f := newProposeFixture(t)
	f.commitMain(t, "web.yaml", loose)
	off := "Off"
	_, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{VMEdit: model.VMEdit{Power: &off}})
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("stage beside a namespace-less document: err = %v, want ErrInvalid", err)
	}
	items := f.c.toChangesetItems(f.proj, []draft.Entry{{Kind: draft.KindEdit, Namespace: "alpha", Name: "web", SourceFile: "web.yaml",
		Edit: &model.VMEdit{Power: &off}}})
	if _, err := items[0].Transform([]byte(loose)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("commit beside a namespace-less document: err = %v, want ErrInvalid", err)
	}
}

// An entry carrying both a manifest and a settings edit is refused: the gate
// and the commit must read one change.
func TestGateRefusesAnEntryOfTwoWrites(t *testing.T) {
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	e := draft.Entry{Kind: draft.KindEdit, Namespace: "alpha", Name: "web", SourceFile: "web.yaml",
		Manifest: "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata: {name: web, namespace: alpha}\n", Edit: &model.VMEdit{SetLabels: map[string]string{"a": "b"}}}
	if err := c.gate(auth.Identity{Username: "alice"}, proj, []draft.Entry{e}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// The gate reads an entry off the version it is pinned to: an entry made from
// another version is a conflict, and the commit's pin error to propose.
func TestGateHoldsTheEntryToItsPin(t *testing.T) {
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	e := draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "web", SourceFile: "web.yaml", BaseBlob: git.BlobID([]byte("older"))}
	err := c.gate(auth.Identity{Username: "alice"}, proj, []draft.Entry{e})
	var moved *git.BaseMovedError
	if !errors.Is(err, model.ErrConflict) || !errors.As(err, &moved) || moved.Path != "web.yaml" {
		t.Errorf("err = %v, want a conflict on web.yaml", err)
	}
	unpinned := []draft.Entry{{Kind: draft.KindDelete, Namespace: "alpha", Name: "web", SourceFile: "web.yaml"}}
	if err := c.gate(auth.Identity{Username: "alice"}, proj, unpinned); err != nil || unpinned[0].BaseBlob == "" {
		t.Errorf("an unpinned entry: err = %v, pin %q", err, unpinned[0].BaseBlob)
	}
}

// Whether an object is new is the branch's answer, not one file's: an
// object declared elsewhere is updated, and a file declaring one object
// twice is refused. An object Argo replaces is deleted and created.
func TestGateReadsEffectsPerObject(t *testing.T) {
	replaced := strings.Replace(netUDN, "  namespace: alpha\n", "  namespace: alpha\n  annotations: {argocd.argoproj.io/sync-options: \"Replace=true,Force=true\"}\n", 1)
	bare := seedBareFiles(t, map[string][]byte{"alpha/elsewhere.yaml": []byte(netUDN), "alpha/networks/net.yaml": []byte(netUDN)})
	c := newTestCoordinator(t, false)
	r := &rbac{}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	udn := func(verb string) Access {
		return Access{Verb: verb, Group: "k8s.ovn.org", Resource: "userdefinednetworks", Namespace: "alpha", Name: "net"}
	}

	moved := draft.Entry{Kind: draft.KindCreate, Resource: draft.ResourceNetwork, Namespace: "alpha", Name: "net", SourceFile: "alpha/net.yaml", Manifest: netUDN}
	if err := c.gate(id, proj, []draft.Entry{moved}); err != nil {
		t.Fatal(err)
	}
	if got := r.questions(); !slices.Equal(got, []Access{udn("update")}) {
		t.Errorf("an object declared in another file: asked %+v", got)
	}

	twice := moved
	twice.Manifest = netUDN + "---\n" + netUDN
	if err := c.gate(id, proj, []draft.Entry{twice}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("one object twice: err = %v, want ErrInvalid", err)
	}

	r.asked = nil
	if _, err := c.StageUpdateManifest(id, proj, "network", "alpha", "net", model.UpdateManifestRequest{YAML: replaced}); err != nil {
		t.Fatal(err)
	}
	if got := r.verbs(); !slices.Equal(got, []Access{udn("delete"), udn("create")}) {
		t.Errorf("a replace: asked %+v, want a delete and a create", got)
	}
}

// Project creation is gated before its repo is made: a refusal leaves no
// repo behind.
func TestCreateProjectGatedBeforeTheRepo(t *testing.T) {
	f := newProposeFixture(t, when("GET", "team-a", http.StatusNotFound, ""), func(m, path string) (int, string, bool) {
		if m == "POST" && strings.HasSuffix(path, "/repos") {
			t.Error("the repo was created")
			return http.StatusCreated, "{}", true
		}
		return 0, "", false
	})
	f.c.UseAuthorizer(&rbac{deny: verbOn("create", "namespaces")})
	_, err := f.c.StageCreateProject(f.id, f.platform(), json.RawMessage(`{"name":"team-a","namespace":"team-a"}`), nil)
	if !errors.Is(err, model.ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

// Restore brings back a state base held: a commit only a proposal branch
// carries is not one. A past version passes the gate as an edit would: the
// identity check refuses a second object, the placement guard a namespace
// left to Argo and a NAD in a tenant repo.
func TestRestoreOnlyFromBase(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	nad := "apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: net\n  namespace: alpha\nspec:\n  config: '{}'\n"
	bare, work := seedWork(t, map[string][]byte{"web.yaml": []byte(vm + "---\n" + ownerRB), "alpha/networks/net.yaml": []byte(nad)})
	smuggled := head(t, work)
	writeWorkFile(t, work, "web.yaml", []byte(strings.Replace(vm, "  namespace: alpha\n", "", 1)))
	gitRun(t, work, "commit", "-qam", "drop the grant")
	unplaced := head(t, work)
	writeWorkFile(t, work, "web.yaml", []byte(vm))
	writeWorkFile(t, work, "alpha/networks/net.yaml", []byte(netUDN))
	gitRun(t, work, "commit", "-qam", "place web, net as a UDN")
	gitRun(t, work, "push", "-q", "origin", "main")
	gitRun(t, work, "checkout", "-qb", "dotvirt/proposed/eve/p-1")
	writeWorkFile(t, work, "web.yaml", []byte(strings.Replace(vm, "Always", "Halted", 1)))
	gitRun(t, work, "commit", "-qam", "proposal")
	gitRun(t, work, "push", "-q", "origin", "dotvirt/proposed/eve/p-1")
	proposed := head(t, work)

	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	for _, tc := range []struct {
		name, resource, object, hash string
		want                         error
	}{
		{"a proposal's commit", "", "web", proposed, model.ErrNotFound},
		{"a version declaring a second object", "", "web", smuggled, model.ErrInvalid},
		{"a version without its namespace", "", "web", unplaced, model.ErrInvalid},
		{"a version of the UDN that was a NAD", "network", "net", smuggled, model.ErrInvalid},
	} {
		if _, err := c.RestoreVersion(id, proj, tc.resource, "alpha", tc.object, tc.hash); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("staged %+v", entries)
	}
}

// A platform revert asks for each object it restores, so a template
// curator reverts a library change but not a network one.
func TestPlatformRevertByATemplateCurator(t *testing.T) {
	cudn := func(topology string) string {
		return "apiVersion: k8s.ovn.org/v1\nkind: ClusterUserDefinedNetwork\nmetadata:\n  name: wan\nspec:\n  topology: " + topology + "\n"
	}
	tpl := func(desc string) string {
		return "apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: small\n  annotations: {description: " + desc + "}\n"
	}
	bare, work := seedWork(t, map[string][]byte{"networks/wan.yaml": []byte(cudn("Layer2")), "templates/small.yaml": []byte(tpl("one"))})
	writeWorkFile(t, work, "networks/wan.yaml", []byte(cudn("Layer3")))
	gitRun(t, work, "commit", "-qam", "network")
	network := head(t, work)
	writeWorkFile(t, work, "templates/small.yaml", []byte(tpl("two")))
	gitRun(t, work, "commit", "-qam", "template")
	library := head(t, work)
	gitRun(t, work, "push", "-q", "origin", "main")

	c := newTestCoordinator(t, true)
	curator := func(a Access) bool { return a.Resource != "virtualmachinetemplates" }
	c.UseAuthorizer(&rbac{deny: curator})
	id := auth.Identity{Username: "carol"}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	if _, err := c.Revert(id, plat, network); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("reverting a network: err = %v, want ErrForbidden", err)
	}
	if _, err := c.Revert(id, plat, library); err != nil {
		t.Errorf("reverting a template: %v", err)
	}
}

// A user whose rights were withdrawn after staging cannot propose what they
// staged: propose asks again, and nothing is pushed.
func TestProposeRegatesTheDraft(t *testing.T) {
	f, _ := newPRFixture(t)
	r := &rbac{}
	f.c.UseAuthorizer(r)
	f.stageLabel(t, "env", "prod")

	r.refuse(verbOn("update", "virtualmachines"))
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if out, err := exec.Command("git", "-C", f.proj.Repo, "branch", "--list", "dotvirt/*").Output(); err != nil || len(out) != 0 {
		t.Errorf("pushed %q (%v)", out, err)
	}
	if sent := f.sentTo("POST", "/pulls"); len(sent) != 0 {
		t.Errorf("opened a PR: %v", sent)
	}
	if n := f.draftCount(t); n != 1 {
		t.Errorf("draft = %d entries, want the one kept", n)
	}
}

// Re-proposing rebuilds the open PR with what it carries: those entries are
// asked again too, not only the draft laid over them.
func TestReproposeRegatesWhatThePRCarries(t *testing.T) {
	f, prs := newPRFixture(t)
	r := &rbac{}
	f.c.UseAuthorizer(r)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	pushed := revParse(t, f.proj.Repo, prs.head)
	raw, _ := json.Marshal(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha"})
	if _, err := f.c.StageCreate(f.id, f.proj, draft.ResourceNetworkPolicy, raw); err != nil {
		t.Fatal(err)
	}

	r.refuse(verbOn("update", "virtualmachines"))
	_, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if !errors.Is(err, model.ErrForbidden) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("err = %v, want ErrForbidden naming PR #1", err)
	}
	if now := revParse(t, f.proj.Repo, prs.head); now != pushed {
		t.Errorf("the PR branch moved: %s -> %s", pushed, now)
	}
}

func head(t *testing.T, work string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Keys the same but for case read differently in Go's decoding and the
// apiserver's, so a file holding them is refused, as is one turning the
// apiserver's validation off.
func TestArgoObjectsRefusesCaseFoldedKeysAndSkippedValidation(t *testing.T) {
	decoy := strings.Replace(ownerRB, "subjects:", "roleref: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: view}\nsubjects:", 1)
	unvalidated := strings.Replace(netUDN, "  namespace: alpha\n", "  namespace: alpha\n  annotations: {argocd.argoproj.io/sync-options: Validate=false}\n", 1)
	nested := strings.Replace(netUDN, "  topology: Layer2\n", "  topology: Layer2\n  Topology: Layer3\n", 1)
	for name, content := range map[string]string{"a decoy roleref": decoy, "Validate=false": unvalidated, "a nested decoy": nested} {
		if objs, err := argoObjects("alpha/x.yaml", []byte(content)); err == nil {
			t.Errorf("%s: read as %+v", name, objs)
		}
	}
}

// A settings edit of a VM Argo replaces rather than patches asks for its
// delete and create: the replace takes its template disks with it.
func TestSettingsEditOfAReplacedVM(t *testing.T) {
	f := newProposeFixture(t)
	r := &rbac{}
	f.c.UseAuthorizer(r)
	f.commitMain(t, "web.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\n  annotations: {argocd.argoproj.io/sync-options: Replace=true}\nspec:\n  runStrategy: Always\n")
	off := "Off"
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{VMEdit: model.VMEdit{Power: &off}}); err != nil {
		t.Fatal(err)
	}
	vm := func(verb string) Access {
		return Access{Verb: verb, Group: "kubevirt.io", Resource: "virtualmachines", Namespace: "alpha", Name: "web"}
	}
	if got := r.verbs(); !slices.Equal(got, []Access{vm("delete"), vm("create")}) {
		t.Errorf("asked %+v, want the delete and the create", got)
	}
}

// An entry staged before entries were pinned is refused at propose: the gate
// would weigh it against one version and the commit apply it to another.
func TestProposeRefusesAnUnpinnedEntry(t *testing.T) {
	f, _ := newPRFixture(t)
	legacy := draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "web", SourceFile: "web.yaml"}
	if err := f.c.store.Stage(f.id.Username, f.proj.Name, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "stage it again") {
		t.Errorf("err = %v, want a conflict asking to stage it again", err)
	}
}

// An apiVersion goes into a dry run's URL, so it must read as one group and
// one version, nothing more.
func TestArgoObjectsRefusesAnAPIVersionOfMoreThanGroupAndVersion(t *testing.T) {
	for _, v := range []string{"kubevirt.io/v1/../x", "kubevirt.io/../v1", "kubevirt.io/v1?x=y", "kubevirt.io//v1", "kubevirt.io/V1"} {
		y := "apiVersion: " + v + "\nkind: VirtualMachine\nmetadata: {name: web, namespace: alpha}\n"
		if objs, err := argoObjects("alpha/web.yaml", []byte(y)); err == nil {
			t.Errorf("%s: read as %+v", v, objs)
		}
	}
	if _, err := argoObjects("alpha/web.yaml", []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata: {name: web, namespace: alpha}\n")); err != nil {
		t.Errorf("kubevirt.io/v1: %v", err)
	}
}
