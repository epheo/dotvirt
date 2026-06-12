// Package metrics queries a Prometheus-compatible endpoint (OpenShift's Thanos
// querier) for a VM's performance time-series — the data behind the Performance
// tab. It runs a fixed, curated set of range queries per VM under the caller's
// token (so the metrics backend's own RBAC is the access gate) and shapes the
// results into chart-ready series aligned on a shared time axis.
package metrics

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
}

// New builds a Client for the query API at baseURL (e.g. the thanos-querier
// Route). insecure skips TLS verification for a self-signed dev Route. Returns nil
// when baseURL is empty — the API treats a nil client as "Performance disabled".
func New(baseURL string, insecure bool) *Client {
	if baseURL == "" {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		base:      strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 20 * time.Second, Transport: tr},
		vmMetrics: ttlcache.New[model.VMMetrics](vmTTL),
		vmUsage:   ttlcache.New[model.VMUsage](vmTTL),
		cluster:   ttlcache.New[model.ClusterSummary](clusterTTL),
		scope:     ttlcache.New[model.VMMetrics](clusterTTL),
	}
}

// rangeSpec maps a UI range to a window + sample step, mirroring vCenter's tiers
// (real-time / day / week).
type rangeSpec struct {
	window time.Duration
	step   time.Duration
}

var ranges = map[string]rangeSpec{
	"1h": {time.Hour, 30 * time.Second},
	"1d": {24 * time.Hour, 5 * time.Minute},
	"1w": {7 * 24 * time.Hour, 30 * time.Minute},
}

const defaultRange = "1h"

// rateWindow is the lookback for rate(): a couple of steps, floored so even the
// 30s step spans enough scrapes to be smooth.
func rateWindow(step time.Duration) time.Duration {
	if w := 2 * step; w > 2*time.Minute {
		return w
	}
	return 2 * time.Minute
}

// promDur formats a duration the way PromQL wants it (e.g. "2m", "1h").
func promDur(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", d/time.Second)
	}
}

type seriesSpec struct {
	name  string
	query string
}

type chartSpec struct {
	key, title, unit string
	series           []seriesSpec
}

// chartSpecs builds the curated Overview charts for one VM — vCenter's CPU /
// Memory / Network / Disk, plus disk latency. rw is the rate() window.
func chartSpecs(ns, name, rw string) []chartSpec {
	s := fmt.Sprintf("{namespace=%q,name=%q}", ns, name)
	return []chartSpec{
		{"cpu", "CPU", "%", []seriesSpec{
			{"Usage", fmt.Sprintf("rate(kubevirt_vmi_cpu_usage_seconds_total%s[%s])*100 / on(namespace,name) kubevirt_vmi_vcpu_count%s", s, rw, s)},
			{"Wait", fmt.Sprintf("rate(kubevirt_vmi_vcpu_wait_seconds_total%s[%s])*100", s, rw)},
			{"Steal", fmt.Sprintf("rate(kubevirt_vmi_vcpu_delay_seconds_total%s[%s])*100", s, rw)},
		}},
		{"memory", "Memory", "bytes", []seriesSpec{
			{"Used", fmt.Sprintf("kubevirt_vmi_memory_used_bytes%s", s)},
			{"Cached", fmt.Sprintf("kubevirt_vmi_memory_cached_bytes%s", s)},
			{"Free", fmt.Sprintf("kubevirt_vmi_memory_unused_bytes%s", s)},
		}},
		{"network", "Network", "Bps", []seriesSpec{
			{"Rx", fmt.Sprintf("sum(rate(kubevirt_vmi_network_receive_bytes_total%s[%s]))", s, rw)},
			{"Tx", fmt.Sprintf("sum(rate(kubevirt_vmi_network_transmit_bytes_total%s[%s]))", s, rw)},
		}},
		{"disk", "Disk throughput", "Bps", []seriesSpec{
			{"Read", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_read_traffic_bytes_total%s[%s]))", s, rw)},
			{"Write", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_write_traffic_bytes_total%s[%s]))", s, rw)},
		}},
		{"latency", "Disk latency", "ms", []seriesSpec{
			{"Read", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_read_times_seconds_total%s[%s])) / sum(rate(kubevirt_vmi_storage_iops_read_total%s[%s])) * 1000", s, rw, s, rw)},
			{"Write", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_write_times_seconds_total%s[%s])) / sum(rate(kubevirt_vmi_storage_iops_write_total%s[%s])) * 1000", s, rw, s, rw)},
		}},
	}
}

