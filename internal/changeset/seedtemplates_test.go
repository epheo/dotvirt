package changeset

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
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

// seedPRs is a PR plane that keeps each PR's head and base as created, can
// refuse creates or closes, and can answer a create with another head than
// was pushed.
type seedPRs struct {
	f *proposeFixture

	mu          sync.Mutex
	prs         []forge.PR
	refuse      bool   // creates answer 403 with a forge message
	refuseClose bool   // closes answer 500
	movedTo     string // creates report this head commit, as if the branch moved
	lists       int
	creates     int
}

func newSeedFixture(t *testing.T) (*proposeFixture, *seedPRs) {
	t.Helper()
	p := &seedPRs{}
	f := newProposeFixture(t, p.route)
	f.c.seedPause = 0
	p.f = f
	return f, p
}

func (p *seedPRs) route(m, path string) (int, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i := strings.LastIndex(path, "/pulls/"); i >= 0 && m == "PATCH" {
		n, err := strconv.Atoi(path[i+len("/pulls/"):])
		if err != nil || n < 1 || n > len(p.prs) {
			return 0, "", false
		}
		var edit forge.PREdit
		_ = json.Unmarshal([]byte(p.f.lastBody()), &edit)
		if edit.State == "closed" && p.refuseClose {
			return http.StatusInternalServerError, `{"message":"try again"}`, true
		}
		if edit.State != "" {
			p.prs[n-1].State = edit.State
		}
		b, _ := json.Marshal(p.prs[n-1])
		return http.StatusOK, string(b), true
	}
	if !strings.HasSuffix(path, "/pulls") {
		return 0, "", false
	}
	switch m {
	case "GET":
		p.lists++
		b, _ := json.Marshal(p.prs)
		return http.StatusOK, string(b), true
	case "POST":
		p.creates++
		if p.refuse {
			return http.StatusForbidden, `{"message":"pull requests are closed at https://internal.example"}`, true
		}
		var in struct{ Title, Body, Head, Base string }
		_ = json.Unmarshal([]byte(p.f.lastBody()), &in)
		pr := forge.PR{Number: len(p.prs) + 1, State: "open", Title: in.Title, Body: in.Body}
		pr.Head.Ref, pr.Base.Ref, pr.Head.Sha = in.Head, in.Base, p.movedTo
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

func (p *seedPRs) counts() (lists, creates int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lists, p.creates
}

// openMirror opens f's project mirror at the repo root, as a lane read has by
// the time the refresher offers the seed.
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
		settled := st != nil && !st.queued && (st.decided || st.err != "")
		c.seedMu.Unlock()
		if settled {
			return
		}
	}
	t.Fatal("the offer never settled")
}

// waitMirror waits for f's mirror to hold branch at want ("" for gone).
func waitMirror(t *testing.T, f *proposeFixture, branch, want string) {
	t.Helper()
	f.repos.Poke(f.proj.Repo)
	read, err := f.c.read(f.proj)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if read.BranchHash(branch) == want {
			return
		}
	}
	t.Fatalf("the mirror never saw %s at %q", branch, want)
}

// offerFiles is what the seed branch adds on top of main.
func offerFiles(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "diff", "--name-only", "main", testSeedBranch).Output()
	if err != nil {
		t.Fatalf("diff main..%s: %v", testSeedBranch, err)
	}
	return strings.Fields(string(out))
}

func wantOfferFiles(t *testing.T, repo string) {
	t.Helper()
	got := strings.Join(offerFiles(t, repo), " ")
	if want := "templates/.dotvirt-seeded templates/fedora-minimal.yaml templates/fedora-server.yaml"; got != want {
		t.Errorf("the seed branch must add exactly the starter files and the marker:\n got %s\nwant %s", got, want)
	}
}

// resetRetry makes the repo's next refresh attempt the offer again.
func resetRetry(f *proposeFixture) {
	f.c.seedMu.Lock()
	f.c.seeds[forge.NormalizeRepoURL(f.proj.Repo)].retryAt = time.Time{}
	f.c.seedMu.Unlock()
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
	wantOfferFiles(t, f.proj.Repo)
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

// Declined and its branch deleted with the forge's "Delete branch": the
// forge then reports the PR's own refs/pull/<n>/head, keeping the branch name
// in the label. Still an offer made, after a restart too.
func TestADeclinedOfferWithItsBranchDeletedStaysDeclined(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	prs.mu.Lock()
	prs.prs[0].State = "closed"
	prs.prs[0].Head.Label = testSeedBranch
	prs.prs[0].Head.Ref = "refs/pull/1/head"
	prs.mu.Unlock()
	gitRun(t, f.proj.Repo, "branch", "-D", testSeedBranch)
	waitMirror(t, f, testSeedBranch, "")

	f.c.seeds = nil
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	if _, creates := prs.counts(); creates != 1 {
		t.Errorf("a declined offer was made again after its branch was deleted: %d creates", creates)
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
			decided := st != nil && st.decided && !st.queued
			f.c.seedMu.Unlock()
			if !decided {
				t.Fatal("the offer must be decided from the mirror alone")
			}
			if lists, creates := prs.counts(); lists != 0 || creates != 0 {
				t.Errorf("no forge call expected, got %d lists and %d creates", lists, creates)
			}
		})
	}
}

