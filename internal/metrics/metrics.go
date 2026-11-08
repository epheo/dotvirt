// Package metrics queries a Prometheus-compatible endpoint (OpenShift's Thanos
// querier) for a VM's performance time-series - the data behind the Performance
// tab. It runs a fixed, curated set of range queries per VM under the caller's
// token (so the metrics backend's own RBAC is the access gate) and shapes the
// results into chart-ready series aligned on a shared time axis.
package metrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/epheo/dotvirt/pkg/forge"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// Result caches are keyed by QUERY PARAMETERS, not the caller's token: the data for
// a VM (or a scope) is identical for any user authorized to see it, whether one
// cluster-wide query or a tenancy query per namespace fetched it, and the API
// handler has already gated access before calling here. So same-scope users and
// re-mounts share one Thanos fan-out instead of each re-querying, and concurrent
// misses on one key share one fill. Every input that changes what a caller may
// see (namespace set, node-read access) is part of the key, and a result with
// any failed query is never cached: one caller's Forbidden must not become
// another's zeros. TTLs are short (well under the UI's 30s poll) to stay fresh
// while absorbing bursts.
const (
	vmTTL      = 15 * time.Second
	clusterTTL = 20 * time.Second
	// viewTimeout bounds one view's whole fan-out: the queries still waiting
	// past it fail, and the view answers with what it has.
	viewTimeout = 20 * time.Second
)

// Client talks to a Prometheus query API: OpenShift's Thanos querier, its
// cluster-wide port and its tenancy port. A nil Client is metrics switched
// off: every view taking a Caller answers model.MetricsUnconfigured.
type Client struct {
	base      string
	tenancy   string // "" when no tenancy endpoint is configured
	http      *http.Client
	vmMetrics *ttlcache.Cache[model.VMMetrics]
	vmUsage   *ttlcache.Cache[model.VMUsage]
	cluster   *ttlcache.Cache[model.ClusterSummary]
	scope     *ttlcache.Cache[model.VMMetrics]
	alerts    *ttlcache.Cache[alertRows]
	hosts     *ttlcache.Cache[model.HostLoad]
	capacity  *ttlcache.Cache[model.HostCapacity]
	traffic   *ttlcache.Cache[model.NetworkTraffic]
}

// New builds a Client for the cluster-wide query API at baseURL and the
// tenancy one at tenancyURL (the thanos-querier service's ports 9091 and
// 9092). caPath, when set, is a PEM bundle to trust for both - in-cluster,
// the mounted service-CA that signs thanos-querier's serving cert, so the
// connection needn't be insecure. insecure skips TLS verification instead
// (self-signed dev Route). Returns (nil, nil) when baseURL is empty - the
// API treats a nil client as "Performance disabled".
func New(baseURL, tenancyURL, caPath string, insecure bool) (*Client, error) {
	if baseURL == "" {
		return nil, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	switch {
	case caPath != "":
		if pool := forge.RootCAs("metrics", caPath); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool}
		}
	case insecure:
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		base:      strings.TrimRight(baseURL, "/"),
		tenancy:   strings.TrimRight(tenancyURL, "/"),
		http:      &http.Client{Timeout: 20 * time.Second, Transport: tr},
		vmMetrics: ttlcache.New[model.VMMetrics](vmTTL),
		vmUsage:   ttlcache.New[model.VMUsage](vmTTL),
		cluster:   ttlcache.New[model.ClusterSummary](clusterTTL),
		scope:     ttlcache.New[model.VMMetrics](clusterTTL),
		alerts:    ttlcache.New[alertRows](clusterTTL),
		hosts:     ttlcache.New[model.HostLoad](clusterTTL),
		capacity:  ttlcache.New[model.HostCapacity](clusterTTL),
		traffic:   ttlcache.New[model.NetworkTraffic](clusterTTL),
	}, nil
}

// unserved is why who gets no metrics before any query is sent: no backend,
// or no tenancy endpoint for a caller who may not query cluster-wide.
func (c *Client) unserved(who Caller) model.MetricsReason {
	if c == nil || !who.Wide && c.tenancy == "" {
		return model.MetricsUnconfigured
	}
	return ""
}

