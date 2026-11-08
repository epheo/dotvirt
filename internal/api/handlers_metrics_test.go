package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/model"
)

// The band is the descheduler's real trigger window: exact outside-band
// counts, a floor at 0, AsymmetricLow's below-mean-is-a-target semantics, and
// no band at all for an unknown (hand-edited) threshold.
func TestFoldDRSBand(t *testing.T) {
	workers := func(pcts ...float64) []model.HostWorker {
		ws := make([]model.HostWorker, len(pcts))
		for i, p := range pcts {
			ws[i] = model.HostWorker{Pct: p}
		}
		return ws
	}
	load := model.HostLoad{Mean: 30, Nodes: workers(76, 30, 25, 7)}
	foldDRSBand(&load, "Low")
	if load.Band == nil || load.Band.Low != 20 || load.Band.High != 40 {
		t.Fatalf("band = %+v, want [20,40] around mean 30", load.Band)
	}
	if load.Band.Above != 1 || load.Band.Below != 1 {
		t.Errorf("above/below = %d/%d, want 1/1 (30 and 25 are in-band)", load.Band.Above, load.Band.Below)
	}

	asym := model.HostLoad{Mean: 5, Nodes: workers(2, 4, 20)}
	foldDRSBand(&asym, "AsymmetricLow")
	if asym.Band == nil || asym.Band.Low != 5 || asym.Band.High != 15 {
		t.Fatalf("asymmetric band = %+v, want [5,15]: below-mean already counts as a target", asym.Band)
	}
	if asym.Band.Above != 1 || asym.Band.Below != 2 {
		t.Errorf("asymmetric above/below = %d/%d, want 1/2", asym.Band.Above, asym.Band.Below)
	}

	unknown := model.HostLoad{Mean: 30, Nodes: workers(76, 30, 25, 7)}
	foldDRSBand(&unknown, "hand-edited")
	if unknown.Band != nil {
		t.Errorf("unknown threshold must not fabricate a band, got %+v", unknown.Band)
	}
}

// The phase map derives from kubevirt_vmi_info, which only sees VMIs - a
// stopped VM has none and was invisible ("2 Running" on a 3-VM project, found
// on the live console). foldStoppedVMs adds them under the same lowercase
// key convention the metric-derived entries use.
func TestFoldStoppedVMs(t *testing.T) {
	cs := model.ClusterSummary{VMs: map[string]int{"running": 2}}
	foldStoppedVMs(&cs, 1)
	if cs.VMs["running"] != 2 || cs.VMs["stopped"] != 1 {
		t.Fatalf("vms = %v, want running:2 stopped:1", cs.VMs)
	}

	// All running: the map is left untouched (no zero entry).
	cs = model.ClusterSummary{VMs: map[string]int{"running": 1}}
	foldStoppedVMs(&cs, 0)
	if _, ok := cs.VMs["stopped"]; ok {
		t.Fatal("no stopped VMs must add no entry")
	}

	// Nil map (metrics returned nothing): still reports the stopped VM.
	cs = model.ClusterSummary{}
	foldStoppedVMs(&cs, 1)
	if cs.VMs["stopped"] != 1 {
		t.Fatalf("vms = %v, want stopped:1", cs.VMs)
	}
}

// A network's attachments are the VM interfaces that resolve to it: a pod
// interface rides the namespace's primary network when one exists, a multus
// reference resolves a project network by "namespace/name" (bare names are
// namespace-relative) and a shared network by name.
func TestNetworkAttachments(t *testing.T) {
	nets := []model.Network{
		{Name: "web-net", Kind: model.NetworkDefault, Scope: model.ScopeProject, Namespace: "web-prod", AttachRef: "web-prod/web-net"},
		{Name: "l2", Kind: model.NetworkInternal, Scope: model.ScopeProject, Namespace: "db-prod", AttachRef: "db-prod/l2"},
		{Name: "l2", Kind: model.NetworkInternal, Scope: model.ScopeProject, Namespace: "web-prod", AttachRef: "web-prod/l2"},
		{Name: "lan-101", Kind: model.NetworkVLAN, Scope: model.ScopeShared, AttachRef: "lan-101", Namespaces: []string{"web-prod", "db-prod"}},
	}
	vm := func(ns, name string, nws ...kubevirtcorev1.Network) kubevirtcorev1.VirtualMachine {
		return kubevirtcorev1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: kubevirtcorev1.VirtualMachineSpec{Template: &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtcorev1.VirtualMachineInstanceSpec{Networks: nws},
			}},
		}
	}
	pod := func(name string) kubevirtcorev1.Network {
		return kubevirtcorev1.Network{Name: name, NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}}
	}
	multus := func(name, ref string) kubevirtcorev1.Network {
		return kubevirtcorev1.Network{Name: name, NetworkSource: kubevirtcorev1.NetworkSource{Multus: &kubevirtcorev1.MultusNetwork{NetworkName: ref}}}
	}
	vms := []kubevirtcorev1.VirtualMachine{
		vm("web-prod", "web-1", pod("default"), multus("lan", "lan-101"), multus("l2", "l2")),
		vm("db-prod", "db-1", pod("default"), multus("lan", "db-prod/lan-101"), multus("l2", "db-prod/l2")),
	}
	str := func(as []metrics.Attachment) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.Namespace+"/"+a.Name+"/"+a.Interface)
		}
		return out
	}
	for _, c := range []struct {
		target model.Network
		want   []string
	}{
		{nets[0], []string{"web-prod/web-1/default"}},
		{model.Network{}, []string{"db-prod/db-1/default"}},
		{nets[1], []string{"db-prod/db-1/l2"}},
		{nets[2], []string{"web-prod/web-1/l2"}},
		{nets[3], []string{"web-prod/web-1/lan", "db-prod/db-1/lan"}},
	} {
		got := str(networkAttachments(vms, nets, c.target))
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s/%s: attachments = %v, want %v", c.target.Namespace, c.target.Name, got, c.want)
		}
	}
}

