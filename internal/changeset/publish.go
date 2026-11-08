package changeset

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Propose turns (id, proj)'s draft into the per-(user, project) branch and
// its pull request. The branch is always built fresh on current base: with
// an open PR it carries what that PR proposes (draft.Proposal) with the
// draft laid over it, so every pin and no-op check runs against what the
// merge lands on, and the push is leased to the head dotvirt last pushed. A
// closed, unmerged PR is reopened for the draft alone: closing it turned its
// content down. On success the committed entries are unstaged; anything
// staged or restaged meanwhile stays for the next propose. One propose per
// (user, project) runs at a time, so two tabs cannot drop each other's work.
func (c *Coordinator) Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error) {
	defer c.proposing.lock(pairOf(id, proj))()

	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.ProposeResult{}, err
	}
	if len(entries) == 0 {
		return model.ProposeResult{}, fmt.Errorf("%w: draft is empty", model.ErrInvalid)
	}
	read, write, err := c.write(proj)
	if err != nil {
		return model.ProposeResult{}, err
	}
	run := &proposeRun{
		c: c, id: id, proj: proj, req: req, read: read, write: write, entries: entries,
		fc:     c.forgeFor(proj.Repo),
		branch: c.proposedBranch(id.Username, proj.Name),
	}
	// Staged earlier, perhaps by rights since withdrawn: every entry passes
	// the gate again, against the base it now lands on. A copy is gated: the
	// entries are unstaged after the commit only while they equal the draft's.
	if err := requirePinned(entries); err != nil {
		return model.ProposeResult{}, err
	}
	if err := c.gate(id, proj, slices.Clone(entries)); err != nil {
		return model.ProposeResult{}, run.commitFailed(err, entries, 0)
	}
	// The semantic view becomes the PR description (best-effort: a body render
	// failure must not block the propose).
	if view, err := c.Get(id, proj); err == nil {
		run.draft, run.rendered = view, true
	}

	rec, hasRec, err := c.store.Proposal(id.Username, proj.Name)
	if err != nil {
		return model.ProposeResult{}, err
	}
	hasRec = hasRec && rec.Repo == forge.NormalizeRepoURL(proj.Repo) && rec.Branch == run.branch
	if hasRec && run.fc == nil {
		// No forge to ask about the PR: rebuild what the record carries under
		// its lease all the same, never force over it.
		return run.extend(forge.PR{Number: rec.PR}, rec, true)
	}
	pr, found, err := run.lookup(rec, hasRec)
	if err != nil {
		return model.ProposeResult{}, err
	}
	if found && pr.State == "open" {
		return run.extend(pr, rec, hasRec)
	}
	if hasRec {
		// Its PR merged or closed: what it carried is in base, or turned down.
		_ = c.store.DropProposal(id.Username, proj.Name)
	}
	return run.fresh(pr, found)
}

// proposeRun is one Propose: the draft, where it goes, and how far it got.
type proposeRun struct {
	c        *Coordinator
	id       auth.Identity
	proj     project.ProjectInfo
	req      model.ProposeRequest
	read     *git.Repo
	write    *git.WriteRepo
	fc       *forge.Client // nil: no forge serves the repo, so the branch is pushed alone
	branch   string
	entries  []draft.Entry
	draft    model.DraftView
	rendered bool
}

// lookup is the branch's pull request: the one dotvirt recorded opening, by
// number, while it is open and still this branch's; else the forge's search,
// which prefers an open PR to the recorded closed one. A search that finds
// none while the branch exists is asked again before a fresh build
// force-pushes over that branch: a PR list read across pages can skip an
// entry that moved meanwhile.
func (r *proposeRun) lookup(rec draft.Proposal, hasRec bool) (forge.PR, bool, error) {
	if r.fc == nil {
		return forge.PR{}, false, nil
	}
	var recorded *forge.PR
	if hasRec && rec.PR > 0 {
		pr, err := r.fc.PR(rec.PR)
		switch {
		case err == nil && (!pr.From(r.branch) || pr.Base.Ref != r.c.baseBranch):
		case err == nil && pr.State == "open":
			return pr, true, nil
		case err == nil:
			recorded = &pr
		case !isNotFound(err):
			return forge.PR{}, false, r.forgeDown(err)
		}
	}
	pr, found, err := r.fc.FindPR(r.branch, r.c.baseBranch)
	if err != nil {
		return forge.PR{}, false, r.forgeDown(err)
	}
	if found {
		return pr, true, nil
	}
	head, err := r.write.RemoteHead(r.branch)
	if err != nil {
		log.Printf("changeset: %s: read remote head: %v", r.branch, err)
		return forge.PR{}, false, fmt.Errorf("%w: the project repo did not answer; nothing was pushed and the draft is kept, so propose again in a moment", model.ErrUnavailable)
	}
	if head != "" {
		if pr, found, err = r.fc.FindPR(r.branch, r.c.baseBranch); err != nil || found {
			return pr, found, r.forgeDown(err)
		}
	}
	if recorded != nil {
		return *recorded, true, nil
	}
	return forge.PR{}, false, nil
}

