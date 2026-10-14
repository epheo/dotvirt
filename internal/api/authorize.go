package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The tenant authoring gate. Namespace membership (resolveProject) only proves
// the caller can read VMs there; a draft write needs the RBAC the change would
// need if the caller applied it themselves, because the PR opens under
// dotvirt's forge identity and Argo applies it with its own. The platform tier
// keeps its own create-SSAR gates (platformScope), so these pass it through.

const (
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

// canAuthor answers verb on ref in ns under the caller's token, behind the
// rbacVersion-stamped cache.
func (s *Server) canAuthor(ctx context.Context, id auth.Identity, c *cluster.Client, verb string, ref ssarRef, ns string) (bool, error) {
	key := "author\x00" + verb + "\x00" + ref.group + "/" + ref.resource + "\x00" + ns
	return s.ssarChecked(id, key, func() (bool, error) {
		return c.Can(ctx, verb, ref.group, ref.resource, ns)
	})
}

// authorize gates one staged change: verb on ref in ns. ok=false means the
// response is written.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, sc scope, verb string, ref ssarRef, ns string) bool {
	if sc.platform {
		return true
	}
	ok, err := s.canAuthor(r.Context(), sc.id, sc.cluster, verb, ref, ns)
	if err != nil {
		fail(w, unavailable(err))
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
		ok, err := s.canAuthor(r.Context(), sc.id, sc.cluster, verbUpdate, ssarVM, ns)
		if err != nil {
			fail(w, unavailable(err))
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

// authorizeRepo gates a write that rewrites repo content no per-change gate
// saw (revert): authoring rights in every namespace of the project, the ones
// hidden from the caller included, since a commit may touch any of them.
func (s *Server) authorizeRepo(w http.ResponseWriter, r *http.Request, sc scope) bool {
	if sc.platform {
		return true
	}
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
	if all, err := s.canAuthor(ctx, id, c, verbUpdate, ssarVM, ""); err == nil && all {
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
			ok, err := s.canAuthor(ctx, id, c, verbUpdate, ssarVM, ns)
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
