package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/model"
)

func TestPromDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2 * time.Minute:  "2m",
		90 * time.Second: "90s",
		time.Hour:        "1h",
		2 * time.Hour:    "2h",
	} {
		if got := promDur(d); got != want {
			t.Errorf("promDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRateWindow(t *testing.T) {
	if got := rateWindow(30 * time.Second); got != 2*time.Minute {
		t.Errorf("rateWindow(30s) = %v, want 2m (floored)", got)
	}
	if got := rateWindow(5 * time.Minute); got != 10*time.Minute {
		t.Errorf("rateWindow(5m) = %v, want 10m", got)
	}
}

// TestVMMetricsAlignsSeries verifies the parsing + shared-axis assembly: two series
// in one chart whose samples land on different timestamps must align onto the union
// axis, with nil gaps where a series has no sample at a given time.
func TestVMMetricsAlignsSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		values := `[[100,"5"]]`
		switch {
		case strings.Contains(q, "cpu_usage_seconds_total"):
			values = `[[100,"10"],[130,"20"]]`
		case strings.Contains(q, "vcpu_wait_seconds_total"):
			values = `[[130,"1"],[160,"2"]]`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":%s}]}}`, values)
	}))
	defer srv.Close()

	m, err := New(srv.URL, false).VMMetrics(context.Background(), "tok", "ns", "vm", "1h")
	if err != nil {
		t.Fatalf("VMMetrics: %v", err)
	}

	var cpu model.MetricChart
	for _, c := range m.Charts {
		if c.Key == "cpu" {
			cpu = c
		}
	}
	if cpu.Key == "" {
		t.Fatal("no cpu chart in result")
	}
	if len(cpu.Series) < 2 || cpu.Series[0].Name != "Usage" || cpu.Series[1].Name != "Wait" {
		t.Fatalf("series = %+v, want [Usage Wait ...]", cpu.Series)
	}
	// Union of {100,130} (Usage) and {130,160} (Wait).
	if len(cpu.Times) != 3 || cpu.Times[0] != 100 || cpu.Times[1] != 130 || cpu.Times[2] != 160 {
		t.Fatalf("times = %v, want [100 130 160]", cpu.Times)
	}
	usage, wait := cpu.Series[0].Values, cpu.Series[1].Values
	if usage[0] == nil || *usage[0] != 10 || usage[1] == nil || *usage[1] != 20 || usage[2] != nil {
		t.Errorf("usage not aligned: [%s %s %s], want [10 20 nil]", p(usage[0]), p(usage[1]), p(usage[2]))
	}
	if wait[0] != nil || wait[1] == nil || *wait[1] != 1 || wait[2] == nil || *wait[2] != 2 {
		t.Errorf("wait not aligned: [%s %s %s], want [nil 1 2]", p(wait[0]), p(wait[1]), p(wait[2]))
	}
}

func p(f *float64) string {
	if f == nil {
		return "nil"
	}
	return fmt.Sprintf("%g", *f)
}

