package api

import (
	"testing"

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
