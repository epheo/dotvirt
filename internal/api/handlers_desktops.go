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
	// Whether the caller lists a kind anywhere reads its whole view: a page
	// narrowed to a namespace it lists nothing in still says what it may not
	// list there.
	all := repoNamespaces(s.resolver.Resolve(s.state.Namespaces(), v.names))
	writeJSON(w, s.desktops.Overview(s.desktopsReadable(v, nss, all)))
}

// desktopsReadable is what the caller may list: each namespaced kind in scope,
// and the classes cluster-wide. For a caller who may list some kind in all or
// the classes, unlisted are the kinds it may not list in each namespace of
// scope, and the classes when it may not list them. Both come from its own
// rights, asked whether rows exist or not, which a review would tell it
// anyway: no answer depends on objects it may not read. A caller who may list
// none gets neither. Not readKinds: the view would then review them for every
// token, on clusters with no desktops too.
func (s *Server) desktopsReadable(v callerView, scope, all []string) (readable map[desktops.Kind]bool, unlisted []desktops.Kind) {
	if !s.desktops.Started() {
		return nil, nil
	}
	classes := desktops.Kind{Resource: desktops.GVRClass.Resource}
	readable = map[desktops.Kind]bool{}
	if v.lists(ssarRef{desktops.Group, desktops.GVRClass.Resource}, "") {
		readable[classes] = true
	}
	for _, gvr := range desktops.Namespaced {
		for _, ns := range v.listsIn(ssarRef{gvr.Group, gvr.Resource}, scope) {
			readable[desktops.Kind{Namespace: ns, Resource: gvr.Resource}] = true
		}
	}
	if len(readable) == 0 && !s.listsAnyDesktopKind(v, all) {
		return nil, nil
	}
	if !readable[classes] {
		unlisted = append(unlisted, classes)
	}
	for _, ns := range scope {
		for _, gvr := range desktops.Namespaced {
			if k := (desktops.Kind{Namespace: ns, Resource: gvr.Resource}); !readable[k] {
				unlisted = append(unlisted, k)
			}
		}
	}
	return readable, unlisted
}

// listsAnyDesktopKind reports whether the caller may list a namespaced kind in
// one of namespaces. The frame asks the same reviews, so they are cached.
func (s *Server) listsAnyDesktopKind(v callerView, namespaces []string) bool {
	for _, gvr := range desktops.Namespaced {
		if len(v.listsIn(ssarRef{gvr.Group, gvr.Resource}, namespaces)) > 0 {
			return true
		}
	}
	return false
}
