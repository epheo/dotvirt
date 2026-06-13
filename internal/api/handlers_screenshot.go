package api

import "net/http"

// handleScreenshot serves a PNG of the VM's graphical console (the Summary's
// console preview), via KubeVirt's vnc/screenshot subresource under the caller's
// token. A non-running VM (or one without a graphics device, or a KubeVirt that
// doesn't expose the subresource) errors — the UI hides the thumbnail then.
func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	png, err := sc.cluster.Screenshot(r.Context(), ns, name)
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store") // a live console frame, never cache
	_, _ = w.Write(png)
}