// labeledValue is one instant-query result series: its labels and current value.
type labeledValue struct {
	labels map[string]string
	value  float64
}

// vector runs an instant query and returns its result series. A failed/empty query
// yields an empty slice (callers treat a missing value as zero).
func (c *Client) vector(ctx context.Context, token, query string) []labeledValue {
	v := url.Values{}
	v.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/query?"+v.Encode(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil
	}
	out := make([]labeledValue, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		var vs string
		if json.Unmarshal(r.Value[1], &vs) != nil {
			continue
		}
		f, err := strconv.ParseFloat(vs, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		out = append(out, labeledValue{labels: r.Metric, value: f})
	}
	return out
}

// scalars runs named instant queries concurrently, returning name→first value (0
// when a query has no result).
func (c *Client) scalars(ctx context.Context, token string, queries map[string]string) map[string]float64 {
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := make(map[string]float64, len(queries))
	for k, q := range queries {
		wg.Add(1)
		go func(k, q string) {
			defer wg.Done()
			var v float64
			if vec := c.vector(ctx, token, q); len(vec) > 0 {
				v = vec[0].value
			}
			mu.Lock()
			out[k] = v
			mu.Unlock()
		}(k, q)
	}
	wg.Wait()
	return out
}

type sparkResult struct {
	vals []float64
	last float64
}

