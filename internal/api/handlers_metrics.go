package api

import (
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

// The Prometheus/Thanos-backed reads (Performance tab, capacity bars, cluster
// rings). All run under the caller's token, so the metrics backend's own RBAC
// gates which namespaces' data is returned. A nil metrics client means the
// feature is off (-metrics-url unset).

// handleMetrics returns a VM's performance time-series (the Performance tab).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	m, err := s.metrics.VMMetrics(r.Context(), sc.id.Token, ns, name, r.URL.Query().Get("range"))
	respond(w, m, err)
}

// handleVMUsage returns a VM's point-in-time capacity-and-usage (the Summary tab's
// "Capacity and Usage" bars).
func (s *Server) handleVMUsage(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	u, err := s.metrics.VMUsage(r.Context(), sc.id.Token, ns, name)
	respond(w, u, err)
}

// scopeNamespaces resolves a container-scope metrics read's namespaces: the
// repo-backed projects' namespaces — the same VMs the inventory grid shows —
// optionally narrowed by ?project= / ?namespace= so every container level
// (all, project, namespace, node) gets its own view.
func (s *Server) scopeNamespaces(r *http.Request) (auth.Identity, []string, error) {
	id, c, err := s.userCluster(r)
	if err != nil {
		return auth.Identity{}, nil, fmt.Errorf("%w: %v", model.ErrUnavailable, err)
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		return auth.Identity{}, nil, err
	}
	wantProject := r.URL.Query().Get("project")
	wantNamespace := r.URL.Query().Get("namespace")
	var nss []string
	for _, p := range projects {
		if p.Repo == "" || (wantProject != "" && p.Name != wantProject) {
			continue
		}
		for _, n := range p.Namespaces {
			if wantNamespace != "" && n != wantNamespace {
				continue
			}
			nss = append(nss, n)
		}
	}
	return id, nss, nil
}

// handleClusterSummary returns the aggregate capacity view (the "All VMs" cluster
// landing): rings of VM usage vs node-allocatable capacity, VM counts by phase, and
// top-consumer VMs. VM-scoped sums are limited to the caller's visible namespaces.
func (s *Server) handleClusterSummary(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	id, nss, err := s.scopeNamespaces(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	cs, err := s.metrics.ClusterSummary(r.Context(), id.Token, nss, r.URL.Query().Get("node"))
	respond(w, cs, err)
}

// handleScopeMetrics returns the per-VM top-consumer time-series for a container
// scope — the container Monitor's Performance view.
func (s *Server) handleScopeMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	id, nss, err := s.scopeNamespaces(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	m, err := s.metrics.ScopeMetrics(r.Context(), id.Token, nss, r.URL.Query().Get("node"), r.URL.Query().Get("range"))
	respond(w, m, err)
}
