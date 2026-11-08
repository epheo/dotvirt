package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/model"
)

const (
	adminTok  = "admin"
	tenantTok = "tenant"
)

// querier stubs the thanos-querier's two ports. The cluster-wide one serves
// only adminTok; the tenancy one serves a token in a namespace allow admits,
// demands the namespace parameter, and fails the test on a query whose
// namespace matcher differs from the one its label proxy injects. Answers are shaped
// like the real ones: a sum has no labels, a ranking or raw series names its
// VM, and namespace ns contributes value weight[ns] to each.
type querier struct {
	t             *testing.T
	wide, tenancy *httptest.Server
	allow         func(token, ns string) bool
	delay         time.Duration

	mu             sync.Mutex
	wideQ          []string            // queries sent to the cluster-wide port
	tenantQ        map[string][]string // namespace -> queries sent to the tenancy port
	inflight, peak int
}

var weight = map[string]float64{"a": 1, "b": 2}

func newQuerier(t *testing.T) *querier {
	q := &querier{t: t, tenantQ: map[string][]string{}, allow: func(tok, ns string) bool { return tok == tenantTok || tok == adminTok }}
	q.wide = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.record("", r)
		if bearer(r) != adminTok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		q.serve(w, r, "")
	}))
	q.tenancy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query()["namespace"]
		if len(ns) != 1 {
			t.Errorf("tenancy request without exactly one namespace: %v", r.URL.Query())
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		q.record(ns[0], r)
		if !q.allowed(bearer(r), ns[0]) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		query := r.URL.Query().Get("query")
		if strings.Contains(query, "namespace=~") || !strings.Contains(query, fmt.Sprintf("namespace=%q", ns[0])) {
			t.Errorf("tenancy query in %s must match exactly that namespace: %s", ns[0], query)
		}
		q.serve(w, r, ns[0])
	}))
	t.Cleanup(q.wide.Close)
	t.Cleanup(q.tenancy.Close)
	return q
}

func (q *querier) allowed(token, ns string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.allow(token, ns)
}

func (q *querier) setAllow(allow func(token, ns string) bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.allow = allow
}

func (q *querier) peakInFlight() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (q *querier) client() *Client {
	c, err := New(q.wide.URL, q.tenancy.URL, "", false)
	if err != nil {
		q.t.Fatal(err)
	}
	return c
}

// record notes one request to the cluster-wide port (ns "") or the tenancy
// port in ns, answered or refused.
func (q *querier) record(ns string, r *http.Request) {
	query := r.URL.Query().Get("query")
	q.mu.Lock()
	defer q.mu.Unlock()
	if ns == "" {
		q.wideQ = append(q.wideQ, query)
	} else {
		q.tenantQ[ns] = append(q.tenantQ[ns], query)
	}
}

