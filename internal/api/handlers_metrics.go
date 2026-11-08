package api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/model"
)

// The Prometheus/Thanos-backed reads (Performance tab, capacity bars, cluster
// rings). All run under the caller's token, so the metrics backend's own RBAC
// gates which namespaces' data is returned. A nil metrics client means the
// feature is off (-metrics-url unset): the VM and scope views answer it as
// not available, the node views and alarms as 503.

// metricsWide is the check the Thanos querier's cluster-wide port (9091) makes
// of every request: its kube-rbac-proxy asks this SubjectAccessReview, the verb
// taken from the HTTP method (a query is a GET). cluster-monitoring-view and
// openshift-monitoring's cluster-monitoring-metrics-api grant it.
var metricsWide = question{
	Verb: verbGet, Group: "monitoring.coreos.com", Resource: "prometheuses", Subresource: "api",
	Namespace: "openshift-monitoring", Name: "k8s",
}

// metricsCaller is how id's metrics reads reach the querier: one cluster-wide
// query when id holds metricsWide, else one tenancy-port query per namespace,
// which the caller's view of that namespace grants. A failed review reads as
// no: the narrower path, never the wider.
func (s *Server) metricsCaller(ctx context.Context, id auth.Identity, c *cluster.Client) metrics.Caller {
	if s.metrics == nil {
		return metrics.Caller{Token: id.Token}
	}
	wide, _ := s.can(ctx, id, c, metricsWide)
	return metrics.Caller{Token: id.Token, Wide: wide}
}

// metricsReady 503s (and reports false) when the metrics backend isn't
// configured - the preamble of the node views and alarms.
func (s *Server) metricsReady(w http.ResponseWriter) bool {
	if s.metrics == nil {
		fail(w, fmt.Errorf("%w: metrics not configured", model.ErrUnavailable))
		return false
	}
	return true
}

// handleMetrics returns a VM's performance time-series (the Performance tab).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	who := s.metricsCaller(r.Context(), sc.id, sc.cluster)
	m, err := s.metrics.VMMetrics(r.Context(), who, ns, name, r.URL.Query().Get("range"))
	respond(w, m, err)
}

// handleVMUsage returns a VM's point-in-time capacity-and-usage (the Summary tab's
// "Capacity and Usage" bars).
func (s *Server) handleVMUsage(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	who := s.metricsCaller(r.Context(), sc.id, sc.cluster)
	u, err := s.metrics.VMUsage(r.Context(), who, ns, name)
	respond(w, u, err)
}

// handleClusterSummary returns the aggregate capacity view (the "All VMs" cluster
// landing): rings of VM usage vs node-allocatable capacity, VM counts by phase, and
// top-consumer VMs. VM-scoped sums are limited to the caller's visible namespaces.
func (s *Server) handleClusterSummary(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	who := s.metricsCaller(r.Context(), sc.id, sc.cluster)
	// The node-allocatable totals are node data, gated like handleHostLoad.
	nodeTotals := s.capable(r.Context(), sc.id, sc.cluster, ask(verbList, ssarNodes, ""))
	cs, err := s.metrics.ClusterSummary(r.Context(), who, nss, r.URL.Query().Get("node"), nodeTotals)
	hasPhases := cs.Unavailable == "" || cs.Unavailable == model.MetricsPartial
	if err == nil && hasPhases && r.URL.Query().Get("node") == "" {
		// The phase map comes from kubevirt_vmi_info, and a stopped VM has no
		// VMI - so a 3-VM project read "2 Running" with the third invisible.
		// Fold the snapshot's VMI-less VMs in as stopped. Node scope keeps the
		// metric view: a stopped VM runs on no node.
		foldStoppedVMs(&cs, s.state.StoppedVMs(nss))
	}
	respond(w, cs, err)
}

// foldStoppedVMs adds the VMs without a running instance to the phase map,
// under the lowercase key the kubevirt_vmi_info-derived entries use. The map
// is cloned first: cs came from the metrics cache, which every caller shares.
func foldStoppedVMs(cs *model.ClusterSummary, stopped int) {
	if stopped == 0 {
		return
	}
	cs.VMs = maps.Clone(cs.VMs)
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
// balance card. Node-level data, gated by nodeMetricsScope. The band reflects
// the platform repo's committed DRS threshold - the configuration merges have
// made real - and is absent until DRS is configured.
func (s *Server) handleHostLoad(w http.ResponseWriter, r *http.Request) {
	who, ok := s.nodeMetricsScope(w, r)
	if !ok {
		return
	}
	load, err := s.metrics.HostLoad(r.Context(), who)
	if err != nil {
		fail(w, err)
		return
	}
	if s.cfg.PlatformRepo != "" && len(load.Nodes) > 0 {
		platform := s.platformProject()
		if st, err := s.reader.DRSState(platform); err == nil && st.Configured && st.Config != nil {
			foldDRSBand(&load, st.Config.Threshold)
		}
	}
	writeJSON(w, load)
}

// handleHostCapacity returns each worker's committed-vs-allocatable picture.
// Node-level data, gated by nodeMetricsScope.
func (s *Server) handleHostCapacity(w http.ResponseWriter, r *http.Request) {
	who, ok := s.nodeMetricsScope(w, r)
	if !ok {
		return
	}
	hc, err := s.metrics.Capacity(r.Context(), who)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, hc)
}