// cached serves key from cache, else fills it once for every concurrent
// caller of key under viewTimeout, caching only a whole result.
func cached[V any](ctx context.Context, cache *ttlcache.Cache[V], key string, fill func(context.Context) (V, bool)) (V, error) {
	if v, ok := cache.Get(key); ok {
		return v, nil
	}
	v, err := cache.Do(ctx, key, func(ctx context.Context) (V, error) {
		if v, ok := cache.Get(key); ok {
			return v, nil
		}
		ctx, cancel := context.WithTimeout(ctx, viewTimeout)
		defer cancel()
		v, whole := fill(ctx)
		if whole {
			cache.Put(key, v)
		}
		return v, nil
	})
	if err != nil {
		// The fill returns no error: the caller left, or the fill panicked.
		return v, fmt.Errorf("%w: %v", model.ErrUnavailable, err)
	}
	return v, nil
}

// vmSelector scopes a VM metric to namespaces (+ a node when drilling into
// one).
func vmSelector(namespaces []string, node string) string {
	inner := nsMatch(namespaces)
	if node != "" {
		inner += fmt.Sprintf(",node=%q", node)
	}
	return "{" + inner + "}"
}

// scopeKey is the canonical cache key for a namespace scope plus extras -
// sorted, so equal sets share one cache entry regardless of order.
func scopeKey(namespaces []string, extra ...string) string {
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	return strings.Join(append([]string{strings.Join(sorted, ",")}, extra...), "|")
}

// VMUsage returns a VM's point-in-time capacity-and-usage for the Summary tab: CPU
// % of allocated, memory used of allocated, guest-FS used of provisioned - each
// with a short sparkline.
func (c *Client) VMUsage(ctx context.Context, who Caller, ns, name string) (model.VMUsage, error) {
	if gap := c.unserved(who); gap != "" {
		return model.VMUsage{Updated: time.Now().Unix(), Unavailable: gap}, nil
	}
	return cached(ctx, c.vmUsage, ns+"/"+name, func(ctx context.Context) (model.VMUsage, bool) {
		r, at, w := c.read(who.Token), who.in(ns), lastHour()
		s := fmt.Sprintf("{namespace=%q,name=%q}", ns, name)
		var cpu, mem, stor sparkResult
		var memTotal, storTotal float64
		parallel(
			func() { cpu = sparkOf(r.series(ctx, at, cpuPercent(s, "2m"), w)) },
			func() { mem = sparkOf(r.series(ctx, at, "kubevirt_vmi_memory_used_bytes"+s, w)) },
			func() {
				stor = sparkOf(r.series(ctx, at, fmt.Sprintf("sum(kubevirt_vmi_filesystem_used_bytes%s)", s), w))
			},
			func() { memTotal = first(r.vector(ctx, at, "kubevirt_vmi_memory_domain_bytes"+s)) },
			func() {
				storTotal = first(r.vector(ctx, at, fmt.Sprintf("sum(kubevirt_vmi_filesystem_capacity_bytes%s)", s)))
			},
		)
		gap, whole := r.outcome()
		if gap != "" {
			return model.VMUsage{Updated: time.Now().Unix(), Unavailable: gap}, false
		}
		return model.VMUsage{
			Updated: time.Now().Unix(),
			CPU:     model.UsageMetric{Used: cpu.last, Total: 100, Spark: cpu.vals},
			Memory:  model.UsageMetric{Used: mem.last, Total: memTotal, Spark: mem.vals},
			Storage: model.UsageMetric{Used: stor.last, Total: storTotal, Spark: stor.vals},
		}, whole
	})
}

