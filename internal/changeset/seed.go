package changeset

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/internal/vmtemplate"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The starter template library is offered once to every project repo with no
// library of its own, as a pull request dotvirt's own account opens in every
// pullRequests mode: no user asked for it, and forges that refuse direct
// pushes to the base branch still take a PR.

const (
	seedBranchName = "starter-templates"
	// SeedMarker records in the repo that the starter library was offered:
	// the seed PR adds it beside the examples, so a library whose examples
	// were deleted is not offered again. Under templates/, which ArgoCD never
	// applies, and not YAML, so no library reader lists it.
	SeedMarker = model.TemplatesDir + "/.dotvirt-seeded"
	// seedRetry spaces the attempts at a repo whose last offer failed: a
	// refused push or PR is retried, not hammered on every lane refresh.
	seedRetry = time.Hour
	// seedSpacing paces the one worker that runs offers, so the first
	// refresh after an upgrade, which may find every repo a candidate, makes
	// one offer at a time.
	seedSpacing = 2 * time.Second
	// seedQueueSize bounds the offers waiting; a repo that finds the queue
	// full is queued again by a later refresh.
	seedQueueSize = 256
)

const seedMarkerContent = "The starter VM template library was offered to this repository.\n" +
	"Its examples may be deleted; this file keeps them from being offered again.\n"

// errSeedMoved is an offer whose branch was pushed to by someone else between
// dotvirt's push and its pull request: closed rather than shown as dotvirt's.
// Final, like any closed offer: the next attempt would find it and count it
// declined, so "once per repo" holds and nothing promises a retry.
var errSeedMoved = errors.New("someone else changed the offer's branch while its pull request was opened, so the console withdrew the offer; it is not made again")

// seedBranch is the one branch the starter library is offered from:
// <proposed>/_seed/starter-templates, never a user's (userSegment).
func (r *Reader) seedBranch() string {
	return r.proposed + "/" + tasks.SeedSegment + "/" + seedBranchName
}

// seedState is what this process knows of one repo's offer.
type seedState struct {
	decided bool      // offered, declined or not needed: nothing left to do
	queued  bool      // an offer waits for, or runs on, the worker
	retryAt time.Time // after a failure, the next attempt's earliest time
	pr      int       // the open offer, from the lane
	prURL   string
	err     string // the last failure, as a user may read it
	// judged is the base and seed-branch commits exact was read at.
	judged string
	exact  bool
}

func (c *Coordinator) seedFor(key string) *seedState {
	if c.seeds == nil {
		c.seeds = map[string]*seedState{}
	}
	st := c.seeds[key]
	if st == nil {
		st = &seedState{}
		c.seeds[key] = st
	}
	return st
}

// SeedTemplates offers the starter library to proj's repo when it has none.
// lane is the repo's open PRs as the proposals refresher just read them, so
// an open offer costs nothing more; the rest is decided from the repo-root
// mirror dotvirt already holds. Only a candidate - no marker, no template on
// the base branch, no open offer - is queued for the worker, which asks the
// forge whether an offer ever existed before making one.
//
// A project living in a folder of its repo (dotvirt.io/path) is never
// offered: the library the Templates tab lists is the repo root's, which no
// single folder project owns, and reading the root would evict the
// project's folder-scoped mirror.
func (c *Coordinator) SeedTemplates(proj project.ProjectInfo, lane []model.Proposal) {
	if proj.Repo == "" || proj.Path != "" {
		return
	}
	key := forge.NormalizeRepoURL(proj.Repo)
	var open *model.Proposal
	for i := range lane {
		if lane[i].Seed {
			open = &lane[i]
			break
		}
	}
	read, ok := c.repos.Peek(proj.Repo)
	if ok && read.Root() != "" {
		read, ok = nil, false // a later refresh decides, once the repo-root mirror is open
	}
	var exact, known bool
	if open != nil {
		exact, known = c.seedExact(key, read, open.ForgeHead)
	}
	c.seedMu.Lock()
	st := c.seedFor(key)
	switch {
	case open == nil:
		st.pr, st.prURL = 0, "" // merged or closed since
	case st.decided && st.err != "":
		// Withdrawn: its pull request, left open by a failed close, is never shown.
	default:
		st.err, st.decided = "", true
		// Shown only while it holds exactly the starter set, whoever pushed
		// it; while the mirror cannot tell, what this process knows stands.
		if known {
			st.pr, st.prURL = 0, ""
			if exact {
				st.pr, st.prURL = open.PRNumber, open.PRURL
			}
		}
	}
	skip := st.queued || st.decided && st.err == ""
	c.seedMu.Unlock()
	if skip || !ok {
		return
	}
	need, err := c.libraryMissing(read)
	if err != nil {
		return // an unreadable base branch is decided later
	}
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	switch {
	case st.queued:
	case !need:
		st.decided, st.err, st.retryAt = true, "", time.Time{}
	case st.decided: // a withdrawn offer, still reported
	case time.Now().Before(st.retryAt):
	default:
		st.queued = true
		c.enqueueSeedLocked(proj.Repo, key, st)
	}
}

