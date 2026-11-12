package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOwnerRepo(t *testing.T) {
	cases := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://forge.example/dotvirt/team-a.git", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a/", "dotvirt", "team-a", true},
		{"http://forgejo-http.forgejo.svc:3000/dotvirt/vmrepo.git", "dotvirt", "vmrepo", true},
		// Query string must not leak into the repo name (fails closed otherwise).
		{"https://forge.example/dotvirt/team-a.git?ref=main", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a?x=1#frag", "dotvirt", "team-a", true},
		// Nested groups: last two segments win.
		{"https://forge.example/org/sub/team-a.git", "sub", "team-a", true},
		// Unparseable -> fail closed.
		{"https://forge.example/onlyone", "", "", false},
		{"not-a-url", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := ownerRepo(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("ownerRepo(%q) = (%q,%q,%v), want (%q,%q,%v)", c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestOwnerPrefixURL(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		// The scheme's "//" must survive - path.Dir would collapse it to "https:/",
		// yielding a prefix Argo never longest-prefix-matches (the original bug).
		{"https://forge.example/dotvirt/platform.git", "https://forge.example/dotvirt"},
		{"https://forge.example/dotvirt/platform", "https://forge.example/dotvirt"},
		{"https://forge.example/dotvirt/platform/", "https://forge.example/dotvirt"},
		{"http://forgejo-http.forgejo.svc:3000/dotvirt/platform.git", "http://forgejo-http.forgejo.svc:3000/dotvirt"},
		// Nested groups: only the last segment is stripped.
		{"https://forge.example/org/sub/platform.git", "https://forge.example/org/sub"},
		// No repo segment to strip -> returned unchanged.
		{"https://forge.example", "https://forge.example"},
		{"https://forge.example/", "https://forge.example/"},
	}
	for _, c := range cases {
		if got := OwnerPrefixURL(c.url); got != c.want {
			t.Errorf("OwnerPrefixURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// A forge names its repos at its ROOT_URL in webhook payloads; they must key
// as the repos dotvirt clones from the API's base.
func TestRepoKeyReadsTheForgesReposAtItsAPIBase(t *testing.T) {
	f := NewFactory("http://forge.ns.svc:3000/", StaticToken("t"), false, "").WithWebURL("https://Forge.example/")
	cases := map[string]string{
		"https://forge.example/o/r.git":  "http://forge.ns.svc:3000/o/r",
		"https://forge.example/O/r":      "http://forge.ns.svc:3000/o/r",
		"http://forge.ns.svc:3000/o/r/":  "http://forge.ns.svc:3000/o/r",
		"https://public.example/sub/o/r": "http://forge.ns.svc:3000/o/r",
		"https://forge.example/":         "https://forge.example",
		"":                               "",
	}
	for in, want := range cases {
		if got := f.RepoKey(in); got != want {
			t.Errorf("RepoKey(%q) = %q, want %q", in, got, want)
		}
	}
	var none *Factory
	if got := none.RepoKey("https://forge.example/o/r.git"); got != "https://forge.example/o/r" {
		t.Errorf("nil factory: RepoKey = %q", got)
	}
}

// testClient points a Client at an httptest server with the standard owner/repo.
func testClient(srvURL string) *Client {
	return NewFactory(srvURL, StaticToken("tok"), false, "").For(srvURL + "/dotvirt/team-a.git")
}

// pullsServer lists prs the way Forgejo's GET /pulls does: state, base and
// head filter by exact column match (head against the bare branch name the
// PR was opened from, which the label keeps after a branch delete),
// pages cap at maxPage items whatever limit asks, X-Total-Count carries the
// match count, and the list keeps the given order (callers pass it most
// recently updated first). calls counts the requests.
func pullsServer(t *testing.T, maxPage int, prs ...PR) (srv *httptest.Server, calls *atomic.Int32) {
	t.Helper()
	calls = new(atomic.Int32)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// GET /pulls/{base}/{head}: the first PR of the pair, in no order.
		if pair, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/dotvirt/team-a/pulls/"); ok {
			base, head, _ := strings.Cut(pair, "/")
			for _, pr := range prs {
				branch := pr.Head.Label
				if branch == "" {
					branch = pr.Head.Ref
				}
				if pr.Base.Ref == base && branch == head {
					writeJSON(t, w, pr)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var match []PR
		for _, pr := range prs {
			if st := q.Get("state"); st != "all" && st != pr.State {
				continue
			}
			if b := q.Get("base"); b != "" && b != pr.Base.Ref {
				continue
			}
			branch := pr.Head.Label
			if branch == "" {
				branch = pr.Head.Ref
			}
			if h := q.Get("head"); h != "" && h != branch {
				continue
			}
			match = append(match, pr)
		}
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		from, to := min((page-1)*maxPage, len(match)), min(page*maxPage, len(match))
		w.Header().Set("X-Total-Count", strconv.Itoa(len(match)))
		writeJSON(t, w, match[from:to])
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func listedPR(number int, state, head, base string, merged bool) PR {
	p := PR{Number: number, State: state, Merged: merged}
	p.Head.Ref, p.Base.Ref = head, base
	return p
}

func TestHeadBranch(t *testing.T) {
	const head = "dotvirt/proposed/alice/team-a-abc123"
	deleted := listedPR(7, "closed", "refs/pull/7/head", "main", true)
	deleted.Head.Label = head
	other := listedPR(7, "closed", "refs/pull/8/head", "main", true)
	other.Head.Label = head
	bare := listedPR(7, "closed", "refs/pull/7/head", "main", true)
	fork := listedPR(3, "open", head, "main", false)
	fork.Head.RepoID, fork.Base.RepoID = 2, 1
	forkDeleted := deleted
	forkDeleted.Head.RepoID, forkDeleted.Base.RepoID = -1, 1
	for _, c := range []struct {
		name string
		pr   PR
		want string
	}{
		{"live branch", listedPR(3, "open", head, "main", false), head},
		{"deleted branch keeps its name in the label", deleted, head},
		{"another PR's ref is no deletion", other, "refs/pull/8/head"},
		{"an AGit PR has no branch", bare, ""},
		{"a fork's branch is not the base repo's", fork, ""},
		{"a deleted fork's label is not the base repo's", forkDeleted, ""},
	} {
		if got := c.pr.HeadBranch(); got != c.want {
			t.Errorf("%s: HeadBranch = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFindPR(t *testing.T) {
	const head = "dotvirt/proposed/alice/team-a-abc123"
	fork := listedPR(3, "open", head, "main", false)
	fork.Head.RepoID, fork.Base.RepoID = 2, 1
	// Merged, then its branch deleted: the ref is the PR's own.
	deleted := func(number int, merged bool) PR {
		p := listedPR(number, "closed", fmt.Sprintf("refs/pull/%d/head", number), "main", merged)
		p.Head.Label = head
		return p
	}
	cases := []struct {
		name string
		prs  []PR
		want int // 0: none
	}{
		{"closed only", []PR{listedPR(4, "closed", head, "main", false)}, 4},
		{"open wins over a more recent closed", []PR{listedPR(4, "closed", head, "main", true), listedPR(7, "open", head, "main", false)}, 7},
		{"most recent closed decides", []PR{listedPR(9, "closed", head, "main", true), listedPR(4, "closed", head, "main", false)}, 9},
		{"open PR past the first page", []PR{
			listedPR(1, "open", "other-1", "main", false), listedPR(2, "open", "other-2", "main", false),
			listedPR(5, "open", head, "main", false),
		}, 5},
		{"other base ignored", []PR{listedPR(6, "open", head, "release", false)}, 0},
		{"other head ignored", []PR{listedPR(9, "open", "someone-elses-branch", "main", false)}, 0},
		{"fork reusing the branch name ignored", []PR{fork}, 0},
		{"merged PR whose branch was deleted", []PR{deleted(7, true)}, 7},
		{"closed PR whose branch was deleted ignored", []PR{deleted(8, false)}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := pullsServer(t, 2, c.prs...)
			got, ok, err := testClient(srv.URL).FindPR(head, "main")
			if err != nil {
				t.Fatalf("FindPR: %v", err)
			}
			if c.want == 0 {
				if ok {
					t.Fatalf("want none, got #%d", got.Number)
				}
				return
			}
			if !ok || got.Number != c.want {
				t.Fatalf("got (#%d, ok=%v), want #%d", got.Number, ok, c.want)
			}
		})
	}
}

// The forge's pair lookup ends the search when it answers with the open PR;
// any other answer falls back to the list, which finds the open one.
func TestFindPRPairLookup(t *testing.T) {
	const head = "dotvirt/proposed/alice/team-a-abc123"
	srv, calls := pullsServer(t, 50, listedPR(4, "open", head, "main", false))
	if pr, ok, err := testClient(srv.URL).FindPR(head, "main"); err != nil || !ok || pr.Number != 4 || calls.Load() != 1 {
		t.Fatalf("got #%d ok=%v err=%v in %d calls, want open #4 in one", pr.Number, ok, err, calls.Load())
	}
	srv, _ = pullsServer(t, 50, listedPR(2, "closed", head, "main", true), listedPR(4, "open", head, "main", false))
	if pr, ok, err := testClient(srv.URL).FindPR(head, "main"); err != nil || !ok || pr.Number != 4 {
		t.Fatalf("an old merged pair answer must not hide the open PR, got #%d ok=%v err=%v", pr.Number, ok, err)
	}
}

// A forge ignoring the head filter lists every closed PR of the repo; the
// scan for the branch's closed PR stops after two pages rather than walk the
// whole history on every propose.
func TestFindPRCapsTheClosedScan(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pulls") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("X-Total-Count", "1000")
		if r.URL.Query().Get("state") == "open" {
			w.Header().Set("X-Total-Count", "0")
			writeJSON(t, w, []PR{})
			return
		}
		writeJSON(t, w, []PR{listedPR(1, "closed", "other", "main", true)})
	}))
	defer srv.Close()
	if _, ok, err := testClient(srv.URL).FindPR("mine", "main"); err != nil || ok {
		t.Fatalf("want none, got ok=%v err=%v", ok, err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("one open page and two closed pages, made %d calls", n)
	}
}

// The lane lists every open PR into base across pages, and stops at the
// forge's stated total instead of asking for an empty page.
func TestOpenPRsPages(t *testing.T) {
	srv, calls := pullsServer(t, 2,
		listedPR(1, "open", "a", "main", false), listedPR(2, "open", "b", "release", false),
		listedPR(3, "open", "c", "main", false), listedPR(4, "closed", "d", "main", true),
		listedPR(5, "open", "e", "main", false))
	prs, err := testClient(srv.URL).OpenPRs("main")
	if err != nil {
		t.Fatalf("OpenPRs: %v", err)
	}
	var got []int
	for _, pr := range prs {
		got = append(got, pr.Number)
	}
	if !slices.Equal(got, []int{1, 3, 5}) {
		t.Fatalf("got %v, want every open PR into main", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("3 PRs at 2 a page are 2 requests, made %d", n)
	}
}

// MergedPRs filters the closed-PR list down to merges into base, and a null
// merged_at (Forgejo's unmerged form) must parse to a zero time, not an error.
func TestMergedPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "closed" {
			t.Errorf("state = %q, want closed", got)
		}
		_, _ = w.Write([]byte(`[
			{"number": 4, "title": "landed", "merged": true, "merged_at": "2026-07-17T10:00:00Z",
			 "user": {"login": "dotvirt-bot"}, "head": {"ref": "dotvirt/proposed/alice/team-a-abc"},
			 "base": {"ref": "main"}},
			{"number": 5, "title": "abandoned", "merged": false, "merged_at": null,
			 "base": {"ref": "main"}},
			{"number": 6, "title": "other base", "merged": true, "merged_at": "2026-07-17T09:00:00Z",
			 "base": {"ref": "release"}}
		]`))
	}))
	defer srv.Close()

	prs, err := testClient(srv.URL).MergedPRs("main", 20)
	if err != nil {
		t.Fatalf("MergedPRs: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 4 || prs[0].User.Login != "dotvirt-bot" || prs[0].MergedAt.IsZero() {
		t.Fatalf("got %+v, want merged #4 with author and timestamp", prs)
	}
}

func TestEditPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if want := "/api/v1/repos/dotvirt/team-a/pulls/4"; r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if len(body) != 2 || body["state"] != "open" || body["title"] != "t" {
			t.Errorf("body = %v, want state=open and the title alone", body)
		}
		writeJSON(t, w, PR{Number: 4, State: "open"})
	}))
	defer srv.Close()

	pr, err := testClient(srv.URL).EditPR(4, PREdit{State: "open", Title: "t"})
	if err != nil {
		t.Fatalf("EditPR: %v", err)
	}
	if pr.State != "open" || pr.Number != 4 {
		t.Fatalf("got %+v, want open #4", pr)
	}
}

func TestCreatePRConflictPreservesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "pull request already exists", http.StatusConflict)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL).CreatePR("t", "b", "head", "main")
	if err == nil {
		t.Fatal("want error on 409")
	}
	// The 409 status must survive in the error string (log format relies on it).
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("error %q does not mention 409", err)
	}
}

func writeJSON(t *testing.T, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// resetHookCache clears the package-level hook-secret fingerprint cache so each webhook
// test starts from a cold process (first-sight re-asserts; converged sweeps don't).
func resetHookCache() {
	hookSecretsMu.Lock()
	defer hookSecretsMu.Unlock()
	hookSecrets = map[string]string{}
}

// EnsureWebhook creates the hook when absent (recording the landed secret), leaves a
// converged hook untouched on later sweeps, and RECREATES - never edits - a hook whose
// secret this process hasn't proven: Forgejo's hook-edit API silently ignores
// config.secret, so a PATCH re-assert would leave a stale secret 403ing every delivery.
func TestEnsureWebhookReconcilesOnce(t *testing.T) {
	resetHookCache()
	posts, deletes := 0, 0
	existing := `[]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/hooks"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, existing)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/hooks"):
			posts++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/hooks/9"):
			deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, StaticToken("tok"), false, "").For("https://forge/o/r.git")
	const target = "https://dotvirt/api/webhooks/forge"

	// Absent -> created once; the create lands the secret, so the sweeps that
	// follow see a proven hook and write nothing.
	if err := c.EnsureWebhook(target, "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook (create): %v", err)
	}
	existing = `[{"id":1,"config":{"url":"https://dotvirt/api/webhooks/forge"}}]`
	for i := 0; i < 2; i++ {
		if err := c.EnsureWebhook(target, "s3cret"); err != nil {
			t.Fatalf("EnsureWebhook (converged): %v", err)
		}
	}
	if posts != 1 || deletes != 0 {
		t.Fatalf("converged hook must stay untouched: posts=%d deletes=%d", posts, deletes)
	}

	// A hook this process never wrote (survived a reinstall; secret unprovable)
	// is recreated exactly once, then reads converged.
	resetHookCache()
	existing = `[{"id":9,"config":{"url":"https://dotvirt/api/webhooks/forge"}}]`
	if err := c.EnsureWebhook(target, "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook (stale): %v", err)
	}
	if deletes != 1 || posts != 2 {
		t.Fatalf("unproven hook must be recreated once: posts=%d deletes=%d", posts, deletes)
	}
	existing = `[{"id":1,"config":{"url":"https://dotvirt/api/webhooks/forge"}}]`
	if err := c.EnsureWebhook(target, "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook (after recreate): %v", err)
	}
	if posts != 2 || deletes != 1 {
		t.Fatalf("the replacement hook must read converged: posts=%d deletes=%d", posts, deletes)
	}
}

// EnsureWebhook replaces a hook when its host changed (external Route -> in-cluster
// Service): matched by URL PATH, recreated with the new target, never duplicated.
func TestEnsureWebhookHostChangeRecreates(t *testing.T) {
	resetHookCache()
	var deletedOld bool
	var createdURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/hooks"):
			// Same path, old host.
			fmt.Fprint(w, `[{"id":5,"config":{"url":"https://old-route.example/api/webhooks/forge"}}]`)
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/hooks/5"):
			deletedOld = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/hooks"):
			var body struct {
				Config map[string]string `json:"config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode POST: %v", err)
			}
			createdURL = body.Config["url"]
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":6}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, StaticToken("tok"), false, "").For("https://forge/o/r.git")
	const target = "http://dotvirt.dotvirt.svc:8080/api/webhooks/forge"
	if err := c.EnsureWebhook(target, "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook: %v", err)
	}
	if !deletedOld {
		t.Error("the old-host hook must be deleted")
	}
	if createdURL != target {
		t.Errorf("recreated hook url = %q, want %q", createdURL, target)
	}
}

