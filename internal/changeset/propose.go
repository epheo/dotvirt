package changeset

import (
	"errors"
	"fmt"
	"log"
	"strings"
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

	// The semantic view becomes the PR description (best-effort: a body render
	// failure must not block the propose). Read BEFORE the commit below - the
	// view diffs against the base branch the draft still targets.
	body := req.Message
	view, verr := c.Get(id, proj)
	if verr == nil {
		body = prBody(view, req.Message, id.Username)
	}

	title := req.Title
	if title == "" && verr == nil {
		title = defaultTitle(view)
	}
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
	if errors.Is(err, git.ErrNoChanges) {
		// Self-heal: the draft's whole intent is already true in git (typical
		// after staging against a briefly-stale mirror), so keeping it would
		// wedge the user behind a permanent error. Clear it and say what happened.
		_ = c.store.Clear(id.Username, proj.Name)
		return model.ProposeResult{}, fmt.Errorf("%w: every staged change already matches git; the draft has been cleared", model.ErrInvalid)
	}
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	fc := c.forge.For(proj.Repo)
	if fc == nil {
		// No forge configured (or unparsable repo): report the pushed branch only.
		return out, c.store.Clear(id.Username, proj.Name)
	}

	// The branch is per-(user, project) and reused on every propose, so a PR for
	// this head->base often already exists, possibly closed. Look it up before
	// creating: reuse an open one, reopen a closed-unmerged one so the freshly
	// pushed commits surface, and create only when the branch has no live PR
	// (none yet, or the last one merged).
	if existing, ok, ferr := fc.FindPR(branch, c.baseBranch); ferr == nil && ok {
		switch {
		case existing.State == "open":
			out.PRURL, out.PRNumber, out.Existing = existing.HTMLURL, existing.Number, true
			return out, c.store.Clear(id.Username, proj.Name)
		case !existing.Merged:
			if reopened, rerr := fc.ReopenPR(existing.Number); rerr == nil {
				out.PRURL, out.PRNumber, out.Existing = reopened.HTMLURL, reopened.Number, true
				return out, c.store.Clear(id.Username, proj.Name)
			} else {
				log.Printf("propose %s/%s: found closed PR #%d but reopen failed: %v", proj.Name, branch, existing.Number, rerr)
			}
		}
	}

	pr, err := fc.CreatePR(title, body, branch, c.baseBranch)
	if err == nil {
		out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
		return out, c.store.Clear(id.Username, proj.Name)
	}

	// The branch IS pushed. Hand back the compare URL so the user can open the PR
	// manually, and KEEP the draft staged so they can retry; this is a 200, not an
	// error (returning err here would make the handler drop the result body).
	log.Printf("propose %s/%s: branch pushed but PR unavailable: %v", proj.Name, branch, err)
	out.CompareURL = fc.CompareURL(branch, c.baseBranch)
	return out, nil
}

