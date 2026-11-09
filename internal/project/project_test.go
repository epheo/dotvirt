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

func resolver() *Resolver {
	return NewResolver("dotvirt.io/project", "dotvirt.io/repo", "https://forge.example")
}

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
// see (absent from visible) must never surface - not even to leak its project's
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

// A namespace claiming the platform tier's reserved name joins no project.
func TestResolveIgnoresReservedName(t *testing.T) {
	r := NewResolver("p", "r", "")
	got := r.Resolve([]Namespace{
		{Name: "a", Labels: map[string]string{"p": PlatformName}, Annotations: map[string]string{"r": "https://f/x.git"}},
		{Name: "b", Labels: map[string]string{"p": "team"}, Annotations: map[string]string{"r": "https://f/t.git"}},
	}, nil)
	if len(got) != 1 || got[0].Name != "team" {
		t.Fatalf("Resolve = %+v, want only team", got)
	}
}

// Owners names every labeled namespace's project, whoever asks, and never the
// reserved platform name.
func TestOwners(t *testing.T) {
	r := NewResolver("dotvirt.io/project", "dotvirt.io/repo", "")
	got := r.Owners([]Namespace{
		{Name: "a", Labels: map[string]string{"dotvirt.io/project": "team-a"}},
		{Name: "p", Labels: map[string]string{"dotvirt.io/project": PlatformName}},
		{Name: "u"},
	})
	if len(got) != 1 || got["a"] != "team-a" {
		t.Errorf("owners = %v", got)
	}
}

func withPath(n Namespace, p string) Namespace {
	n.Annotations["dotvirt.io/path"] = p
	return n
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"": "", ".": "", "clusters/a": "clusters/a", "clusters/a/": "clusters/a", "a/./b": "a/b", "a/b/..": "a"} {
		if got, ok := CleanPath(in); !ok || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/etc", "..", "../x", "a/../../x", ".git", "a/.git/hooks", `a\b`} {
		if got, ok := CleanPath(in); ok {
			t.Errorf("CleanPath(%q) = %q, accepted", in, got)
		}
	}
}

func TestResolvePath(t *testing.T) {
	repo := "https://forge/team-a.git"
	resolve := func(nss ...Namespace) ProjectInfo {
		t.Helper()
		infos := resolver().Resolve(nss, nil)
		if len(infos) != 1 {
			t.Fatalf("got %d projects", len(infos))
		}
		return infos[0]
	}
	if p := resolve(withPath(nsInfo("a", "team-a", repo), "clusters/a/")); p.Path != "clusters/a" || p.Repo != repo || p.Error != "" {
		t.Errorf("a folder: %+v", p)
	}
	// A namespace added later through dotvirt carries the repo alone.
	if p := resolve(withPath(nsInfo("a", "team-a", repo), "clusters/a"), nsInfo("b", "team-a", repo)); p.Path != "clusters/a" || p.Error != "" {
		t.Errorf("a namespace with no opinion: %+v", p)
	}
	for name, nss := range map[string][]Namespace{
		"conflict": {withPath(nsInfo("a", "team-a", repo), "x"), withPath(nsInfo("b", "team-a", repo), "y")},
		"invalid":  {withPath(nsInfo("a", "team-a", repo), "../x")},
	} {
		if p := resolve(nss...); p.Error == "" || p.Repo != "" || p.Path != "" {
			t.Errorf("%s: %+v, want an Error and no repo", name, p)
		}
	}
}

// Reading a project as the whole repo would let its writes land outside the folder.
func TestAFolderHoldsForACallerWhoSeesOnlyOtherNamespaces(t *testing.T) {
	repo := "https://forge/team-a.git"
	nss := []Namespace{withPath(nsInfo("a", "team-a", repo), "clusters/a"), nsInfo("b", "team-a", repo)}
	infos := resolver().Resolve(nss, map[string]bool{"b": true})
	if len(infos) != 1 || infos[0].Path != "clusters/a" || infos[0].Error != "" {
		t.Errorf("filtered view: %+v", infos)
	}
}

// Two spellings of one URL are one repo, as the mirror cache keys them.
func TestASharedRepoIsFoundUnderAnySpelling(t *testing.T) {
	nss := []Namespace{withPath(nsInfo("a", "team-a", "https://forge/shared.git"), "a"), nsInfo("b", "team-b", "https://forge/shared")}
	for _, p := range resolver().Resolve(nss, nil) {
		if p.Error == "" {
			t.Errorf("%s: %+v, want it refused", p.Name, p)
		}
	}
}

// A repo is one tenant: Argo and the forge grant by repo, folder or no folder.
func TestARepoIsOneProject(t *testing.T) {
	repo := "https://forge/shared.git"
	refused := func(name string, nss []Namespace, visible map[string]bool) {
		t.Helper()
		infos := resolver().Resolve(nss, visible)
		if len(infos) == 0 {
			t.Fatalf("%s: no projects", name)
		}
		for _, p := range infos {
			if p.Error == "" || p.Repo != "" {
				t.Errorf("%s: %s = %+v, want it refused", name, p.Name, p)
			}
		}
	}
	plain := []Namespace{nsInfo("a", "team-a", repo), nsInfo("b", "team-b", repo)}
	refused("no folder", plain, nil)
	refused("a folder", []Namespace{withPath(nsInfo("a", "team-a", repo), "a"), nsInfo("b", "team-b", repo)}, nil)
	refused("a caller who sees one of them", plain, map[string]bool{"b": true})
}

// One project over several namespaces is the normal shape, not sharing.
func TestOneProjectMayHoldManyNamespaces(t *testing.T) {
	repo := "https://forge/team-a.git"
	infos := resolver().Resolve([]Namespace{nsInfo("a", "team-a", repo), nsInfo("b", "team-a", repo)}, nil)
	if len(infos) != 1 || infos[0].Repo != repo || infos[0].Error != "" {
		t.Errorf("infos = %+v", infos)
	}
}
