// Package api wires dotvirt's JSON API. The frontend is a separate service
// (SvelteKit), so this serves /api only and applies CORS for the UI origin.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// InventoryProvider supplies branch lists and per-branch inventories.
// Implemented by the git plane (internal/git); kept as an interface so the API
// package doesn't depend on go-git directly.
type InventoryProvider interface {
	Branches() ([]string, error)
	Inventory(branch string) (any, error)
}

// EditRequest is the body of an edit: which VM, on which source branch, and
// which fields to change. Power is "On"/"Off"; nil fields are left unchanged.
type EditRequest struct {
	SourceBranch string  `json:"sourceBranch"`
	SourceFile   string  `json:"sourceFile"`
	Power        *string `json:"power,omitempty"`
	CPUCores     *int    `json:"cpuCores,omitempty"`
	Memory       *string `json:"memory,omitempty"`
	Instancetype *string `json:"instancetype,omitempty"`
	Preference   *string `json:"preference,omitempty"`

	SetLabels      map[string]string `json:"setLabels,omitempty"`
	RemoveLabels   []string          `json:"removeLabels,omitempty"`
	AddDisks       []DiskAdd         `json:"addDisks,omitempty"`
	RemoveDisks    []string          `json:"removeDisks,omitempty"`
	AddNetworks    []NetworkAdd      `json:"addNetworks,omitempty"`
	RemoveNetworks []string          `json:"removeNetworks,omitempty"`

	Message string `json:"message,omitempty"` // optional commit message; auto-generated when empty
}

// DiskAdd / NetworkAdd mirror git.DiskAdd / git.NetworkAdd for the request body.
type DiskAdd struct {
	Name string `json:"name"`
	Size string `json:"size"`
}
type NetworkAdd struct {
	Name string `json:"name"`
}

// Editor commits a VM field edit to a feature branch and returns a diff.
// Implemented by the git plane.
type Editor interface {
	EditVM(namespace, name string, req EditRequest) (any, error)
}

// OptionsProvider lists cluster choices for the wizard/editor (instancetypes,
// preferences, OS images, networks). Implemented by the cluster client.
type OptionsProvider interface {
	ListOptions(ctx context.Context) (any, error)
}

// Creator generates a new VM manifest from the wizard spec and commits it to a
// feature branch. spec is the raw JSON wizard body. Implemented by the git plane.
type Creator interface {
	CreateVM(sourceBranch string, spec json.RawMessage) (any, error)
}

// StreamHandler upgrades a request to a WebSocket that pushes live inventory.
// Implemented by internal/stream.Hub.
type StreamHandler interface {
	Handler(w http.ResponseWriter, r *http.Request)
}

// VNCHandler upgrades a request to a WebSocket bridged to a VMI's VNC console.
// Implemented by internal/stream.VNCProxy.
type VNCHandler interface {
	Handler(w http.ResponseWriter, r *http.Request)
}

// Deps are the collaborators the API needs. Nil providers degrade gracefully:
// their routes return 503 so the skeleton runs before later slices land.
type Deps struct {
	Inventory InventoryProvider
	Editor    Editor
	Options   OptionsProvider
	Creator   Creator
	Stream    StreamHandler
	VNC       VNCHandler

	// AllowOrigin is the UI origin permitted via CORS (e.g. http://localhost:5173).
	// Empty disables CORS headers (same-origin or reverse-proxied deployments).
	AllowOrigin string
}

// Handler builds the API http.Handler.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/branches", func(w http.ResponseWriter, r *http.Request) {
		if d.Inventory == nil {
			http.Error(w, "inventory provider not configured", http.StatusServiceUnavailable)
			return
		}
		branches, err := d.Inventory.Branches()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, branches)
	})

	mux.HandleFunc("GET /api/inventory", func(w http.ResponseWriter, r *http.Request) {
		if d.Inventory == nil {
			http.Error(w, "inventory provider not configured", http.StatusServiceUnavailable)
			return
		}
		branch := r.URL.Query().Get("branch")
		inv, err := d.Inventory.Inventory(branch)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, inv)
	})

	if d.Stream != nil {
		// WebSocket: live inventory push. The handler manages its own protocol;
		// it bypasses the JSON CORS wrapper but the upgrader checks origin.
		mux.HandleFunc("GET /api/inventory/stream", d.Stream.Handler)
	}

	if d.VNC != nil {
		// WebSocket: VNC console bridged to KubeVirt's VNC subresource.
		mux.HandleFunc("GET /api/vms/{namespace}/{name}/vnc", d.VNC.Handler)
	}

	mux.HandleFunc("POST /api/vms", func(w http.ResponseWriter, r *http.Request) {
		if d.Creator == nil {
			http.Error(w, "creator not configured", http.StatusServiceUnavailable)
			return
		}
		// Body is the wizard spec plus a sourceBranch field; split them.
		var envelope struct {
			SourceBranch string `json:"sourceBranch"`
		}
		raw, err := readAll(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if envelope.SourceBranch == "" {
			http.Error(w, "sourceBranch is required", http.StatusBadRequest)
			return
		}
		result, err := d.Creator.CreateVM(envelope.SourceBranch, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /api/options", func(w http.ResponseWriter, r *http.Request) {
		if d.Options == nil {
			http.Error(w, "options provider not configured (cluster reads disabled)", http.StatusServiceUnavailable)
			return
		}
		opts, err := d.Options.ListOptions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, opts)
	})

	mux.HandleFunc("POST /api/vms/{namespace}/{name}/edit", func(w http.ResponseWriter, r *http.Request) {
		if d.Editor == nil {
			http.Error(w, "editor not configured (read-only or git auth missing)", http.StatusServiceUnavailable)
			return
		}
		var req EditRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.SourceBranch == "" || req.SourceFile == "" {
			http.Error(w, "sourceBranch and sourceFile are required", http.StatusBadRequest)
			return
		}
		result, err := d.Editor.EditVM(r.PathValue("namespace"), r.PathValue("name"), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	return withCORS(d.AllowOrigin, mux)
}

// withCORS adds CORS headers for the configured UI origin and answers
// preflight OPTIONS requests.
func withCORS(origin string, next http.Handler) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