// EnsureWebhook deletes extra hooks sharing the dotvirt path (duplicates from an earlier
// URL-change migration or a manual add), collapsing to a single delivery target.
func TestEnsureWebhookDeletesDuplicates(t *testing.T) {
	resetHookCache()
	deleted := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/hooks"):
			fmt.Fprint(w, `[{"id":1,"config":{"url":"https://dotvirt/api/webhooks/forge"}},`+
				`{"id":2,"config":{"url":"https://dotvirt/api/webhooks/forge"}}]`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/hooks"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":3}`)
		case r.Method == "DELETE":
			deleted[r.URL.Path] = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, StaticToken("tok"), false, "").For("https://forge/o/r.git")
	if err := c.EnsureWebhook("https://dotvirt/api/webhooks/forge", "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook: %v", err)
	}
	// The unproven primary is recreated (its delete) and the duplicate removed.
	if len(deleted) != 2 {
		t.Fatalf("want the primary's recreate-delete + the duplicate's delete, got %v", deleted)
	}
	if !deleted["/api/v1/repos/o/r/hooks/2"] {
		t.Fatalf("duplicate hook 2 not deleted: %v", deleted)
	}
	for path := range deleted {
		if !strings.HasSuffix(path, "/hooks/2") && !strings.HasSuffix(path, "/hooks/1") {
			t.Errorf("deleted %q, want the duplicate /hooks/2", path)
		}
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	// Every spelling of the same repo must canonicalize to one key, so a push
	// webhook reliably finds the repo's poller and its managing ArgoCD Application.
	want := "https://forge.example/org/team-a"
	for _, in := range []string{
		"https://forge.example/org/team-a.git",
		"https://forge.example/org/team-a",
		"https://forge.example/org/team-a/",
		"https://forge.example/org/team-a.GIT", // mixed-case suffix must still strip
		"  https://Forge.Example/org/team-a.git  ",
	} {
		if got := NormalizeRepoURL(in); got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeRepoURL(""); got != "" {
		t.Errorf("NormalizeRepoURL(\"\") = %q, want empty", got)
	}
}

// One net/url parse serves every URL helper, so the forms a repo is written in
// - host-free annotation refs, credentialed clone URLs, bare paths - resolve
// the same way everywhere.
func TestURLHelpersAgreeOnEveryForm(t *testing.T) {
	cases := []struct {
		url                      string
		host, pathRef, ownerRepo string
	}{
		{"https://forge.example/dotvirt/team-a.git", "forge.example", "dotvirt/team-a.git", "dotvirt/team-a"},
		{"https://bot:secret@Forge.Example:3000/dotvirt/team-a.git", "forge.example:3000", "dotvirt/team-a.git", "dotvirt/team-a"},
		{"dotvirt/team-a.git", "", "dotvirt/team-a.git", "dotvirt/team-a"},
		{"/tmp/repos/dotvirt/team-a.git", "", "tmp/repos/dotvirt/team-a.git", "dotvirt/team-a"},
	}
	for _, c := range cases {
		if got := parseURL(c.url).host; got != c.host {
			t.Errorf("parseURL(%q).host = %q, want %q", c.url, got, c.host)
		}
		if got := PathRef(c.url); got != c.pathRef {
			t.Errorf("PathRef(%q) = %q, want %q", c.url, got, c.pathRef)
		}
		owner, repo, ok := ownerRepo(c.url)
		if !ok || owner+"/"+repo != c.ownerRepo {
			t.Errorf("ownerRepo(%q) = (%q, %q, %v), want %q", c.url, owner, repo, ok, c.ownerRepo)
		}
	}
	if !NewFactory("https://forge.example", StaticToken("t"), false, "").SameForge("https://bot:x@FORGE.example/o/r.git") {
		t.Error("credentials and case must not make a repo foreign")
	}
	if got := ResolveRef("https://forge.example", "o/r.git"); got != "https://forge.example/o/r.git" {
		t.Errorf("ResolveRef relative = %q", got)
	}
	if got := ResolveRef("https://forge.example", "file:///srv/o/r.git"); got != "file:///srv/o/r.git" {
		t.Errorf("ResolveRef absolute = %q", got)
	}
}

// CHG-11: the git token travels only to the forge's own host, and over plain
// http only when the forge itself is http - a namespace annotation naming
// another host must not harvest it.
func TestCarriesToken(t *testing.T) {
	for _, c := range []struct {
		forge, repo string
		want        bool
	}{
		{"https://forge.example", "https://forge.example/dotvirt/team-a.git", true},
		{"https://forge.example/", "https://FORGE.example/dotvirt/team-a.git", true},
		{"https://forge.example", "https://bot:x@forge.example/o/r.git", true},
		{"https://forge.example", "https://evil.example/dotvirt/team-a.git", false},
		{"https://forge.example", "https://forge.example.evil.example/o/r.git", false},
		{"https://forge.example", "http://forge.example/dotvirt/team-a.git", false},
		{"https://forge.example", "https://forge.example:8443/o/r.git", false},
		{"http://forgejo.dotvirt.svc:3000", "http://forgejo.dotvirt.svc:3000/o/r.git", true},
		{"https://forge.example", "dotvirt/team-a.git", false},
		{"https://forge.example", "/srv/git/team-a.git", false},
		{"", "https://forge.example/o/r.git", false},
	} {
		if got := CarriesToken(c.forge, c.repo); got != c.want {
			t.Errorf("CarriesToken(%q, %q) = %v, want %v", c.forge, c.repo, got, c.want)
		}
	}
}

func TestBrowserLinksUseTheWebBase(t *testing.T) {
	api := "http://forgejo.dotvirt.svc:3000"
	repo := api + "/dotvirt/team-a.git"
	f := NewFactory(api, StaticToken("t"), false, "")
	if got := f.For(repo).PullURL(7); got != api+"/dotvirt/team-a/pulls/7" {
		t.Errorf("no web base: pull link %q, want the API base's", got)
	}

	f.WithWebURL("https://c.example/forge/ ")
	web := "https://c.example/forge/dotvirt/team-a"
	for name, got := range map[string]string{
		"pull":    f.For(repo).PullURL(7),
		"page":    f.PullPage(repo, 7),
		"compare": f.For(repo).CompareURL("dotvirt/proposed/u", "main"),
		"repo":    f.RepoPage(repo),
		"pathref": f.RepoPage("dotvirt/team-a.git"),
	} {
		want := map[string]string{
			"pull": web + "/pulls/7", "page": web + "/pulls/7",
			"compare": web + "/compare/main...dotvirt/proposed/u",
			"repo":    web, "pathref": web,
		}[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// A repo another forge hosts keeps its own address.
	if other := "https://elsewhere.example/o/r.git"; f.RepoPage(other) != other {
		t.Errorf("foreign repo page = %q", f.RepoPage(other))
	}
	var none *Factory
	if none.RepoPage(repo) != repo || none.PullPage(repo, 7) != "" || none.WithWebURL("https://x") != nil {
		t.Error("a nil factory links the repo as written and no pull request")
	}
}