// A project living in a folder of its repo is never offered: the library the
// Templates tab lists is the repo root's, which no folder project owns.
func TestAFolderProjectIsNotOffered(t *testing.T) {
	f, prs := newSeedFixture(t)
	f.commitMain(t, "sub/vm.yaml", "kind: ConfigMap\n")
	proj := f.proj
	proj.Path = "sub"
	if _, err := f.c.read(proj); err != nil {
		t.Fatal(err)
	}
	f.c.SeedTemplates(proj, nil)
	time.Sleep(50 * time.Millisecond)
	if lists, creates := prs.counts(); lists != 0 || creates != 0 {
		t.Errorf("a folder project must not be offered: %d lists, %d creates", lists, creates)
	}
	if _, ok := f.c.TemplateSeed(proj); ok {
		t.Error("a folder project has no offer to report")
	}
}

// Whatever was pushed to the seed branch before the offer is dropped: the
// branch is rebuilt from main with exactly the starter files and the marker.
func TestTheOfferRebuildsItsBranchFromMain(t *testing.T) {
	f, prs := newSeedFixture(t)
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	gitRun(t, work, "checkout", "-q", "-b", testSeedBranch)
	writeWorkFile(t, work, "alpha/evil.yaml", []byte("kind: ClusterRoleBinding\n"))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "pre-filled")
	gitRun(t, work, "push", "-q", "origin", testSeedBranch)
	openMirror(t, f)

	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	wantOfferFiles(t, f.proj.Repo)
	if n := len(prs.all()); n != 1 {
		t.Fatalf("want the offer opened on the rebuilt branch, got %d PRs", n)
	}
}

// A branch moved between dotvirt's push and its PR: the PR is closed, never
// left open as dotvirt's offer, and the failure says so.
func TestAnOfferWhoseBranchMovedIsClosed(t *testing.T) {
	f, prs := newSeedFixture(t)
	prs.movedTo = "0123456789abcdef0123456789abcdef01234567"
	f.c.seedCreated(f.proj.Repo)
	all := prs.all()
	if len(all) != 1 || all[0].State != "closed" {
		t.Fatalf("want the moved offer closed, got %+v", all)
	}
	seed, _ := f.c.TemplateSeed(f.proj)
	if !strings.Contains(seed.Error, "withdrew the offer") || seed.Retries {
		t.Errorf("a withdrawn offer must say so and promise no retry, got %+v", seed)
	}
	// Final: no attempt follows, whatever the retry delay.
	openMirror(t, f)
	resetRetry(f)
	f.c.SeedTemplates(f.proj, nil)
	time.Sleep(50 * time.Millisecond)
	if _, creates := prs.counts(); creates != 1 {
		t.Errorf("a withdrawn offer was made again: %d creates", creates)
	}
}

// seedLane is a lane listing the open offer as PR #1.
var seedLane = []model.Proposal{{Project: "p", PRNumber: 1, Branch: testSeedBranch, Seed: true}}

// pushOnSeed commits edit onto the seed branch from outside the console and
// waits for the mirror to fetch it.
func pushOnSeed(t *testing.T, f *proposeFixture, edit func(work string)) {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", "-b", testSeedBranch, f.proj.Repo, ".")
	edit(work)
	gitRun(t, work, "push", "-q", "-f", "origin", testSeedBranch)
	waitMirror(t, f, testSeedBranch, revParse(t, f.proj.Repo, testSeedBranch))
}

func seedDecided(f *proposeFixture) bool {
	f.c.seedMu.Lock()
	defer f.c.seedMu.Unlock()
	st := f.c.seeds[forge.NormalizeRepoURL(f.proj.Repo)]
	return st != nil && st.decided && !st.queued
}

// A moved offer whose close failed stays open: the lane lists it, and the
// Templates tab still says it was withdrawn, whatever its branch holds.
func TestAMovedOfferLeftOpenIsNotShown(t *testing.T) {
	f, prs := newSeedFixture(t)
	prs.movedTo, prs.refuseClose = "0123456789abcdef0123456789abcdef01234567", true
	f.c.seedCreated(f.proj.Repo)
	if all := prs.all(); len(all) != 1 || all[0].State != "open" {
		t.Fatalf("want the offer left open by the failed close, got %+v", all)
	}
	waitMirror(t, f, testSeedBranch, revParse(t, f.proj.Repo, testSeedBranch))
	f.c.SeedTemplates(f.proj, seedLane)
	seed, _ := f.c.TemplateSeed(f.proj)
	if seed.PRNumber != 0 || !strings.Contains(seed.Error, "withdrew the offer") {
		t.Errorf("want the withdrawal, not the PR, got %+v", seed)
	}
}

