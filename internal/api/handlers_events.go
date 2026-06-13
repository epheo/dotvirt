package api

import "net/http"

// Kubernetes Events reads (the Monitor tabs + the dock's Events lane), under the
// caller's token.

// handleEvents lists recent Events for a VM (+ its VMI) — the per-VM Monitor tab.
// resolveProject gates the namespace.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	events, err := sc.cluster.ListEvents(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	respond(w, events, err)
}

// handleAllEvents lists recent VM/VMI Events across the caller's visible
// namespaces — the dock's Events lane. scopeNamespaces bounds it to the
// repo-backed projects' namespaces (the managed inventory), not every visible
// namespace — listing events across an admin's whole cluster takes many
// seconds and matches no VM the UI shows.
func (s *Server) handleAllEvents(w http.ResponseWriter, r *http.Request) {
	sc, nss, err := s.scopeNamespaces(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	events, err := sc.cluster.ListVMEvents(r.Context(), nss)
	respond(w, events, err)
}
