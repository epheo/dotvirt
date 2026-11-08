package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The tenant authoring gate. Namespace membership (resolveProject) only proves
// the caller can read VMs there; a draft write needs the RBAC the change would
// need if the caller applied it themselves, because the PR opens under
// dotvirt's forge identity and Argo applies it with its own. The platform tier
// keeps its own create-SSAR gates (platformScope), so these pass it through.

const (
	verbList   = "list"
	verbCreate = "create"
	verbUpdate = "update"
	verbDelete = "delete"
)

var ssarVM = ssarFor(model.KindVM)

// stagedKinds is what a change to resource ns/name stages: its namespaced
// backing, or for a network (UDN or a raw NAD) the backing the catalog shows
// for that name. An unknown network checks every namespaced backing.
func (s *Server) stagedKinds(resource, ns, name string) []model.Kind {
	res, ok := model.LookupResource(resource)
	if !ok {
		return nil
	}
	var kinds []model.Kind
	for _, k := range res.Kinds {
		if !k.ClusterScoped {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) > 1 && s.netstate != nil {
		for _, n := range s.netstate.Catalog().Networks {
			if n.Namespace == ns && n.Name == name {
				for _, k := range kinds {
					if k.Kind == n.Backing {
						return []model.Kind{k}
					}
				}
			}
		}
	}
	return kinds
}

// authorize gates one staged change: verb on ref in ns. ok=false means the
// response is written.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, sc scope, verb string, ref ssarRef, ns string) bool {
	if sc.platform {
		return true
	}
	ok, err := s.can(r.Context(), sc.id, sc.cluster, verb, ref, ns)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: you may not %s %s in %s; staging changes needs that right, read access is not enough",
			model.ErrForbidden, verb, ref.resource, ns))
		return false
	}
	return true
}

// authorizeObject gates a change to a network-family object by its draft
// identity; a cluster-scoped one already passed platformScope.
func (s *Server) authorizeObject(w http.ResponseWriter, r *http.Request, sc scope, verb, resource, ns, name string) bool {
	if ns == model.ClusterScopeNS {
		return true
	}
	kinds := s.stagedKinds(resource, ns, name)
	if len(kinds) == 0 {
		fail(w, invalid(fmt.Errorf("%s is not namespace-scoped", resource)))
		return false
	}
	for _, k := range kinds {
		if !s.authorize(w, r, sc, verb, ssarFor(k), ns) {
			return false
		}
	}
	return true
}

// authorizeDraft gates the routes that open a PR from the caller's draft or
// write a project library: authoring rights in any of the project's
// namespaces. Drafts are per user and every entry passed its own per-change
// gate, so a mixed-role caller may still propose what they could stage.
// Discard and unstage stay ungated: they only clear the caller's own local
// draft, so a demoted user can drop leftovers.
func (s *Server) authorizeDraft(w http.ResponseWriter, r *http.Request, sc scope) bool {
	if sc.platform {
		return true
	}
	for _, ns := range sc.proj.Namespaces {
		ok, err := s.can(r.Context(), sc.id, sc.cluster, verbUpdate, ssarVM, ns)
		if err != nil {
			fail(w, s.reviewFailed(r.Context(), sc.id, err))
			return false
		}
		if ok {
			return true
		}
	}
	fail(w, fmt.Errorf("%w: you may not update virtualmachines in any namespace of %s; proposing changes needs edit rights",
		model.ErrForbidden, sc.proj.Name))
	return false
}

// authorizeRepo gates a revert before the coordinator weighs it: authoring
// rights in every namespace of a tenant project, the ones hidden from the
// caller included, since a commit may touch any of them. The coordinator then
// checks each object the revert changes with its exact verb, which is all the
// platform tier's revert passes: its repo has no namespaces of its own.
func (s *Server) authorizeRepo(w http.ResponseWriter, r *http.Request, sc scope) bool {
	// A namespace the caller cannot read is one they cannot author; it is
	// refused without being named.
	if full, ok := s.projectByName(sc.proj.Name); ok && len(full.Namespaces) > len(sc.proj.Namespaces) {
		fail(w, fmt.Errorf("%w: reverting needs edit rights in every namespace of %s", model.ErrForbidden, sc.proj.Name))
		return false
	}
	for _, ns := range sc.proj.Namespaces {
		if !s.authorize(w, r, sc, verbUpdate, ssarVM, ns) {
			return false
		}
	}
	return true
}

// authorConcurrency bounds the per-namespace reviews one frame fans out.
const authorConcurrency = 8

// authorNamespaces is the UI's authoring signal for nss. One cluster-wide
// review answers for every namespace when it allows; otherwise each namespace
// is reviewed, at most authorConcurrency at once. A namespace whose review
// fails is left out of the map, so a timeout reads as unknown, never as no.
func (s *Server) authorNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) map[string]bool {
	out := make(map[string]bool, len(nss))
	if all, err := s.can(ctx, id, c, verbUpdate, ssarVM, ""); err == nil && all {
		for _, ns := range nss {
			out[ns] = true
		}
		return out
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, authorConcurrency)
	)
	for _, ns := range nss {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ok, err := s.can(ctx, id, c, verbUpdate, ssarVM, ns)
			if err != nil {
				return
			}
			mu.Lock()
			out[ns] = ok
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// authorizeTimeout bounds the reviews one gated change asks for: the
// coordinator calls without the request's context.
const authorizeTimeout = 15 * time.Second

// Authorize implements changeset.Authorizer: every access a staged, proposed
// or reverted change makes Argo perform, asked under id's own token through
// the cached reviews, at most authorConcurrency at once. A named access asks
// for the whole resource first, the question the route gates cached, and for
// the one object only when that is denied, as RBAC may grant it by name.
func (s *Server) Authorize(id auth.Identity, need []changeset.Access) error {
	if s.clusterF == nil {
		return unavailable(errors.New("cluster not configured"))
	}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		return unavailable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), authorizeTimeout)
	defer cancel()
	seen := make(map[changeset.Access]bool, len(need))
	var asks []changeset.Access
	for _, a := range need {
		if !seen[a] {
			seen[a] = true
			asks = append(asks, a)
		}
	}
	oks, errs := make([]bool, len(asks)), make([]error, len(asks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, authorConcurrency)
	for i, a := range asks {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			oks[i], errs[i] = s.allows(ctx, id, c, a)
		})
	}
	wg.Wait()
	var denied []string
	for i, a := range asks {
		switch {
		case errs[i] != nil:
			return s.reviewFailed(ctx, id, errs[i])
		case !oks[i]:
			denied = append(denied, a.String())
		}
	}
	if len(denied) > 0 {
		return fmt.Errorf("%w: you may not %s, which this change makes the cluster do once merged",
			model.ErrForbidden, strings.Join(denied, ", or "))
	}
	return nil
}

// allows answers one access, the whole resource before the named object.
func (s *Server) allows(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	ref := ssarRef{a.Group, a.Resource}
	ok, err := s.can(ctx, id, c, a.Verb, ref, a.Namespace)
	if ok || err != nil || a.Name == "" {
		return ok, err
	}
	return s.canNamed(ctx, id, c, a.Verb, ref, a.Namespace, a.Name)
}
