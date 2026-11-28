package api

import (
	"net/http"
	"slices"

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

// desktopsReadable reviews each kind held in namespaces, and the classes
// cluster-wide: those the caller may list, and those it may not. Not
// readKinds: the view would then review them for every token, on clusters
// with no desktops too.
func (s *Server) desktopsReadable(v callerView, namespaces []string) (readable map[desktops.Held]bool, hidden []desktops.Held) {
	// "" is where the classes are held: listsIn grants it only on the
	// cluster-wide review.
	held := s.desktops.HeldIn(append(slices.Clip(namespaces), ""))
	byKind := map[string][]string{}
	for _, h := range held {
		byKind[h.Resource] = append(byKind[h.Resource], h.Namespace)
	}
	readable = map[desktops.Held]bool{}
	for resource, nss := range byKind {
		for _, ns := range v.listsIn(ssarRef{desktops.Group, resource}, nss) {
			readable[desktops.Held{Namespace: ns, Resource: resource}] = true
		}
	}
	for _, h := range held {
		if !readable[h] {
			hidden = append(hidden, h)
		}
	}
	return readable, hidden
}
