package api

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The Forgejo webhook: push/PR events trigger an immediate fetch of that repo,
// and PR events a refresh of its proposals lane, so the UI repaints in webhook
// latency instead of the next poll tick (and the poll interval can stretch to
// minutes). The endpoint authenticates by HMAC signature, not a user session -
// it's exempted in auth.isOpenPath and disabled entirely when no secret is
// configured.

// handleForgeWebhook validates the HMAC-SHA256 signature Forgejo puts on every
// delivery, pokes the named repo's poller, and on a PR event nudges the proposals
// refresher for that repo.
func (s *Server) handleForgeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WebhookSecret == "" {
		fail(w, fmt.Errorf("%w: webhook not configured", model.ErrNotFound))
		return
	}
	body, ok := readRaw(w, r)
	if !ok {
		return
	}
	// Forgejo sends its own header; Gitea-lineage servers send X-Gitea-Signature.
	sig := r.Header.Get("X-Forgejo-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Gitea-Signature")
	}
	if !validSignature(body, sig, s.cfg.WebhookSecret) {
		fail(w, fmt.Errorf("%w: invalid signature", model.ErrForbidden))
		return
	}

	var event struct {
		Action      string   `json:"action"`
		PullRequest forge.PR `json:"pull_request"`
		Repository  struct {
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	}
	_ = json.Unmarshal(body, &event)
	// Every spelling of a repo normalizes to the open-repo cache's key (an
	// unknown repo is simply not being watched yet).
	repo := cmp.Or(forge.NormalizeRepoURL(event.Repository.CloneURL), forge.NormalizeRepoURL(event.Repository.HTMLURL))
	if repo != "" {
		s.repos.Poke(repo)
	}
	// A push reaches the lane through the poke's GitChanged; a PR event may move
	// no head (opened after its push, closed unmerged), so it nudges that repo.
	if event.PullRequest.Number != 0 && repo != "" {
		s.nudgeProposals(repo)
	}
	// A PR merged into the trunk lands in the Recent Tasks feed now, in webhook
	// latency; the proposals refresher's forge poll is the backstop that records
	// the same (deduped) entry when no webhook arrives.
	if pr := event.PullRequest; s.tasks != nil && pr.Merged && pr.Base.Ref == s.cfg.BaseBranch {
		if repo != "" {
			s.tasks.RecordMerge(tasks.Merge{
				RepoURL: repo,
				Number:  pr.Number,
				URL:     s.forge.PullPage(event.Repository.CloneURL, pr.Number),
				Title:   pr.Title,
				By:      tasks.MergeAuthor(pr.HeadBranch(), s.cfg.ProposedBranch, pr.User.Login),
				Branch:  pr.HeadBranch(),
				Head:    pr.Head.Sha,
				At:      pr.MergedAt,
			})
		}
	}
	if pr := event.PullRequest; pr.Base.Ref == s.cfg.BaseBranch && (pr.Merged || pr.State == "open") {
		s.settlePushed(repo, pr.HeadBranch(), pr.Number, pr.Head.Sha, pr.Merged)
	}
	// Drive ArgoCD to pick up the push directly: nudge the Application(s) sourcing
	// this repo to hard-refresh (auto-sync then reconciles), so a merge applies in
	// webhook latency rather than ArgoCD's poll interval. Fire-and-forget on a
	// detached, bounded context - the handler returns 204 now; best-effort, since
	// Argo's own webhook + poll remain as backstops. Only when Argo is wired.
	if s.drift != nil {
		clone, html := event.Repository.CloneURL, event.Repository.HTMLURL
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.drift.RefreshForRepo(ctx, clone, html)
		}()
	}
	w.WriteHeader(http.StatusNoContent)
}

// validSignature checks the hex HMAC-SHA256 of body against the delivery
// signature in constant time.
func validSignature(body []byte, sigHex, secret string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
