package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// Image upload. dotvirt mints the upload-target
// DataVolume + token under the caller's token; the BROWSER then streams the
// image bytes straight to cdi-uploadproxy (which ships open CORS), so multi-GB
// images never pass through dotvirt. Disabled when -upload-proxy-url is unset.
// An import has CDI download the image itself and needs no proxy.

// handleCreateUpload creates the upload-target DataVolume in the caller's
// project; with bootImage it also registers the volume as a boot image. The
// whole action is gated first, the questions NamespaceCaps.DataVolumeCreate
// and BootImageCreate ask, so a refusal creates nothing: without the
// upload-token right a DataVolume would wait for bytes that can never come.
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
		BootImage    bool   `json:"bootImage"`
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
	act := dataVolumeAction(req.Namespace)
	if req.BootImage {
		act = bootImageAction(req.Namespace)
	}
	if !s.gate(w, r, sc, act) {
		return
	}
	if err := sc.cluster.CreateUploadDataVolume(r.Context(), req.Namespace, req.Name, req.Size, req.StorageClass); err != nil {
		fail(w, err)
		return
	}
	target := model.UploadTarget{Namespace: req.Namespace, Name: req.Name}
	if req.BootImage {
		if err := sc.cluster.CreateBootImage(r.Context(), req.Namespace, req.Name, nil); err != nil {
			fail(w, fmt.Errorf("the disk %s was created but not registered as a boot image: %w", req.Name, err))
			return
		}
		target.Image = req.Namespace + "/" + req.Name
		s.options.Delete("all")
	}
	writeJSON(w, target)
}

// handleCreateImport creates a boot image the cluster downloads itself: a
// DataVolume importing from a registry or a URL, and the DataSource over it,
// both under the caller's token and gated whole first, so a refusal creates
// nothing. CDI's importer fetches the source from inside the cluster, so no
// upload proxy is needed: it runs where upload is off.
func (s *Server) handleCreateImport(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[struct {
		Namespace    string `json:"namespace"`
		Name         string `json:"name"`
		Size         string `json:"size"`
		StorageClass string `json:"storageClass"`
		Kind         string `json:"kind"` // model.ImportRegistry | model.ImportURL
		Source       string `json:"source"`
	}](w, r)
	if !ok {
		return
	}
	if req.Namespace == "" || req.Name == "" || req.Size == "" {
		fail(w, invalid(errors.New("namespace, name and size are required")))
		return
	}
	if err := errors.Join(
		validate.RequireDNS1123("name", req.Name),
		validate.RequireQuantity("size", req.Size),
	); err != nil {
		fail(w, err)
		return
	}
	url, err := validate.ImportSource(req.Kind, req.Source)
	if err != nil {
		fail(w, err)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(req.Namespace))
	if !ok {
		return
	}
	if !s.gate(w, r, sc, bootImageImportAction(req.Namespace)) {
		return
	}
	if err := sc.cluster.CreateImportDataVolume(r.Context(), req.Namespace, req.Name, req.Size, req.StorageClass, req.Kind, url); err != nil {
		fail(w, err)
		return
	}
	if err := sc.cluster.CreateBootImage(r.Context(), req.Namespace, req.Name, model.ImportFrom(req.Kind, url)); err != nil {
		fail(w, fmt.Errorf("the disk %s is importing but not registered as a boot image: %w", req.Name, err))
		return
	}
	s.options.Delete("all")
	writeJSON(w, model.UploadTarget{Namespace: req.Namespace, Name: req.Name, Image: req.Namespace + "/" + req.Name})
}

// handleDeleteBootImage removes a boot image that never finished (a failed
// or abandoned download or upload), under the caller's token and gated
// whole first, so a refusal deletes nothing. The cluster call refuses a
// ready boot image and any shape dotvirt does not make.
func (s *Server) handleDeleteBootImage(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmCallerScope(w, r)
	if !ok {
		return
	}
	if !s.gate(w, r, sc, bootImageDeleteAction(ns)) {
		return
	}
	note, err := sc.cluster.DeleteBootImage(r.Context(), ns, name)
	s.options.Delete("all")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, model.BootImageRemoval{Note: note})
}

// handleUploadStatus reports the upload DataVolume's phase + import progress.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmCallerScope(w, r)
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
	sc, ns, name, ok := s.vmCallerScope(w, r)
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
