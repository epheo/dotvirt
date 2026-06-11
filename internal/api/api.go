// Package api wires dotvirt's JSON API. The frontend is a separate service
// (SvelteKit), so this serves /api only and applies CORS for the UI origin.
// Every request is identity-scoped: the auth middleware injects the caller's
// Identity, and handlers resolve the caller's projects (and the repo behind each)
// with the caller's own token, so cluster RBAC + per-project repos are the sole
// isolation boundary.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// Draft is the per-(user,project) staging area for pending VM changes, proposed
// as one PR to the project's repo. Implemented by the changeset coordinator. Each
// method takes the caller's Identity and the resolved target project. Request/
// result DTOs live in model so the implementation needn't depend on this package.
type Draft interface {
	StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error)
	StageCreate(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageDelete(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error)
	Unstage(id auth.Identity, proj project.ProjectInfo, namespace, name string) error
	Get(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error)
	Discard(id auth.Identity, proj project.ProjectInfo) error
	Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error)
	VMDrift(proj project.ProjectInfo, namespace, name string) (model.DriftResult, error)
	Adopt(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error)
	Resync(namespace, name string) (model.ResyncResult, error) // SA-identity; no user/project context
	OpenProposal(id auth.Identity, proj project.ProjectInfo) (model.Proposal, bool, error)
}

// StreamHandler upgrades a request to a WebSocket that pushes live inventory.
type StreamHandler interface {
	Handler(w http.ResponseWriter, r *http.Request)
}

// VNCHandler upgrades a request to a WebSocket bridged to a VMI's VNC console.
type VNCHandler interface {
	Handler(w http.ResponseWriter, r *http.Request)
}

// Config carries the non-collaborator settings the handlers need.
type Config struct {
	BaseBranch  string // repo branch the inventory reads + drafts target
	AllowOrigin string // CORS origin for the SvelteKit frontend; empty disables CORS
}

// visibleTTL bounds how long a token's visible-namespace set is reused. The set
// only changes when the user's RBAC does (rare), so a short cache turns the former
// per-build VisibleNamespaces call (a namespace LIST or an SSRR-per-candidate
// probe) into one call per token per window — the only cluster touch left on the
// read path.
const visibleTTL = 30 * time.Second

// Server holds the long-lived collaborators and builds per-request, identity-
// scoped state. It replaces the old interface-bundle Deps.
type Server struct {
	clusterF *cluster.Factory
	state    *clusterstate.State // SA-owned live+topology snapshot; the read path's source
	drift    *argo.DriftCache    // nil when Argo disabled; SA-read, shared across subscribers
	resolver *project.Resolver
	repos    *git.RepoSet
	visible  *ttlcache.Cache[map[string]bool] // per-token visible-namespace set
	draft    Draft
	auth     *auth.Authenticator // nil leaves the API open (dev)
	stream   StreamHandler
	vnc      VNCHandler
	cfg      Config
}

// Deps are the collaborators for NewServer. Nil pieces degrade gracefully.
type Deps struct {
	ClusterFactory *cluster.Factory
	State          *clusterstate.State
	Drift          *argo.DriftCache // shared, TTL-cached SA drift; nil when Argo disabled
	Resolver       *project.Resolver
	Repos          *git.RepoSet
	Draft          Draft
	Auth           *auth.Authenticator
	Stream         StreamHandler
	VNC            VNCHandler
	Config         Config
}

// NewServer builds the API server from its collaborators.
func NewServer(d Deps) *Server {
	return &Server{
		clusterF: d.ClusterFactory,
		state:    d.State,
		drift:    d.Drift,
		resolver: d.Resolver,
		repos:    d.Repos,
		visible:  ttlcache.New[map[string]bool](visibleTTL),
		draft:    d.Draft,
		auth:     d.Auth,
		stream:   d.Stream,
		vnc:      d.VNC,
		cfg:      d.Config,
	}
}

// UseStream attaches the live-inventory WebSocket hub. Set after construction
// because the hub is built over the server's own InventoryForIdentity (chicken-
// and-egg otherwise). nil leaves the stream route unmounted.
func (s *Server) UseStream(h StreamHandler) { s.stream = h }

// UseVNC attaches the VNC-console WebSocket proxy. nil leaves it unmounted.
func (s *Server) UseVNC(h VNCHandler) { s.vnc = h }

// Handler builds the API http.Handler with auth + CORS applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	if s.auth != nil {
		mux.HandleFunc("POST /api/login", s.auth.Login)
		mux.HandleFunc("POST /api/logout", s.auth.Logout)
		mux.HandleFunc("GET /api/me", s.auth.Me)
	}

	mux.HandleFunc("GET /api/inventory", s.handleInventory)
	mux.HandleFunc("GET /api/options", s.handleOptions)
	mux.HandleFunc("GET /api/proposals", s.handleProposals)

	if s.stream != nil {
		mux.HandleFunc("GET /api/inventory/stream", s.stream.Handler)
	}
	if s.vnc != nil {
		mux.HandleFunc("GET /api/vms/{namespace}/{name}/vnc", s.vnc.Handler)
	}

	// Draft changeset routes (all project-scoped).
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/edit", s.handleEdit)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/delete", s.handleDelete)
	mux.HandleFunc("POST /api/vms", s.handleCreate)
	mux.HandleFunc("GET /api/draft", s.handleDraftGet)
	mux.HandleFunc("DELETE /api/draft", s.handleDraftDiscard)
	mux.HandleFunc("DELETE /api/draft/{namespace}/{name}", s.handleUnstage)
	mux.HandleFunc("POST /api/draft/propose", s.handlePropose)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/drift", s.handleDrift)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/events", s.handleEvents)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/adopt", s.handleAdopt)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/resync", s.handleResync)

	// CORS wraps the outside so it can answer preflight OPTIONS without auth; auth
	// gates everything inside.
	var handler http.Handler = mux
	if s.auth != nil {
		handler = s.auth.Middleware(mux)
	}
	return withCORS(s.cfg.AllowOrigin, handler)
}

// withCORS adds CORS headers for the configured UI origin and answers preflight
// OPTIONS requests. Credentials are allowed (the session cookie is sent
// cross-origin in dev), which requires echoing a specific origin — never "*".
func withCORS(origin string, next http.Handler) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readAll reads a request body with a sane size cap.
func readAll(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB
}

// respond writes v as JSON, or the error mapped to a status by its kind.
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// statusFor maps a domain error to an HTTP status by the kind it wraps (see
// model.Err*), defaulting to 500 for anything unclassified.
func statusFor(err error) int {
	switch {
	case errors.Is(err, model.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, model.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, model.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, model.ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
