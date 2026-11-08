package changeset

import (
	"context"
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

type fakeResyncer struct{ called bool }

func (f *fakeResyncer) Resync(ctx context.Context, username, namespace, name string) (model.ResyncResult, error) {
	f.called = true
	return model.ResyncResult{Application: "app"}, nil
}

// A resync asks what Argo applying the VM's declared object asks - its
// create, which the authorizer weighs as the update of a running VM - and a
// replacing sync option makes it a delete and a create; a refusal never
// reaches the escalation.
func TestResyncWeighsWhatTheSyncApplies(t *testing.T) {
	const vm = "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	const replaced = "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n  annotations:\n    argocd.argoproj.io/sync-options: Replace=true\nspec:\n  runStrategy: Always\n"
	bare := seedBareFiles(t, map[string][]byte{"alpha/web.yaml": []byte(vm), "alpha/db.yaml": []byte(replaced)})
	c := newTestCoordinator(t, false)
	rs := &fakeResyncer{}
	c.resyncer = rs
	r := &rbac{}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}

	verbs := func() []string {
		var out []string
		for _, a := range r.questions() {
			out = append(out, a.Verb+" "+a.Resource+" "+a.Name)
		}
		return out
	}
	if res, err := c.Resync(context.Background(), id, proj, "alpha", "web"); err != nil || res.Application != "app" {
		t.Fatalf("allowed caller: got %+v, %v", res, err)
	}
	if got := verbs(); len(got) != 1 || got[0] != "create virtualmachines web" {
		t.Errorf("asked %v, want the VM's create", got)
	}
	if _, err := c.Resync(context.Background(), id, proj, "alpha", "db"); err != nil {
		t.Fatal(err)
	}
	if got := verbs(); len(got) != 3 || got[1] != "delete virtualmachines db" || got[2] != "create virtualmachines db" {
		t.Errorf("asked %v, want a replacing sync's delete and create", got)
	}

	rs.called = false
	r.refuse(func(Access) bool { return true })
	if _, err := c.Resync(context.Background(), id, proj, "alpha", "web"); !errors.Is(err, model.ErrForbidden) {
		t.Fatalf("denied caller: want ErrForbidden, got %v", err)
	}
	if rs.called {
		t.Fatal("resyncer reached despite the refusal")
	}
	if _, err := c.Resync(context.Background(), id, proj, "alpha", "ghost"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("a VM git does not declare: %v", err)
	}
}

func TestResyncUnavailableWithoutArgo(t *testing.T) {
	c := newTestCoordinator(t, false)
	if _, err := c.Resync(context.Background(), auth.Identity{}, project.ProjectInfo{}, "a", "b"); !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