// libraryMissing reports whether the base branch, as the mirror holds it,
// has neither the marker nor a single template.
func (c *Coordinator) libraryMissing(read *git.Repo) (bool, error) {
	if _, marked, err := read.LookupOnBranch(c.baseBranch, SeedMarker); err != nil || marked {
		return false, err
	}
	files, err := read.TemplatesOnBranch(c.baseBranch)
	if err != nil {
		return false, err
	}
	return len(files) == 0, nil
}

// seedExact reports whether the seed branch, as the mirror read holds it, is
// exactly the starter set committed on the base branch
// (git.Repo.ChangesExactly). known is false while read cannot tell. A forge
// head the mirror has not fetched yet is not exact. The verdict is kept per
// base and head commit, and read outside seedMu: it walks history.
func (c *Coordinator) seedExact(key string, read *git.Repo, forgeHead string) (exact, known bool) {
	if read == nil {
		return false, false
	}
	base, head := read.BranchHash(c.baseBranch), read.BranchHash(c.seedBranch())
	if base == "" || head == "" {
		return false, false
	}
	if forgeHead != "" && forgeHead != head {
		return false, true
	}
	at := base + ".." + head
	c.seedMu.Lock()
	st := c.seedFor(key)
	judged, cached := st.judged, st.exact
	c.seedMu.Unlock()
	if at == judged {
		return cached, true
	}
	exact, err := read.ChangesExactly(base, head, starterSet())
	if errors.Is(err, git.ErrNoCommit) {
		return false, false // the mirror moved between the reads; the next refresh judges
	}
	if err != nil {
		// The same commits fail the same way: kept as not exact.
		log.Printf("changeset: starter templates for %s at %s: %v", key, at, err)
		exact = false
	}
	c.seedMu.Lock()
	st = c.seedFor(key)
	st.judged, st.exact = at, exact
	c.seedMu.Unlock()
	return exact, true
}

// starterSet is every file the offer adds: the starter templates and the
// marker.
func starterSet() []model.File {
	return append(vmtemplate.SeedFiles(), model.File{Path: SeedMarker, Content: []byte(seedMarkerContent)})
}

type seedJob struct{ repo, key string }

// enqueueSeedLocked hands an offer to the single worker, started on first
// use. A full queue leaves the repo for a later refresh. Holds seedMu.
func (c *Coordinator) enqueueSeedLocked(repo, key string, st *seedState) {
	if c.seedQueue == nil {
		c.seedQueue = make(chan seedJob, seedQueueSize)
		go c.seedWorker(c.seedQueue)
	}
	select {
	case c.seedQueue <- seedJob{repo, key}:
	default:
		st.queued = false
	}
}

// seedWorker runs the queued offers one at a time, paced by seedPause.
func (c *Coordinator) seedWorker(jobs <-chan seedJob) {
	for job := range jobs {
		c.offerSeed(job.repo, job.key, true)
		time.Sleep(c.seedPause)
	}
}

// seedCreated offers the starter library to a repo dotvirt just created,
// synchronously: no offer can exist there yet, so the forge is not asked.
// Its failure never fails the project's creation; it is reported with the
// library instead.
func (c *Coordinator) seedCreated(repoURL string) {
	key := forge.NormalizeRepoURL(repoURL)
	c.seedMu.Lock()
	st := c.seedFor(key)
	if st.decided || st.queued {
		c.seedMu.Unlock()
		return
	}
	st.queued = true
	c.seedMu.Unlock()
	c.offerSeed(repoURL, key, false)
}

// offerSeed runs one offer and records its outcome. ask makes it look for an
// earlier offer first.
func (c *Coordinator) offerSeed(repoURL, key string, ask bool) {
	pr, err := c.proposeSeed(repoURL, ask)
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	st := c.seedFor(key)
	st.queued = false
	if err != nil {
		log.Printf("changeset: starter templates for %s: %v", repoURL, err)
		st.err = seedFailure(err)
		if errors.Is(err, errSeedMoved) {
			st.decided = true
		} else {
			st.retryAt = time.Now().Add(seedRetry)
		}
		return
	}
	st.decided, st.err = true, ""
	if pr.Number != 0 {
		st.pr, st.prURL = pr.Number, c.forge.PullPage(repoURL, pr.Number)
	}
}

