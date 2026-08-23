package api

import (
	"fmt"
	"net/http"

	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/model"
)

// The Prometheus/Thanos-backed reads (Performance tab, capacity bars, cluster
// rings). All run under the caller's token, so the metrics backend's own RBAC
// gates which namespaces' data is returned. A nil metrics client means the
// feature is off (-metrics-url unset).

// metricsReady 503s (and reports false) when the metrics backend isn't
// configured - the shared preamble of every Thanos-backed handler.
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
// identity + cluster client, and the repo-backed projects' namespaces - the
// same VMs the inventory grid shows - optionally narrowed by ?project= /
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
	if err == nil && r.URL.Query().Get("node") == "" {
		// The phase map comes from kubevirt_vmi_info, and a stopped VM has no
		// VMI - so a 3-VM project read "2 Running" with the third invisible.
		// Fold the snapshot's VMI-less VMs in as stopped. Node scope keeps the
		// metric view: a stopped VM runs on no node.
		foldStoppedVMs(&cs, s.state.VMObjects(nss), s.state.LiveVMs())
	}
	respond(w, cs, err)
}

// foldStoppedVMs adds the VMs without a running instance to the phase map,
// under the lowercase key the kubevirt_vmi_info-derived entries use.
func foldStoppedVMs(cs *model.ClusterSummary, vms []kubevirtcorev1.VirtualMachine, live map[string]clusterstate.LiveVM) {
	stopped := 0
	for i := range vms {
		if live[vms[i].Namespace+"/"+vms[i].Name].Phase == "" {
			stopped++
		}
	}
	if stopped == 0 {
		return
	}
	if cs.VMs == nil {
		cs.VMs = map[string]int{}
	}
	cs.VMs["stopped"] += stopped
}

// drsDeviation maps a committed DRS threshold to its (under, over) percent
// deviation from the mean utilization - KubeVirtRelieveAndMigrate's actual
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
// [mean-under, mean+over] plus counts of workers outside it, kept server-side
// beside the threshold mapping so band semantics have one home.
func foldDRSBand(load *model.HostLoad, threshold string) {
	under, over, ok := drsDeviation(threshold)
	if !ok {
		return
	}
	b := model.HostBand{Low: load.Mean - under, High: load.Mean + over}
	if b.Low < 0 {
		b.Low = 0
	}
	for _, n := range load.Nodes {
		switch {
		case n.Pct > b.High:
			b.Above++
		case n.Pct < b.Low:
			b.Below++
		}
	}
	load.Band = &b
}

// handleHostLoad returns the worker utilization distribution behind the DRS
// balance card. Node-level data: the distribution is cached once for all
// callers, so a node-read SSAR must gate it here - on a cache hit the
// caller's token never reaches Thanos, and without the gate a hit would hand
// a tenant the node data an admin's token fetched. The band reflects the
// platform repo's committed DRS threshold - the configuration merges have
// made real - and is absent until DRS is configured.
func (s *Server) handleHostLoad(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nodeMetricsScope(w, r)
	if !ok {
		return
	}
	load, err := s.metrics.HostLoad(r.Context(), id.Token)
	if err != nil {
		fail(w, err)
		return
	}
	if s.cfg.PlatformRepo != "" && s.draft != nil {
		platform := s.platformProject()
		if st, err := s.draft.DRSState(platform); err == nil && st.Configured && st.Config != nil {
			foldDRSBand(&load, st.Config.Threshold)
		}
	}
	writeJSON(w, http.StatusOK, load)
}

// handleHostCapacity returns each worker's committed-vs-allocatable picture.
// Node-level data cached once for all callers, so the same node-read SSAR
// gate as handleHostLoad applies, for the same reason.
func (s *Server) handleHostCapacity(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nodeMetricsScope(w, r)
	if !ok {
		return
	}
	hc, err := s.metrics.Capacity(r.Context(), id.Token)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hc)
}

// nodeMetricsScope is the shared gate of the node-data handlers: metrics wired,
// caller resolvable, and a node-read SSAR - required because these responses are
// cached once for ALL callers, so on a cache hit the caller's token never
// reaches Thanos and RBAC must be enforced here.
func (s *Server) nodeMetricsScope(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	if !s.metricsReady(w) {
		return auth.Identity{}, false
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return auth.Identity{}, false
	}
	if !s.canReadNodesCached(r.Context(), id, c) {
		http.Error(w, "node metrics require node read access", http.StatusForbidden)
		return auth.Identity{}, false
	}
	return id, true
}

// handleScopeMetrics returns the per-VM top-consumer time-series for a container
// scope - the container Monitor's Performance view.
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

// handleAlarms returns the firing Prometheus alerts across the caller's scope -
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
// namespaces - the project capacity band + container Configure. Read under the
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
