package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/git/gittest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// A fake forge reached at two addresses: the console's own (the forge
// credential's url, where dotvirt's account reads and writes) and the one
// users' tokens go to (the admin's spec.forge.url). Both serve the same bare
// repos over git's smart HTTP and the slice of the API a write uses, and
// every request is recorded with the credential it carried.

// logins is who each token acts as at the fake forge.
var logins = map[string]string{"bot-tok": "dotvirt-bot", "alice-tok": "alice-forge", "bob-tok": "bob-forge"}

type forgeCall struct {
	site, method, path, cred string
	refused                  bool // the forge answered it with an error
}

// write reports whether the call went to change the forge, a push or an API
// write, refused or not.
func (c forgeCall) write() bool {
	api := strings.HasPrefix(c.path, "/api/") && (c.method == http.MethodPost || c.method == http.MethodPatch)
	return api || strings.Contains(c.path, "git-receive-pack")
}

// recording notes the status a call was answered with.
type recording struct {
	http.ResponseWriter
	status int
}

func (r *recording) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

type fakePR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type fakeForgeState struct {
	t    *testing.T
	root string
	git  http.Handler

	mu    sync.Mutex
	calls []forgeCall
	prs   []*fakePR
	// noPush lists the tokens the forge reports without push on the repos;
	// refuse is the status the forge answers a token with, API and git alike.
	noPush map[string]bool
	refuse map[string]int
	// gitRefuse is the status git alone answers a token with; pushRefuse
	// the status its pushes alone get, as a repo the token may read;
	// prRefuse the status its pull request writes alone get.
	gitRefuse, pushRefuse, prRefuse map[string]int
}

var pullPath = regexp.MustCompile(`^/api/v1/repos/([^/]+)/([^/]+)(/pulls(?:/(\d+))?)?$`)

// site serves the fake forge under name, the address calls are recorded at.
func (f *fakeForgeState) site(name string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, cred := "", ""
		if u, p, ok := r.BasicAuth(); ok {
			token, cred = p, "basic "+u+":"+p
		} else if h := r.Header.Get("Authorization"); h != "" {
			token, cred = strings.TrimPrefix(h, "token "), h
		}
		rec := &recording{ResponseWriter: w}
		w = rec
		defer func() {
			f.mu.Lock()
			f.calls = append(f.calls, forgeCall{site: name, method: r.Method, path: r.URL.RequestURI(), cred: cred, refused: rec.status >= 400})
			f.mu.Unlock()
		}()
		f.mu.Lock()
		status, gitStatus := f.refuse[token], f.gitRefuse[token]
		if strings.Contains(r.URL.RequestURI(), "git-receive-pack") && f.pushRefuse[token] != 0 {
			gitStatus = f.pushRefuse[token]
		}
		if strings.Contains(r.URL.Path, "/pulls") && r.Method != http.MethodGet && f.prRefuse[token] != 0 {
			status = f.prRefuse[token]
		}
		f.mu.Unlock()
		if _, known := logins[token]; !known {
			status = http.StatusUnauthorized
		}
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if !api && gitStatus != 0 {
			status = gitStatus
		}
		if status != 0 {
			http.Error(w, "refused", status)
			return
		}
		if !api {
			f.git.ServeHTTP(w, r)
			return
		}
		f.api(w, r, logins[token], token)
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakeForgeState) api(w http.ResponseWriter, r *http.Request, login, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := pullPath.FindStringSubmatch(r.URL.Path)
	switch {
	case m == nil:
		// The forge's base/head pair lookup knows no PR: the list decides.
		http.NotFound(w, r)
	case m[3] == "" && r.Method == http.MethodGet:
		_, _ = fmt.Fprintf(w, `{"full_name":%q,"permissions":{"push":%v,"pull":true}}`, m[1]+"/"+m[2], !f.noPush[token])
	case m[4] == "" && r.Method == http.MethodGet:
		out := []*fakePR{}
		for _, pr := range f.prs {
			if pr.State == r.URL.Query().Get("state") {
				out = append(out, pr)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case m[4] == "" && r.Method == http.MethodPost:
		var req struct{ Title, Body, Head, Base string }
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		pr := &fakePR{Number: len(f.prs) + 1, State: "open", Title: req.Title, Body: req.Body}
		pr.Head.Ref, pr.Base.Ref, pr.User.Login = req.Head, req.Base, login
		f.prs = append(f.prs, pr)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pr)
	default:
		n, _ := strconv.Atoi(m[4])
		if n < 1 || n > len(f.prs) {
			http.NotFound(w, r)
			return
		}
		pr := f.prs[n-1]
		if r.Method == http.MethodPatch {
			var edit forge.PREdit
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &edit)
			if edit.State != "" {
				pr.State = edit.State
			}
			if edit.Title != "" {
				pr.Title, pr.Body = edit.Title, edit.Body
			}
		}
		_ = json.NewEncoder(w).Encode(pr)
	}
}

