package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

	// A second poll within the negative TTL sends no second fan-out; past it,
	// the grant shows.
	_, before := q.seen()
	if cs, _ := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", false); cs.Unavailable != model.MetricsForbidden {
		t.Errorf("second poll = %+v, want the cached forbidden", cs)
	}
	if _, after := q.seen(); len(after["a"]) != len(before["a"]) || len(after["b"]) != len(before["b"]) {
		t.Errorf("second poll within the negative TTL fanned out again: a %d to %d", len(before["a"]), len(after["a"]))
	}
	q.setAllow(func(string, string) bool { return true })
	c.negTTL = 0
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
	wideBefore, _ := q.seen()
	if sm, _ := noTenancy.ScopeMetrics(ctx, tenant, []string{"a"}, "", "1h"); sm.Unavailable != model.MetricsUnconfigured {
		t.Errorf("no tenancy URL: scope = %+v, want unconfigured", sm)
	}
	if wideAfter, _ := q.seen(); len(wideAfter) != len(wideBefore) {
		t.Error("a tenant with no tenancy URL was sent to the cluster-wide port")
	}
	var off *Client
	if u, _ := off.VMUsage(ctx, Caller{Token: adminTok, Wide: true}, "a", "vm-a"); u.Unavailable != model.MetricsUnconfigured {
		t.Errorf("metrics off: usage = %+v, want unconfigured", u)
	}
}

// No cached result reaches a caller with fewer rights: a wide fill serves only
// wide callers, and a tenancy fill only the token it ran under, so the
// tenancy port judges every other caller itself.
func TestCacheKeepsCallersApart(t *testing.T) {
	ctx := context.Background()
	q := newQuerier(t)
	q.setAllow(func(tok, ns string) bool { return tok == tenantTok })
	c := q.client()
	admin, other := Caller{Token: adminTok, Wide: true}, Caller{Token: "tenant-b"}

	if m, _ := c.VMMetrics(ctx, admin, "a", "vm-a", "1h"); m.Unavailable != "" {
		t.Fatalf("admin VM metrics = %+v", m)
	}
	if m, _ := c.VMMetrics(ctx, other, "a", "vm-a", "1h"); m.Unavailable != model.MetricsForbidden {
		t.Errorf("a wide fill reached a tenancy caller: %+v", m)
	}
	if cs, _ := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", false); cs.VMs["running"] != 3 {
		t.Fatalf("tenant summary = %+v", cs)
	}
	if cs, _ := c.ClusterSummary(ctx, other, []string{"a", "b"}, "", false); cs.Unavailable != model.MetricsForbidden {
		t.Errorf("one tenant's fill reached another: %+v", cs)
	}
}

// Node views come only from the cluster-wide port: an admin's fill never
// reaches a node reader without that port's right, who is told forbidden
// and sends no query.
func TestNodeViewsServeOnlyWideCallers(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"node":"w1","instance":"w1"},"value":[100,"0.5"]}]}}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.URL, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, reader := Caller{Token: adminTok, Wide: true}, Caller{Token: "node-reader"}
	if load, err := c.HostLoad(ctx, admin); err != nil || len(load.Nodes) != 1 {
		t.Fatalf("admin host load = %+v, %v", load, err)
	}
	if hc, err := c.Capacity(ctx, admin); err != nil || len(hc.Nodes) != 1 {
		t.Fatalf("admin capacity = %+v, %v", hc, err)
	}
	filled := hits.Load()
	if load, err := c.HostLoad(ctx, reader); err != nil || load.Unavailable != model.MetricsForbidden || len(load.Nodes) != 0 {
		t.Errorf("node reader host load = %+v, %v, want forbidden and no nodes", load, err)
	}
	if hc, err := c.Capacity(ctx, reader); err != nil || hc.Unavailable != model.MetricsForbidden || len(hc.Nodes) != 0 {
		t.Errorf("node reader capacity = %+v, %v, want forbidden and no nodes", hc, err)
	}
	if hits.Load() != filled {
		t.Error("a caller who is not wide sent a node query")
	}
}