func (q *querier) serve(w http.ResponseWriter, r *http.Request, ns string) {
	query := r.URL.Query().Get("query")
	q.mu.Lock()
	q.inflight++
	q.peak = max(q.peak, q.inflight)
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.inflight--
		q.mu.Unlock()
	}()
	time.Sleep(q.delay)

	nss := []string{ns}
	if ns == "" {
		nss = []string{"a", "b"}
	}
	var results []string
	var total float64
	for _, n := range nss {
		total += weight[n]
	}
	labels := func(n string) string {
		switch {
		case strings.HasPrefix(query, "sum by(phase)"):
			return `{"phase":"running"}`
		case strings.HasPrefix(query, "sum("):
			return `{}`
		case strings.HasPrefix(query, "ALERTS"):
			return fmt.Sprintf(`{"alertname":"VMDown","severity":"warning","namespace":%q}`, n)
		default:
			return fmt.Sprintf(`{"namespace":%q,"name":"vm-%s","interface":"eth0"}`, n, n)
		}
	}
	if strings.HasPrefix(query, "sum(") || strings.HasPrefix(query, "sum by(phase)") {
		results = append(results, sample1(r, labels(""), total))
	} else {
		for _, n := range nss {
			results = append(results, sample1(r, labels(n), weight[n]))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"success","data":{"result":[%s]}}`, strings.Join(results, ","))
}

// sample1 is one result series: a point at t=100 (instant) or two (range).
func sample1(r *http.Request, metric string, v float64) string {
	if strings.HasSuffix(r.URL.Path, "query_range") {
		return fmt.Sprintf(`{"metric":%s,"values":[[100,"%g"],[160,"%g"]]}`, metric, v, v)
	}
	return fmt.Sprintf(`{"metric":%s,"value":[100,"%g"]}`, metric, v)
}

func (q *querier) seen() (wide []string, tenant map[string][]string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	tenant = map[string][]string{}
	for ns, qs := range q.tenantQ {
		tenant[ns] = append([]string(nil), qs...)
	}
	return append([]string(nil), q.wideQ...), tenant
}

var tenant = Caller{Token: tenantTok}

// A tenant reads its own namespaces' series through the tenancy port, one
// query per namespace, folded into what the cluster-wide query answers; it
// never reaches the cluster-wide port, and gets no node totals there is no
// tenancy answer for.
func TestTenantReadsThroughTenancyPort(t *testing.T) {
	q := newQuerier(t)
	c := q.client()
	ctx := context.Background()

	cs, err := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Unavailable != "" || cs.VMs["running"] != 3 || cs.Memory.Allocated != 3 || cs.CPU.Used != 3 {
		t.Errorf("summary = %+v, want the two namespaces' sums (3)", cs)
	}
	if cs.CPU.Total != 0 || cs.Memory.Total != 0 {
		t.Errorf("tenant got node totals %v/%v", cs.CPU.Total, cs.Memory.Total)
	}
	if len(cs.TopCPU) != 2 || cs.TopCPU[0].Name != "vm-b" || cs.TopCPU[1].Name != "vm-a" {
		t.Errorf("top CPU = %+v, want vm-b then vm-a across both namespaces", cs.TopCPU)
	}

	m, err := c.VMMetrics(ctx, tenant, "a", "vm-a", "1h")
	if err != nil || m.Unavailable != "" || len(m.Charts) == 0 {
		t.Fatalf("VM metrics = %+v, %v", m, err)
	}
	sm, err := c.ScopeMetrics(ctx, tenant, []string{"a", "b"}, "", "1h")
	if err != nil || sm.Unavailable != "" || len(sm.Charts[0].Series) != 2 {
		t.Fatalf("scope metrics = %+v, %v, want both namespaces' VMs", sm, err)
	}
	nt, err := c.NetworkTraffic(ctx, tenant, []Attachment{{"a", "vm-a", "eth0"}, {"b", "vm-b", "eth0"}}, 5)
	if err != nil || nt.Unavailable != "" || len(nt.VMs) != 2 {
		t.Fatalf("traffic = %+v, %v, want both namespaces' VMs", nt, err)
	}
	alerts, err := c.Alerts(ctx, tenant, []string{"a", "b"})
	if err != nil || len(alerts) != 2 {
		t.Fatalf("alerts = %+v, %v, want one per namespace", alerts, err)
	}

	wide, perNS := q.seen()
	if len(wide) != 0 {
		t.Errorf("a tenant reached the cluster-wide port: %v", wide)
	}
	// Each view query once per namespace: the summary's 9, the scope's
	// charts, traffic and alerts in both; the VM's own charts in a only.
	shared := 9 + len(scopeChartSpecs("2m")) + 1 + 1
	if len(perNS["b"]) != shared || len(perNS["a"]) != shared+countVMQueries() {
		t.Errorf("tenancy queries a=%d b=%d, want %d and %d", len(perNS["a"]), len(perNS["b"]), shared+countVMQueries(), shared)
	}
	for _, qs := range perNS {
		for _, query := range qs {
			if strings.Contains(query, "kube_node") {
				t.Errorf("node query sent through the tenancy port: %s", query)
			}
		}
	}
}

// countVMQueries is the queries one VMMetrics view sends.
func countVMQueries() int {
	n := 0
	for _, cs := range chartSpecs("a", "vm-a", "2m") {
		n += len(cs.series)
	}
	return n
}

// A caller who may query cluster-wide sends each view query once, over all its
// namespaces, and never fans out.
func TestWideCallerSendsOneQuery(t *testing.T) {
	q := newQuerier(t)
	cs, err := q.client().ClusterSummary(context.Background(), Caller{Token: adminTok, Wide: true}, []string{"a", "b"}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cs.VMs["running"] != 3 || cs.CPU.Total != 3 {
		t.Errorf("summary = %+v, want running 3 and node totals", cs)
	}
	wide, perNS := q.seen()
	if len(perNS) != 0 {
		t.Errorf("a wide caller fanned out: %v", perNS)
	}
	seen := map[string]int{}
	for _, query := range wide {
		seen[query]++
		if !strings.Contains(query, "kube_node") && !strings.Contains(query, `namespace=~"a|b"`) {
			t.Errorf("query not spanning both namespaces: %s", query)
		}
	}
	for query, n := range seen {
		if n != 1 {
			t.Errorf("query sent %d times: %s", n, query)
		}
	}
	if len(wide) != 11 {
		t.Errorf("sent %d queries, want 11 (9 VM sums and rankings, 2 node totals)", len(wide))
	}
}

// Forbidden, down, or no endpoint for the caller: every view answers why,
// never zeros or an empty phase map, and nothing failed is cached.
func TestUnavailableIsAState(t *testing.T) {
	ctx := context.Background()
	q := newQuerier(t)
	q.setAllow(func(string, string) bool { return false })
	c := q.client()

	cs, err := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Unavailable != model.MetricsForbidden || cs.VMs != nil {
		t.Errorf("forbidden summary = %+v, want forbidden and no phase map", cs)
	}
	if wide, tenantQ := q.seen(); len(wide) != 0 || len(tenantQ["a"]) == 0 {
		t.Fatalf("forbidden tenant sent %d cluster-wide and %d tenancy queries, want 0 and some", len(wide), len(tenantQ["a"]))
	}
	m, err := c.VMMetrics(ctx, tenant, "a", "vm-a", "1h")
	if err != nil || m.Unavailable != model.MetricsForbidden {
		t.Errorf("forbidden VM metrics = %+v, %v", m, err)
	}
	u, err := c.VMUsage(ctx, tenant, "a", "vm-a")
	if err != nil || u.Unavailable != model.MetricsForbidden {
		t.Errorf("forbidden VM usage = %+v, %v", u, err)
	}
	if _, err := c.Alerts(ctx, tenant, []string{"a"}); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("forbidden alerts err = %v, want ErrUnavailable, never an empty list", err)
	}

	// Allowed again: the failure was not cached.
	q.setAllow(func(string, string) bool { return true })
	if cs, _ := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", false); cs.Unavailable != "" || cs.VMs["running"] != 3 {
		t.Errorf("after the grant, summary = %+v, want data", cs)
	}

	q.tenancy.Close()
	nt, err := c.NetworkTraffic(ctx, tenant, []Attachment{{"a", "vm-a", "eth0"}}, 5)
	if err != nil || nt.Unavailable != model.MetricsUnreachable || nt.VMs == nil {
		t.Errorf("down traffic = %+v, %v, want unreachable", nt, err)
	}

	noTenancy, err := New(q.wide.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := q.seen()
	if sm, _ := noTenancy.ScopeMetrics(ctx, tenant, []string{"a"}, "", "1h"); sm.Unavailable != model.MetricsUnconfigured {
		t.Errorf("no tenancy URL: scope = %+v, want unconfigured", sm)
	}
	if after, _ := q.seen(); len(after) != len(before) {
		t.Error("a tenant with no tenancy URL was sent to the cluster-wide port")
	}
	var off *Client
	if u, _ := off.VMUsage(ctx, Caller{Token: adminTok, Wide: true}, "a", "vm-a"); u.Unavailable != model.MetricsUnconfigured {
		t.Errorf("metrics off: usage = %+v, want unconfigured", u)
	}
}

// One view's namespace fan-out keeps at most viewConcurrency queries in
// flight, and concurrent callers of one view share one fan-out.
func TestTenantFanOutBounded(t *testing.T) {
	q := newQuerier(t)
	q.delay = 5 * time.Millisecond
	c := q.client()
	nss := make([]string, 30)
	for i := range nss {
		nss[i] = fmt.Sprintf("ns-%02d", i)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := c.ScopeMetrics(context.Background(), tenant, nss, "", "1h"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	_, perNS := q.seen()
	charts := len(scopeChartSpecs("2m"))
	for _, ns := range nss {
		if n := len(perNS[ns]); n != charts {
			t.Errorf("%s queried %d times, want %d: one fan-out for the three callers", ns, n, charts)
		}
	}
	if peak := q.peakInFlight(); peak > viewConcurrency {
		t.Errorf("peak in flight = %d, want at most %d", peak, viewConcurrency)
	}
}

// Folded per-namespace topk answers are the cluster-wide topk: at each step
// only the k largest survive, and a series left with no point is dropped.
func TestTopKSeriesPerStep(t *testing.T) {
	s := func(name string, pts ...[2]float64) labeledSeries {
		ls := labeledSeries{labels: map[string]string{"name": name}}
		for _, p := range pts {
			ls.samples = append(ls.samples, sample{t: int64(p[0]), v: p[1]})
		}
		return ls
	}
	got := topKSeries([]labeledSeries{
		s("a", [2]float64{1, 5}, [2]float64{2, 1}),
		s("b", [2]float64{1, 3}, [2]float64{2, 9}),
		s("c", [2]float64{1, 1}, [2]float64{2, 2}),
	}, 2)
	want := map[string][]int64{"a": {1}, "b": {1, 2}, "c": {2}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, ls := range got {
		var ts []int64
		for _, smp := range ls.samples {
			ts = append(ts, smp.t)
		}
		if fmt.Sprint(ts) != fmt.Sprint(want[ls.labels["name"]]) {
			t.Errorf("%s kept steps %v, want %v", ls.labels["name"], ts, want[ls.labels["name"]])
		}
	}
	if len(topKSeries([]labeledSeries{s("a", [2]float64{1, 1})}, 0)) != 0 {
		t.Error("k=0 keeps nothing")
	}
}

// Range answers fold per timestamp: shared steps add, others pass through.
func TestFoldSeriesAddsPerStep(t *testing.T) {
	sum := map[string]string{}
	got := foldSeries([][]labeledSeries{
		{{labels: sum, samples: []sample{{1, 1}, {2, 2}}}},
		{{labels: sum, samples: []sample{{2, 3}, {3, 4}}}},
	})
	if len(got) != 1 || fmt.Sprint(got[0].samples) != "[{1 1} {2 5} {3 4}]" {
		t.Errorf("folded = %+v, want [{1 1} {2 5} {3 4}]", got)
	}
}