func (f *fakeForgeState) recorded() []forgeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgeCall(nil), f.calls...)
}

func (f *fakeForgeState) forget() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// fakeAccounts is the console sessions' forge accounts, by user.
type fakeAccounts struct {
	mu       sync.Mutex
	creds    map[string]auth.ForgeCredential
	err      error
	rejected []string
}

func (a *fakeAccounts) Credential(_ context.Context, id auth.Identity) (auth.ForgeCredential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return auth.ForgeCredential{}, a.err
	}
	c, ok := a.creds[id.Username]
	if !ok {
		return auth.ForgeCredential{}, auth.ErrForgeNotConnected
	}
	return c, nil
}

func (a *fakeAccounts) Rejected(id auth.Identity, token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rejected = append(a.rejected, id.Username+":"+token)
	if a.creds[id.Username].Token == token {
		delete(a.creds, id.Username)
	}
}

type delegatedFixture struct {
	c        *Coordinator
	repos    *git.RepoSet
	forge    *fakeForgeState
	accounts *fakeAccounts
	root     string
	bot      *httptest.Server // the forge credential's url
	user     *httptest.Server // where users' tokens go
	proj     project.ProjectInfo
	platform project.ProjectInfo
	alice    auth.Identity
}

// newDelegatedFixture serves bare as the project's repo and the platform
// repo, with alice's forge account connected.
func newDelegatedFixture(t *testing.T, bare string) *delegatedFixture {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"p", "platform"} {
		gitRun(t, root, "clone", "-q", "--bare", bare, filepath.Join("dotvirt", name+".git"))
	}
	h, err := gittest.Handler(root)
	if err != nil {
		t.Skip(err)
	}
	ff := &fakeForgeState{t: t, root: root, git: h, noPush: map[string]bool{}, refuse: map[string]int{}, gitRefuse: map[string]int{}, pushRefuse: map[string]int{}, prRefuse: map[string]int{}}
	f := &delegatedFixture{forge: ff, root: root, bot: ff.site("bot"), user: ff.site("user"), alice: auth.Identity{Username: "alice"}}
	f.accounts = &fakeAccounts{creds: map[string]auth.ForgeCredential{
		"alice": {Login: "alice-forge", Token: "alice-tok"},
		"bob":   {Login: "bob-forge", Token: "bob-tok"},
	}}

	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, f.bot.URL, "dotvirt-bot", forge.StaticToken("bot-tok"), true, nil, time.Hour)
	f.repos = repos
	f.c = New(store, repos, forge.NewFactory(f.bot.URL, forge.StaticToken("bot-tok"), false, ""), nil, nil, nil, "main", "dotvirt/proposed")
	f.c.UseAuthorizer(allowAll{})
	f.proj = project.ProjectInfo{Name: "p", Repo: f.bot.URL + "/dotvirt/p.git"}
	f.platform = project.ProjectInfo{Name: project.PlatformName, Repo: f.bot.URL + "/dotvirt/platform.git"}
	f.c.WithDelegation(Delegation{
		Accounts:     f.accounts,
		Forge:        forge.NewFactory(f.user.URL, forge.StaticToken(""), false, "").WithWebURL(f.bot.URL),
		PlatformRepo: f.platform.Repo,
	})
	return f
}

func (f *delegatedFixture) stage(t *testing.T, id auth.Identity, proj project.ProjectInfo) {
	t.Helper()
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"env": id.Username}}}
	if _, err := f.c.StageEdit(id, proj, "alpha", "web", req); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
}

// signatures is branch's head commit in repo as "author / committer".
func (f *delegatedFixture) signatures(t *testing.T, repo, branch string) string {
	t.Helper()
	return gitOut(t, f.root, "--git-dir", filepath.Join("dotvirt", repo+".git"), "log", "-1", "--format=%an <%ae> / %cn <%ce>", branch)
}

