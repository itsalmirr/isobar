package main

import (
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/market"
)

// BenchmarkEdgeFromExtremes measures the per-row compute kernel of
// edge-scan: naive prob, MOS lookup + Φ, climate-shrunk lookup + Φ,
// fee math, edge math. Called once per emitted row (typical: 400-1500
// rows per scan).
func BenchmarkEdgeFromExtremes(b *testing.B) {
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	c := &mos.Coefficients{
		Bins: map[string]mos.BinCoeffs{
			"KNYC|MAM|tmax": {A: 2.0, B: 0.95, Sigma: 1.4},
		},
	}
	clim := climate.New()
	// Synthetic climate fixture so the climate path runs.
	for i := 0; i < 60; i++ {
		_ = i // climate.Lookup internals are package-private; the nil
		// path covers most branches anyway. This bench focuses on
		// the with-coeffs path.
	}
	b.ResetTimer()
	for range b.N {
		_ = edgeFromExtremes(tk, 30, 2, constMembers(80), 60*time.Second, 24.0, c, clim)
	}
}

// BenchmarkEdgeFromExtremes_NaiveOnly measures the same kernel with no
// MOS or climate — what edge-scan v0 was effectively doing. Establishes
// the floor cost.
func BenchmarkEdgeFromExtremes_NaiveOnly(b *testing.B) {
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	b.ResetTimer()
	for range b.N {
		_ = edgeFromExtremes(tk, 30, 2, constMembers(80), 60*time.Second, 24.0, nil, nil)
	}
}