// ClusterSummary returns the aggregate capacity view for a container scope (the
// whole inventory, a project, a namespace, or a node). VM-scoped sums are limited
// to namespaces (the caller's visible set) and optionally a node; capacity is the
// node-allocatable total (cluster-wide, or that one node). topConsumers lists the
// heaviest VMs by CPU + memory. nodeTotals gates the node-allocatable totals:
// they are node data, served only to a caller who may read nodes.
func (c *Client) ClusterSummary(ctx context.Context, who Caller, namespaces []string, node string, nodeTotals bool) (model.ClusterSummary, error) {
	if gap := c.unserved(who); gap != "" {
		return unavailableSummary(gap), nil
	}
	// Node series carry no namespace label, so the tenancy port answers them
	// empty: only a wide caller has node totals to read.
	nodeTotals = nodeTotals && who.Wide
	// Cache by the scope (sorted namespaces + node + node access), not the
	// token: any user with this view gets the same aggregate.
	key := scopeKey(namespaces, node, fmt.Sprintf("nodes=%t", nodeTotals))
	return cached(ctx, c.cluster, key, func(ctx context.Context) (model.ClusterSummary, bool) {
		r, o, w := c.read(who.Token), over{wide: who.Wide, nss: namespaces}, lastHour()
		// q is one VM query, format's %s the caller's selector.
		q := func(format string) func([]string) string {
			return func(nss []string) string { return fmt.Sprintf(format, vmSelector(nss, node)) }
		}
		top := func(format string) func([]string) string {
			f := q(format)
			return func(nss []string) string { return topVMs(f(nss)) }
		}
		// Capacity boundary = all nodes, or the single node when scoped to one.
		nodeFilter := ""
		if node != "" {
			nodeFilter = fmt.Sprintf(`,node=%q`, node)
		}

		var cpu, mem, stor sparkResult
		var cpuAlloc, memAlloc, storTotal, cpuTotal, memTotal float64
		var phases, topCPU, topMem []labeledValue
		fns := []func(){
			func() {
				cpu = sparkOf(r.seriesOver(ctx, o, w, q("sum(rate(kubevirt_vmi_cpu_usage_seconds_total%s[2m]))")))
			},
			func() { mem = sparkOf(r.seriesOver(ctx, o, w, q("sum(kubevirt_vmi_memory_used_bytes%s)"))) },
			func() { stor = sparkOf(r.seriesOver(ctx, o, w, q("sum(kubevirt_vmi_filesystem_used_bytes%s)"))) },
			func() {
				cpuAlloc = first(r.vectorOver(ctx, o, func(nss []string) string { return vcpuTotal(vmSelector(nss, node)) }))
			},
			func() { memAlloc = first(r.vectorOver(ctx, o, q("sum(kubevirt_vmi_memory_domain_bytes%s)"))) },
			func() { storTotal = first(r.vectorOver(ctx, o, q("sum(kubevirt_vmi_filesystem_capacity_bytes%s)"))) },
			// kubevirt_vmi_phase_count has no namespace label; kubevirt_vmi_info
			// does (one series per VMI, with a phase label), so it counts per
			// namespace.
			func() { phases = r.vectorOver(ctx, o, q("sum by(phase)(kubevirt_vmi_info%s)")) },
			func() { topCPU = r.vectorOver(ctx, o, top("rate(kubevirt_vmi_cpu_usage_seconds_total%s[2m])")) },
			func() { topMem = r.vectorOver(ctx, o, top("kubevirt_vmi_memory_used_bytes%s")) },
		}
		if nodeTotals {
			fns = append(fns,
				func() {
					cpuTotal = first(r.vector(ctx, "", fmt.Sprintf(`sum(kube_node_status_allocatable{resource="cpu"%s})`, nodeFilter)))
				},
				func() {
					memTotal = first(r.vector(ctx, "", fmt.Sprintf(`sum(kube_node_status_allocatable{resource="memory"%s})`, nodeFilter)))
				},
			)
		}
		parallel(fns...)
		gap, whole := r.outcome()
		if gap != "" {
			return unavailableSummary(gap), false
		}
		vms := map[string]int{}
		for _, lv := range phases {
			if p := lv.labels["phase"]; p != "" {
				vms[p] = int(lv.value)
			}
		}
		return model.ClusterSummary{
			Updated:   time.Now().Unix(),
			CPU:       model.ClusterMetric{Used: cpu.last, Allocated: cpuAlloc, Total: cpuTotal, Spark: cpu.vals},
			Memory:    model.ClusterMetric{Used: mem.last, Allocated: memAlloc, Total: memTotal, Spark: mem.vals},
			Storage:   model.ClusterMetric{Used: stor.last, Total: storTotal, Spark: stor.vals},
			VMs:       vms,
			TopCPU:    consumers(topCPU),
			TopMemory: consumers(topMem),
		}, whole
	})
}