// mirrored waits until proj's mirror holds branch as the repo does.
func (f *delegatedFixture) mirrored(t *testing.T, proj project.ProjectInfo, repo, branch string) {
	t.Helper()
	want := gitOut(t, f.root, "--git-dir", filepath.Join("dotvirt", repo+".git"), "rev-parse", branch)
	read, err := f.c.read(proj)
	if err != nil {
		t.Fatal(err)
	}
	f.repos.Poke(proj.Repo)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if read.BranchHash(branch) == want {
			return
		}
	}
	t.Fatalf("the mirror never saw %s", branch)
}

// writes are the recorded calls that change the forge.
func (f *delegatedFixture) writes() []forgeCall {
	var out []forgeCall
	for _, c := range f.forge.recorded() {
		if c.write() {
			out = append(out, c)
		}
	}
	return out
}

// requireWritesAs fails unless every forge write went to site with cred, a
// push and a pull request among them, and no call carried token anywhere but
// at site.
func (f *delegatedFixture) requireWritesAs(t *testing.T, site, basic, token string) {
	t.Helper()
	writes := f.writes()
	pushed, opened := false, false
	for _, c := range writes {
		if c.site != site || c.cred != basic && c.cred != "token "+token {
			t.Errorf("write %s %s at %s carried %q, want %s's own at %s", c.method, c.path, c.site, c.cred, token, site)
		}
		pushed = pushed || strings.Contains(c.path, "git-receive-pack")
		opened = opened || c.method == http.MethodPost && strings.HasSuffix(c.path, "/pulls")
	}
	if !pushed || !opened {
		t.Errorf("writes %+v: want a push and a pull request", writes)
	}
	for _, c := range f.forge.recorded() {
		if c.site != site && strings.Contains(c.cred, token) {
			t.Errorf("%s's token went to %s: %s %s", token, c.site, c.method, c.path)
		}
	}
}

// requireNoWrites fails when a write landed; one the forge refused did not.
func (f *delegatedFixture) requireNoWrites(t *testing.T) {
	t.Helper()
	for _, c := range f.writes() {
		if !c.refused {
			t.Errorf("a refused write still wrote: %+v", c)
		}
	}
}

// A delegated user's propose pushes, and opens the pull request, with their
// own forge account at the address the admin named, and they author and
// commit it; dotvirt's account only reads.
func TestDelegatedProposeWritesAsTheUser(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	f.stage(t, f.alice, f.proj)
	out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if out.PRNumber != 1 || !strings.HasPrefix(out.PRURL, f.bot.URL) {
		t.Fatalf("result %+v: want PR #1 linked where dotvirt's own links point", out)
	}
	f.requireWritesAs(t, "user", "basic alice-forge:alice-tok", "alice-tok")
	if pr := f.forge.prs[0]; pr.User.Login != "alice-forge" || !strings.Contains(pr.Body, "by alice.") {
		t.Errorf("PR posted by %q with body %q, want alice's own", pr.User.Login, pr.Body)
	}
	want := "alice <alice@dotvirt.noreply> / alice <alice@dotvirt.noreply>"
	if got := f.signatures(t, "p", out.Branch); got != want {
		t.Errorf("commit %q, want %q", got, want)
	}

	// A second propose extends the open PR, editing it as alice too.
	f.forge.forget()
	f.stage(t, f.alice, f.proj)
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"tier": "web"}}}
	if _, err := f.c.StageEdit(f.alice, f.proj, "alpha", "web", req); err != nil {
		t.Fatal(err)
	}
	if out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{}); err != nil || !out.Existing {
		t.Fatalf("second Propose: %+v, %v; want PR #1 extended", out, err)
	}
	edited := false
	for _, c := range f.writes() {
		if c.site != "user" || c.cred != "basic alice-forge:alice-tok" && c.cred != "token alice-tok" {
			t.Errorf("extend wrote %s %s at %s as %q", c.method, c.path, c.site, c.cred)
		}
		edited = edited || c.method == http.MethodPatch
	}
	if !edited {
		t.Error("the extend did not retitle the PR")
	}
}

