package project

import (
	"testing"
)

func nsInfo(name, project, repo string) Namespace {
	n := Namespace{Name: name}
	if project != "" {
		n.Labels = map[string]string{"dotvirt.io/project": project}
	}
	if repo != "" {
		n.Annotations = map[string]string{"dotvirt.io/repo": repo}
	}
	return n
}

func resolver() *Resolver { return NewResolver("dotvirt.io/project", "dotvirt.io/repo") }

func TestResolveGroupsByProject(t *testing.T) {
	infos := resolver().Resolve([]Namespace{
		nsInfo("tenant-a-dev", "team-a", "https://forge/team-a.git"),
		nsInfo("tenant-a-prod", "team-a", "https://forge/team-a.git"),
		nsInfo("tenant-b", "team-b", "https://forge/team-b.git"),
		nsInfo("kube-system", "", ""), // unlabeled: skipped
	}, nil)

	if len(infos) != 2 {
		t.Fatalf("want 2 projects (unlabeled skipped), got %d: %+v", len(infos), infos)
	}
	a := infos[0]
	if a.Name != "team-a" || a.Repo != "https://forge/team-a.git" || a.Error != "" {
		t.Errorf("team-a resolved wrong: %+v", a)
	}
	if len(a.Namespaces) != 2 || a.Namespaces[0] != "tenant-a-dev" || a.Namespaces[1] != "tenant-a-prod" {
		t.Errorf("team-a namespaces wrong/unsorted: %v", a.Namespaces)
	}
}

func TestResolveNoRepo(t *testing.T) {
	infos := resolver().Resolve([]Namespace{nsInfo("tenant-c", "team-c", "")}, nil)
	if len(infos) != 1 || infos[0].Repo != "" || infos[0].Error == "" {
		t.Errorf("expected team-c with no repo + an Error, got %+v", infos)
	}
}

// TestResolveVisibleFilter is the isolation guard: a namespace the caller can't
// see (absent from visible) must never surface — not even to leak its project's
// repo URL. With visible listing only team-a's namespace, team-b disappears
// entirely. A nil visible means "no filter" (the SA/background path).
func TestResolveVisibleFilter(t *testing.T) {
	all := []Namespace{
		nsInfo("team-a-ns", "team-a", "https://forge/team-a.git"),
		nsInfo("team-b-ns", "team-b", "https://forge/team-b.git"),
	}

	visible := map[string]bool{"team-a-ns": true} // caller can't see team-b-ns
	infos := resolver().Resolve(all, visible)
	if len(infos) != 1 || infos[0].Name != "team-a" {
		t.Fatalf("visible filter should yield only team-a, got %+v", infos)
	}

	// No filter: both projects resolve (background/SA view).
	if got := resolver().Resolve(all, nil); len(got) != 2 {
		t.Errorf("nil visible should not filter; want 2 projects, got %d", len(got))
	}
}

func TestResolveConflictingRepo(t *testing.T) {
	infos := resolver().Resolve([]Namespace{
		nsInfo("tenant-d-1", "team-d", "https://forge/d-one.git"),
		nsInfo("tenant-d-2", "team-d", "https://forge/d-two.git"),
	}, nil)
	if len(infos) != 1 || infos[0].Repo != "" || infos[0].Error == "" {
		t.Errorf("expected team-d with conflict Error and empty repo, got %+v", infos)
	}
}