// A fill every caller left is cut short: it is never kept, so the next caller
// fills afresh instead of reading the remains as data.
func TestCancelledFillIsNotKept(t *testing.T) {
	cache := newCache[int]()
	c := &Client{negTTL: negativeTTL}
	ctx, leave := context.WithCancel(context.Background())
	returned := make(chan struct{})
	_, err := cached(ctx, c, cache, Caller{Token: adminTok, Wide: true}, clusterTTL, "v", func(fctx context.Context) (int, outcome) {
		defer close(returned)
		leave()
		<-fctx.Done()
		return 1, outcome{}
	})
	if err == nil {
		t.Fatal("the caller who left got no error")
	}
	<-returned
	time.Sleep(20 * time.Millisecond) // the fill's Put, if any, follows its return
	if _, _, ok := cache.GetAge(Caller{Token: adminTok, Wide: true}.key("v")); ok {
		t.Error("a fill cut short by every caller leaving was kept")
	}
}

// The querier refusing one wide token is that token's answer: the next wide
// caller is not served it.
func TestWideRefusalIsNotShared(t *testing.T) {
	ctx := context.Background()
	q := newQuerier(t)
	c := q.client()
	revoked, admin := Caller{Token: "revoked-admin", Wide: true}, Caller{Token: adminTok, Wide: true}
	if cs, _ := c.ClusterSummary(ctx, revoked, []string{"a", "b"}, "", false); cs.Unavailable != model.MetricsForbidden {
		t.Fatalf("refused admin summary = %+v, want forbidden", cs)
	}
	if cs, _ := c.ClusterSummary(ctx, admin, []string{"a", "b"}, "", false); cs.Unavailable != "" || cs.VMs["running"] != 3 {
		t.Errorf("one admin's refusal reached another: %+v", cs)
	}
}

// Some namespaces failing still shows the rest, marked partial.
func TestPartialSaysSo(t *testing.T) {
	ctx := context.Background()
	q := newQuerier(t)
	q.setAllow(func(tok, ns string) bool { return ns == "a" })
	c := q.client()
	if cs, _ := c.ClusterSummary(ctx, tenant, []string{"a", "b"}, "", false); cs.Unavailable != model.MetricsPartial || cs.Memory.Allocated != 1 {
		t.Errorf("summary = %+v, want namespace a's sums marked partial", cs)
	}
	if sm, _ := c.ScopeMetrics(ctx, tenant, []string{"a", "b"}, "", "1h"); sm.Unavailable != model.MetricsPartial || len(sm.Charts[0].Series) != 1 {
		t.Errorf("scope = %+v, want a's VM marked partial", sm)
	}
	if nt, _ := c.NetworkTraffic(ctx, tenant, []Attachment{{"a", "vm-a", "eth0"}, {"b", "vm-b", "eth0"}}, 5); nt.Unavailable != model.MetricsPartial || len(nt.VMs) != 1 {
		t.Errorf("traffic = %+v, want a's VM marked partial", nt)
	}
	if _, err := c.Alerts(ctx, tenant, []string{"a", "b"}); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("partial alerts err = %v, want ErrUnavailable", err)
	}
}

// A tenancy view wider than maxTenancyNamespaces answers scope and sends
// nothing; a wide caller is not capped.
func TestTenancyScopeCap(t *testing.T) {
	ctx := context.Background()
	q := newQuerier(t)
	c := q.client()
	nss := make([]string, maxTenancyNamespaces+1)
	for i := range nss {
		nss[i] = fmt.Sprintf("ns-%02d", i)
	}
	if cs, _ := c.ClusterSummary(ctx, tenant, nss, "", false); cs.Unavailable != model.MetricsScope {
		t.Errorf("wide tenant scope = %+v, want scope", cs)
	}
	if _, err := c.Alerts(ctx, tenant, nss); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("wide tenant alerts err = %v, want ErrUnavailable", err)
	}
	if _, perNS := q.seen(); len(perNS) != 0 {
		t.Errorf("a capped view sent tenancy queries: %d namespaces", len(perNS))
	}
	if cs, _ := c.ClusterSummary(ctx, Caller{Token: adminTok, Wide: true}, nss, "", false); cs.Unavailable == model.MetricsScope {
		t.Error("a wide caller was capped")
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

	// Four users' views at once still share the process-wide tenancy bound.
	q.mu.Lock()
	q.peak = 0
	q.mu.Unlock()
	q.setAllow(func(string, string) bool { return true })
	for i := range 4 {
		wg.Go(func() {
			who := Caller{Token: fmt.Sprintf("user-%d", i)}
			if _, err := c.ScopeMetrics(context.Background(), who, nss, "", "1h"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if peak := q.peakInFlight(); peak > tenancyConcurrency {
		t.Errorf("peak in flight across users = %d, want at most %d", peak, tenancyConcurrency)
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
