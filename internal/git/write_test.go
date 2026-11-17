package git

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/epheo/dotvirt/internal/git/gittest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
)

// seedMultiRepo creates a bare repo with a README + two VM manifests under
// tenant-a/ on main, plus a `seed` branch mirroring it, returning the bare path.
func seedMultiRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, content string) {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	write("README.md", "# project\n")
	write("tenant-a/web.yaml", "kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")
	write("tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\n")
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	// A non-default branch: a fresh clone materializes it only as a remote ref,
	// so committing to it exercises checkoutBranch's remote-ref path.
	run(work, "branch", "seed")
	run(work, "push", "-q", "origin", "seed")
	return bare
}

// Commits are additive: files absent from the set survive, and re-committing
// identical content is a no-op that never churns history.
func TestCommitAdditiveAndNoOp(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)

	content := []byte("kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")
	files := []model.File{{Path: "tenant-a/web.yaml", Content: content}}
	res, err := w.Commit("seed", "sync", files)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Committed {
		t.Fatal("identical content must not commit")
	}

	files = []model.File{{Path: "tenant-a/new.yaml", Content: []byte("kind: VirtualMachine\nmetadata: {name: new, namespace: tenant-a}\n")}}
	res, err = w.Commit("seed", "sync", files)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected a commit for the new file")
	}

	tree := lsTree(t, bare, "seed")
	for _, want := range []string{"tenant-a/new.yaml", "tenant-a/web.yaml", "tenant-a/db.yaml", "README.md"} {
		if !contains(tree, want) {
			t.Errorf("%s missing after additive commit", want)
		}
	}
}

func lsTree(t *testing.T, bare, ref string) []string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "ls-tree", "-r", "--name-only", ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// Writes clone one commit deep; the pushed history must still be complete on
// the remote, for an existing branch, a new one, and a changeset.
func TestShallowWritesKeepRemoteConnected(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		bare := seedMultiRepo(t)
		testShallowWrites(t, bare, bare)
	})
	// Over smart http go-git takes the network push path, not the local one.
	t.Run("http", func(t *testing.T) {
		bare := seedMultiRepo(t)
		testShallowWrites(t, bare, serveHTTP(t, bare))
	})
}

// serveHTTP serves bare through git http-backend with pushes enabled.
func serveHTTP(t *testing.T, bare string) string {
	t.Helper()
	h, err := gittest.Handler(filepath.Dir(bare))
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(bare)
}

func testShallowWrites(t *testing.T, bare, url string) {
	w := OpenWrite(url, "", nil, true)
	file := func(n string) []model.File {
		return []model.File{{Path: "tenant-a/" + n + ".yaml", Content: []byte("kind: ConfigMap\n")}}
	}
	for _, n := range []string{"a", "b"} {
		if _, err := w.Commit("seed", "add "+n, file(n)); err != nil {
			t.Fatalf("Commit seed %s: %v", n, err)
		}
	}
	if _, err := w.Commit("fresh", "add c", file("c")); err != nil {
		t.Fatalf("Commit fresh: %v", err)
	}
	items := []ChangesetItem{{Path: "tenant-a/d.yaml", NewContent: []byte("kind: ConfigMap\n")}}
	if _, err := w.CommitChangeset("seed", "proposed", "add d", items, Author{Name: "u"}); err != nil {
		t.Fatalf("CommitChangeset: %v", err)
	}

	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("fsck", "--strict", "--no-dangling")
	if n := git("rev-list", "--count", "proposed"); n != "4" {
		t.Errorf("proposed history = %s commits, want 4 (seed + a + b + d)", n)
	}
	if n := git("rev-list", "--count", "fresh"); n != "2" {
		t.Errorf("fresh history = %s commits, want 2 (forked from main)", n)
	}
}

// A user's view clones, lists and pushes at its own address with its own
// credentials alone, and commits as the user; the view it came from still
// writes as dotvirt's account, committed by dotvirt.
func TestAsWritesAsTheUser(t *testing.T) {
	bare := seedMultiRepo(t)
	h, err := gittest.Handler(filepath.Dir(bare))
	if err != nil {
		t.Skip(err)
	}
	var mu sync.Mutex
	sent := map[string][]string{} // credentials -> requests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		mu.Lock()
		sent[user+":"+pass] = append(sent[user+":"+pass], r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	url := srv.URL + "/" + filepath.Base(bare)
	bot := OpenWrite(url, "bot", forge.StaticToken("bot-token"), true)
	alice := bot.As(url, "alice-forge", "alice-token")

	items := []ChangesetItem{{Path: "tenant-a/d.yaml", NewContent: []byte("kind: ConfigMap\n")}}
	by := Author{Name: "alice", Email: "alice@dotvirt.noreply", Commits: true}
	res, err := alice.ProposeChangeset("main", "proposed", "add d", items, by, PushPlan{})
	if err != nil {
		t.Fatalf("ProposeChangeset as alice: %v", err)
	}
	if head, err := alice.RemoteHead("proposed"); err != nil || head != res.Hash {
		t.Fatalf("RemoteHead as alice = %q, %v; want %q", head, err, res.Hash)
	}
	mu.Lock()
	if len(sent) != 1 || !slices.ContainsFunc(sent["alice-forge:alice-token"], func(s string) bool { return strings.Contains(s, "git-receive-pack") }) {
		t.Fatalf("alice's write sent %v, want her credentials alone, her push included", sent)
	}
	clear(sent)
	mu.Unlock()
	log := func(branch string) string {
		out, err := exec.Command("git", "-C", bare, "log", "-1", "--format=%an <%ae> / %cn <%ce>", branch).CombinedOutput()
		if err != nil {
			t.Fatalf("git log: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got, want := log("proposed"), "alice <alice@dotvirt.noreply> / alice <alice@dotvirt.noreply>"; got != want {
		t.Errorf("alice's commit = %q, want %q", got, want)
	}

	if _, err := bot.CommitChangeset("main", "bot", "add d", items, Author{Name: "alice", Email: "alice@dotvirt.noreply"}); err != nil {
		t.Fatalf("CommitChangeset as dotvirt: %v", err)
	}
	mu.Lock()
	if len(sent) != 1 || len(sent["bot:bot-token"]) == 0 {
		t.Errorf("dotvirt's write sent %v, want its own credentials alone", sent)
	}
	mu.Unlock()
	if got, want := log("bot"), "alice <alice@dotvirt.noreply> / dotvirt <dotvirt@localhost>"; got != want {
		t.Errorf("dotvirt's commit = %q, want %q", got, want)
	}
}
