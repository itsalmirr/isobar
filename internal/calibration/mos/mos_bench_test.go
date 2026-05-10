package mos

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// BenchmarkPredict measures the Coefficients.Predict lookup cost. Called
// once per row in edge-scan (~400-1500 rows per run today, scaling with
// n verifiable brackets).
func BenchmarkPredict(b *testing.B) {
	c := Coefficients{
		Bins: map[string]BinCoeffs{
			binKey("KNYC", "MAM", "tmax"): {A: 2.0, B: 0.95, Sigma: 1.4},
			binKey("KNYC", "MAM", "tmin"): {A: 1.5, B: 0.97, Sigma: 1.7},
			binKey("KMIA", "MAM", "tmax"): {A: 10.0, B: 0.90, Sigma: 1.4},
			binKey("KMDW", "MAM", "tmax"): {A: 2.9, B: 0.98, Sigma: 1.3},
			binKey("KLAX", "MAM", "tmax"): {A: 2.6, B: 0.97, Sigma: 0.9},
		},
	}
	d, _ := time.Parse("2006-01-02", "2026-04-22")
	b.ResetTimer()
	for range b.N {
		_, _, _ = c.Predict("KNYC", d, "tmax", 75.0)
	}
}

// BenchmarkProbBelow measures the bracket-probability call. Called twice
// per (row, bracket) — once for ProbBelow, once for ProbAbove. Uses
// math.Erf, which dominates the cost.
func BenchmarkProbBelow(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_ = ProbBelow(75.0, 73.5, 1.4)
	}
}

// BenchmarkFit measures the cold-start fit cost on 1000 pairs across 4
// stations × 2 vars (8 bins). Real-world: 32 bins × ~125 train pairs
// each = ~4000 pairs, fitted once per refresh (offline).
func BenchmarkFit(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 1))
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	pairs := make([]Pair, 1000)
	stations := []string{"KNYC", "KMIA", "KLAX", "KMDW"}
	vars := []string{"tmax", "tmin"}
	for i := range pairs {
		pairs[i] = Pair{
			Station:   stations[i%4],
			ValidDate: startDate.AddDate(0, 0, i%180),
			Variable:  vars[i%2],
			Forecast:  60 + rng.Float64()*30,
			Observed:  60 + rng.Float64()*30 + rng.NormFloat64()*1.5,
		}
	}
	// Mute math import warning if Erf-based code path is unused above.
	_ = math.Erf
	b.ResetTimer()
	for range b.N {
		_, err := Fit(pairs, 0)
		if err != nil {
			b.Fatal(err)
		}
	}
}