// An open offer holding exactly the starter set is shown by a process that
// did not push it: after a restart, and whoever committed it.
func TestAnExactStarterHeadIsShownAfterARestart(t *testing.T) {
	f, prs := newSeedFixture(t)
	f.c.seedCreated(f.proj.Repo)
	pushOnSeed(t, f, func(work string) {
		gitRun(t, work, "commit", "-q", "--amend", "--no-edit", "--reset-author")
	})
	f.c.seeds = nil
	f.c.SeedTemplates(f.proj, seedLane)
	if seed, ok := f.c.TemplateSeed(f.proj); !ok || seed.PRNumber != 1 || seed.Error != "" {
		t.Errorf("want the open offer shown, got %+v %v", seed, ok)
	}
	if _, creates := prs.counts(); creates != 1 || !seedDecided(f) {
		t.Errorf("an open offer must leave the repo decided: %d creates", creates)
	}
}

// An offer whose branch holds anything but exactly the starter set is not
// shown, and is not made again.
func TestAnOfferNotExactlyTheStarterSetIsNotShown(t *testing.T) {
	for name, edit := range map[string]func(t *testing.T, work string){
		"extra file": func(t *testing.T, work string) {
			writeWorkFile(t, work, "alpha/evil.yaml", []byte("kind: ClusterRoleBinding\n"))
			gitRun(t, work, "add", "-A")
		},
		"changed file": func(t *testing.T, work string) {
			writeWorkFile(t, work, "templates/fedora-minimal.yaml", []byte("kind: VirtualMachineTemplate\n"))
			gitRun(t, work, "add", "-A")
		},
		"missing file": func(t *testing.T, work string) {
			gitRun(t, work, "rm", "-q", "templates/fedora-server.yaml")
		},
		"executable file": func(t *testing.T, work string) {
			gitRun(t, work, "update-index", "--chmod=+x", "templates/fedora-minimal.yaml")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, prs := newSeedFixture(t)
			f.c.seedCreated(f.proj.Repo)
			waitMirror(t, f, testSeedBranch, revParse(t, f.proj.Repo, testSeedBranch))
			f.c.SeedTemplates(f.proj, seedLane)
			if seed, _ := f.c.TemplateSeed(f.proj); seed.PRNumber != 1 {
				t.Fatalf("want the offer shown before its branch moves, got %+v", seed)
			}
			pushOnSeed(t, f, func(work string) {
				edit(t, work)
				gitRun(t, work, "commit", "-qm", name)
			})
			f.c.SeedTemplates(f.proj, seedLane)
			if seed, ok := f.c.TemplateSeed(f.proj); ok {
				t.Errorf("an offer with %s was shown: %+v", name, seed)
			}
			if _, creates := prs.counts(); creates != 1 || !seedDecided(f) {
				t.Errorf("the repo must stay decided: %d creates", creates)
			}
		})
	}
}

// A write that fails without a classified cause (here the remote refuses
// every pack, so the push fails while reads still work) shows one fixed line,
// never the raw error, and is retried.
func TestAFailedWriteShowsTheFixedLine(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	// A server-side limit, not file modes: root ignores those.
	gitRun(t, f.proj.Repo, "config", "receive.maxInputSize", "1")
	f.c.seedCreated(f.proj.Repo)
	seed, ok := f.c.TemplateSeed(f.proj)
	if !ok || seed.Error != "the starter templates could not be written to the repository" || !seed.Retries {
		t.Errorf("want the fixed line and a retry, got %+v %v", seed, ok)
	}
	if _, creates := prs.counts(); creates != 0 {
		t.Errorf("no PR may be opened for an offer that was not pushed: %d creates", creates)
	}
}

// A refused PR is reported by its class only, never the forge's words; no
// attempt is made inside the retry delay; after it, the offer is made.
func TestARefusedOfferIsReportedThenRetried(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	prs.refuse = true
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	seed, ok := f.c.TemplateSeed(f.proj)
	if !ok || seed.Error != "the forge refused the console's account (403)" || seed.PRNumber != 0 {
		t.Fatalf("the refusal must be reported by its class, got %+v %v", seed, ok)
	}

	prs.mu.Lock()
	prs.refuse = false
	prs.mu.Unlock()
	f.c.SeedTemplates(f.proj, nil)
	time.Sleep(50 * time.Millisecond)
	if _, creates := prs.counts(); creates != 1 {
		t.Fatalf("an attempt was made inside the retry delay: %d creates", creates)
	}

	resetRetry(f)
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	all := prs.all()
	if len(all) != 1 || all[0].Head.Ref != testSeedBranch {
		t.Fatalf("want the offer made on retry, got %+v", all)
	}
	wantOfferFiles(t, f.proj.Repo)
	if seed, _ := f.c.TemplateSeed(f.proj); seed.Error != "" || seed.PRNumber != 1 {
		t.Errorf("the retry must clear the error, got %+v", seed)
	}
}