// Without a forge connection, or with a token the forge no longer takes, a
// delegated write is refused with the connect offer: nothing is written, by
// the user or by dotvirt's account, and the draft is kept.
func TestDelegatedProposeWithoutAConnectionIsRefused(t *testing.T) {
	cases := map[string]func(f *delegatedFixture){
		"not connected": func(f *delegatedFixture) { delete(f.accounts.creds, "alice") },
		"revoked":       func(f *delegatedFixture) { f.forge.refuse["alice-tok"] = http.StatusUnauthorized },
		"revoked at the push": func(f *delegatedFixture) {
			f.forge.pushRefuse["alice-tok"] = http.StatusUnauthorized
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDelegatedFixture(t, seedBare(t))
			f.stage(t, f.alice, f.proj)
			setup(f)
			_, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
			if !errors.Is(err, model.ErrForgeConnect) || !errors.Is(err, model.ErrForbidden) {
				t.Fatalf("Propose: %v, want the connect offer", err)
			}
			f.requireNoWrites(t)
			if entries, _ := f.c.store.List("alice", "p"); len(entries) != 1 {
				t.Errorf("draft holds %d entries, want it kept", len(entries))
			}
			if name != "not connected" {
				if len(f.accounts.rejected) != 1 || f.accounts.rejected[0] != "alice:alice-tok" {
					t.Errorf("rejected %v, want alice's refused token dropped", f.accounts.rejected)
				}
				if _, err := f.accounts.Credential(context.Background(), f.alice); !errors.Is(err, auth.ErrForgeNotConnected) {
					t.Error("the refused token is still served")
				}
			}
		})
	}
}

// A user whose forge account may not push to the repo is refused with what
// to ask for, naming the repo and the account; nothing is written.
func TestDelegatedProposeWithoutForgeWriteIsRefused(t *testing.T) {
	cases := map[string]func(f *delegatedFixture){
		"no push":               func(f *delegatedFixture) { f.forge.noPush["alice-tok"] = true },
		"hidden repo":           func(f *delegatedFixture) { f.forge.refuse["alice-tok"] = http.StatusNotFound },
		"refused at the API":    func(f *delegatedFixture) { f.forge.refuse["alice-tok"] = http.StatusForbidden },
		"refused at the push":   func(f *delegatedFixture) { f.forge.pushRefuse["alice-tok"] = http.StatusForbidden },
		"hidden from git alone": func(f *delegatedFixture) { f.forge.gitRefuse["alice-tok"] = http.StatusNotFound },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDelegatedFixture(t, seedBare(t))
			f.stage(t, f.alice, f.proj)
			setup(f)
			_, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
			if !errors.Is(err, model.ErrForgeWrite) || !errors.Is(err, model.ErrForbidden) {
				t.Fatalf("Propose: %v, want the write guidance", err)
			}
			if !strings.Contains(err.Error(), "dotvirt/p") || !strings.Contains(err.Error(), "alice-forge") {
				t.Errorf("refusal %q must name the repo and the account to grant", err)
			}
			f.requireNoWrites(t)
			if len(f.accounts.rejected) != 0 {
				t.Errorf("a refusal of write dropped the connection: %v", f.accounts.rejected)
			}
		})
	}
}

