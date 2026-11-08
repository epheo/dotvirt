package changeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
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

func verbOn(verb, resource string) func(Access) bool {
	return func(a Access) bool { return a.Verb == verb && a.Resource == resource }
}

const (
	webPolicy = "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  podSelector: {}\n"
	netUDN    = "apiVersion: k8s.ovn.org/v1\nkind: UserDefinedNetwork\nmetadata:\n  name: net\n  namespace: alpha\nspec:\n  topology: Layer2\n"
	ownerRB   = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: own\n  namespace: alpha\nroleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: admin}\nsubjects: [{kind: User, name: mallory}]\n"
)

// The manifest editor stages only what Argo would apply as exactly the object
// edited: nothing Argo unpacks or reads past dotvirt's own parser, no
// namespace left for Argo to choose, no other kind under the same name.
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

// A RoleBinding takes the right to bind the role it grants, beyond its own
// create: the apiserver's escalation check, which Argo applying it skips.
func TestRoleBindingNeedsBind(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t, false)
	r := &rbac{deny: verbOn("bind", "clusterroles")}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	entries, err := projectAdoption([]netgen.NamespaceSpec{{Name: "alpha", Project: "team"}}, []string{"bob"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.stage(id, plat, entries...); !errors.Is(err, model.ErrForbidden) {
		t.Fatalf("a grant without bind: err = %v, want ErrForbidden", err)
	}
	bind := Access{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Namespace: "alpha", Name: "admin"}
	if !slices.Contains(r.questions(), bind) {
		t.Errorf("asked %+v, want %+v among them", r.questions(), bind)
	}
	r.refuse(nil)
	if _, err := c.stage(id, plat, entries...); err != nil {
		t.Errorf("with bind: %v", err)
	}
}

// Restore brings back a state base held: a commit only a proposal branch
// carries is not one, and a past version is held to the object it restores,
// as the manifest editor holds an edit.
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