// proposeSeed rebuilds the seed branch from the base branch with exactly the
// starter files and the marker (one force push: whatever anyone else left on
// it is dropped) and opens its PR as dotvirt's own account, whatever
// pullRequests says. A PR from the seed branch in any state means the offer
// was made, a declined one included: nothing to do.
func (c *Coordinator) proposeSeed(repoURL string, ask bool) (forge.PR, error) {
	fc := c.forgeFor(repoURL)
	if fc == nil {
		return forge.PR{}, nil // no forge serving the repo to open a PR in
	}
	branch := c.seedBranch()
	if ask {
		offered, err := fc.EverOpened(branch, c.baseBranch)
		if err != nil {
			return forge.PR{}, seedForgeError{err}
		}
		if offered {
			return forge.PR{}, nil
		}
	}
	_, write, err := c.pair(project.ProjectInfo{Name: "starter templates", Repo: repoURL})
	if err != nil {
		return forge.PR{}, err
	}
	files := starterSet()
	items := make([]git.ChangesetItem, len(files))
	for i, f := range files {
		items[i] = git.ChangesetItem{Path: f.Path, NewContent: f.Content}
	}
	res, err := write.ProposeChangeset(c.baseBranch, branch, c.productName()+": seed starter templates", items, git.Author{}, git.PushPlan{})
	if errors.Is(err, git.ErrNoChanges) {
		return forge.PR{}, nil // the base branch already holds them
	}
	if err != nil {
		return forge.PR{}, err
	}
	pr, err := fc.CreatePR(c.productName()+": starter VM templates", c.seedBody(), branch, c.baseBranch)
	if err != nil {
		return forge.PR{}, seedForgeError{err}
	}
	if pr.Head.Sha != "" && pr.Head.Sha != res.Hash {
		if _, cerr := fc.EditPR(pr.Number, forge.PREdit{State: "closed"}); cerr != nil {
			log.Printf("changeset: starter templates PR #%d: close: %v", pr.Number, cerr)
		}
		return forge.PR{}, errSeedMoved
	}
	return pr, nil
}

// seedBody is the offer's PR description: what it adds and that declining
// it is final.
func (c *Coordinator) seedBody() string {
	return fmt.Sprintf("%s offers this project a starter VM template library: example "+
		"VirtualMachineTemplates under %s/, deployable with their defaults.\n\n"+
		"Optional. Merge to add them to the project's library, or close this pull request "+
		"to decline: the library is offered once per repository (%s records it).\n",
		c.productName(), model.TemplatesDir, SeedMarker)
}

// seedForgeError marks a forge API failure, shown by its class only.
type seedForgeError struct{ err error }

func (e seedForgeError) Error() string { return e.err.Error() }
func (e seedForgeError) Unwrap() error { return e.err }

// seedFailure is a failed offer as any reader of the repo may see it. Only
// classified failures carry words: a refused push the forge's hook explained,
// a forge answer's class, a moved branch. Anything else (a raw git error can
// embed the repo URL, credentials and internal hosts included) is one fixed
// line; the detail is in the console's log.
func seedFailure(err error) string {
	var refused *git.BranchRefusedError
	var fe seedForgeError
	switch {
	case errors.As(err, &refused):
		return refused.Error()
	case errors.As(err, &fe):
		return forgeClass(fe.err)
	case errors.Is(err, errSeedMoved):
		return errSeedMoved.Error()
	case errors.Is(err, model.ErrUnavailable):
		return "the repository could not be read"
	}
	return "the starter templates could not be written to the repository"
}

// forgeClass names a failed forge call by its class, never its body.
func forgeClass(err error) string {
	var se *forge.StatusError
	switch {
	case !errors.As(err, &se):
		return "the forge is unreachable"
	case se.Status == http.StatusUnauthorized, se.Status == http.StatusForbidden:
		return fmt.Sprintf("the forge refused the console's account (%d)", se.Status)
	case se.Status == http.StatusNotFound:
		return "the forge does not know this repository"
	case se.Status >= http.StatusInternalServerError, se.Status == http.StatusTooManyRequests:
		return fmt.Sprintf("the forge failed (%d)", se.Status)
	}
	return fmt.Sprintf("the forge rejected the request (%d)", se.Status)
}

// TemplateSeed is proj's pending offer: its open PR, or why it could not be
// made. ok is false when there is nothing to report.
func (c *Coordinator) TemplateSeed(proj project.ProjectInfo) (model.TemplateSeed, bool) {
	if proj.Repo == "" || proj.Path != "" {
		return model.TemplateSeed{}, false
	}
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	st := c.seeds[forge.NormalizeRepoURL(proj.Repo)]
	if st == nil || st.pr == 0 && st.err == "" {
		return model.TemplateSeed{}, false
	}
	return model.TemplateSeed{Library: proj.Name, PRNumber: st.pr, PRURL: st.prURL, Error: st.err,
		Retries: st.err != "" && !st.decided}, true
}