// unavailableSummary has no numbers and no phase map: nothing in it may read
// as an empty scope.
func unavailableSummary(gap model.MetricsReason) model.ClusterSummary {
	return model.ClusterSummary{Updated: time.Now().Unix(), TopCPU: []model.ConsumerVM{}, TopMemory: []model.ConsumerVM{}, Unavailable: gap}
}

// byLabel indexes an instant vector by one label's value (node-exporter labels
// nodes `instance`, kube-state-metrics `node`; the joins happen in Go).
// Empty-label series are dropped.
func byLabel(vec []labeledValue, label string) map[string]float64 {
	m := map[string]float64{}
	for _, lv := range vec {
		if n := lv.labels[label]; n != "" {
			m[n] = lv.value
		}
	}
	return m
}

// HostLoad returns the worker-node utilization distribution behind the DRS
// balance card: every worker with CPU and memory percent, hottest-CPU first.
// Four instant vectors - CPU and memory utilization, the worker role set, and
// cordon state - are joined here rather than in PromQL (node-exporter labels
// nodes `instance`, kube-state-metrics labels them `node`; a Go join beats a
// label_replace). Node-level data, same sensitivity class as the
// node-allocatable totals ClusterSummary serves; cached once for all users.
func (c *Client) HostLoad(ctx context.Context, token string) (model.HostLoad, error) {
	if v, ok := c.hosts.Get("hosts"); ok {
		return v, nil
	}
	r := c.read(token)
	util := byLabel(r.vector(ctx, "", `1 - avg by(instance) (rate(node_cpu_seconds_total{mode="idle"}[2m]))`), "instance")
	mem := byLabel(r.vector(ctx, "", `1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes`), "instance")
	unsched := byLabel(r.vector(ctx, "", `kube_node_spec_unschedulable`), "node")
	var nodes []model.HostWorker
	for _, lv := range r.vector(ctx, "", `kube_node_role{role="worker"}`) {
		n := lv.labels["node"]
		u, ok := util[n]
		if n == "" || !ok {
			continue // no exporter series: absent from the distribution, not a fake 0%
		}
		nodes = append(nodes, model.HostWorker{Node: n, Pct: u * 100, Mem: mem[n] * 100, Unschedulable: unsched[n] > 0})
	}
	if len(nodes) == 0 {
		return model.HostLoad{}, fmt.Errorf("%w: no worker utilization series", model.ErrUnavailable)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Pct > nodes[j].Pct }) // hottest first

	load := model.HostLoad{
		Updated: time.Now().Unix(),
		Workers: len(nodes),
		Nodes:   nodes,
	}
	var sum float64
	for _, n := range nodes {
		sum += n.Pct
	}
	load.Mean = sum / float64(len(nodes))
	if _, whole := r.outcome(); whole {
		c.hosts.Put("hosts", load)
	}
	return load, nil
}

