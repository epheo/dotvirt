package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The Forgejo webhook: push/PR events trigger an immediate fetch of that repo
// plus a proposals refresh, so the UI repaints in webhook latency instead of
// the next poll tick (and the poll interval can stretch to minutes). The
// endpoint authenticates by HMAC signature, not a user session — it's exempted
// in auth.isOpenPath and disabled entirely when no secret is configured.

// handleForgeWebhook validates the HMAC-SHA256 signature Forgejo puts on every
// delivery, pokes the named repo's poller, and nudges the proposals refresher
// (a PR opening/merging doesn't necessarily move branch heads).
func (s *Server) handleForgeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WebhookSecret == "" {
		http.Error(w, "webhook not configured", http.StatusNotFound)
		return
	}
	body, err := readAll(r)
	if err != nil {
		fail(w, invalid(err))
		return
	}
	// Forgejo sends its own header; Gitea-lineage servers send X-Gitea-Signature.
	sig := r.Header.Get("X-Forgejo-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Gitea-Signature")
	}
	if !validSignature(body, sig, s.cfg.WebhookSecret) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	var event struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number   int       `json:"number"`
			Title    string    `json:"title"`
			HTMLURL  string    `json:"html_url"`
			Merged   bool      `json:"merged"`
			MergedAt time.Time `json:"merged_at"`
			User     struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		Repository struct {
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	}
	_ = json.Unmarshal(body, &event)
	// Projects annotate the https clone URL; try the payload's forms against the
	// open-repo cache (an unknown repo is simply not being watched yet).
	for _, u := range []string{event.Repository.CloneURL, event.Repository.HTMLURL, event.Repository.HTMLURL + ".git"} {
		if u != "" && u != ".git" {
			s.repos.Poke(u)
		}
	}
	s.nudgeProposals()
	// A PR merged into the trunk lands in the Recent Tasks feed now, in webhook
	// latency; the proposals refresher's forge poll is the backstop that records
	// the same (deduped) entry when no webhook arrives.
	if pr := event.PullRequest; s.tasks != nil && pr.Merged && pr.Base.Ref == s.cfg.BaseBranch {
		repo := forge.NormalizeRepoURL(event.Repository.CloneURL)
		if repo == "" {
			repo = forge.NormalizeRepoURL(event.Repository.HTMLURL)
		}
		if repo != "" {
			s.tasks.RecordMerge(tasks.Merge{
				RepoURL: repo,
				Number:  pr.Number,
				URL:     pr.HTMLURL,
				Title:   pr.Title,
				By:      tasks.MergeAuthor(pr.Head.Ref, s.cfg.ProposedBranch, pr.User.Login),
				At:      pr.MergedAt,
			})
		}
	}
	// Drive ArgoCD to pick up the push directly: nudge the Application(s) sourcing
	// this repo to hard-refresh (auto-sync then reconciles), so a merge applies in
	// webhook latency rather than ArgoCD's poll interval. Fire-and-forget on a
	// detached, bounded context — the handler returns 204 now; best-effort, since
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