// sparklines runs named queries over the last hour concurrently, returning each as
// a recent-values slice plus its latest value (for an inline sparkline + readout).
func (c *Client) sparklines(ctx context.Context, token string, queries map[string]string) map[string]sparkResult {
	end := time.Now().Unix()
	start := end - 3600
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := make(map[string]sparkResult, len(queries))
	for k, q := range queries {
		wg.Add(1)
		go func(k, q string) {
			defer wg.Done()
			smp, _ := c.rangeQuery(ctx, token, q, start, end, 120) // 2m step → ~30 points
			vals := make([]float64, len(smp))
			for i, s := range smp {
				vals[i] = s.v
			}
			var last float64
			if len(vals) > 0 {
				last = vals[len(vals)-1]
			}
			mu.Lock()
			out[k] = sparkResult{vals: vals, last: last}
			mu.Unlock()
		}(k, q)
	}
	wg.Wait()
	return out
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
		"cpu":  fmt.Sprintf("rate(kubevirt_vmi_cpu_usage_seconds_total%s[2m])*100 / on(namespace,name) kubevirt_vmi_vcpu_count%s", s, s),
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
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	key := strings.Join(sorted, ",") + "|" + node
	if v, ok := c.cluster.Get(key); ok {
		return v, nil
	}
	// Scope VM metrics to the caller's namespaces (+ a node when drilling into one).
	// With no namespaces, match nothing so usage reads zero but node capacity shows.
	nsSel := `{namespace="__dotvirt_none__"}`
	if len(namespaces) > 0 {
		inner := fmt.Sprintf("namespace=~%q", strings.Join(namespaces, "|"))
		if node != "" {
			inner += fmt.Sprintf(",node=%q", node)
		}
		nsSel = "{" + inner + "}"
	}
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
		"cpuAlloc":  fmt.Sprintf("sum(%s)", vm("kubevirt_vmi_vcpu_count")),
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

// scopeChartSpecs builds the per-VM top-consumer charts for a container scope
// (the whole inventory, a project, a namespace, or a node). Each chart is ONE
// topk query whose result series are the heaviest VMs, labeled namespace/name.
// sel is the namespace(+node) selector, rw the rate() window.
func scopeChartSpecs(sel, rw string) []chartSpec {
	topk := func(expr string) string { return fmt.Sprintf("topk(5, sum by(namespace,name)(%s))", expr) }
	rate := func(metric string) string { return fmt.Sprintf("rate(%s%s[%s])", metric, sel, rw) }
	return []chartSpec{
		{"cpu", "CPU — top VMs", "cores", []seriesSpec{
			{"", topk(rate("kubevirt_vmi_cpu_usage_seconds_total"))},
		}},
		{"memory", "Memory — top VMs", "bytes", []seriesSpec{
			{"", topk(fmt.Sprintf("kubevirt_vmi_memory_used_bytes%s", sel))},
		}},
		{"network", "Network — top VMs", "Bps", []seriesSpec{
			{"", topk(rate("kubevirt_vmi_network_receive_bytes_total") + " + " + rate("kubevirt_vmi_network_transmit_bytes_total"))},
		}},
		{"disk", "Disk throughput — top VMs", "Bps", []seriesSpec{
			{"", topk(rate("kubevirt_vmi_storage_read_traffic_bytes_total") + " + " + rate("kubevirt_vmi_storage_write_traffic_bytes_total"))},
		}},
	}
}

// ScopeMetrics returns the per-VM top-consumer time-series for a container
// scope over the given range — the container Monitor's Performance view. Each
// chart is one topk query; its result series are named namespace/name. Cached
// by scope (not token), like ClusterSummary: any user authorized for this
// namespace set gets identical data.
func (c *Client) ScopeMetrics(ctx context.Context, token string, namespaces []string, node, rng string) (model.VMMetrics, error) {
	spec, ok := ranges[rng]
	if !ok {
		rng, spec = defaultRange, ranges[defaultRange]
	}
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	key := strings.Join(sorted, ",") + "|" + node + "|" + rng
	if v, ok := c.scope.Get(key); ok {
		return v, nil
	}

	// Scope to the caller's namespaces (+ a node when drilling into one); with
	// no namespaces, match nothing — same boundary as ClusterSummary.
	sel := `{namespace="__dotvirt_none__"}`
	if len(namespaces) > 0 {
		inner := fmt.Sprintf("namespace=~%q", strings.Join(namespaces, "|"))
		if node != "" {
			inner += fmt.Sprintf(",node=%q", node)
		}
		sel = "{" + inner + "}"
	}

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
		out.Charts[ci] = chartFromSeries(cs, results[ci])
	}
	c.scope.Put(key, out)
	return out, nil
}

// chartFromSeries aligns one chart's labeled result series onto a shared time
// axis, naming each series by its namespace/name labels. Series are sorted by
// name so colors stay stable across refreshes (topk order churns per step).
func chartFromSeries(cs chartSpec, series []labeledSeries) model.MetricChart {
	sort.Slice(series, func(i, j int) bool { return seriesName(series[i].labels) < seriesName(series[j].labels) })
	set := map[int64]struct{}{}
	for _, s := range series {
		for _, smp := range s.samples {
			set[smp.t] = struct{}{}
		}
	}
	times := make([]int64, 0, len(set))
	for t := range set {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	at := make(map[int64]int, len(times))
	for i, t := range times {
		at[t] = i
	}
	chart := model.MetricChart{Key: cs.key, Title: cs.title, Unit: cs.unit, Times: times, Series: make([]model.MetricSeries, len(series))}
	for si, s := range series {
		vals := make([]*float64, len(times))
		for _, smp := range s.samples {
			v := smp.v
			vals[at[smp.t]] = &v
		}
		chart.Series[si] = model.MetricSeries{Name: seriesName(s.labels), Values: vals}
	}
	return chart
}

// seriesName labels one scope-chart series by the VM it tracks.
func seriesName(labels map[string]string) string {
	ns, name := labels["namespace"], labels["name"]
	switch {
	case ns != "" && name != "":
		return ns + "/" + name
	case name != "":
		return name
	default:
		return "value"
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

type sample struct {
	t int64
	v float64
}

// labeledSeries is one query_range result series: its labels and samples.
type labeledSeries struct {
	labels  map[string]string
	samples []sample
}

// rangeSeries runs one query_range and returns every result series with its
// labels — the multi-series read behind the scope charts (and the per-NIC/
// per-drive variants to come). NaN/Inf values are dropped as gaps.
func (c *Client) rangeSeries(ctx context.Context, token, query string, start, end int64, step int) ([]labeledSeries, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start, 10))
	q.Set("end", strconv.FormatInt(end, 10))
	q.Set("step", strconv.Itoa(step))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query API status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, nil
	}
	out := make([]labeledSeries, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		samples := make([]sample, 0, len(r.Values))
		for _, pair := range r.Values {
			var ts float64
			var vs string
			if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &vs) != nil {
				continue
			}
			v, err := strconv.ParseFloat(vs, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			samples = append(samples, sample{t: int64(ts), v: v})
		}
		out = append(out, labeledSeries{labels: r.Metric, samples: samples})
	}
	return out, nil
}