// Through the routes: the cluster-wide metrics right, asked as the querier's
// cluster-wide port asks it, sends an admin's summary there as one query per
// sum; a tenant without it reads its own namespace on the tenancy port and
// never the cluster-wide one. With metrics off the view says so, not 503.
func TestMetricsCallerPicksThePort(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	var mu sync.Mutex
	var asked []authzv1.ResourceAttributes
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if a.Resource == "prometheuses" {
			mu.Lock()
			asked = append(asked, *a)
			mu.Unlock()
		}
		return token == adminToken
	}
	type hit struct{ port, token, ns string }
	var hits []hit
	querier := func(port string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits = append(hits, hit{port, bearer(r), r.URL.Query().Get("namespace")})
			mu.Unlock()
			if port == "wide" && bearer(r) != adminToken {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "query_range") {
				fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{},"values":[[100,"2"]]}]}}`)
				return
			}
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"phase":"running"},"value":[100,"2"]}]}}`)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	wide, tenancy := querier("wide"), querier("tenancy")
	s := fc.server(nil, Config{})
	fresh := func() {
		var err error
		if s.metrics, err = metrics.New(wide.URL, tenancy.URL, "", false); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		hits = nil
		mu.Unlock()
	}

	fresh()
	rec := do(s, tenantToken, http.MethodGet, "/api/metrics/cluster", "")
	wantStatus(t, rec, http.StatusOK)
	if cs := decodeBody[model.ClusterSummary](t, rec); cs.Unavailable != "" || cs.VMs["running"] != 2 {
		t.Errorf("tenant summary = %+v, want its namespace's VMs", cs)
	}
	mu.Lock()
	for _, h := range hits {
		if h != (hit{"tenancy", tenantToken, "team-a-dev"}) {
			t.Errorf("tenant query went to %+v, want the tenancy port in team-a-dev", h)
		}
	}
	tenantHits := len(hits)
	mu.Unlock()
	if tenantHits == 0 {
		t.Fatal("tenant summary sent no query")
	}

	fresh()
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/metrics/cluster", ""), http.StatusOK)
	mu.Lock()
	for _, h := range hits {
		if h != (hit{"wide", adminToken, ""}) {
			t.Errorf("admin query went to %+v, want the cluster-wide port", h)
		}
	}
	if len(hits) != tenantHits+2 {
		t.Errorf("admin sent %d queries, want %d: one per sum plus the 2 node totals", len(hits), tenantHits+2)
	}
	want := authzv1.ResourceAttributes{Verb: "get", Group: "monitoring.coreos.com", Resource: "prometheuses", Subresource: "api", Namespace: "openshift-monitoring", Name: "k8s"}
	if len(asked) != 2 || asked[0] != want || asked[1] != want {
		t.Errorf("metrics reviews = %+v, want one per caller of %+v", asked, want)
	}
	mu.Unlock()

	s.metrics = nil
	rec = do(s, tenantToken, http.MethodGet, "/api/metrics/cluster", "")
	wantStatus(t, rec, http.StatusOK)
	if cs := decodeBody[model.ClusterSummary](t, rec); cs.Unavailable != model.MetricsUnconfigured {
		t.Errorf("metrics off: summary = %+v, want unconfigured", cs)
	}
}

// The summary served from the metrics cache is shared by every caller: the
// stopped-VM fold must not write into it. Writing grew the count on every
// hit, and two concurrent hits wrote one map at once (fatal under load).
func TestClusterSummaryFoldLeavesCacheAlone(t *testing.T) {
	fc := newFakeCluster(t)
	fc.vms = []kubevirtcorev1.VirtualMachine{{ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "team-a-dev"}}}
	querier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "query_range") {
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{},"values":[[100,"2"]]}]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"phase":"running"},"value":[100,"2"]}]}}`)
	}))
	t.Cleanup(querier.Close)
	s := fc.server(nil, Config{})
	var err error
	if s.metrics, err = metrics.New(querier.URL, querier.URL, "", false); err != nil {
		t.Fatal(err)
	}
	check := func() {
		rec := do(s, adminToken, http.MethodGet, "/api/metrics/cluster", "")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d", rec.Code)
			return
		}
		if cs := decodeBody[model.ClusterSummary](t, rec); cs.VMs["stopped"] != 1 || cs.VMs["running"] != 2 {
			t.Errorf("vms = %v, want running 2, stopped 1 on every hit", cs.VMs)
		}
	}
	for range 3 {
		check()
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(check)
	}
	wg.Wait()
}
