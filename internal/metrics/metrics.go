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
)

// Client talks to a Prometheus query API (the OpenShift Thanos querier Route).
type Client struct {
	base string
	http *http.Client
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
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 20 * time.Second, Transport: tr},
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

type sample struct {
	t int64
	v float64
}

// rangeQuery runs one query_range and returns the first series' samples (our
// queries each yield a single series). NaN/Inf values are dropped as gaps.
func (c *Client) rangeQuery(ctx context.Context, token, query string, start, end int64, step int) ([]sample, error) {
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
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" || len(body.Data.Result) == 0 {
		return nil, nil
	}
	raw := body.Data.Result[0].Values
	out := make([]sample, 0, len(raw))
	for _, pair := range raw {
		var ts float64
		var vs string
		if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &vs) != nil {
			continue
		}
		v, err := strconv.ParseFloat(vs, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out = append(out, sample{t: int64(ts), v: v})
	}
	return out, nil
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
	return out, nil
}
