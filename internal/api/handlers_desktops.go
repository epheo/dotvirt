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

// desktopsReadable reviews each kind present in namespaces, and the classes
// cluster-wide. Seeing a namespace means listing its VMs, which says nothing
// of its desktops. Not readKinds: the view would then review them for every
// token, on clusters with no desktops too.
func (s *Server) desktopsReadable(v callerView, namespaces []string) map[desktops.Held]bool {
	// "" is where the classes are held: listsIn grants it only on the
	// cluster-wide review.
	byKind := map[string][]string{}
	for _, h := range s.desktops.HeldIn(append(slices.Clip(namespaces), "")) {
		byKind[h.Resource] = append(byKind[h.Resource], h.Namespace)
	}
	out := map[desktops.Held]bool{}
	for resource, nss := range byKind {
		for _, ns := range v.listsIn(ssarRef{desktops.Group, resource}, nss) {
			out[desktops.Held{Namespace: ns, Resource: resource}] = true
		}
	}
	return out
}
