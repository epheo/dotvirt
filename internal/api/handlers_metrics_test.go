package api

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// The band is the descheduler's real trigger window: exact outside-band counts
// (the coarse histogram can't provide them), a floor at 0, AsymmetricLow's
// below-mean-is-a-target semantics, and no band at all for an unknown
// (hand-edited) threshold.
func TestFoldDRSBand(t *testing.T) {
	load := model.HostLoad{Mean: 30}
	pcts := []float64{76, 30, 25, 7}
	foldDRSBand(&load, pcts, "Low")
	if load.Band == nil || load.Band.Low != 20 || load.Band.High != 40 {
		t.Fatalf("band = %+v, want [20,40] around mean 30", load.Band)
	}
	if load.Band.Above != 1 || load.Band.Below != 1 {
		t.Errorf("above/below = %d/%d, want 1/1 (30 and 25 are in-band)", load.Band.Above, load.Band.Below)
	}

	asym := model.HostLoad{Mean: 5}
	foldDRSBand(&asym, []float64{2, 4, 20}, "AsymmetricLow")
	if asym.Band == nil || asym.Band.Low != 5 || asym.Band.High != 15 {
		t.Fatalf("asymmetric band = %+v, want [5,15]: below-mean already counts as a target", asym.Band)
	}
	if asym.Band.Above != 1 || asym.Band.Below != 2 {
		t.Errorf("asymmetric above/below = %d/%d, want 1/2", asym.Band.Above, asym.Band.Below)
	}

	unknown := model.HostLoad{Mean: 30}
	foldDRSBand(&unknown, pcts, "hand-edited")
	if unknown.Band != nil {
		t.Errorf("unknown threshold must not fabricate a band, got %+v", unknown.Band)
	}
}
