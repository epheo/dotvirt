// Prometheus query-API transport: the HTTP path, instant/range reads, one
// view's tally of how they went, and PromQL duration helpers.

package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/model"
)

// statusError is a query API answer other than 200.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("query API status %d", e.code) }

// reasonFor names a failed read for the UI: the backend refusing the token,
// or anything else going wrong on the way.
func reasonFor(err error) model.MetricsReason {
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusUnauthorized || se.code == http.StatusForbidden) {
		return model.MetricsForbidden
	}
	return model.MetricsUnreachable
}

// viewConcurrency bounds the queries one view has in flight, whatever its
// namespace fan-out.
const viewConcurrency = 8

// read is one view's queries: who they run as, the bound on how many are in
// flight, and how they went. Its methods return no error: a failed query
// reads as no series, and outcome says what the view may claim.
type read struct {
	c     *Client
	token string
	sem   chan struct{}

	mu   sync.Mutex
	data bool  // some query returned a series
	err  error // the first failure
}

func (c *Client) read(token string) *read {
	return &read{c: c, token: token, sem: make(chan struct{}, viewConcurrency)}
}

func (r *read) note(data bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if r.err == nil {
			r.err = err
		}
		return
	}
	r.data = r.data || data
}

// outcome reports, once every query returned, why the view has nothing to
// show ("" when a query returned data or none failed) and whether it is
// whole: only a whole view is cached, so one caller's failure never serves
// another.
func (r *read) outcome() (model.MetricsReason, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		return "", true
	}
	if r.data {
		return "", false
	}
	// The backend's text can carry the querier's URL: logged, never answered.
	log.Printf("metrics: query failed: %v", r.err)
	return reasonFor(r.err), false
}

// get performs one query-API GET under the view's token and decodes the
// response envelope into out. ns "" asks the cluster-wide API; any other
// asks the tenancy API, which restricts the query to ns.
func (r *read) get(ctx context.Context, ns, apiPath string, params url.Values, out any) error {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	base := r.c.base
	if ns != "" {
		base = r.c.tenancy
		params.Set("namespace", ns)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+apiPath+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &statusError{code: resp.StatusCode}
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

// vector runs one instant query in ns ("" cluster-wide).
func (r *read) vector(ctx context.Context, ns, query string) []labeledValue {
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
	if err := r.get(ctx, ns, "/api/v1/query", v, &body); err != nil {
		r.note(false, err)
		return nil
	}
	out := make([]labeledValue, 0, len(body.Data.Result))
	for _, res := range body.Data.Result {
		f, ok := parseSampleValue(res.Value[1])
		if !ok {
			continue
		}
		out = append(out, labeledValue{labels: res.Metric, value: f})
	}
	r.note(len(out) > 0, nil)
	return out
}

// first is an instant answer's first value (a sum's only one): 0 when absent.
func first(vec []labeledValue) float64 {
	if len(vec) == 0 {
		return 0
	}
	return vec[0].value
}

// parallel runs fns concurrently and waits for all of them.
func parallel(fns ...func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Go(fn)
	}
	wg.Wait()
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

// span is a query_range window: unix seconds and a step in seconds.
type span struct {
	start, end int64
	step       int
}

// lastHour is the sparkline window: the last hour at a 2m step, ~30 points.
func lastHour() span {
	end := time.Now().Unix()
	return span{start: end - 3600, end: end, step: 120}
}

// series runs one query_range in ns ("" cluster-wide) and returns every
// result series with its labels. NaN/Inf values are dropped as gaps.
func (r *read) series(ctx context.Context, ns, query string, w span) []labeledSeries {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(w.start, 10))
	q.Set("end", strconv.FormatInt(w.end, 10))
	q.Set("step", strconv.Itoa(w.step))
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	err := r.get(ctx, ns, "/api/v1/query_range", q, &body)
	if err == nil && body.Status != "success" {
		err = fmt.Errorf("query API status %q", body.Status)
	}
	if err != nil {
		r.note(false, err)
		return nil
	}
	out := make([]labeledSeries, 0, len(body.Data.Result))
	data := false
	for _, res := range body.Data.Result {
		samples := make([]sample, 0, len(res.Values))
		for _, pair := range res.Values {
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
		data = data || len(samples) > 0
		out = append(out, labeledSeries{labels: res.Metric, samples: samples})
	}
	r.note(data, nil)
	return out
}

// samplesOf is the first series' samples (the curated per-VM queries each
// yield a single series).
func samplesOf(series []labeledSeries) []sample {
	if len(series) == 0 {
		return nil
	}
	return series[0].samples
}

type sparkResult struct {
	vals []float64
	last float64
}

// sparkOf shapes a sparkline query's answer as its recent values plus the
// latest one (an inline sparkline + readout).
func sparkOf(series []labeledSeries) sparkResult {
	smp := samplesOf(series)
	vals := make([]float64, len(smp))
	for i, s := range smp {
		vals[i] = s.v
	}
	var last float64
	if len(vals) > 0 {
		last = vals[len(vals)-1]
	}
	return sparkResult{vals: vals, last: last}
}

// rateWindow is the lookback for rate(): a couple of steps, floored so even the
// 30s step spans enough scrapes to be smooth.
func rateWindow(step time.Duration) time.Duration {
	if w := 2 * step; w > 2*time.Minute {
		return w
	}
	return 2 * time.Minute
}

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
