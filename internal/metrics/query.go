// Prometheus query-API transport: the HTTP path, instant/range reads, and
// PromQL duration helpers.

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// queryJSON performs one query-API GET under the caller's token and decodes
// the response envelope into out — the single HTTP path under vector,
// rangeSeries, and friends.
func (c *Client) queryJSON(ctx context.Context, token, apiPath string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+apiPath+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("query API status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// parseSampleValue decodes one Prometheus sample value; NaN/Inf are dropped as
// gaps (false).
func parseSampleValue(raw json.RawMessage) (float64, bool) {
	var vs string
	if json.Unmarshal(raw, &vs) != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(vs, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
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
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if c.queryJSON(ctx, token, "/api/v1/query", v, &body) != nil {
		return nil
	}
	out := make([]labeledValue, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		f, ok := parseSampleValue(r.Value[1])
		if !ok {
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
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.queryJSON(ctx, token, "/api/v1/query_range", q, &body); err != nil {
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
			if json.Unmarshal(pair[0], &ts) != nil {
				continue
			}
			v, ok := parseSampleValue(pair[1])
			if !ok {
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