// Capacity returns each worker's committed-to-VMs vCPU and guest memory
// against its allocatable capacity - the per-host breakdown of the cluster
// overcommit ratios ClusterSummary serves. kube-state-metrics and the KubeVirt
// VMI series both label nodes `node`, so the join is direct. Node-level data,
// same sensitivity class as HostLoad; cached once for all users.
func (c *Client) Capacity(ctx context.Context, token string) (model.HostCapacity, error) {
	if v, ok := c.capacity.Get("capacity"); ok {
		return v, nil
	}
	r := c.read(token)
	cpuAlloc := byLabel(r.vector(ctx, "", `kube_node_status_allocatable{resource="cpu"}`), "node")
	memAlloc := byLabel(r.vector(ctx, "", `kube_node_status_allocatable{resource="memory"}`), "node")
	vcpu := byLabel(r.vector(ctx, "", nodeVCPUs()), "node")
	memVM := byLabel(r.vector(ctx, "", `sum by(node)(kubevirt_vmi_memory_domain_bytes)`), "node")

	var nodes []model.HostCapacityNode
	for _, lv := range r.vector(ctx, "", `kube_node_role{role="worker"}`) {
		n := lv.labels["node"]
		alloc, ok := cpuAlloc[n]
		if n == "" || !ok {
			continue // no allocatable series: absent from the list, not a fake zero-capacity row
		}
		nodes = append(nodes, model.HostCapacityNode{
			Node:           n,
			CPUAllocatable: alloc,
			VCPUAllocated:  vcpu[n],
			MemAllocatable: memAlloc[n],
			MemAllocated:   memVM[n],
		})
	}
	if len(nodes) == 0 {
		return model.HostCapacity{}, fmt.Errorf("%w: no worker capacity series", model.ErrUnavailable)
	}
	// Most-committed memory first: overcommit risk leads, and memory (unlike
	// time-shared CPU) is the ratio that hurts.
	ratio := func(n model.HostCapacityNode) float64 {
		if n.MemAllocatable <= 0 {
			return 0
		}
		return n.MemAllocated / n.MemAllocatable
	}
	sort.Slice(nodes, func(i, j int) bool { return ratio(nodes[i]) > ratio(nodes[j]) })

	out := model.HostCapacity{Updated: time.Now().Unix(), Nodes: nodes}
	if _, whole := r.outcome(); whole {
		c.capacity.Put("capacity", out)
	}
	return out, nil
}

// unavailableMetrics is a chart set with no charts and the reason why.
func unavailableMetrics(rng string, gap model.MetricsReason) model.VMMetrics {
	return model.VMMetrics{Range: rng, Charts: []model.MetricChart{}, Unavailable: gap}
}

// ScopeMetrics returns the per-VM top-consumer time-series for a container
// scope over the given range - the container Monitor's Performance view. Each
// chart is one topk query; its result series are named namespace/name. Cached
// by scope (not token), like ClusterSummary: any user authorized for this
// namespace set gets identical data.
func (c *Client) ScopeMetrics(ctx context.Context, who Caller, namespaces []string, node, rng string) (model.VMMetrics, error) {
	rng, spec := resolveRange(rng)
	if gap := c.unserved(who); gap != "" {
		return unavailableMetrics(rng, gap), nil
	}
	return cached(ctx, c.scope, scopeKey(namespaces, node, rng), func(ctx context.Context) (model.VMMetrics, bool) {
		r, o, w := c.read(who.Token), over{wide: who.Wide, nss: namespaces}, spec.span()
		charts := scopeChartSpecs(promDur(rateWindow(spec.step)))
		results := make([][]labeledSeries, len(charts))
		fns := make([]func(), len(charts))
		for ci, ch := range charts {
			fns[ci] = func() {
				results[ci] = topKSeries(r.seriesOver(ctx, o, w, func(nss []string) string {
					return ch.query(vmSelector(nss, node))
				}), topK)
			}
		}
		parallel(fns...)
		gap, whole := r.outcome()
		if gap != "" {
			return unavailableMetrics(rng, gap), false
		}
		out := model.VMMetrics{Range: rng, Charts: make([]model.MetricChart, len(charts))}
		for ci, ch := range charts {
			out.Charts[ci] = buildChart(ch.chartSpec, namedFromLabeled(results[ci]))
		}
		return out, whole
	})
}

// alertRows is one Alerts answer and, when every query failed, why.
type alertRows struct {
	rows []model.Alert
	gap  model.MetricsReason
}

// Alerts returns the firing Prometheus alerts in the given namespaces - the
// dock's Alarms tab and badge. It reads the ALERTS series directly (no
// Alertmanager dependency); identical (name, severity, namespace, vm) series
// collapse into one row with a count. Cached by scope like ClusterSummary.
// Alarm definitions (PrometheusRules) are platform config, out of scope. An
// unavailable read is ErrUnavailable, never an empty list that reads as none
// firing.
func (c *Client) Alerts(ctx context.Context, who Caller, namespaces []string) ([]model.Alert, error) {
	if gap := c.unserved(who); gap != "" {
		return nil, fmt.Errorf("%w: alerts %s", model.ErrUnavailable, gap)
	}
	if len(namespaces) == 0 {
		return []model.Alert{}, nil
	}
	got, err := cached(ctx, c.alerts, scopeKey(namespaces), func(ctx context.Context) (alertRows, bool) {
		r := c.read(who.Token)
		vec := r.vectorOver(ctx, over{wide: who.Wide, nss: namespaces}, func(nss []string) string {
			return fmt.Sprintf(`ALERTS{alertstate="firing",%s}`, nsMatch(nss))
		})
		gap, whole := r.outcome()
		return alertRows{rows: collapseAlerts(vec), gap: gap}, whole
	})
	switch {
	case err != nil:
		return nil, err
	case got.gap != "":
		return nil, fmt.Errorf("%w: alerts %s", model.ErrUnavailable, got.gap)
	}
	return got.rows, nil
}

