package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
)

// Image upload. dotvirt mints the upload-target
// DataVolume + token under the caller's token; the BROWSER then streams the
// image bytes straight to cdi-uploadproxy (which ships open CORS), so multi-GB
// images never pass through dotvirt. Disabled when -upload-proxy-url is unset.

// handleCreateUpload creates the upload-target DataVolume in the caller's project.
func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UploadProxyURL == "" {
		fail(w, fmt.Errorf("%w: image upload not configured", model.ErrUnavailable))
		return
	}
	req, ok := decode[struct {
		Namespace    string `json:"namespace"`
		Name         string `json:"name"`
		Size         string `json:"size"`
		StorageClass string `json:"storageClass"`
	}](w, r)
	if !ok {
		return
	}
	if req.Namespace == "" || req.Name == "" || req.Size == "" {
		fail(w, invalid(errors.New("namespace, name and size are required")))
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(req.Namespace))
	if !ok {
		return
	}
	if err := sc.cluster.CreateUploadDataVolume(r.Context(), req.Namespace, req.Name, req.Size, req.StorageClass); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, model.UploadTarget{Namespace: req.Namespace, Name: req.Name})
}

// handleUploadStatus reports the upload DataVolume's phase + import progress.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	st, err := sc.cluster.UploadStatus(r.Context(), ns, name)
	respond(w, st, err)
}

// handleUploadToken mints the upload token + returns the proxy endpoint the
// browser POSTs the image to.
func (s *Server) handleUploadToken(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UploadProxyURL == "" {
		fail(w, fmt.Errorf("%w: image upload not configured", model.ErrUnavailable))
		return
	}
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	token, err := sc.cluster.CreateUploadToken(r.Context(), ns, name)
	if err != nil {
		fail(w, err)
		return
	}
	url := strings.TrimRight(s.cfg.UploadProxyURL, "/") + "/v1beta1/upload-async"
	writeJSON(w, model.UploadToken{Token: token, UploadURL: url})
}