// A failure that is not classified shows one fixed line: a raw git error may
// carry the repo's URL, its credentials or internal hosts.
func TestAnUnclassifiedFailureShowsNoDetail(t *testing.T) {
	f, _ := newSeedFixture(t)
	gone := f.proj.Repo + "-gone.git"
	f.c.seedCreated(gone)
	seed, ok := f.c.TemplateSeed(project.ProjectInfo{Name: "p", Repo: gone})
	if !ok || seed.Error != "the repository could not be read" || strings.Contains(seed.Error, gone) {
		t.Errorf("want the fixed line without the repo's address, got %+v %v", seed, ok)
	}
}

// A failed offer whose repo then gets a library of its own stops reporting.
func TestAStockedLibraryClearsAFailedOffer(t *testing.T) {
	f, prs := newSeedFixture(t)
	openMirror(t, f)
	prs.refuse = true
	f.c.SeedTemplates(f.proj, nil)
	waitSeed(t, f.c, f.proj.Repo)
	f.commitMain(t, "templates/mine.yaml", "apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: mine\n")
	f.c.SeedTemplates(f.proj, nil)
	if seed, ok := f.c.TemplateSeed(f.proj); ok {
		t.Errorf("a stocked library still reports a failed offer: %+v", seed)
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
	// The lane and the feed read an escaped first segment back; a revert's user
	// segment is never escaped, so it reads as it is.
	for head, want := range map[string]string{
		"dotvirt/proposed/u-_alice/p-0123456789":        "_alice",
		"dotvirt/proposed/u-u-bob/p-0123456789":         "u-bob",
		"dotvirt/proposed/u-revert/p-0123456789":        "revert",
		"dotvirt/proposed/revert/u-bob/p-0123456789-ab": "u-bob",
	} {
		if got := tasks.MergeAuthor(head, "dotvirt/proposed", "dotvirt-bot"); got != want {
			t.Errorf("MergeAuthor(%s) = %q, want %q", head, got, want)
		}
	}
}

// No username lands in a reserved first segment: neither the seed's '_'
// prefix nor revert, which review services read as "a revert by the next
// segment", matched exactly and case-sensitively.
func TestUsernamesNeverTakeAReservedSegment(t *testing.T) {
	r := &Reader{proposed: "dotvirt/proposed"}
	for _, user := range []string{"_seed", "seed", "revert", "_revert", "__", "_.x", "-revert", "revert.", "Revert", "u-revert"} {
		branch := r.proposedBranch(user, "p")
		first, _, _ := strings.Cut(strings.TrimPrefix(branch, "dotvirt/proposed/"), "/")
		if strings.HasPrefix(first, "_") || first == tasks.RevertSegment || first == "" {
			t.Errorf("user %q proposes on %s: reserved or empty first segment %q", user, branch, first)
		}
		pr := forge.PR{}
		pr.Head.Ref = branch
		row := r.proposalRow(project.ProjectInfo{Name: "p"}, pr, nil)
		if row.Revert || row.Seed {
			t.Errorf("user %q's proposal reads as revert=%v seed=%v", user, row.Revert, row.Seed)
		}
	}
}

// The escape is one-to-one on top of refSegment: each name below keeps its
// own segment, and stripping one leading "u-" from an escaped segment gives
// back the refSegment form.
func TestTheUserEscapeIsOneToOne(t *testing.T) {
	want := map[string]string{
		"bob":      "bob",
		"u-bob":    "u-u-bob",
		"u-u-bob":  "u-u-u-bob",
		"_bob":     "u-_bob",
		"u-_bob":   "u-u-_bob",
		"revert":   "u-revert",
		"u-revert": "u-u-revert",
		"Revert":   "Revert",
	}
	seen := map[string]string{}
	for user, seg := range want {
		got := userSegment(user)
		if got != seg {
			t.Errorf("userSegment(%q) = %q, want %q", user, got, seg)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%q and %q share the segment %q", user, other, got)
		}
		seen[got] = user
		if decoded := strings.TrimPrefix(got, tasks.UserEscape); got != refSegment(user) && decoded != refSegment(user) {
			t.Errorf("%q does not decode to %q", got, refSegment(user))
		}
	}
}
