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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/metrics"
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
	Revert(id auth.Identity, proj project.ProjectInfo, hash string) (model.ProposeResult, error)
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
	BaseBranch        string // repo branch the inventory reads + drafts target
	AllowOrigin       string // CORS origin for the SvelteKit frontend; empty disables CORS
	AppSetPluginToken string // bearer for the ArgoCD ApplicationSet plugin endpoint; empty disables it
	StaticDir         string // built SPA dir to serve at the same origin; empty = dev (SPA on Vite)
	WebhookSecret     string // HMAC secret for the Forgejo webhook endpoint; empty disables it
}

// visibleTTL bounds how long a token's visible-namespace set is reused. The set
// only changes when the user's RBAC does (rare), so a short cache turns the former
// per-build VisibleNamespaces call (a namespace LIST or an SSRR-per-candidate
// probe) into one call per token per window — the only cluster touch left on the
// read path.
const visibleTTL = 30 * time.Second

// optionsTTL caches the wizard catalog (instancetypes/preferences/datasources/
// networks). It's SA-read, identical for every user, and changes rarely — so one
// shared cache spares 4 cluster-wide LISTs on each wizard open.
const optionsTTL = 60 * time.Second

// Server holds the long-lived collaborators and builds per-request, identity-
// scoped state. It replaces the old interface-bundle Deps.
type Server struct {
	clusterF  *cluster.Factory
	state     *clusterstate.State // SA-owned live+topology snapshot; the read path's source
	drift     *argo.DriftCache    // nil when Argo disabled; SA-read, shared across subscribers
	resolver  *project.Resolver
	repos     *git.RepoSet
	visible   *ttlcache.Cache[map[string]bool]  // per-token visible-namespace set
	proposals *ttlcache.Cache[[]model.Proposal] // per-token open-PR set; written by the refresher, read on broadcast
	options   *ttlcache.Cache[model.Options]    // shared wizard catalog (SA-read, identical for all)
	metrics   *metrics.Client                   // Prometheus/Thanos for the Performance tab; nil disables it
	draft     Draft
	auth      *auth.Authenticator // nil leaves the API open (dev)
	stream    StreamHandler
	vnc       VNCHandler
	cfg       Config

	// Proposals-refresher state (see proposals.go): who to refresh, and the
	// coalesced out-of-cycle trigger.
	propMu      sync.Mutex
	propTargets map[string]propTarget
	propNudge   chan struct{}
}

// Deps are the collaborators for NewServer. Nil pieces degrade gracefully.
type Deps struct {
	ClusterFactory *cluster.Factory
	State          *clusterstate.State
	Drift          *argo.DriftCache // shared, TTL-cached SA drift; nil when Argo disabled
	Resolver       *project.Resolver
	Repos          *git.RepoSet
	Metrics        *metrics.Client // Prometheus/Thanos query client; nil disables the Performance tab
	Draft          Draft
	Auth           *auth.Authenticator
	Stream         StreamHandler
	VNC            VNCHandler
	Config         Config
}

// NewServer builds the API server from its collaborators.
func NewServer(d Deps) *Server {
	return &Server{
		clusterF:  d.ClusterFactory,
		state:     d.State,
		drift:     d.Drift,
		resolver:  d.Resolver,
		repos:     d.Repos,
		visible:   ttlcache.New[map[string]bool](visibleTTL),
		proposals: ttlcache.New[[]model.Proposal](proposalsCacheTTL),
		options:   ttlcache.New[model.Options](optionsTTL),
		metrics:   d.Metrics,
		draft:     d.Draft,
		auth:      d.Auth,
		stream:    d.Stream,
		vnc:       d.VNC,
		cfg:       d.Config,

		propTargets: map[string]propTarget{},
		propNudge:   make(chan struct{}, 1),
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
	// ArgoCD ApplicationSet plugin generator (auth: its own shared token, not a
	// user session — exempted in auth.isOpenPath). Emits projects from labels.
	mux.HandleFunc("POST /api/v1/getparams.execute", s.handleAppSetPlugin)
	// Forgejo webhook (auth: HMAC delivery signature, not a user session —
	// exempted in auth.isOpenPath). Pokes the repo's poller for instant updates.
	mux.HandleFunc("POST /api/webhooks/forge", s.handleForgeWebhook)
	mux.HandleFunc("GET /api/proposals", s.handleProposals)
	mux.HandleFunc("GET /api/events", s.handleAllEvents)
	mux.HandleFunc("GET /api/permissions", s.handlePermissions)
	mux.HandleFunc("GET /api/metrics/cluster", s.handleClusterSummary)
	mux.HandleFunc("GET /api/metrics/scope", s.handleScopeMetrics)
	mux.HandleFunc("GET /api/alarms", s.handleAlarms)
	mux.HandleFunc("GET /api/quotas", s.handleQuotas)
	mux.HandleFunc("GET /api/projects/{project}/history", s.handleHistory)
	mux.HandleFunc("POST /api/projects/{project}/revert", s.handleRevert)

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
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/manifest", s.handleManifest)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/events", s.handleEvents)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/usage", s.handleVMUsage)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/adopt", s.handleAdopt)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/resync", s.handleResync)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/migrate", s.handleMigrate)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/pause", s.handlePause)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/unpause", s.handleUnpause)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/snapshots", s.handleSnapshots)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/snapshots", s.handleTakeSnapshot)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/snapshots/{snapshot}/restore", s.handleRestoreSnapshot)
	mux.HandleFunc("DELETE /api/vms/{namespace}/{name}/snapshots/{snapshot}", s.handleDeleteSnapshot)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/clones", s.handleClones)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/clone", s.handleCreateClone)

	// CORS wraps the outside so it can answer preflight OPTIONS without auth; auth
	// gates everything inside.
	var handler http.Handler = mux
	if s.auth != nil {
		handler = s.auth.Middleware(mux)
	}
	apiHandler := withCORS(s.cfg.AllowOrigin, handler)

	// In production the same binary serves the built SPA at the same origin (so
	// ui-origin/CORS is empty). /api/* goes to the API; everything else is a static
	// file or the SPA shell. Dev keeps the SPA on Vite, so StaticDir is empty.
	if s.cfg.StaticDir == "" {
		return apiHandler
	}
	return spaRouter(s.cfg.StaticDir, apiHandler)
}

// spaRouter serves /api/* via the API handler and every other path from the static
// SPA build dir — a real file when one exists, else index.html so client-side
// routes resolve. Static assets bypass auth (the SPA authenticates via /api/login).
func spaRouter(dir string, apiHandler http.Handler) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		clean := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if fi, err := os.Stat(clean); err == nil && !fi.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
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