// rangeQuery runs one query_range and returns the first series' samples (the
// curated per-VM queries each yield a single series).
func (c *Client) rangeQuery(ctx context.Context, token, query string, start, end int64, step int) ([]sample, error) {
	series, err := c.rangeSeries(ctx, token, query, start, end, step)
	if err != nil || len(series) == 0 {
		return nil, err
	}
	return series[0].samples, nil
}

// VMMetrics runs the curated charts for one VM over the given range concurrently,
// then aligns each chart's series onto a shared time axis (one x-array + per-series
// value arrays, gaps as nil — directly chartable). A per-series failure degrades to
// gaps; a dead endpoint (every query errors with no data) returns ErrUnavailable.
func (c *Client) VMMetrics(ctx context.Context, token, ns, name, rng string) (model.VMMetrics, error) {
	spec, ok := ranges[rng]
	if !ok {
		rng, spec = defaultRange, ranges[defaultRange]
	}
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
	samples := make([][][]sample, len(specs)) // samples[chart][series]
	for ci, cs := range specs {
		samples[ci] = make([][]sample, len(cs.series))
		for si, ss := range cs.series {
			wg.Add(1)
			go func(ci, si int, query string) {
				defer wg.Done()
				smp, err := c.rangeQuery(ctx, token, query, start, end, step)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				if len(smp) > 0 {
					anyData = true
				}
				samples[ci][si] = smp
			}(ci, si, ss.query)
		}
	}
	wg.Wait()
	if firstErr != nil && !anyData {
		return model.VMMetrics{}, fmt.Errorf("%w: %v", model.ErrUnavailable, firstErr)
	}

	out := model.VMMetrics{Range: rng, StepSec: step, Charts: make([]model.MetricChart, len(specs))}
	for ci, cs := range specs {
		// Shared x-axis = the union of all series' timestamps in this chart.
		set := map[int64]struct{}{}
		for _, smp := range samples[ci] {
			for _, s := range smp {
				set[s.t] = struct{}{}
			}
		}
		times := make([]int64, 0, len(set))
		for t := range set {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		at := make(map[int64]int, len(times))
		for i, t := range times {
			at[t] = i
		}
		chart := model.MetricChart{Key: cs.key, Title: cs.title, Unit: cs.unit, Times: times, Series: make([]model.MetricSeries, len(cs.series))}
		for si, ss := range cs.series {
			vals := make([]*float64, len(times))
			for _, s := range samples[ci][si] {
				v := s.v
				vals[at[s.t]] = &v
			}
			chart.Series[si] = model.MetricSeries{Name: ss.name, Values: vals}
		}
		out.Charts[ci] = chart
	}
	c.vmMetrics.Put(key, out)
	return out, nil
}