// nodeMetricsScope is the shared gate of the node-data handlers: metrics wired,
// caller resolvable, a node-read SSAR, and the caller's metrics reach. The
// responses are cached for every wide caller, so on a cache hit the caller's
// token never reaches Thanos: node read decides who sees node data here, and
// only a wide caller is served it (metrics.HostLoad).
func (s *Server) nodeMetricsScope(w http.ResponseWriter, r *http.Request) (metrics.Caller, bool) {
	if !s.metricsReady(w) {
		return metrics.Caller{}, false
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return metrics.Caller{}, false
	}
	ok, err := s.can(r.Context(), id, c, refQ(verbList, ssarNodes, ""))
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), id, err))
		return metrics.Caller{}, false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: node metrics require node read access", model.ErrForbidden))
		return metrics.Caller{}, false
	}
	return s.metricsCaller(r.Context(), id, c), true
}

// handleScopeMetrics returns the per-VM top-consumer time-series for a container
// scope - the container Monitor's Performance view.
func (s *Server) handleScopeMetrics(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	who := s.metricsCaller(r.Context(), sc.id, sc.cluster)
	m, err := s.metrics.ScopeMetrics(r.Context(), who, nss, r.URL.Query().Get("node"), r.URL.Query().Get("range"))
	respond(w, m, err)
}

// handleAlarms returns the firing Prometheus alerts across the caller's scope -
// the dock's Alarms tab + header badge.
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if !s.metricsReady(w) {
		return
	}
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	a, err := s.metrics.Alerts(r.Context(), s.metricsCaller(r.Context(), sc.id, sc.cluster), nss)
	respond(w, a, err)
}

// handleQuotas returns the ResourceQuotas across a container scope's
// namespaces - the project capacity band + container Configure. Read under the
// caller's token, so RBAC gates which namespaces' quotas are visible.
func (s *Server) handleQuotas(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	q, err := sc.cluster.ListQuotas(r.Context(), nss)
	respond(w, q, err)
}

// handleNetworkTraffic ranks the VMs on one network by traffic over the last
// hour: the network page's Top VMs. The network resolves through the
// caller-scoped catalog and its VMs through the caller's visible namespaces;
// the query runs under the caller's token like every metrics read.
func (s *Server) handleNetworkTraffic(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	nets := s.callerNetworks(r.Context(), id, c, visible)
	key := r.URL.Query().Get("network")
	target, ok := model.Network{}, key == podNetworkKey
	if !ok {
		if target, ok = findNetwork(nets, key); !ok {
			fail(w, fmt.Errorf("%w: network %q", model.ErrNotFound, key))
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	nss := make([]string, 0, len(visible))
	for ns, ok := range visible {
		if ok {
			nss = append(nss, ns)
		}
	}
	att := networkAttachments(s.state.VMObjects(nss), nets, target)
	nt, err := s.metrics.NetworkTraffic(r.Context(), s.metricsCaller(r.Context(), id, c), att, limit)
	nt.Network = key
	respond(w, nt, err)
}

// podNetworkKey is the UI's key for the cluster's built-in pod network.
const podNetworkKey = "Pod network"

// networkAttachments lists the VM interfaces attached to target (the pod
// network when target is the zero Network), resolving each interface the way
// the UI does: a pod interface rides its namespace's primary network when one
// exists, a multus reference matches a project network by "namespace/name"
// and a shared network by name.
func networkAttachments(vms []kubevirtcorev1.VirtualMachine, nets []model.Network, target model.Network) []metrics.Attachment {
	same := func(n *model.Network) bool {
		if target.Name == "" {
			return n == nil
		}
		return n != nil && n.Name == target.Name && n.Namespace == target.Namespace && n.Scope == target.Scope
	}
	primary := func(ns string) *model.Network {
		for i := range nets {
			n := &nets[i]
			if n.Kind == model.NetworkDefault && (n.Namespace == ns || slices.Contains(n.Namespaces, ns)) {
				return n
			}
		}
		return nil
	}
	byRef := func(ns, ref string) *model.Network {
		if !strings.Contains(ref, "/") {
			ref = ns + "/" + ref
		}
		base := ref[strings.LastIndex(ref, "/")+1:]
		for i := range nets {
			if nets[i].AttachRef == ref {
				return &nets[i]
			}
		}
		for i := range nets {
			if nets[i].Scope == model.ScopeShared && nets[i].Name == base {
				return &nets[i]
			}
		}
		return nil
	}
	var out []metrics.Attachment
	for _, vm := range vms {
		if vm.Spec.Template == nil {
			continue
		}
		for _, n := range vm.Spec.Template.Spec.Networks {
			var on *model.Network
			switch {
			case n.Pod != nil:
				on = primary(vm.Namespace)
			case n.Multus != nil:
				if on = byRef(vm.Namespace, n.Multus.NetworkName); on == nil {
					continue // an unknown network is no network's traffic
				}
			default:
				continue
			}
			if same(on) {
				out = append(out, metrics.Attachment{Namespace: vm.Namespace, Name: vm.Name, Interface: n.Name})
			}
		}
	}
	return out
}
