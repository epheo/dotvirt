package changeset

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// ForgeAccounts holds each console session's own forge account (auth.Forge).
type ForgeAccounts interface {
	// Credential is id's forge account and its current token, or
	// auth.ErrForgeNotConnected.
	Credential(ctx context.Context, id auth.Identity) (auth.ForgeCredential, error)
	// Rejected drops id's connection when it still holds token, which the
	// forge refused.
	Rejected(id auth.Identity, token string)
}

// Delegation writes each user's changes with their own forge account
// (pullRequests: Delegated), on every repo but the platform tier's. A write
// it carries is refused, never made with the console's account.
type Delegation struct {
	// Accounts is each console session's forge account; nil while Off.
	Accounts ForgeAccounts
	// Forge is the forge users' tokens go to, at the admin's spec.forge.url
	// alone: never the forge credential Secret's url, which the operator may
	// write.
	Forge *forge.Factory
	// PlatformRepo is the platform tier's repo. Its pull requests stay the
	// console account's, gated by the user's Kubernetes rights and naming the
	// user in branch, author and body: staff review and merge them.
	PlatformRepo string
	// Off is why no user can connect a forge account; set, every write the
	// delegation carries is refused.
	Off string
}

// WithDelegation writes users' changes with their own forge accounts (see
// Delegation). Set once, before serving.
func (c *Coordinator) WithDelegation(d Delegation) *Coordinator {
	if d.Accounts == nil && d.Off == "" {
		d.Off = "no forge accounts are wired"
	}
	c.delegation = &d
	return c
}

// delegates reports whether a user's write to proj runs as their own forge
// account: under Delegation, on every repo but the platform tier's.
func (r *Reader) delegates(proj project.ProjectInfo) bool {
	d := r.delegation
	if d == nil {
		return false
	}
	platform := proj.Name == project.PlatformName && d.PlatformRepo != "" &&
		forge.NormalizeRepoURL(proj.Repo) == forge.NormalizeRepoURL(d.PlatformRepo)
	return !platform
}

// author is who id's commits to proj are attributed to. K8s usernames are
// not emails, so a stable noreply address stands in. A user writing as their
// own forge account commits too; otherwise dotvirt, which pushes, does.
func (r *Reader) author(id auth.Identity, proj project.ProjectInfo) git.Author {
	return git.Author{Name: id.Username, Email: authorEmail(id.Username), Commits: r.delegates(proj)}
}

// hand is who one user's write runs as in the forge: the push clone, the
// forge client (nil when no forge serves the repo: the branch is pushed
// alone) and the commits' signatures.
type hand struct {
	write *git.WriteRepo
	fc    *forge.Client
	by    git.Author
	// as is the user's own forge account; nil writes as the console's.
	as *account
}

// account is the forge account one user's write runs as.
type account struct {
	accounts ForgeAccounts
	id       auth.Identity
	login    string
	token    string
	repo     string // owner/repo, as the forge names it
}

// hand resolves who id's write to proj runs as: every write a user starts
// picks its forge identity here, once the gate cleared it. write is proj's
// push clone. Under Delegation the write runs as id's own forge account and
// needs write on the repo there; a user without either is refused.
func (c *Coordinator) hand(id auth.Identity, proj project.ProjectInfo, write *git.WriteRepo) (hand, error) {
	by := c.author(id, proj)
	if !c.delegates(proj) {
		return hand{write: write, fc: c.forgeFor(proj.Repo), by: by}, nil
	}
	d := c.delegation
	if d.Off != "" {
		return hand{}, fmt.Errorf("%w: changes go to the forge as each user here, and no git account can connect to this console: %s", model.ErrUnavailable, d.Off)
	}
	// For keeps owner/repo alone: a repo neither address of the forge serves
	// would be written on the wrong one.
	if d.Forge.For(proj.Repo) == nil || !c.forge.SameForge(proj.Repo) && !d.Forge.SameForge(proj.Repo) {
		return hand{}, fmt.Errorf("%w: project %s's repo is not on the forge the console writes to as you", model.ErrConflict, proj.Name)
	}
	cred, err := d.Accounts.Credential(context.Background(), id)
	switch {
	case errors.Is(err, auth.ErrForgeNotConnected):
		return hand{}, errConnect
	case err != nil:
		log.Printf("changeset: %s: forge account: %v", id.Username, err)
		return hand{}, fmt.Errorf("%w: your git account's access could not be renewed; try again in a moment", model.ErrUnavailable)
	}
	fc := d.Forge.As(cred.Token).For(proj.Repo)
	h := hand{
		write: write.As(fc.CloneURL(), cred.Login, cred.Token),
		fc:    fc,
		by:    by,
		as:    &account{accounts: d.Accounts, id: id, login: cred.Login, token: cred.Token, repo: fc.FullName()},
	}
	push, err := fc.CanPush()
	if err != nil {
		if refused := h.refusal(err); refused != nil {
			return hand{}, refused
		}
		return hand{}, forgeFailed("repo for project "+proj.Name, err)
	}
	if !push {
		return hand{}, h.as.noWrite()
	}
	return h, nil
}

// errConnect refuses a write the user's forge account must make while their
// console session holds none that works.
var errConnect = fmt.Errorf("%w: %w: this change goes to the forge as you; connect your git account, then try again (nothing was pushed)", model.ErrForbidden, model.ErrForgeConnect)

func (a *account) noWrite() error {
	return fmt.Errorf("%w: %w: ask the owners of %s in the forge to give %s write access, then try again (nothing was pushed)", model.ErrForbidden, model.ErrForgeWrite, a.repo, a.login)
}

// refusal is err as the user's own forge account met it; nil for any other
// error, and for every error of a write the console's account makes. A token
// the forge no longer takes ends the session's connection and asks for the
// connect; a repo the account may not see or push to asks for write on it.
func (h hand) refusal(err error) error {
	if h.as == nil || err == nil {
		return nil
	}
	switch {
	case h.spent(err):
		return errConnect
	case forgeStatus(err) == http.StatusForbidden, errors.Is(err, transport.ErrAuthorizationFailed),
		errors.Is(err, transport.ErrRepositoryNotFound):
		return h.as.noWrite()
	}
	return nil
}

// spent reports whether err is the forge no longer taking the user's token,
// and drops it from their session then, so their next write offers the
// connect. Also called where a failed forge call is only logged.
func (h hand) spent(err error) bool {
	if h.as == nil || err == nil {
		return false
	}
	if forgeStatus(err) != http.StatusUnauthorized && !errors.Is(err, transport.ErrAuthenticationRequired) {
		return false
	}
	h.as.accounts.Rejected(h.as.id, h.as.token)
	return true
}

// forgeStatus is the status the forge answered err with, 0 for none.
func forgeStatus(err error) int {
	if se := (*forge.StatusError)(nil); errors.As(err, &se) {
		return se.Status
	}
	return 0
}
