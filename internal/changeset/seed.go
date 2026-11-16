package changeset

import (
	"errors"
	"fmt"
	"log"
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
)

const seedMarkerContent = "The starter VM template library was offered to this repository.\n" +
	"Its examples may be deleted; this file keeps them from being offered again.\n"

// seedBranch is the one branch the starter library is offered from:
// <proposed>/_seed/starter-templates, never a user's (userSegment).
func (r *Reader) seedBranch() string {
	return r.proposed + "/" + tasks.SeedSegment + "/" + seedBranchName
}

// seedState is what this process knows of one repo's offer.
type seedState struct {
	decided  bool      // offered, declined or not needed: nothing left to do
	inflight bool      // an offer is running
	retryAt  time.Time // after a failure, the next attempt's earliest time
	pr       int       // the open offer, from the lane
	prURL    string
	err      string // the last failure, as the forge or git gave it
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
// an open offer costs nothing more; the rest is decided from the mirror
// dotvirt already holds. Only a candidate - no marker, no template on the
// base branch, no open offer - asks the forge, once per process, whether an
// offer ever existed: a closed one was declined, and is never repeated. The
// push and the PR run off the caller.
func (c *Coordinator) SeedTemplates(proj project.ProjectInfo, lane []model.Proposal) {
	if proj.Repo == "" {
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
	c.seedMu.Lock()
	st := c.seedFor(key)
	if open != nil {
		st.pr, st.prURL, st.err, st.decided = open.PRNumber, open.PRURL, "", true
	} else {
		st.pr, st.prURL = 0, "" // merged or closed since
	}
	if st.decided || st.inflight || time.Now().Before(st.retryAt) {
		c.seedMu.Unlock()
		return
	}
	read, ok := c.repos.Peek(proj.Repo)
	if !ok {
		c.seedMu.Unlock()
		return // a later refresh decides, once the mirror is open
	}
	st.inflight = true
	c.seedMu.Unlock()

	need, err := c.libraryMissing(read)
	if err != nil || !need {
		c.seedMu.Lock()
		st.inflight = false
		st.decided = err == nil // an unreadable base branch is decided later
		c.seedMu.Unlock()
		return
	}
	go c.offerSeed(proj.Repo, key, true)
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

// seedCreated offers the starter library to a repo dotvirt just created,
// synchronously: no offer can exist there yet, so the forge is not asked.
// Its failure never fails the project's creation; it is reported with the
// library instead.
func (c *Coordinator) seedCreated(repoURL string) {
	key := forge.NormalizeRepoURL(repoURL)
	c.seedMu.Lock()
	st := c.seedFor(key)
	if st.decided || st.inflight {
		c.seedMu.Unlock()
		return
	}
	st.inflight = true
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
	st.inflight = false
	if err != nil {
		log.Printf("changeset: starter templates for %s: %v", repoURL, err)
		st.err, st.retryAt = err.Error(), time.Now().Add(seedRetry)
		return
	}
	st.decided, st.err = true, ""
	if pr.Number != 0 {
		st.pr, st.prURL = pr.Number, c.forge.PullPage(repoURL, pr.Number)
	}
}

// proposeSeed pushes the seed branch (unless it already holds the offer) and
// opens its PR as dotvirt's own account, whatever pullRequests says. A PR
// from the seed branch in any state means the offer was made: nothing to do.
func (c *Coordinator) proposeSeed(repoURL string, ask bool) (forge.PR, error) {
	fc := c.forge.For(repoURL)
	if fc == nil {
		return forge.PR{}, nil // no forge to open a PR in
	}
	branch := c.seedBranch()
	read, write, err := c.repos.Get(repoURL, "")
	if err != nil {
		return forge.PR{}, err
	}
	pushed := false
	if ask {
		if _, found, err := fc.FindPR(branch, c.baseBranch); err != nil {
			return forge.PR{}, forgeFailed("starter templates pull request", err)
		} else if found {
			return forge.PR{}, nil
		}
		pushed = read.BranchHash(branch) != "" // pushed before its PR could be opened
	}
	if !pushed {
		files := append(vmtemplate.SeedFiles(), model.File{Path: SeedMarker, Content: []byte(seedMarkerContent)})
		items := make([]git.ChangesetItem, len(files))
		for i, f := range files {
			items[i] = git.ChangesetItem{Path: f.Path, NewContent: f.Content}
		}
		_, err := write.ProposeChangeset(c.baseBranch, branch, c.productName()+": seed starter templates", items, git.Author{}, git.PushPlan{})
		if errors.Is(err, git.ErrNoChanges) {
			return forge.PR{}, nil // the base branch already holds them
		}
		if err != nil {
			return forge.PR{}, err
		}
	}
	pr, err := fc.CreatePR(c.productName()+": starter VM templates", c.seedBody(), branch, c.baseBranch)
	if err != nil {
		return forge.PR{}, forgeFailed("starter templates pull request", err)
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

// TemplateSeed is proj's pending offer: its open PR, or why it could not be
// made. ok is false when there is nothing to report.
func (c *Coordinator) TemplateSeed(proj project.ProjectInfo) (model.TemplateSeed, bool) {
	if proj.Repo == "" {
		return model.TemplateSeed{}, false
	}
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	st := c.seeds[forge.NormalizeRepoURL(proj.Repo)]
	if st == nil || st.pr == 0 && st.err == "" {
		return model.TemplateSeed{}, false
	}
	return model.TemplateSeed{Library: proj.Name, PRNumber: st.pr, PRURL: st.prURL, Error: st.err}, true
}
