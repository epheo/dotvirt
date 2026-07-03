// Package api wires dotvirt's JSON API. The frontend is a separate service
// (SvelteKit), so this serves /api only and applies CORS for the UI origin.
// Every request is identity-scoped: the auth middleware injects the caller's
// Identity, and handlers resolve the caller's projects (and the repo behind each)
// with the caller's own token, so cluster RBAC + per-project repos are the sole
// isolation boundary.
package api

import (
	"context"
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
	"github.com/epheo/dotvirt/internal/desched"
	"github.com/epheo/dotvirt/internal/eventbus"
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
	StageCreateNetwork(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateUplink(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateEgressFirewall(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateEgressIP(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateExternalRoute(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateNetworkPolicy(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateAdminNetworkPolicy(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageCreateProject(id auth.Identity, commitProj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageEnableDRS(id auth.Identity, proj project.ProjectInfo, spec json.RawMessage) (model.DraftView, error)
	StageDisableDRS(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error)
	DRSState(proj project.ProjectInfo) (model.DRSGitState, error)
	StageDelete(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error)
	Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error
	Get(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error)
	Discard(id auth.Identity, proj project.ProjectInfo) error
	Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error)
	VMDrift(proj project.ProjectInfo, namespace, name string) (model.DriftResult, error)
	Adopt(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error)
	AdoptNamespace(id auth.Identity, proj project.ProjectInfo, namespace string) (model.DraftView, error)
	AdoptProject(id auth.Identity, commitProj, target project.ProjectInfo, owners []string) (model.DraftView, error)
	Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error) // SA-identity; no user/project context
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
	UploadProxyURL    string // cdi-uploadproxy base for image uploads; empty disables it
	PlatformRepo      string // platform-tier repo for cluster-scoped + tenancy manifests; empty disables those creates
}

// visibleTTL bounds a token's cached visible-namespace set. The RBAC version stamp
// (bus.Version(RBACChanged, NamespaceChanged)) makes the common case instant: a
// RoleBinding or project-namespace change invalidates the entry immediately, lazily,
// that token only — no all-token herd. But the version watches only namespaced
// RoleBindings + namespaces; a token's visibility can ALSO change via a
// ClusterRoleBinding/ClusterRole, which the version does NOT observe. The TTL is the
// backstop that bounds staleness for those un-watched RBAC sources, so it stays short
// — long enough to spare the periodic SelfSubjectRulesReview on quiet tokens, short
// enough that a cluster-level revocation can't linger.
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
	drift     *argo.Snapshot      // nil when Argo disabled; SA-owned, watch-fed Application snapshot
	desched   *desched.Snapshot   // nil disables the DRS live plane; SA-owned KubeDescheduler snapshot
	bus       *eventbus.Bus       // change-version source for the version-stamped auth caches
	resolver  *project.Resolver
	repos     *git.RepoSet
	visible   *ttlcache.Cache[visibleSet]             // per-token visible-namespace set, RBAC-version-stamped
	platform  *ttlcache.Cache[platformAuth]           // per-token platform-author SSAR, RBAC-version-stamped
	proposals *ttlcache.Cache[[]model.Proposal]       // per-token open-PR set; written by the refresher, read on broadcast
	options   *ttlcache.Cache[model.Options]          // shared wizard catalog (SA-read, identical for all)
	networks  *ttlcache.Cache[model.NetworkInventory] // shared network catalog (SA-read; per-tenant scoping at serve time)
	metrics   *metrics.Client                         // Prometheus/Thanos for the Performance tab; nil disables it
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

// Deps are the collaborators for NewServer. Nil pieces degrade gracefully. The
// stream + VNC handlers aren't here: they're wired post-construction via
// UseStream/UseVNC, because the hub is built over the server's own
// InventoryForIdentity (chicken-and-egg otherwise).
type Deps struct {
	ClusterFactory *cluster.Factory
	State          *clusterstate.State
	Drift          *argo.Snapshot    // SA-owned drift snapshot (watch-fed); nil when Argo disabled
	Desched        *desched.Snapshot // SA-owned KubeDescheduler snapshot; nil disables the DRS live plane
	Bus            *eventbus.Bus     // change-version source for version-stamped caches
	Resolver       *project.Resolver
	Repos          *git.RepoSet
	Metrics        *metrics.Client // Prometheus/Thanos query client; nil disables the Performance tab
	Draft          Draft
	Auth           *auth.Authenticator
	Config         Config
}

// NewServer builds the API server from its collaborators.
func NewServer(d Deps) *Server {
	return &Server{
		clusterF:  d.ClusterFactory,
		state:     d.State,
		drift:     d.Drift,
		desched:   d.Desched,
		bus:       d.Bus,
		resolver:  d.Resolver,
		repos:     d.Repos,
		visible:   ttlcache.New[visibleSet](visibleTTL),
		platform:  ttlcache.New[platformAuth](visibleTTL),
		proposals: ttlcache.New[[]model.Proposal](proposalsCacheTTL),
		options:   ttlcache.New[model.Options](optionsTTL),
		networks:  ttlcache.New[model.NetworkInventory](optionsTTL),
		metrics:   d.Metrics,
		draft:     d.Draft,
		auth:      d.Auth,
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
	mux.HandleFunc("GET /api/networks", s.handleNetworks)
	mux.HandleFunc("POST /api/networks", s.handleCreateNetwork)
	mux.HandleFunc("POST /api/uplinks", s.handleCreateUplink)
	mux.HandleFunc("POST /api/egressfirewalls", s.handleCreateEgressFirewall)
	mux.HandleFunc("POST /api/egressips", s.handleCreateEgressIP)
	mux.HandleFunc("POST /api/externalroutes", s.handleCreateExternalRoute)
	mux.HandleFunc("POST /api/networkpolicies", s.handleCreateNetworkPolicy)
	mux.HandleFunc("POST /api/adminnetworkpolicies", s.handleCreateAdminNetworkPolicy)
	mux.HandleFunc("POST /api/namespaces", s.handleCreateNamespace)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/drs", s.handleDRS)
	mux.HandleFunc("POST /api/drs", s.handleDRSEnable)
	mux.HandleFunc("DELETE /api/drs", s.handleDRSDisable)
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
	mux.HandleFunc("GET /api/nodes/{node}", s.handleNodeInfo)
	mux.HandleFunc("POST /api/nodes/{node}/cordon", s.handleNodeCordon)
	mux.HandleFunc("POST /api/uploads", s.handleCreateUpload)
	mux.HandleFunc("GET /api/uploads/{namespace}/{name}", s.handleUploadStatus)
	mux.HandleFunc("POST /api/uploads/{namespace}/{name}/token", s.handleUploadToken)
	mux.HandleFunc("GET /api/projects/{project}/history", s.handleHistory)
	mux.HandleFunc("POST /api/projects/{project}/revert", s.handleRevert)
	mux.HandleFunc("POST /api/projects/{project}/adopt", s.handleAdoptProject)

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
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/screenshot", s.handleScreenshot)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/usage", s.handleVMUsage)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/adopt", s.handleAdopt)
	mux.HandleFunc("POST /api/namespaces/{namespace}/adopt", s.handleAdoptNamespace)
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