// forgeDown classifies a forge failure before anything was pushed; nil
// stays nil. A 404 is the repo missing on the forge, which waiting does not
// fix.
func (r *proposeRun) forgeDown(err error) error {
	if err == nil {
		return nil
	}
	log.Printf("changeset: %s: find PR: %v", r.branch, err)
	if isNotFound(err) {
		return fmt.Errorf("%w: the forge has no repo at %s (404); check project %s's repo, nothing was pushed", model.ErrNotFound, r.proj.Repo, r.proj.Name)
	}
	return fmt.Errorf("%w: the forge did not say whether this draft already has a pull request; nothing was pushed and the draft is kept, so propose again in a moment", model.ErrUnavailable)
}

// extend rebuilds the open PR's branch from base with what it carries and the
// draft on top, then rewrites its title and body to describe the whole. The
// record is written ahead of the push (draft.Proposal.Pending), so a push
// that lands while the write after it fails is still known.
func (r *proposeRun) extend(pr forge.PR, rec draft.Proposal, hasRec bool) (model.ProposeResult, error) {
	c := r.c
	if !hasRec || rec.PR != pr.Number && !rec.Pushed(pr.Head.Sha) {
		return model.ProposeResult{}, fmt.Errorf("%w: pull request #%d was opened before dotvirt kept a record of what it carries, so it cannot be rebuilt; merge or close #%d, then propose", model.ErrConflict, pr.Number, pr.Number)
	}
	// A PR opened by hand on the branch dotvirt pushed is adopted.
	rec.PR = pr.Number
	head, err := r.write.RemoteHead(r.branch)
	if err != nil {
		log.Printf("changeset: %s: read remote head: %v", r.branch, err)
		return model.ProposeResult{}, fmt.Errorf("%w: the project repo did not answer; nothing was pushed and the draft is kept, so propose again in a moment", model.ErrUnavailable)
	}
	if !rec.Settle(head) {
		return r.moved(pr, rec.Entries, git.ErrBranchMoved)
	}
	all, err := c.combine(r.read, pr.Number, rec.Entries, r.entries)
	if err != nil {
		return model.ProposeResult{}, err
	}
	view, err := c.render(r.id, r.proj, all)
	if err != nil {
		return model.ProposeResult{}, err
	}
	title := r.req.Title
	if title == "" || title == r.draft.DefaultTitle {
		title = view.DefaultTitle
	}
	message := userMessage(r.req.Message)
	if message == "" {
		message = userMessage(pr.Body)
	}
	body := prBody(view, message, r.id.Username)

	plan := git.PushPlan{Lease: rec.Head, BeforePush: func(hash string) error {
		ahead := rec
		ahead.Pending = &draft.Pending{Head: hash, Entries: all}
		return r.record(ahead)
	}}
	if err := c.deletesStillDeclared(r.read, all); err != nil {
		return model.ProposeResult{}, err
	}
	// The rebuild pushes again what the PR carries, so the proposer must still
	// hold every right it uses. A carried file base moved past is answered as
	// the commit's own pin check would answer it.
	var res git.CommitResult
	var moved *git.BaseMovedError
	err = requirePinned(rec.Entries)
	if err == nil {
		err = c.gate(r.id, r.proj, slices.Clone(rec.Entries))
	}
	if err != nil && !errors.As(err, &moved) {
		return model.ProposeResult{}, fmt.Errorf("%w (carried by pull request #%d)", err, pr.Number)
	}
	if err == nil {
		res, err = r.write.ProposeChangeset(c.baseBranch, r.branch, commitMessage(title, message), c.toChangesetItems(r.proj, all), r.author(), plan)
	}
	if r.fc == nil && errors.As(err, &moved) && !r.staged(moved.Path) {
		return r.supersede(rec, moved.Path, all)
	}
	if errors.Is(err, git.ErrBranchMoved) || errors.As(err, &moved) {
		return r.moved(pr, all, err)
	}
	// Unstaging cannot help an edit the PR carries: the rebuild replays it.
	var stale *staleEdit
	if errors.As(err, &stale) && slices.ContainsFunc(rec.Entries, func(e draft.Entry) bool { return e.Key() == stale.entry.Key() }) {
		return model.ProposeResult{}, fmt.Errorf("%w: %s, and pull request #%d carries that edit: merge or close #%d, then propose", model.ErrConflict, stale.what(), pr.Number, pr.Number)
	}
	if err != nil {
		return model.ProposeResult{}, r.commitFailed(err, all, pr.Number)
	}
	out := model.ProposeResult{Branch: res.Branch, PRURL: pr.HTMLURL, PRNumber: pr.Number, Existing: true}
	rec.Head, rec.Entries, rec.Pending = res.Hash, all, nil
	if r.fc == nil {
		return out, r.done(rec)
	}

	// The PR may have merged or closed while the branch was rebuilt.
	now, err := r.fc.PR(pr.Number)
	switch {
	case err != nil:
		// Unknown: keep the draft staged and record what was pushed, so the
		// next propose replays it on whatever the PR turned out to be.
		log.Printf("changeset: PR #%d: re-read after push: %v", pr.Number, err)
		return out, r.record(rec)
	case now.Merged:
		_ = c.store.DropProposal(r.id.Username, r.proj.Name)
		return r.afterMerge(res.Hash)
	case now.State != "open":
		_ = c.store.DropProposal(r.id.Username, r.proj.Name)
		return model.ProposeResult{}, fmt.Errorf("%w: pull request #%d was closed while this was proposed; the draft is kept, so propose again to reopen it with the draft alone", model.ErrConflict, pr.Number)
	}
	if _, err := r.fc.EditPR(pr.Number, forge.PREdit{Title: title, Body: body}); err != nil {
		log.Printf("changeset: PR #%d: describe: %v", pr.Number, err)
	}
	return out, r.done(rec)
}