// collapseAlerts folds identical alert series into counted rows, most urgent
// first.
func collapseAlerts(vec []labeledValue) []model.Alert {
	rows := map[string]*model.Alert{}
	for _, lv := range vec {
		a := model.Alert{
			Name:      lv.labels["alertname"],
			Severity:  lv.labels["severity"],
			Namespace: lv.labels["namespace"],
			VM:        lv.labels["name"],
		}
		k := a.Name + "\x00" + a.Severity + "\x00" + a.Namespace + "\x00" + a.VM
		if got, ok := rows[k]; ok {
			got.Count++
			continue
		}
		a.Count = 1
		rows[k] = &a
	}
	out := make([]model.Alert, 0, len(rows))
	for _, a := range rows {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := severityRank(out[i].Severity), severityRank(out[j].Severity); ri != rj {
			return ri < rj
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Namespace+"/"+out[i].VM < out[j].Namespace+"/"+out[j].VM
	})
	return out
}

// severityRank orders alerts most-urgent-first (unknown severities last).
func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}

// consumers ranks a topk answer highest first, keeping topK: per-namespace
// answers folded together hold up to topK per namespace.
func consumers(vec []labeledValue) []model.ConsumerVM {
	out := make([]model.ConsumerVM, 0, len(vec))
	for _, lv := range vec {
		out = append(out, model.ConsumerVM{Namespace: lv.labels["namespace"], Name: lv.labels["name"], Value: lv.value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out[:min(len(out), topK)]
}

// VMMetrics runs the curated charts for one VM over the given range concurrently,
// then aligns each chart's series onto a shared time axis (one x-array + per-series
// value arrays, gaps as nil - directly chartable). A fixed spec yields one series;
// a byLabel spec yields one per label value (per NIC, per drive). A per-series
// failure degrades to gaps; a dead endpoint (every query errors with no data)
// answers the reason instead of charts.
func (c *Client) VMMetrics(ctx context.Context, who Caller, ns, name, rng string) (model.VMMetrics, error) {
	rng, spec := resolveRange(rng)
	if gap := c.unserved(who); gap != "" {
		return unavailableMetrics(rng, gap), nil
	}
	return cached(ctx, c.vmMetrics, ns+"/"+name+"|"+rng, func(ctx context.Context) (model.VMMetrics, bool) {
		r, at, w := c.read(who.Token), who.in(ns), spec.span()
		specs := chartSpecs(ns, name, promDur(rateWindow(spec.step)))
		results := make([][][]namedSeries, len(specs)) // results[chart][spec] = its series
		var fns []func()
		for ci, cs := range specs {
			results[ci] = make([][]namedSeries, len(cs.series))
			for si, ss := range cs.series {
				fns = append(fns, func() {
					list := r.series(ctx, at, ss.query, w)
					if ss.byLabel == "" {
						// Fixed series: keep its legend entry even when empty.
						results[ci][si] = []namedSeries{{name: ss.name, samples: samplesOf(list)}}
						return
					}
					got := make([]namedSeries, 0, len(list))
					for _, ls := range list {
						got = append(got, namedSeries{name: joinName(ss.name, ls.labels[ss.byLabel]), samples: ls.samples})
					}
					sort.Slice(got, func(i, j int) bool { return got[i].name < got[j].name })
					results[ci][si] = got
				})
			}
		}
		parallel(fns...)
		gap, whole := r.outcome()
		if gap != "" {
			return unavailableMetrics(rng, gap), false
		}
		out := model.VMMetrics{Range: rng, Charts: make([]model.MetricChart, len(specs))}
		for ci, cs := range specs {
			var series []namedSeries
			for _, got := range results[ci] {
				series = append(series, got...)
			}
			out.Charts[ci] = buildChart(cs, series)
		}
		return out, whole
	})
}

// Attachment is one VM interface on a network: KubeVirt's network counters
// carry the interface name, so traffic on a network is measured per attachment.
type Attachment struct {
	Namespace, Name, Interface string
}

// trafficWindow is the averaging window of NetworkTraffic.
const trafficWindow = "1h"

// NetworkTraffic ranks the VMs on one network by traffic (received plus sent,
// averaged over trafficWindow) and returns the busiest limit, in bits/s. One
// instant query narrowed by namespace and interface name; the exact (VM,
// interface) pairs are matched here, since an interface name may repeat on
// another network. Cached by the attachment set, like the scope views. The
// caller names the network.
func (c *Client) NetworkTraffic(ctx context.Context, who Caller, attached []Attachment, limit int) (model.NetworkTraffic, error) {
	none := model.NetworkTraffic{Window: trafficWindow, VMs: []model.TrafficVM{}}
	if gap := c.unserved(who); gap != "" {
		none.Unavailable = gap
		return none, nil
	}
	if len(attached) == 0 {
		return none, nil
	}
	want := make(map[Attachment]bool, len(attached))
	ifaces := map[string]map[string]bool{} // namespace -> interface names
	keys := make([]string, 0, len(attached))
	for _, a := range attached {
		want[a] = true
		if ifaces[a.Namespace] == nil {
			ifaces[a.Namespace] = map[string]bool{}
		}
		ifaces[a.Namespace][a.Interface] = true
		keys = append(keys, a.Namespace+"/"+a.Name+"/"+a.Interface)
	}
	return cached(ctx, c.traffic, scopeKey(keys, fmt.Sprint(limit)), func(ctx context.Context) (model.NetworkTraffic, bool) {
		r := c.read(who.Token)
		o := over{wide: who.Wide, nss: slices.Sorted(maps.Keys(ifaces))}
		vec := r.vectorOver(ctx, o, func(nss []string) string {
			ifs := map[string]bool{}
			for _, ns := range nss {
				maps.Copy(ifs, ifaces[ns])
			}
			sel := fmt.Sprintf("{%s,interface=~%q}", nsMatch(nss), alternation(ifs))
			sum := func(metric string) string {
				return fmt.Sprintf("sum by(namespace,name,interface)(rate(%s%s[%s]))", metric, sel, trafficWindow)
			}
			return sum("kubevirt_vmi_network_receive_bytes_total") + " + " + sum("kubevirt_vmi_network_transmit_bytes_total")
		})
		gap, whole := r.outcome()
		if gap != "" {
			out := none
			out.Unavailable = gap
			return out, false
		}
		perVM := map[[2]string]float64{}
		for _, lv := range vec {
			a := Attachment{Namespace: lv.labels["namespace"], Name: lv.labels["name"], Interface: lv.labels["interface"]}
			if want[a] {
				perVM[[2]string{a.Namespace, a.Name}] += lv.value * 8
			}
		}
		vms := make([]model.TrafficVM, 0, len(perVM))
		for k, v := range perVM {
			vms = append(vms, model.TrafficVM{Namespace: k[0], Name: k[1], BitsPerSecond: v})
		}
		sort.Slice(vms, func(i, j int) bool {
			if vms[i].BitsPerSecond != vms[j].BitsPerSecond {
				return vms[i].BitsPerSecond > vms[j].BitsPerSecond
			}
			return vms[i].Namespace+"/"+vms[i].Name < vms[j].Namespace+"/"+vms[j].Name
		})
		if limit > 0 && len(vms) > limit {
			vms = vms[:limit]
		}
		out := none
		out.VMs = vms
		return out, whole
	})
}

// alternation is an anchored-by-PromQL regex matching exactly the given names.
func alternation(set map[string]bool) string {
	parts := make([]string, 0, len(set))
	for s := range set {
		parts = append(parts, regexp.QuoteMeta(s))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}
