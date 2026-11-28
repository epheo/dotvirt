package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/desktops"
)

// handleDesktops serves the desktops' live state in the caller's project
// namespaces, narrowed by ?project= or ?namespace=. Any caller may ask; each
// kind shows where the caller may list it.
func (s *Server) handleDesktops(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	v, err := s.viewFor(r.Context(), sc.id, sc.cluster)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, s.desktops.Overview(s.desktopsReadable(v, nss)))
}

// desktopsReadable is what the caller may list: each namespaced kind in
// namespaces, and the classes cluster-wide. unlisted are the kinds it may not
// list in each namespace where it may list another. Both come from its own
// rights, asked whether rows exist or not, which a review would tell it
// anyway: no answer depends on objects it may not read. Not readKinds: the
// view would then review them for every token, on clusters with no desktops
// too.
func (s *Server) desktopsReadable(v callerView, namespaces []string) (readable map[desktops.Kind]bool, unlisted []desktops.Kind) {
	if !s.desktops.Started() {
		return nil, nil
	}
	readable = map[desktops.Kind]bool{}
	if v.lists(ssarRef{desktops.Group, desktops.GVRClass.Resource}, "") {
		readable[desktops.Kind{Resource: desktops.GVRClass.Resource}] = true
	}
	lists := map[string]bool{}
	for _, gvr := range desktops.Namespaced {
		for _, ns := range v.listsIn(ssarRef{gvr.Group, gvr.Resource}, namespaces) {
			readable[desktops.Kind{Namespace: ns, Resource: gvr.Resource}] = true
			lists[ns] = true
		}
	}
	for _, ns := range namespaces {
		if !lists[ns] {
			continue
		}
		for _, gvr := range desktops.Namespaced {
			if k := (desktops.Kind{Namespace: ns, Resource: gvr.Resource}); !readable[k] {
				unlisted = append(unlisted, k)
			}
		}
	}
	return readable, unlisted
}