// moved answers a rebuild refused because the branch, or a file a carried
// entry was made from, moved. A PR that merged or closed meanwhile explains
// it: its record is stale and the draft goes out alone. Otherwise the PR is
// in the way.
func (r *proposeRun) moved(pr forge.PR, carried []draft.Entry, err error) (model.ProposeResult, error) {
	if r.fc != nil {
		if now, rerr := r.fc.PR(pr.Number); rerr == nil && now.State != "open" {
			_ = r.c.store.DropProposal(r.id.Username, r.proj.Name)
			return r.fresh(now, true)
		}
	}
	return model.ProposeResult{}, r.commitFailed(err, carried, pr.Number)
}

// supersede answers, with no forge to ask, a rebuild refused because main
// moved a file an entry the PR carries was made from: that PR evidently
// merged or was overtaken. Its record goes, and the draft is pushed on its
// own, leased to the head the record held; the result says so.
func (r *proposeRun) supersede(rec draft.Proposal, path string, carried []draft.Entry) (model.ProposeResult, error) {
	_ = r.c.store.DropProposal(r.id.Username, r.proj.Name)
	title, _ := r.draftText()
	if err := r.c.deletesStillDeclared(r.read, r.entries); err != nil {
		return model.ProposeResult{}, err
	}
	res, err := r.write.ProposeChangeset(r.c.baseBranch, r.branch, commitMessage(title, r.req.Message), r.c.toChangesetItems(r.proj, r.entries), r.author(), git.PushPlan{Lease: rec.Head})
	if err != nil {
		return model.ProposeResult{}, r.commitFailed(err, r.entries, 0)
	}
	what := path
	for _, e := range carried {
		if e.SourceFile == path {
			what = objectLabel(e)
		}
	}
	earlier := "the earlier proposal"
	if rec.PR > 0 {
		earlier = fmt.Sprintf("pull request #%d", rec.PR)
	}
	out := model.ProposeResult{Branch: res.Branch, Warning: fmt.Sprintf(
		"%s changed on %s since %s proposed it, so that proposal has merged or been overtaken; this draft was pushed on its own.", what, r.c.baseBranch, earlier)}
	return out, r.c.store.Remove(r.id.Username, r.proj.Name, r.entries...)
}

// staged reports whether a draft entry of this run writes path.
func (r *proposeRun) staged(path string) bool {
	for _, e := range r.entries {
		if e.SourceFile == path {
			return true
		}
	}
	return false
}