// TestVMMetricsFansOutPerLabel verifies a byLabel spec turns one query into one
// chart series per label value ("Rx eth0", "Rx eth1"), sorted, while fixed
// specs keep their single named series — and that memory carries the stacked
// flag.
func TestVMMetricsFansOutPerLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(q, "network_receive_bytes_total") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"interface":"eth1"},"values":[[100,"2"]]},
				{"metric":{"interface":"eth0"},"values":[[100,"1"]]}]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[100,"5"]]}]}}`)
	}))
	defer srv.Close()

	m, err := New(srv.URL, false).VMMetrics(context.Background(), "tok", "ns", "vm", "1h")
	if err != nil {
		t.Fatalf("VMMetrics: %v", err)
	}
	charts := map[string]model.MetricChart{}
	for _, c := range m.Charts {
		charts[c.Key] = c
	}

	net := charts["network"]
	if len(net.Series) != 3 {
		t.Fatalf("network series = %+v, want Rx eth0, Rx eth1, Tx <one>", net.Series)
	}
	if net.Series[0].Name != "Rx eth0" || net.Series[1].Name != "Rx eth1" {
		t.Errorf("per-NIC fan-out wrong: %q, %q", net.Series[0].Name, net.Series[1].Name)
	}
	if iops := charts["iops"]; iops.Unit != "iops" || len(iops.Series) == 0 {
		t.Errorf("iops chart missing or unitless: %+v", iops)
	}
	if !charts["memory"].Stacked {
		t.Error("memory chart should be marked stacked")
	}
	if charts["cpu"].Stacked || len(charts["cpu"].Series) != 3 || charts["cpu"].Series[0].Name != "Usage" {
		t.Errorf("fixed cpu chart changed: %+v", charts["cpu"].Series)
	}
}

// TestScopeMetricsNamesAndAlignsSeries verifies the multi-series read behind the
// scope charts: a topk result with two labeled series must come back as two
// chart series named namespace/name (sorted), aligned on the union time axis,
// and the query must carry the namespace scope.
func TestScopeMetricsNamesAndAlignsSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, `namespace=~"a|b"`) {
			t.Errorf("query not scoped to the namespaces: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(q, "cpu_usage_seconds_total") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"namespace":"b","name":"vm2"},"values":[[130,"3"]]},
				{"metric":{"namespace":"a","name":"vm1"},"values":[[100,"1"],[130,"2"]]}]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"namespace":"a","name":"vm1"},"values":[[100,"5"]]}]}}`)
	}))
	defer srv.Close()

	m, err := New(srv.URL, false).ScopeMetrics(context.Background(), "tok", []string{"a", "b"}, "", "1h")
	if err != nil {
		t.Fatalf("ScopeMetrics: %v", err)
	}

	var cpu model.MetricChart
	for _, c := range m.Charts {
		if c.Key == "cpu" {
			cpu = c
		}
	}
	if cpu.Key == "" {
		t.Fatal("no cpu chart in result")
	}
	if len(cpu.Series) != 2 || cpu.Series[0].Name != "a/vm1" || cpu.Series[1].Name != "b/vm2" {
		t.Fatalf("series = %+v, want [a/vm1 b/vm2] sorted by name", cpu.Series)
	}
	if len(cpu.Times) != 2 || cpu.Times[0] != 100 || cpu.Times[1] != 130 {
		t.Fatalf("times = %v, want [100 130]", cpu.Times)
	}
	vm1, vm2 := cpu.Series[0].Values, cpu.Series[1].Values
	if vm1[0] == nil || *vm1[0] != 1 || vm1[1] == nil || *vm1[1] != 2 {
		t.Errorf("vm1 not aligned: [%s %s], want [1 2]", p(vm1[0]), p(vm1[1]))
	}
	if vm2[0] != nil || vm2[1] == nil || *vm2[1] != 3 {
		t.Errorf("vm2 not aligned: [%s %s], want [nil 3]", p(vm2[0]), p(vm2[1]))
	}
}

// TestAlertsCollapsesAndSorts verifies the ALERTS read: identical alert tuples
// collapse with a count, and rows order most-urgent-first.
func TestAlertsCollapsesAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, `alertstate="firing"`) || !strings.Contains(q, `namespace=~"a|b"`) {
			t.Errorf("unexpected alerts query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"alertname":"VMIDown","severity":"warning","namespace":"a","name":"vm1"},"value":[100,"1"]},
			{"metric":{"alertname":"VMIDown","severity":"warning","namespace":"a","name":"vm1"},"value":[100,"1"]},
			{"metric":{"alertname":"NodePressure","severity":"critical","namespace":"b"},"value":[100,"1"]}]}}`)
	}))
	defer srv.Close()

	alerts, err := New(srv.URL, false).Alerts(context.Background(), "tok", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Alerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("want 2 collapsed rows, got %+v", alerts)
	}
	if alerts[0].Name != "NodePressure" || alerts[0].Severity != "critical" {
		t.Errorf("critical should sort first: %+v", alerts[0])
	}
	if alerts[1].Name != "VMIDown" || alerts[1].Count != 2 || alerts[1].VM != "vm1" {
		t.Errorf("duplicate series should collapse with count: %+v", alerts[1])
	}
}

// TestVectorAndConsumers verifies instant-vector parsing and that consumers() sorts
// a topk result highest-first.
func TestVectorAndConsumers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"a","name":"low"},"value":[100,"1"]},
			{"metric":{"namespace":"b","name":"high"},"value":[100,"9"]}]}}`)
	}))
	defer srv.Close()

	vec := New(srv.URL, false).vector(context.Background(), "tok", "q")
	if len(vec) != 2 {
		t.Fatalf("vector parsed %d series, want 2", len(vec))
	}
	cons := consumers(vec)
	if cons[0].Name != "high" || cons[0].Value != 9 || cons[1].Name != "low" {
		t.Errorf("consumers not sorted highest-first: %+v", cons)
	}
}
