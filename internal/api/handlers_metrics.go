package api

import (
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The Prometheus/Thanos-backed reads (Performance tab, capacity bars, cluster
// rings). All run under the caller's token, so the metrics backend's own RBAC
// gates which namespaces' data is returned. A nil metrics client means the
// feature is off (-metrics-url unset).

// metricsReady 503s (and reports false) when the metrics backend isn't
// configured — the shared preamble of every Thanos-backed handler.
func (s *Server) metricsReady(w http.ResponseWriter) bool {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// handleMetrics returns a VM's performance time-series (the Performance tab).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
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
	if !s.metricsReady(w) {
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

// scopeNamespaces resolves a container-scope read's preamble: the caller's
// identity + cluster client, and the repo-backed projects' namespaces — the
// same VMs the inventory grid shows — optionally narrowed by ?project= /
// ?namespace= so every container level (all, project, namespace, node) gets
// its own view.
func (s *Server) scopeNamespaces(r *http.Request) (scope, []string, error) {
	id, c, err := s.userCluster(r)
	if err != nil {
		return scope{}, nil, fmt.Errorf("%w: %v", model.ErrUnavailable, err)
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		return scope{}, nil, err
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
	return scope{id: id, cluster: c}, nss, nil
}

// handleClusterSummary returns the aggregate capacity view (the "All VMs" cluster
// landing): rings of VM usage vs node-allocatable capacity, VM counts by phase, and
// top-consumer VMs. VM-scoped sums are limited to the caller's visible namespaces.
func (s *Server) handleClusterSummary(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
		return
	}
	sc, nss, err := s.scopeNamespaces(r)
	if err != nil {
		fail(w, err)
		return
	}
	cs, err := s.metrics.ClusterSummary(r.Context(), sc.id.Token, nss, r.URL.Query().Get("node"))
	respond(w, cs, err)
}

// drsDeviation maps a committed DRS threshold to its (under, over) percent
// deviation from the mean utilization — KubeVirtRelieveAndMigrate's actual
// trigger band. AsymmetricLow flags only clearly-hot nodes, so anything below
// the mean already counts as a migration target (under = 0).
func drsDeviation(threshold string) (under, over float64, ok bool) {
	switch threshold {
	case "AsymmetricLow":
		return 0, 10, true
	case "Low":
		return 10, 10, true
	case "Medium":
		return 20, 20, true
	case "High":
		return 30, 30, true
	}
	return 0, 0, false
}

// foldDRSBand attaches the DRS action band to a host distribution: the window
// [mean-under, mean+over] plus exact counts of workers outside it (the
// histogram's 10%-wide buckets can't count against arbitrary band edges).
func foldDRSBand(load *model.HostLoad, pcts []float64, threshold string) {
	under, over, ok := drsDeviation(threshold)
	if !ok {
		return
	}
	b := model.HostBand{Low: load.Mean - under, High: load.Mean + over}
	if b.Low < 0 {
		b.Low = 0
	}
	for _, p := range pcts {
		switch {
		case p > b.High:
			b.Above++
		case p < b.Low:
			b.Below++
		}
	}
	load.Band = &b
}

// handleHostLoad returns the worker utilization distribution behind the DRS
// balance card. Node-level data: the distribution is cached once for all
// callers, so a node-read SSAR must gate it here — on a cache hit the
// caller's token never reaches Thanos, and without the gate a hit would hand
// a tenant the node data an admin's token fetched. The band reflects the
// platform repo's committed DRS threshold — the configuration merges have
// made real — and is absent until DRS is configured.
func (s *Server) handleHostLoad(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
		return
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	if !s.canReadNodesCached(r.Context(), id, c) {
		http.Error(w, "node metrics require node read access", http.StatusForbidden)
		return
	}
	load, pcts, err := s.metrics.HostLoad(r.Context(), id.Token)
	if err != nil {
		fail(w, err)
		return
	}
	if s.cfg.PlatformRepo != "" && s.draft != nil {
		platform := project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}
		if st, err := s.draft.DRSState(platform); err == nil && st.Configured && st.Config != nil {
			foldDRSBand(&load, pcts, st.Config.Threshold)
		}
	}
	writeJSON(w, http.StatusOK, load)
}

// handleScopeMetrics returns the per-VM top-consumer time-series for a container
// scope — the container Monitor's Performance view.
func (s *Server) handleScopeMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
		return
	}
	sc, nss, err := s.scopeNamespaces(r)
	if err != nil {
		fail(w, err)
		return
	}
	m, err := s.metrics.ScopeMetrics(r.Context(), sc.id.Token, nss, r.URL.Query().Get("node"), r.URL.Query().Get("range"))
	respond(w, m, err)
}

// handleAlarms returns the firing Prometheus alerts across the caller's scope —
// the dock's Alarms tab + header badge.
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
		return
	}
	sc, nss, err := s.scopeNamespaces(r)
	if err != nil {
		fail(w, err)
		return
	}
	a, err := s.metrics.Alerts(r.Context(), sc.id.Token, nss)
	respond(w, a, err)
}

// handleQuotas returns the ResourceQuotas across a container scope's
// namespaces — the project capacity band + container Configure. Read under the
// caller's token, so RBAC gates which namespaces' quotas are visible.
func (s *Server) handleQuotas(w http.ResponseWriter, r *http.Request) {
	sc, nss, err := s.scopeNamespaces(r)
	if err != nil {
		fail(w, err)
		return
	}
	q, err := sc.cluster.ListQuotas(r.Context(), nss)
	respond(w, q, err)
}
