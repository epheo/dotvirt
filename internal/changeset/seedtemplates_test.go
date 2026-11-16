package changeset

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

const testSeedBranch = "dotvirt/proposed/_seed/starter-templates"

// seedPRs is a PR plane that keeps each PR's head and base as created, and can
// refuse creates.
type seedPRs struct {
	f *proposeFixture

	mu     sync.Mutex
	prs    []forge.PR
	refuse bool // creates answer 403 with a forge message
	lists  int
}

func newSeedFixture(t *testing.T) (*proposeFixture, *seedPRs) {
	t.Helper()
	p := &seedPRs{}
	f := newProposeFixture(t, p.route)
	p.f = f
	return f, p
}

func (p *seedPRs) route(m, path string) (int, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !strings.HasSuffix(path, "/pulls") {
		return 0, "", false
	}
	switch m {
	case "GET":
		p.lists++
		b, _ := json.Marshal(p.prs)
		return http.StatusOK, string(b), true
	case "POST":
		if p.refuse {
			return http.StatusForbidden, `{"message":"pull requests are closed here"}`, true
		}
		var in struct{ Title, Body, Head, Base string }
		_ = json.Unmarshal([]byte(p.f.lastBody()), &in)
		pr := forge.PR{Number: len(p.prs) + 1, State: "open", Title: in.Title, Body: in.Body}
		pr.Head.Ref, pr.Base.Ref = in.Head, in.Base
		p.prs = append(p.prs, pr)
		b, _ := json.Marshal(pr)
		return http.StatusCreated, string(b), true
	}
	return 0, "", false
}

func (p *seedPRs) all() []forge.PR {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]forge.PR(nil), p.prs...)
}

func (p *seedPRs) listed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lists
}

// openMirror opens f's project mirror, as a lane read has by the time the
// refresher offers the seed.
func openMirror(t *testing.T, f *proposeFixture) {
	t.Helper()
	if _, err := f.c.read(f.proj); err != nil {
		t.Fatal(err)
	}
}

// waitSeed waits for the offer to settle: decided, or failed.
func waitSeed(t *testing.T, c *Coordinator, repo string) {
	t.Helper()
	key := forge.NormalizeRepoURL(repo)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		c.seedMu.Lock()
		st := c.seeds[key]
		settled := st != nil && !st.inflight && (st.decided || st.err != "")
		c.seedMu.Unlock()
		if settled {
			return
		}
	}
	t.Fatal("the offer never settled")
}

func branchFiles(t *testing.T, repo, branch string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "ls-tree", "-r", "--name-only", branch).Output()
	if err != nil {
		t.Fatalf("ls-tree %s: %v", branch, err)
	}
	return strings.Fields(string(out))
}

// A repo dotvirt just created gets the starter library as a PR from the seed
// branch, never as a commit on main, and the commit names the console.
func TestNewRepoIsOfferedTheStarterLibraryAsAPR(t *testing.T) {
	f, prs := newSeedFixture(t)
	f.c.WithProductName("Uniwatt Cloud")
	mainBefore := revParse(t, f.proj.Repo, "main")
	f.c.seedCreated(f.proj.Repo)

	if got := revParse(t, f.proj.Repo, "main"); got != mainBefore {
		t.Fatal("the seed must never commit to main")
	}
	files := strings.Join(branchFiles(t, f.proj.Repo, testSeedBranch), " ")
	for _, want := range []string{"templates/fedora-server.yaml", "templates/fedora-minimal.yaml", SeedMarker} {
		if !strings.Contains(files, want) {
			t.Errorf("the seed branch lacks %s: %s", want, files)
		}
	}
	out, err := exec.Command("git", "-C", f.proj.Repo, "log", "-1", "--format=%s", testSeedBranch).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "Uniwatt Cloud: seed starter templates" {
		t.Errorf("seed commit = %q", got)
	}
	all := prs.all()
	if len(all) != 1 || all[0].Head.Ref != testSeedBranch || all[0].Base.Ref != "main" {
		t.Fatalf("want one PR from %s into main, got %+v", testSeedBranch, all)
	}
	if !strings.Contains(all[0].Title, "starter VM templates") || !strings.Contains(all[0].Body, "close this pull request") {
		t.Errorf("the PR must say what it is and that it may be declined: %q\n%s", all[0].Title, all[0].Body)
	}
	seed, ok := f.c.TemplateSeed(f.proj)
	if !ok || seed.PRNumber != 1 || seed.Library != "p" || seed.Error != "" {
		t.Errorf("the library must report the open offer, got %+v %v", seed, ok)
	}
}

// Users who open their own PRs still get the offer from dotvirt's account:
// nobody asked for it.
func TestTheSeedPRIsDotvirtsInUsersMode(t *testing.T) {
	f, prs := newSeedFixture(t)
	f.c.WithUsersOpeningPRs(true)
	f.c.seedCreated(f.proj.Repo)
	if n := len(prs.all()); n != 1 {
		t.Fatalf("want the seed PR opened in Users mode, got %d", n)
	}
}

