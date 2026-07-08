package climate

import (
	"testing"
	"time"
)

// BenchmarkBracketProb measures the climate-frequency lookup. Called
// once per row in edge-scan with a real Lookup that has ~730 obs per
// (station, variable) — so ~30 obs in the seasonal window after filter.
// The linear scan dominates; if this becomes a bottleneck, switch to a
// pre-bucketed index by month-day.
func BenchmarkBracketProb(b *testing.B) {
	l := New()
	// Synthetic: 730 days of historical TMAX between 60-90 for KNYC.
	for i := 0; i < 730; i++ {
		d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		md := d.Format("01-02")
		// Deterministic-but-varied to exercise the comparison.
		v := 60.0 + float64(i%30)
		if l.obs["KNYC"] == nil {
			l.obs["KNYC"] = map[string][]obsPoint{}
		}
		l.obs["KNYC"]["tmax"] = append(l.obs["KNYC"]["tmax"], obsPoint{md, v})
	}
	target, _ := time.Parse("2006-01-02", "2026-04-22")
	b.ResetTimer()
	for range b.N {
		_ = l.BracketProb("KNYC", "tmax", target, 70, true)
	}
}

// BenchmarkShrinkP measures the trivial shrinkage call.
func BenchmarkShrinkP(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_ = ShrinkP(0.7, 0.5, 0.2)
	}
}