// The platform tier's pull request stays dotvirt's account's, whatever the
// user's connection, and names the user in branch, author and body.
func TestDelegatedPlatformProposeIsTheConsolesAttributed(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	delete(f.accounts.creds, "alice")
	f.stage(t, f.alice, f.platform)
	out, err := f.c.Propose(f.alice, f.platform, model.ProposeRequest{Title: "label web"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	f.requireWritesAs(t, "bot", "basic dotvirt-bot:bot-tok", "bot-tok")
	if !strings.HasPrefix(out.Branch, "dotvirt/proposed/alice/") {
		t.Errorf("branch %q must name alice", out.Branch)
	}
	if got, want := f.signatures(t, "platform", out.Branch), "alice <alice@dotvirt.noreply> / dotvirt <dotvirt@localhost>"; got != want {
		t.Errorf("commit %q, want %q", got, want)
	}
	if pr := f.forge.prs[0]; pr.User.Login != "dotvirt-bot" || !strings.Contains(pr.Body, "by alice.") {
		t.Errorf("PR posted by %q with body %q, want dotvirt's naming alice", pr.User.Login, pr.Body)
	}
	if f.c.author(f.alice, f.platform).Commits || !f.c.author(f.alice, f.proj).Commits {
		t.Error("alice commits her own changes, but not the platform tier's")
	}
}

// The platform tier is its name and its repo together: a project merely
// named like it, or a tenant project on its repo, is written as the user.
func TestDelegatedPlatformIsItsRepo(t *testing.T) {
	t.Run("named like it", func(t *testing.T) {
		f := newDelegatedFixture(t, seedBare(t))
		named := project.ProjectInfo{Name: project.PlatformName, Repo: f.proj.Repo}
		delete(f.accounts.creds, "alice")
		f.stage(t, f.alice, named)
		if _, err := f.c.Propose(f.alice, named, model.ProposeRequest{Title: "label web"}); !errors.Is(err, model.ErrForgeConnect) {
			t.Fatalf("Propose: %v, want the connect offer", err)
		}
		f.requireNoWrites(t)
	})
	t.Run("on its repo", func(t *testing.T) {
		f := newDelegatedFixture(t, seedBare(t))
		tenant := project.ProjectInfo{Name: "q", Repo: f.platform.Repo}
		f.stage(t, f.alice, tenant)
		if _, err := f.c.Propose(f.alice, tenant, model.ProposeRequest{Title: "label web"}); err != nil {
			t.Fatalf("Propose: %v", err)
		}
		f.requireWritesAs(t, "user", "basic alice-forge:alice-tok", "alice-tok")
	})
}

// A delegated user's own proposal reads as theirs though they committed it,
// and as no one else's.
func TestDelegatedOwnProposalIsTheUsers(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	f.stage(t, f.alice, f.proj)
	out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	f.mirrored(t, f.proj, "p", out.Branch)
	row := model.Proposal{Project: f.proj.Name, PRNumber: out.PRNumber, Branch: out.Branch}
	detail, err := f.c.OwnProposal(f.alice, f.proj, row)
	if err != nil || len(detail.Items) != 1 || detail.Items[0].Name != "web" {
		t.Fatalf("alice's own proposal: %+v, %v; want her edit of web", detail.Items, err)
	}
	if _, err := f.c.OwnProposal(auth.Identity{Username: "bob"}, f.proj, row); !errors.Is(err, git.ErrForeignCommit) {
		t.Fatalf("alice's proposal read by bob: %v, want it foreign", err)
	}
}

// A token the forge refuses once the branch is pushed, opening or retitling
// the PR, is still dropped from the session: the push stands, and the next
// save offers the connect.
func TestDelegatedTokenRefusedAtThePRIsDropped(t *testing.T) {
	t.Run("opening it", func(t *testing.T) {
		f := newDelegatedFixture(t, seedBare(t))
		f.stage(t, f.alice, f.proj)
		f.forge.prRefuse["alice-tok"] = http.StatusUnauthorized
		out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
		if err != nil || out.PRNumber != 0 || out.CompareURL == "" {
			t.Fatalf("Propose: %+v, %v; want the branch pushed and the compare link", out, err)
		}
		f.requireDropped(t)
	})
	t.Run("retitling it", func(t *testing.T) {
		f := newDelegatedFixture(t, seedBare(t))
		f.stage(t, f.alice, f.proj)
		if _, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"}); err != nil {
			t.Fatal(err)
		}
		req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"tier": "web"}}}
		if _, err := f.c.StageEdit(f.alice, f.proj, "alpha", "web", req); err != nil {
			t.Fatal(err)
		}
		f.forge.prRefuse["alice-tok"] = http.StatusUnauthorized
		if out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{}); err != nil || !out.Existing {
			t.Fatalf("Propose: %+v, %v; want PR #1 extended", out, err)
		}
		f.requireDropped(t)
	})
}

// requireDropped fails unless alice's token was dropped from her session,
// so her next save offers the connect.
func (f *delegatedFixture) requireDropped(t *testing.T) {
	t.Helper()
	if len(f.accounts.rejected) != 1 || f.accounts.rejected[0] != "alice:alice-tok" {
		t.Fatalf("rejected %v, want alice's refused token dropped", f.accounts.rejected)
	}
	f.stage(t, f.alice, f.proj)
	if _, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "again"}); !errors.Is(err, model.ErrForgeConnect) {
		t.Fatalf("the next Propose: %v, want the connect offer", err)
	}
}

// A revert is the user's write too: their branch, their pull request; and
// refused, writing nothing, without their connection.
func TestDelegatedRevertWritesAsTheUser(t *testing.T) {
	bare, _, hash := seedMerged(t)
	f := newDelegatedFixture(t, bare)
	out, err := f.c.Revert(f.alice, f.proj, hash, model.PruneAck{})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	f.requireWritesAs(t, "user", "basic alice-forge:alice-tok", "alice-tok")
	want := "alice <alice@dotvirt.noreply> / alice <alice@dotvirt.noreply>"
	if got := f.signatures(t, "p", out.Branch); got != want {
		t.Errorf("revert commit %q, want %q", got, want)
	}

	f.forge.forget()
	delete(f.accounts.creds, "alice")
	if _, err := f.c.Revert(f.alice, f.proj, hash, model.PruneAck{}); !errors.Is(err, model.ErrForgeConnect) {
		t.Fatalf("Revert without a connection: %v, want the connect offer", err)
	}
	f.forge.pushRefuse["bob-tok"] = http.StatusForbidden
	if _, err := f.c.Revert(auth.Identity{Username: "bob"}, f.proj, hash, model.PruneAck{}); !errors.Is(err, model.ErrForgeWrite) {
		t.Fatalf("Revert refused at the push: %v, want the write guidance", err)
	}
	f.requireNoWrites(t)
}

