// Package metrics queries a Prometheus-compatible endpoint (OpenShift's Thanos
// querier) for a VM's performance time-series — the data behind the Performance
// tab. It runs a fixed, curated set of range queries per VM under the caller's
// token (so the metrics backend's own RBAC is the access gate) and shapes the
// results into chart-ready series aligned on a shared time axis.
package metrics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// Result caches are keyed by QUERY PARAMETERS, not the caller's token: the data for
// a VM (or a scope) is identical for any user authorized to see it, and the API
// handler has already gated access before calling here. So same-scope users and
// re-mounts share one Thanos fan-out instead of each re-querying. TTLs are short
// (well under the UI's 30s poll) to stay fresh while absorbing bursts.
const (
	vmTTL      = 15 * time.Second
	clusterTTL = 20 * time.Second
)

// Client talks to a Prometheus query API (the OpenShift Thanos querier Route).
type Client struct {
	base      string
	http      *http.Client
	vmMetrics *ttlcache.Cache[model.VMMetrics]
	vmUsage   *ttlcache.Cache[model.VMUsage]
	cluster   *ttlcache.Cache[model.ClusterSummary]
	scope     *ttlcache.Cache[model.VMMetrics]
	alerts    *ttlcache.Cache[[]model.Alert]
	hosts     *ttlcache.Cache[model.HostLoad]
	capacity  *ttlcache.Cache[model.HostCapacity]
}