// RecentlyMerged lists PRs merged into proj's base branch since 'since' - the
// task feed's merged lane (the poll backstop behind the forge webhook, and the
// reseed after a restart). Attribution comes from the head branch, not the PR
// poster: dotvirt's bot opens every proposal PR (see tasks.MergeAuthor).
func (c *Coordinator) RecentlyMerged(proj project.ProjectInfo, since time.Time) ([]tasks.Merge, error) {
	if proj.Repo == "" {
		return nil, nil
	}
	fc := c.forge.For(proj.Repo) // nil-safe: nil factory / unparsable repo -> nil client
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
// share a branch - without it, two usernames that sanitize to the same string
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
			// An adopt-create carries the live manifest verbatim; a
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

// OpenProposals lists every open PR into proj's base branch - the Changes
// pane's Proposed lane, one read shared by all the project's members: a PR is
// the project's git history in waiting, visible to whoever may read that
// history. Whose it is stays the reader's question (OwnsProposal). Empty (nil
// error) when the project has no repo/forge or nothing is open.
func (c *Coordinator) OpenProposals(proj project.ProjectInfo) ([]model.Proposal, error) {
	if proj.Repo == "" {
		return nil, nil
	}
	fc := c.forge.For(proj.Repo) // nil-safe: nil factory / unparsable repo -> nil client
	if fc == nil {
		return nil, nil
	}
	open, err := fc.OpenPRs(c.baseBranch, 50)
	if err != nil {
		return nil, err
	}
	if len(open) == 0 {
		return nil, nil
	}
	// Review state, each read best-effort: an unreadable plane stays zero
	// (unknown), which the UI renders as nothing - never as "no rule". The
	// branch rule is one read for all of them.
	required := 0
	if req, found, rerr := fc.RequiredApprovals(c.baseBranch); rerr == nil && found {
		required = req
	}
	out := make([]model.Proposal, 0, len(open))
	for _, pr := range open {
		p := c.proposalRow(proj, pr)
		p.RequiredApprovals = required
		if n, aerr := fc.Approvals(pr.Number); aerr == nil {
			p.Approvals = n
		}
		if pr.Head.Sha != "" {
			if st, serr := fc.CombinedStatus(pr.Head.Sha); serr == nil {
				p.Checks = st
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// proposalRow is a PR's identity as the lane carries it. By comes from the
// head branch the way the task feed attributes merges, since dotvirt's bot
// posts every proposal.
func (c *Coordinator) proposalRow(proj project.ProjectInfo, pr forge.PR) model.Proposal {
	return model.Proposal{
		Project: proj.Name, PRNumber: pr.Number, PRURL: pr.HTMLURL, Title: pr.Title,
		Branch: pr.Head.Ref,
		By:     tasks.MergeAuthor(pr.Head.Ref, c.proposed, pr.User.Login),
		Revert: strings.HasPrefix(pr.Head.Ref, c.proposed+"/"+tasks.RevertSegment+"/"),
	}
}

// OwnsProposal reports whether id opened the PR on branch: the per-(user,
// project) draft branch or one of the user's revert branches. Exact by
// construction - both names carry a hash of the raw identity - where the By
// segment a row shows is lossy. Pure string work, safe on the broadcast path.
func (c *Coordinator) OwnsProposal(id auth.Identity, proj project.ProjectInfo, branch string) bool {
	return branch == c.proposedBranch(id.Username, proj.Name) ||
		strings.HasPrefix(branch, c.revertPrefix(id.Username, proj.Name))
}

// defaultTitle names an untitled proposal by what it does, so the history row
// and the forge list read as the change rather than as a count.
func defaultTitle(view model.DraftView) string {
	if len(view.Items) == 0 {
		return ""
	}
	verbs := map[string]string{"edit": "Update", "create": "Create", "delete": "Delete"}
	kind := view.Items[0].Kind
	names := make([]string, 0, len(view.Items))
	for _, it := range view.Items {
		if it.Kind != kind {
			kind = ""
		}
		names = append(names, it.Name)
	}
	list := shortList(names, 3)
	verb, ok := verbs[kind]
	if !ok {
		return fmt.Sprintf("%d changes: %s", len(view.Items), list)
	}
	title := verb + " " + list
	if kind == "edit" && len(view.Items) == 1 {
		title += ": " + changeSummary(view.Items[0].Changes)
	}
	return title
}

// changeSummary is one change spelled out, or the fields a multi-field edit touches.
func changeSummary(changes []model.Change) string {
	if len(changes) == 1 {
		c := changes[0]
		switch c.Action {
		case "change":
			return fmt.Sprintf("%s %s -> %s", c.Field, c.From, c.To)
		case "add":
			return fmt.Sprintf("%s + %s", c.Field, c.To)
		case "remove":
			return fmt.Sprintf("%s - %s", c.Field, c.From)
		}
	}
	fields := make([]string, 0, len(changes))
	seen := map[string]bool{}
	for _, c := range changes {
		if !seen[c.Field] {
			seen[c.Field] = true
			fields = append(fields, c.Field)
		}
	}
	return shortList(fields, 3)
}

// shortList joins up to max names, counting the rest.
func shortList(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:max], ", "), len(names)-max)
}