// Each user's write carries their own token alone.
func TestDelegatedUsersWriteApart(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	bob := auth.Identity{Username: "bob"}
	f.stage(t, f.alice, f.proj)
	f.stage(t, bob, f.proj)
	if _, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "alice"}); err != nil {
		t.Fatal(err)
	}
	f.requireWritesAs(t, "user", "basic alice-forge:alice-tok", "alice-tok")
	f.forge.forget()
	if _, err := f.c.Propose(bob, f.proj, model.ProposeRequest{Title: "bob"}); err != nil {
		t.Fatal(err)
	}
	f.requireWritesAs(t, "user", "basic bob-forge:bob-tok", "bob-tok")
	if f.forge.prs[1].User.Login != "bob-forge" {
		t.Errorf("bob's PR posted by %q", f.forge.prs[1].User.Login)
	}
}

// A repo neither address of the forge serves is refused before the user's
// token goes anywhere.
func TestDelegatedRepoOffTheForgeIsRefused(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	elsewhere := httptest.NewServer(gitOnly(t, f.root))
	defer elsewhere.Close()
	proj := project.ProjectInfo{Name: "q", Repo: elsewhere.URL + "/dotvirt/p.git"}
	f.stage(t, f.alice, proj)
	if _, err := f.c.Propose(f.alice, proj, model.ProposeRequest{Title: "x"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("Propose: %v, want a conflict naming the repo", err)
	}
	f.requireNoWrites(t)
	for _, c := range f.forge.recorded() {
		if strings.Contains(c.cred, "alice-tok") {
			t.Errorf("alice's token went to %s: %s %s", c.site, c.method, c.path)
		}
	}
}

func gitOnly(t *testing.T, root string) http.Handler {
	h, err := gittest.Handler(root)
	if err != nil {
		t.Skip(err)
	}
	return h
}

// Delegation that cannot connect anyone refuses every write it would carry;
// a renewal that cannot run is an outage. Neither writes as dotvirt.
func TestDelegatedWritesNeverFallBackToTheConsole(t *testing.T) {
	cases := map[string]struct {
		setup func(f *delegatedFixture)
		kind  error
	}{
		"delegation off": {func(f *delegatedFixture) {
			f.c.WithDelegation(Delegation{Off: "spec.forge.userAuth is incomplete", PlatformRepo: f.platform.Repo})
		}, model.ErrUnavailable},
		"no accounts wired": {func(f *delegatedFixture) {
			f.c.WithDelegation(Delegation{PlatformRepo: f.platform.Repo})
		}, model.ErrUnavailable},
		"renewal failing": {func(f *delegatedFixture) {
			f.accounts.err = errors.New("the forge token expired and its renewal is failing")
		}, model.ErrUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDelegatedFixture(t, seedBare(t))
			f.stage(t, f.alice, f.proj)
			c.setup(f)
			if _, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "x"}); !errors.Is(err, c.kind) {
				t.Fatalf("Propose: %v, want %v", err, c.kind)
			}
			f.requireNoWrites(t)
		})
	}
}

// The default mode is untouched: dotvirt's account writes every repo, and
// commits as itself.
func TestWithoutDelegationTheConsoleWrites(t *testing.T) {
	f := newDelegatedFixture(t, seedBare(t))
	f.c.delegation = nil
	f.stage(t, f.alice, f.proj)
	out, err := f.c.Propose(f.alice, f.proj, model.ProposeRequest{Title: "label web"})
	if err != nil {
		t.Fatal(err)
	}
	f.requireWritesAs(t, "bot", "basic dotvirt-bot:bot-tok", "bot-tok")
	if got, want := f.signatures(t, "p", out.Branch), "alice <alice@dotvirt.noreply> / dotvirt <dotvirt@localhost>"; got != want {
		t.Errorf("commit %q, want %q", got, want)
	}
}