// deletesStillDeclared refuses a delete whose file is gone from base while
// base still declares its object at another path: the object moved, and
// removing the old path would leave it in git.
func (c *Coordinator) deletesStillDeclared(read *git.Repo, entries []draft.Entry) error {
	var paths []string
	for _, e := range entries {
		if e.Kind == draft.KindDelete {
			paths = append(paths, e.SourceFile)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	pins, err := read.Pins(c.baseBranch, paths...)
	if err != nil {
		return err
	}
	var idx declaredIndex
	for _, e := range entries {
		if e.Kind != draft.KindDelete || pins[e.SourceFile] != git.AbsentBlob {
			continue
		}
		if idx.Files == nil {
			if idx, err = indexDeclared(read, c.baseBranch); err != nil {
				return err
			}
		}
		if ref, ok := declaredRef(idx, e.Resource, e.Namespace, e.Name); ok {
			return fmt.Errorf("%w: %s moved to %s on %s after its removal was staged; unstage it and stage the removal again", model.ErrConflict, objectLabel(e), idx.Files[ref], c.baseBranch)
		}
	}
	return nil
}

// afterMerge proposes the draft alone once the PR merged under the rebuild:
// what it carried is in base now, and the branch just pushed sits on the
// base from before the merge, so it is rebuilt again on current base, leased
// to the head just pushed. A draft base already satisfies opens no PR.
func (r *proposeRun) afterMerge(pushed string) (model.ProposeResult, error) {
	title, body := r.draftText()
	if err := r.c.deletesStillDeclared(r.read, r.entries); err != nil {
		return model.ProposeResult{}, err
	}
	res, err := r.write.ProposeChangeset(r.c.baseBranch, r.branch, commitMessage(title, r.req.Message), r.c.toChangesetItems(r.proj, r.entries), r.author(), git.PushPlan{Lease: pushed})
	if err != nil {
		return model.ProposeResult{}, r.commitFailed(err, r.entries, 0)
	}
	out := model.ProposeResult{Branch: res.Branch}
	if !r.c.createPR(r.fc, &out, r.branch, title, body) {
		return out, r.record(r.proposal(0, res.Hash, r.entries))
	}
	return out, r.done(r.proposal(out.PRNumber, res.Hash, r.entries))
}

// fresh builds the draft alone from base and gives it a PR (see publish);
// found is the branch's last PR, closed or merged. Without a PR in the end
// the push is still recorded, so a PR the user opens by hand on it is
// adopted.
func (r *proposeRun) fresh(prior forge.PR, found bool) (model.ProposeResult, error) {
	title, body := r.draftText()
	ahead := func(hash string) error {
		if r.fc == nil {
			return nil
		}
		rec := r.proposal(0, "", nil)
		rec.Pending = &draft.Pending{Head: hash, Entries: r.entries}
		return r.record(rec)
	}
	if err := r.c.deletesStillDeclared(r.read, r.entries); err != nil {
		return model.ProposeResult{}, err
	}
	out, head, ok, err := r.c.publish(r.fc, r.write, r.branch, commitMessage(title, r.req.Message), title, body,
		r.c.toChangesetItems(r.proj, r.entries), r.author(), prior, found, ahead)
	switch {
	case err != nil:
		return model.ProposeResult{}, r.commitFailed(err, r.entries, 0)
	case r.fc == nil:
		return out, r.c.store.Remove(r.id.Username, r.proj.Name, r.entries...)
	case !ok:
		// Kept staged so the user can retry once the forge answers.
		return out, r.record(r.proposal(0, head, r.entries))
	}
	return out, r.done(r.proposal(out.PRNumber, head, r.entries))
}

// publish builds items fresh from base onto branch, force-pushes it and gives
// it a PR; found is the branch's PR as the forge last reported it. An open
// one is reused; a closed, unmerged one is reopened just before the push,
// since Forgejo syncs a PR's head only while it is open, and closed again
// when the push then fails; otherwise one is created after it. ok is false
// when the branch has no PR in the end, and out then carries the compare
// URL. fc nil pushes the branch alone. ahead, when set, runs with the built
// commit's hash before anything else touches the forge or the branch.
func (c *Coordinator) publish(fc *forge.Client, write *git.WriteRepo, branch, message, title, body string, items []git.ChangesetItem, by git.Author, prior forge.PR, found bool, ahead func(string) error) (out model.ProposeResult, head string, ok bool, err error) {
	reopened := false
	plan := git.PushPlan{BeforePush: func(hash string) error {
		if ahead != nil {
			if err := ahead(hash); err != nil {
				return err
			}
		}
		if fc == nil || !found || prior.State == "open" || prior.Merged {
			return nil
		}
		if _, err := fc.EditPR(prior.Number, forge.PREdit{State: "open", Title: title, Body: body}); err != nil {
			log.Printf("changeset: %s: reopen PR #%d: %v", branch, prior.Number, err)
			return nil
		}
		reopened = true
		return nil
	}}
	res, err := write.ProposeChangeset(c.baseBranch, branch, message, items, by, plan)
	if err != nil {
		if reopened {
			if _, cerr := fc.EditPR(prior.Number, forge.PREdit{State: "closed"}); cerr != nil {
				log.Printf("changeset: %s: re-close PR #%d after a failed push: %v", branch, prior.Number, cerr)
			}
		}
		return model.ProposeResult{}, "", false, err
	}
	out.Branch = res.Branch
	switch {
	case fc == nil:
		return out, res.Hash, false, nil
	case found && prior.State == "open":
		out.PRURL, out.PRNumber, out.Existing = prior.HTMLURL, prior.Number, true
	case reopened:
		out.PRURL, out.PRNumber, out.Existing = prior.HTMLURL, prior.Number, true
	default:
		if !c.createPR(fc, &out, branch, title, body) {
			return out, res.Hash, false, nil
		}
	}
	return out, res.Hash, true, nil
}

// createPR opens branch's PR and records it on out. When the forge will not,
// out carries the compare URL instead and the result is false: the branch IS
// pushed, so the user can still open the PR by hand. That is a 200, not an
// error - an error would make the handler drop the result body.
func (c *Coordinator) createPR(fc *forge.Client, out *model.ProposeResult, branch, title, body string) bool {
	pr, err := fc.CreatePR(title, body, branch, c.baseBranch)
	if err != nil {
		log.Printf("changeset: %s: branch pushed but PR unavailable: %v", branch, err)
		out.CompareURL = fc.CompareURL(branch, c.baseBranch)
		return false
	}
	out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
	return true
}

// proposal is the record of number carrying entries at head on this run's
// repo and branch.
func (r *proposeRun) proposal(number int, head string, entries []draft.Entry) draft.Proposal {
	return draft.Proposal{Repo: forge.NormalizeRepoURL(r.proj.Repo), Branch: r.branch, PR: number, Head: head, Entries: entries}
}

func (r *proposeRun) record(rec draft.Proposal) error {
	return r.c.store.SetProposal(r.id.Username, r.proj.Name, rec)
}

// done records rec and unstages the committed draft entries.
func (r *proposeRun) done(rec draft.Proposal) error {
	rec.Pending = nil
	if err := r.record(rec); err != nil {
		return err
	}
	return r.c.store.Remove(r.id.Username, r.proj.Name, r.entries...)
}

// draftText is the title and body proposing the draft alone.
func (r *proposeRun) draftText() (title, body string) {
	title, body = r.req.Title, r.req.Message
	if r.rendered {
		body = prBody(r.draft, r.req.Message, r.id.Username)
		if title == "" {
			title = r.draft.DefaultTitle
		}
	}
	if title == "" {
		title = fmt.Sprintf("dotvirt: %d change(s)", len(r.entries))
	}
	return title, body
}

// author attributes the commit to the k8s user (committer stays dotvirt, the
// SA that pushes). K8s usernames aren't emails, so a stable noreply address
// stands in.
func (r *proposeRun) author() git.Author {
	return git.Author{Name: r.id.Username, Email: authorEmail(r.id.Username)}
}

// commitFailed classifies a failed build or push of carried; pr is the open
// PR it was rebuilding, 0 for a fresh build.
func (r *proposeRun) commitFailed(err error, carried []draft.Entry, pr int) error {
	if errors.Is(err, git.ErrNoChanges) {
		// Self-heal: the draft's whole intent is already true in git (typical
		// after staging against a briefly-stale mirror), so keeping it would
		// wedge the user behind a permanent error. Drop it and say what happened.
		_ = r.c.store.Remove(r.id.Username, r.proj.Name, r.entries...)
		return fmt.Errorf("%w: every staged change already matches git; the draft has been cleared", model.ErrInvalid)
	}
	if errors.Is(err, git.ErrBranchMoved) && pr == 0 {
		return fmt.Errorf("%w: the branch %s was changed outside dotvirt while this was proposed; nothing was overwritten and the draft is kept, so propose again", model.ErrConflict, r.branch)
	}
	if errors.Is(err, git.ErrBranchMoved) {
		return fmt.Errorf("%w: pull request #%d's branch was changed outside dotvirt (pushed to or deleted); nothing was pushed and the draft is kept: merge or close #%d, then propose", model.ErrConflict, pr, pr)
	}
	var moved *git.BaseMovedError
	if errors.As(err, &moved) {
		return r.pinMoved(carried, moved.Path, pr)
	}
	return err
}

// pinMoved names the object whose file changed on base after its content was
// made, and the way forward: a draft entry is made again on the current
// version; one the open PR already carries needs that PR merged or closed.
func (r *proposeRun) pinMoved(carried []draft.Entry, path string, pr int) error {
	base := r.c.baseBranch
	for _, e := range carried {
		if e.SourceFile != path {
			continue
		}
		what := objectLabel(e)
		if e.BaseBlob == git.AbsentBlob {
			what += " was created on " + base + " after it was staged"
		} else {
			what += " changed on " + base + " after it was staged"
		}
		for _, d := range r.entries {
			if d.Key() == e.Key() {
				return fmt.Errorf("%w: %s, and proposing would undo that change; unstage it and make the edit again on the current version", model.ErrConflict, what)
			}
		}
		return fmt.Errorf("%w: %s by pull request #%d, and rebuilding it would undo that change; merge or close #%d, then propose", model.ErrConflict, what, pr, pr)
	}
	return fmt.Errorf("%w: %s changed on %s after it was staged; unstage it and make the edit again on the current version", model.ErrConflict, path, base)
}

// combine lays the draft over what the open PR carries, by stageEdit's rules:
// field edits of one VM merge, and a draft entry identical to a carried one
// is already carried. Any other change of an object the PR already changes
// is refused - it would replace what reviewers saw without a word - except a
// delete, which supersedes an edit.
func (c *Coordinator) combine(read *git.Repo, pr int, carried, staged []draft.Entry) ([]draft.Entry, error) {
	byKey := make(map[string]draft.Entry, len(carried)+len(staged))
	for _, e := range carried {
		byKey[e.Key()] = e
	}
	for _, e := range staged {
		k := e.Key()
		prev, ok := byKey[k]
		switch {
		case !ok:
			byKey[k] = e
		case reflect.DeepEqual(prev, e):
		case fieldEdit(prev) && fieldEdit(e):
			edit := overlayEdit(*prev.Edit, *e.Edit)
			vm, declared, err := findVM(read, c.baseBranch, e.Namespace, e.Name)
			if err != nil {
				return nil, err
			}
			if declared {
				edit = withoutBase(edit, vm)
			}
			if edit.Empty() {
				delete(byKey, k)
				continue
			}
			e.Edit = &edit
			byKey[k] = e
		case e.Kind == draft.KindDelete && prev.Kind == draft.KindEdit:
			byKey[k] = e
		default:
			return nil, fmt.Errorf("%w: pull request #%d carries a %s of %s, which the draft's %s would replace unseen; merge or close #%d, or unstage it",
				model.ErrConflict, pr, describe(prev), objectLabel(e), describe(e), pr)
		}
	}
	out := make([]draft.Entry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// describe names an entry's change the way a conflict message reads it.
func describe(e draft.Entry) string {
	if fieldEdit(e) {
		return "settings change"
	}
	return stagedChange(e)
}

// commitMessage is the commit's subject over the user's message.
func commitMessage(title, message string) string {
	if message == "" {
		return title
	}
	return title + "\n\n" + message
}

// forgeFor is the forge client for repo, nil unless repo lives on the
// configured forge: For keeps only owner/repo, so a repo hosted elsewhere
// would be looked up, and opened, on the wrong forge.
func (r *Reader) forgeFor(repo string) *forge.Client {
	if !r.forge.SameForge(repo) {
		return nil
	}
	return r.forge.For(repo)
}

func isNotFound(err error) bool {
	var se *forge.StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

func pairOf(id auth.Identity, proj project.ProjectInfo) string {
	return id.Username + "\x00" + proj.Name
}

// keyedMutex serializes work per key; keys are (user, project) pairs, a set
// that stays small.
type keyedMutex struct {
	mu   sync.Mutex
	held map[string]*sync.Mutex
}

// lock takes key's mutex and returns its unlock.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.held == nil {
		k.held = map[string]*sync.Mutex{}
	}
	m, ok := k.held[key]
	if !ok {
		m = &sync.Mutex{}
		k.held[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}
