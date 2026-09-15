package api

import "net/http"

// handleScreenshot serves a PNG of the VM's graphical console (the Summary's
// console preview), via KubeVirt's vnc/screenshot subresource under the caller's
// token. A non-running VM (or one without a graphics device, or a KubeVirt that
// doesn't expose the subresource) errors - the UI hides the thumbnail then.
func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	png, err := sc.cluster.Screenshot(r.Context(), ns, name)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store") // a live console frame, never cache
	_, _ = w.Write(png)
}