// A repo an admin created (it has main, but no library) is offered the
// library once: the refresher's next lane read sees the open offer, and a new
// process finding the offer closed never repeats it.
func TestAPreCreatedRepoIsOfferedOnce(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	all := prs.all()
	if len(all) != 1 || all[0].Head.Ref != testSeedBranch {
		t.Fatalf("want one offer, got %+v", all)
	}

	lane := []model.Proposal{{Project: "p", PRNumber: 1, Branch: testSeedBranch, Seed: true}}
	f.c.SeedTemplates(f.proj, lane)
	if n := len(prs.all()); n != 1 {
		t.Fatalf("an open offer must not be repeated, got %d PRs", n)
	}

	// The customer closes it, and the console restarts.
	prs.mu.Lock()
	prs.prs[0].State = "closed"
	prs.mu.Unlock()
	f.c.seeds = nil
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	if n := len(prs.all()); n != 1 {
		t.Fatalf("a declined offer must never be repeated, got %d PRs", n)
	}
	if _, ok := f.c.TemplateSeed(f.proj); ok {
		t.Error("a declined offer has nothing to report")
	}
}

// A library with the marker, even emptied, or with any template of its own,
// is not offered anything, and costs no forge call.
func TestAMarkedOrStockedLibraryIsNotOffered(t *testing.T) {
	for _, tc := range []struct{ name, path, content string }{
		{"marker, examples deleted", SeedMarker, seedMarkerContent},
		{"its own template", "templates/mine.yaml", "apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: mine\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, prs := newSeedFixture(t)
			f.commitMain(t, tc.path, tc.content)
			f.c.SeedTemplates(f.proj, nil)
			f.c.seedMu.Lock()
			st := f.c.seeds[forge.NormalizeRepoURL(f.proj.Repo)]
			decided := st != nil && st.decided && !st.inflight
			f.c.seedMu.Unlock()
			if !decided {
				t.Fatal("the offer must be decided from the mirror alone")
			}
			if n, l := len(prs.all()), prs.listed(); n != 0 || l != 0 {
				t.Errorf("no forge call expected, got %d PRs and %d lists", n, l)
			}
		})
	}
}

// A refused PR is reported with the forge's words, and the next attempt,
// after the retry delay, opens it from the branch already pushed.
func TestARefusedOfferIsReportedThenResumed(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	prs.refuse = true
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	seed, ok := f.c.TemplateSeed(f.proj)
	if !ok || seed.Error == "" || seed.PRNumber != 0 {
		t.Fatalf("the refusal must be reported, got %+v %v", seed, ok)
	}
	pushed := revParse(t, f.proj.Repo, testSeedBranch)

	f.c.SeedTemplates(f.proj, nil)
	if n := len(prs.all()); n != 0 {
		t.Fatal("no new attempt inside the retry delay")
	}
	prs.mu.Lock()
	prs.refuse = false
	prs.mu.Unlock()
	f.c.seedMu.Lock()
	f.c.seeds[forge.NormalizeRepoURL(f.proj.Repo)].retryAt = time.Time{}
	f.c.seedMu.Unlock()
	f.repos.Poke(f.proj.Repo)
	read, _ := f.c.read(f.proj)
	for deadline := time.Now().Add(10 * time.Second); read.BranchHash(testSeedBranch) == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	all := prs.all()
	if len(all) != 1 || all[0].Head.Ref != testSeedBranch {
		t.Fatalf("want the offer opened on retry, got %+v", all)
	}
	if got := revParse(t, f.proj.Repo, testSeedBranch); got != pushed {
		t.Error("the branch already pushed must be reused, not rebuilt")
	}
	if seed, _ := f.c.TemplateSeed(f.proj); seed.Error != "" || seed.PRNumber != 1 {
		t.Errorf("the retry must clear the error, got %+v", seed)
	}
}

// The seed's lane row is flagged and stays dotvirt's: its poster, never a
// user read from the branch.
func TestTheSeedRowBelongsToNoUser(t *testing.T) {
	f, _ := newSeedFixture(t)
	pr := forge.PR{Number: 3}
	pr.Head.Ref = testSeedBranch
	pr.User.Login = "dotvirt-bot"
	row := f.c.proposalRow(f.proj, pr, nil)
	if !row.Seed || row.Revert || row.By != "dotvirt-bot" {
		t.Errorf("seed row = %+v", row)
	}
	if f.c.OwnsProposal(auth.Identity{Username: "_seed"}, f.proj, testSeedBranch) {
		t.Error("no user owns the seed branch")
	}
	if got := tasks.MergeAuthor(testSeedBranch, "dotvirt/proposed", "dotvirt-bot"); got != "dotvirt-bot" {
		t.Errorf("a merged seed is attributed to %q", got)
	}
}

// No username lands in a reserved first segment: neither the seed's '_'
// prefix nor revert, which review services read as "a revert by the next
// segment".
func TestUsernamesNeverTakeAReservedSegment(t *testing.T) {
	r := &Reader{proposed: "dotvirt/proposed"}
	for _, user := range []string{"_seed", "seed", "revert", "_revert", "__", "_.x", "-revert", "revert."} {
		branch := r.proposedBranch(user, "p")
		first, _, _ := strings.Cut(strings.TrimPrefix(branch, "dotvirt/proposed/"), "/")
		if strings.HasPrefix(first, "_") || first == tasks.RevertSegment || first == "" || strings.HasPrefix(first, ".") || strings.HasPrefix(first, "-") {
			t.Errorf("user %q proposes on %s: reserved or invalid first segment %q", user, branch, first)
		}
		pr := forge.PR{}
		pr.Head.Ref = branch
		row := r.proposalRow(project.ProjectInfo{Name: "p"}, pr, nil)
		if row.Revert || row.Seed {
			t.Errorf("user %q's proposal reads as revert=%v seed=%v", user, row.Revert, row.Seed)
		}
	}
	if got := userSegment("alice"); got != "alice" {
		t.Errorf("an ordinary name must keep its form, got %q", got)
	}
}
