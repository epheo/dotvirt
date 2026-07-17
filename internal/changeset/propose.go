package changeset

import (
	"fmt"
	"log"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/internal/vmgen"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Propose builds the working branch from (id, proj)'s whole draft, pushes it to
// the project repo, and opens (or finds) a Forgejo PR. Clears the draft on success.
func (c *Coordinator) Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.ProposeResult{}, err
	}
	if len(entries) == 0 {
		return model.ProposeResult{}, fmt.Errorf("%w: draft is empty", model.ErrInvalid)
	}
	if err := requireRepo(proj); err != nil {
		return model.ProposeResult{}, err
	}
	_, write, err := c.repos.Get(proj.Repo)
	if err != nil {
		return model.ProposeResult{}, err
	}

	items, err := c.toChangesetItems(entries)
	if err != nil {
		return model.ProposeResult{}, err
	}

	title := req.Title
	if title == "" {
		title = fmt.Sprintf("dotvirt: %d change(s)", len(entries))
	}
	commitMsg := title
	if req.Message != "" {
		commitMsg = title + "\n\n" + req.Message
	}

	// The working branch is per-(user, project): a constant branch would be
	// force-pushed by every user proposing into the same repo, so concurrent
	// proposals would clobber each other's PR. Scoping it isolates them.
	branch := c.proposedBranch(id.Username, proj.Name)

	// Attribute the commit to the k8s user (committer stays dotvirt, the SA that
	// pushes). K8s usernames aren't emails, so synthesize a stable noreply address.
	by := git.Author{Name: id.Username, Email: authorEmail(id.Username)}
	res, err := write.CommitChangeset(c.baseBranch, branch, commitMsg, items, by)
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	fc := c.forge.For(proj.Repo)
	if fc == nil {
		// No forge configured (or unparsable repo): report the pushed branch only.
		return out, c.store.Clear(id.Username, proj.Name)
	}

	pr, err := fc.CreatePR(title, req.Message, branch, c.baseBranch)
	if err == nil {
		out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
		return out, c.store.Clear(id.Username, proj.Name)
	}

	// CreatePR failed — the dominant case is 409: a PR for this stable head→base
	// already exists, possibly closed. The branch is per-(user, project) and reused
	// every propose, so look up the existing PR across all states and recover.
	if existing, ok, ferr := fc.FindPR(branch, c.baseBranch); ferr == nil && ok {
		switch {
		case existing.State == "open":
			out.PRURL, out.PRNumber, out.Existing = existing.HTMLURL, existing.Number, true
			return out, c.store.Clear(id.Username, proj.Name)
		case !existing.Merged:
			// Closed but not merged: reopen so the freshly-pushed commits surface
			// instead of sitting on a branch whose only PR is closed.
			if reopened, rerr := fc.ReopenPR(existing.Number); rerr == nil {
				out.PRURL, out.PRNumber, out.Existing = reopened.HTMLURL, reopened.Number, true
				return out, c.store.Clear(id.Username, proj.Name)
			} else {
				log.Printf("propose %s/%s: found closed PR #%d but reopen failed: %v", proj.Name, branch, existing.Number, rerr)
			}
		default:
			// Merged: the branch already landed; new commits sit atop a merged head.
			log.Printf("propose %s/%s: existing PR #%d is merged; offering compare URL", proj.Name, branch, existing.Number)
		}
	}

	// Real failure / reopen failed / merged: the branch IS pushed. Hand back the
	// compare URL so the user can open the PR manually, and KEEP the draft staged so
	// they can retry — this is a 200, not an error (returning err here would make the
	// handler drop the result body).
	log.Printf("propose %s/%s: branch pushed but PR unavailable: %v", proj.Name, branch, err)
	out.CompareURL = fc.CompareURL(branch, c.baseBranch)
	return out, nil
}