// New builds a Client for the query API at baseURL (e.g. the thanos-querier
// Route, or the in-cluster service). caPath, when set, is a PEM bundle to trust
// for that endpoint — in-cluster, the mounted service-CA that signs
// thanos-querier's serving cert, so the connection needn't be insecure.
// insecure skips TLS verification instead (self-signed dev Route). Returns
// (nil, nil) when baseURL is empty — the API treats a nil client as
// "Performance disabled".
func New(baseURL, caPath string, insecure bool) (*Client, error) {
	if baseURL == "" {
		return nil, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	switch {
	case caPath != "":
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("metrics CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("metrics CA bundle %s: no certificates found", caPath)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	case insecure:
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		base:      strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 20 * time.Second, Transport: tr},
		vmMetrics: ttlcache.New[model.VMMetrics](vmTTL),
		vmUsage:   ttlcache.New[model.VMUsage](vmTTL),
		cluster:   ttlcache.New[model.ClusterSummary](clusterTTL),
		scope:     ttlcache.New[model.VMMetrics](clusterTTL),
		alerts:    ttlcache.New[[]model.Alert](clusterTTL),
		hosts:     ttlcache.New[model.HostLoad](clusterTTL),
		capacity:  ttlcache.New[model.HostCapacity](clusterTTL),
	}, nil
}

// scopeSelector scopes a VM metric to the caller's namespaces (+ a node when
// drilling into one). With no namespaces it matches nothing, so usage reads
// zero rather than leaking other tenants' data.
func scopeSelector(namespaces []string, node string) string {
	if len(namespaces) == 0 {
		return `{namespace="__dotvirt_none__"}`
	}
	inner := fmt.Sprintf("namespace=~%q", strings.Join(namespaces, "|"))
	if node != "" {
		inner += fmt.Sprintf(",node=%q", node)
	}
	return "{" + inner + "}"
}

// scopeKey is the canonical cache key for a namespace scope plus extras —
// sorted, so equal sets share one cache entry regardless of order.
func scopeKey(namespaces []string, extra ...string) string {
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	return strings.Join(append([]string{strings.Join(sorted, ",")}, extra...), "|")
}

// VMUsage returns a VM's point-in-time capacity-and-usage for the Summary tab: CPU
// % of allocated, memory used of allocated, guest-FS used of provisioned — each
// with a short sparkline.
func (c *Client) VMUsage(ctx context.Context, token, ns, name string) (model.VMUsage, error) {
	key := ns + "/" + name
	if v, ok := c.vmUsage.Get(key); ok {
		return v, nil
	}
	s := fmt.Sprintf("{namespace=%q,name=%q}", ns, name)
	sp := c.sparklines(ctx, token, map[string]string{
		"cpu":  fmt.Sprintf("rate(kubevirt_vmi_cpu_usage_seconds_total%s[2m])*100 / on(namespace,name) %s", s, vcpuCount(s)),
		"mem":  fmt.Sprintf("kubevirt_vmi_memory_used_bytes%s", s),
		"stor": fmt.Sprintf("sum(kubevirt_vmi_filesystem_used_bytes%s)", s),
	})
	tot := c.scalars(ctx, token, map[string]string{
		"mem":  fmt.Sprintf("kubevirt_vmi_memory_domain_bytes%s", s),
		"stor": fmt.Sprintf("sum(kubevirt_vmi_filesystem_capacity_bytes%s)", s),
	})
	out := model.VMUsage{
		Updated: time.Now().Unix(),
		CPU:     model.UsageMetric{Used: sp["cpu"].last, Total: 100, Spark: sp["cpu"].vals},
		Memory:  model.UsageMetric{Used: sp["mem"].last, Total: tot["mem"], Spark: sp["mem"].vals},
		Storage: model.UsageMetric{Used: sp["stor"].last, Total: tot["stor"], Spark: sp["stor"].vals},
	}
	c.vmUsage.Put(key, out)
	return out, nil
}

// ClusterSummary returns the aggregate capacity view for a container scope (the
// whole inventory, a project, a namespace, or a node). VM-scoped sums are limited
// to namespaces (the caller's visible set) and optionally a node; capacity is the
// node-allocatable total (cluster-wide, or that one node). topConsumers lists the
// heaviest VMs by CPU + memory.
func (c *Client) ClusterSummary(ctx context.Context, token string, namespaces []string, node string) (model.ClusterSummary, error) {
	// Cache by the scope (sorted namespaces + node), not the token: any user with
	// this namespace set gets the same aggregate, so same-scope viewers share it.
	key := scopeKey(namespaces, node)
	if v, ok := c.cluster.Get(key); ok {
		return v, nil
	}
	nsSel := scopeSelector(namespaces, node)
	vm := func(metric string) string { return metric + nsSel } // VM metric scoped to the caller

	// Capacity boundary = all nodes, or the single node when scoped to one.
	nodeFilter := ""
	if node != "" {
		nodeFilter = fmt.Sprintf(`,node=%q`, node)
	}

	sp := c.sparklines(ctx, token, map[string]string{
		"cpu":  fmt.Sprintf("sum(rate(%s[2m]))", vm("kubevirt_vmi_cpu_usage_seconds_total")),
		"mem":  fmt.Sprintf("sum(%s)", vm("kubevirt_vmi_memory_used_bytes")),
		"stor": fmt.Sprintf("sum(%s)", vm("kubevirt_vmi_filesystem_used_bytes")),
	})
	sc := c.scalars(ctx, token, map[string]string{
		"cpuAlloc":  vcpuTotal(nsSel),
		"cpuTotal":  fmt.Sprintf(`sum(kube_node_status_allocatable{resource="cpu"%s})`, nodeFilter),
		"memAlloc":  fmt.Sprintf("sum(%s)", vm("kubevirt_vmi_memory_domain_bytes")),
		"memTotal":  fmt.Sprintf(`sum(kube_node_status_allocatable{resource="memory"%s})`, nodeFilter),
		"storTotal": fmt.Sprintf("sum(%s)", vm("kubevirt_vmi_filesystem_capacity_bytes")),
	})

	// kubevirt_vmi_phase_count has no namespace label; kubevirt_vmi_info does (one
	// series per VMI, with a phase label), so it counts per namespace.
	vms := map[string]int{}
	for _, lv := range c.vector(ctx, token, fmt.Sprintf("sum by(phase)(%s)", vm("kubevirt_vmi_info"))) {
		if p := lv.labels["phase"]; p != "" {
			vms[p] = int(lv.value)
		}
	}
	topCPU := consumers(c.vector(ctx, token, fmt.Sprintf("topk(5, sum by(namespace,name)(rate(%s[2m])))", vm("kubevirt_vmi_cpu_usage_seconds_total"))))
	topMem := consumers(c.vector(ctx, token, fmt.Sprintf("topk(5, sum by(namespace,name)(%s))", vm("kubevirt_vmi_memory_used_bytes"))))

	out := model.ClusterSummary{
		Updated:   time.Now().Unix(),
		CPU:       model.ClusterMetric{Used: sp["cpu"].last, Allocated: sc["cpuAlloc"], Total: sc["cpuTotal"], Spark: sp["cpu"].vals},
		Memory:    model.ClusterMetric{Used: sp["mem"].last, Allocated: sc["memAlloc"], Total: sc["memTotal"], Spark: sp["mem"].vals},
		Storage:   model.ClusterMetric{Used: sp["stor"].last, Total: sc["storTotal"], Spark: sp["stor"].vals},
		VMs:       vms,
		TopCPU:    topCPU,
		TopMemory: topMem,
	}
	c.cluster.Put(key, out)
	return out, nil
}

// HostLoad returns the worker-node utilization distribution behind the DRS
// balance card: every worker with CPU and memory percent, hottest-CPU first.
// Four instant vectors — CPU and memory utilization, the worker role set, and
// cordon state — are joined here rather than in PromQL (node-exporter labels
// nodes `instance`, kube-state-metrics labels them `node`; a Go join beats a
// label_replace). Node-level data, same sensitivity class as the
// node-allocatable totals ClusterSummary serves; cached once for all users.
func (c *Client) HostLoad(ctx context.Context, token string) (model.HostLoad, error) {
	if v, ok := c.hosts.Get("hosts"); ok {
		return v, nil
	}
	util := map[string]float64{}
	for _, lv := range c.vector(ctx, token, `1 - avg by(instance) (rate(node_cpu_seconds_total{mode="idle"}[2m]))`) {
		if n := lv.labels["instance"]; n != "" {
			util[n] = lv.value
		}
	}
	mem := map[string]float64{}
	for _, lv := range c.vector(ctx, token, `1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes`) {
		if n := lv.labels["instance"]; n != "" {
			mem[n] = lv.value
		}
	}
	unsched := map[string]bool{}
	for _, lv := range c.vector(ctx, token, `kube_node_spec_unschedulable`) {
		if n := lv.labels["node"]; n != "" && lv.value > 0 {
			unsched[n] = true
		}
	}
	var nodes []model.HostWorker
	for _, lv := range c.vector(ctx, token, `kube_node_role{role="worker"}`) {
		n := lv.labels["node"]
		u, ok := util[n]
		if n == "" || !ok {
			continue // no exporter series: absent from the distribution, not a fake 0%
		}
		nodes = append(nodes, model.HostWorker{Node: n, Pct: u * 100, Mem: mem[n] * 100, Unschedulable: unsched[n]})
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
	c.hosts.Put("hosts", load)
	return load, nil
}

// Capacity returns each worker's committed-to-VMs vCPU and guest memory
// against its allocatable capacity — the per-host breakdown of the cluster
// overcommit ratios ClusterSummary serves. kube-state-metrics and the KubeVirt
// VMI series both label nodes `node`, so the join is direct. Node-level data,
// same sensitivity class as HostLoad; cached once for all users.
func (c *Client) Capacity(ctx context.Context, token string) (model.HostCapacity, error) {
	if v, ok := c.capacity.Get("capacity"); ok {
		return v, nil
	}
	byNode := func(q string) map[string]float64 {
		m := map[string]float64{}
		for _, lv := range c.vector(ctx, token, q) {
			if n := lv.labels["node"]; n != "" {
				m[n] = lv.value
			}
		}
		return m
	}
	cpuAlloc := byNode(`kube_node_status_allocatable{resource="cpu"}`)
	memAlloc := byNode(`kube_node_status_allocatable{resource="memory"}`)
	// Per-node vCPU sum, with vcpuCount's per-vCPU-series fallback for older KubeVirt.
	vcpu := byNode(`(sum by(node)(kubevirt_vmi_vcpu_count) or sum by(node)(count by(node,namespace,name)(kubevirt_vmi_vcpu_seconds_total)))`)
	memVM := byNode(`sum by(node)(kubevirt_vmi_memory_domain_bytes)`)

	var nodes []model.HostCapacityNode
	for _, lv := range c.vector(ctx, token, `kube_node_role{role="worker"}`) {
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
	c.capacity.Put("capacity", out)
	return out, nil
}

// ScopeMetrics returns the per-VM top-consumer time-series for a container
// scope over the given range — the container Monitor's Performance view. Each
// chart is one topk query; its result series are named namespace/name. Cached
// by scope (not token), like ClusterSummary: any user authorized for this
// namespace set gets identical data.
func (c *Client) ScopeMetrics(ctx context.Context, token string, namespaces []string, node, rng string) (model.VMMetrics, error) {
	rng, spec := resolveRange(rng)
	key := scopeKey(namespaces, node, rng)
	if v, ok := c.scope.Get(key); ok {
		return v, nil
	}
	sel := scopeSelector(namespaces, node)

	end := time.Now().Unix()
	start := end - int64(spec.window.Seconds())
	step := int(spec.step.Seconds())
	specs := scopeChartSpecs(sel, promDur(rateWindow(spec.step)))

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		anyData  bool
	)
	results := make([][]labeledSeries, len(specs))
	for ci, cs := range specs {
		wg.Add(1)
		go func(ci int, query string) {
			defer wg.Done()
			series, err := c.rangeSeries(ctx, token, query, start, end, step)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if len(series) > 0 {
				anyData = true
			}
			results[ci] = series
		}(ci, cs.series[0].query)
	}
	wg.Wait()
	if firstErr != nil && !anyData {
		return model.VMMetrics{}, fmt.Errorf("%w: %v", model.ErrUnavailable, firstErr)
	}

	out := model.VMMetrics{Range: rng, StepSec: step, Charts: make([]model.MetricChart, len(specs))}
	for ci, cs := range specs {
		out.Charts[ci] = buildChart(cs, namedFromLabeled(results[ci]))
	}
	c.scope.Put(key, out)
	return out, nil
}

// Alerts returns the firing Prometheus alerts in the given namespaces — the
// dock's Alarms tab and badge. It reads the ALERTS series directly (no
// Alertmanager dependency); identical (name, severity, namespace, vm) series
// collapse into one row with a count. Cached by scope like ClusterSummary.
// Alarm definitions (PrometheusRules) are platform config, out of scope.
func (c *Client) Alerts(ctx context.Context, token string, namespaces []string) ([]model.Alert, error) {
	if len(namespaces) == 0 {
		return []model.Alert{}, nil
	}
	key := scopeKey(namespaces)
	if v, ok := c.alerts.Get(key); ok {
		return v, nil
	}
	q := fmt.Sprintf(`ALERTS{alertstate="firing",namespace=~%q}`, strings.Join(namespaces, "|"))
	rows := map[string]*model.Alert{}
	for _, lv := range c.vector(ctx, token, q) {
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
	c.alerts.Put(key, out)
	return out, nil
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

// consumers turns a topk vector into sorted ConsumerVM rows (highest first).
func consumers(vec []labeledValue) []model.ConsumerVM {
	out := make([]model.ConsumerVM, 0, len(vec))
	for _, lv := range vec {
		out = append(out, model.ConsumerVM{Namespace: lv.labels["namespace"], Name: lv.labels["name"], Value: lv.value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

// VMMetrics runs the curated charts for one VM over the given range concurrently,
// then aligns each chart's series onto a shared time axis (one x-array + per-series
// value arrays, gaps as nil — directly chartable). A fixed spec yields one series;
// a byLabel spec yields one per label value (per NIC, per drive). A per-series
// failure degrades to gaps; a dead endpoint (every query errors with no data)
// returns ErrUnavailable.
func (c *Client) VMMetrics(ctx context.Context, token, ns, name, rng string) (model.VMMetrics, error) {
	rng, spec := resolveRange(rng)
	key := ns + "/" + name + "|" + rng
	if v, ok := c.vmMetrics.Get(key); ok {
		return v, nil
	}
	end := time.Now().Unix()
	start := end - int64(spec.window.Seconds())
	step := int(spec.step.Seconds())
	specs := chartSpecs(ns, name, promDur(rateWindow(spec.step)))

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		anyData  bool
	)
	results := make([][][]namedSeries, len(specs)) // results[chart][spec] = its series
	for ci, cs := range specs {
		results[ci] = make([][]namedSeries, len(cs.series))
		for si, ss := range cs.series {
			wg.Add(1)
			go func(ci, si int, ss seriesSpec) {
				defer wg.Done()
				var got []namedSeries
				var err error
				if ss.byLabel == "" {
					// Fixed series: keep its legend entry even when empty.
					var smp []sample
					smp, err = c.rangeQuery(ctx, token, ss.query, start, end, step)
					got = []namedSeries{{name: ss.name, samples: smp}}
				} else {
					var list []labeledSeries
					list, err = c.rangeSeries(ctx, token, ss.query, start, end, step)
					for _, ls := range list {
						got = append(got, namedSeries{name: joinName(ss.name, ls.labels[ss.byLabel]), samples: ls.samples})
					}
					sort.Slice(got, func(i, j int) bool { return got[i].name < got[j].name })
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				for _, s := range got {
					if len(s.samples) > 0 {
						anyData = true
						break
					}
				}
				results[ci][si] = got
			}(ci, si, ss)
		}
	}
	wg.Wait()
	if firstErr != nil && !anyData {
		return model.VMMetrics{}, fmt.Errorf("%w: %v", model.ErrUnavailable, firstErr)
	}

	out := model.VMMetrics{Range: rng, StepSec: step, Charts: make([]model.MetricChart, len(specs))}
	for ci, cs := range specs {
		var series []namedSeries
		for _, got := range results[ci] {
			series = append(series, got...)
		}
		out.Charts[ci] = buildChart(cs, series)
	}
	c.vmMetrics.Put(key, out)
	return out, nil
}