// RecentlyMerged lists PRs merged into proj's base branch since 'since' — the
// task feed's merged lane (the poll backstop behind the forge webhook, and the
// reseed after a restart). Attribution comes from the head branch, not the PR
// poster: dotvirt's bot opens every proposal PR (see tasks.MergeAuthor).
func (c *Coordinator) RecentlyMerged(proj project.ProjectInfo, since time.Time) ([]tasks.Merge, error) {
	if proj.Repo == "" {
		return nil, nil
	}
	fc := c.forge.For(proj.Repo) // nil-safe: nil factory / unparsable repo → nil client
	if fc == nil {
		return nil, nil
	}
	prs, err := fc.MergedPRs(c.baseBranch, 20)
	if err != nil {
		return nil, err
	}
	repo := forge.NormalizeRepoURL(proj.Repo)
	var out []tasks.Merge
	for _, pr := range prs {
		if pr.MergedAt.Before(since) {
			continue
		}
		out = append(out, tasks.Merge{
			RepoURL: repo,
			Number:  pr.Number,
			URL:     pr.HTMLURL,
			Title:   pr.Title,
			By:      tasks.MergeAuthor(pr.Head.Ref, c.proposed, pr.User.Login),
			At:      pr.MergedAt,
		})
	}
	return out, nil
}

// proposedBranch derives the per-(user, project) working branch under the
// configured prefix, e.g. dotvirt/proposed/<user>/<project>-<hash>. The readable
// segments are sanitized to valid git refs (refSegment is lossy), so a short hash
// of the RAW (user, project) is appended to guarantee distinct identities never
// share a branch — without it, two usernames that sanitize to the same string
// would force-push over each other's PR.
func (c *Coordinator) proposedBranch(user, project string) string {
	return c.proposed + "/" + refSegment(user) + "/" + refSegment(project) + "-" + shortHash(user, project)
}

func (c *Coordinator) toChangesetItems(entries []draft.Entry) ([]git.ChangesetItem, error) {
	items := make([]git.ChangesetItem, 0, len(entries))
	for _, e := range entries {
		switch e.Kind {
		case draft.KindEdit:
			// A template edit replaces the file wholesale (Manifest set); a VM
			// edit patches targeted fields in place.
			if e.Manifest != "" {
				items = append(items, git.ChangesetItem{Path: e.SourceFile, Namespace: e.Namespace, Name: e.Name, NewContent: []byte(e.Manifest)})
				continue
			}
			items = append(items, git.ChangesetItem{
				Path: e.SourceFile, Namespace: e.Namespace, Name: e.Name, Edit: e.Edit,
			})
		case draft.KindCreate:
			// An adopt-create carries the running-branch manifest verbatim; a
			// wizard create generates one from its spec.
			if e.Manifest != "" {
				items = append(items, git.ChangesetItem{Path: e.SourceFile, Namespace: e.Namespace, Name: e.Name, NewContent: []byte(e.Manifest)})
				continue
			}
			path, content, err := vmgen.Manifest(*e.Spec)
			if err != nil {
				return nil, fmt.Errorf("generate %s/%s: %w", e.Namespace, e.Name, err)
			}
			items = append(items, git.ChangesetItem{Path: path, Namespace: e.Namespace, Name: e.Name, NewContent: content})
		case draft.KindDelete:
			items = append(items, git.ChangesetItem{Path: e.SourceFile, Namespace: e.Namespace, Name: e.Name, Delete: true})
		}
	}
	return items, nil
}

// OpenProposal returns the open PR backing (id, proj)'s proposed branch, if any —
// the staged→PR→synced lifecycle's middle state for the Recent Tasks feed. Returns
// ok=false (nil error) when the project has no repo/forge or no open PR.
func (c *Coordinator) OpenProposal(id auth.Identity, proj project.ProjectInfo) (model.Proposal, bool, error) {
	if proj.Repo == "" {
		return model.Proposal{}, false, nil
	}
	fc := c.forge.For(proj.Repo) // nil-safe: nil factory / unparsable repo → nil client
	if fc == nil {
		return model.Proposal{}, false, nil
	}
	pr, ok, err := fc.FindPR(c.proposedBranch(id.Username, proj.Name), c.baseBranch)
	if err != nil {
		return model.Proposal{}, false, err
	}
	if !ok || pr.State != "open" {
		return model.Proposal{}, false, nil
	}
	return model.Proposal{Project: proj.Name, PRNumber: pr.Number, PRURL: pr.HTMLURL, Title: pr.Title}, true, nil
}
