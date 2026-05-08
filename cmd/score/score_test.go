package main

import (
	"math"
	"testing"

	"github.com/wh0o0pz/isobar/internal/papertrade"
)

// mkScored builds a synthetic scoredBet for table-driven tests of the
// reduce/regression helpers. predictedEdge and pnl are the only fields
// that matter for fitEdge / computeStats; other fields default-zero.
func mkScored(predictedEdge float64, pnl int, won bool) scoredBet {
	return scoredBet{
		Bet: papertrade.Bet{EdgeCents: predictedEdge, ModelP: 0.5},
		Outcome: papertrade.Outcome{
			PnLCents: pnl,
			Won:      won,
		},
		Scored: true,
	}
}

func TestComputeStats_AllScored(t *testing.T) {
	rows := []scoredBet{
		mkScored(5, 10, true),
		mkScored(5, -10, false),
		mkScored(5, 20, true),
		mkScored(5, -10, false),
	}
	s := computeStats(rows)
	if s.N != 4 || s.Scored != 4 {
		t.Errorf("N/Scored = %d/%d, want 4/4", s.N, s.Scored)
	}
	if s.Wins != 2 {
		t.Errorf("Wins = %d, want 2", s.Wins)
	}
	if s.NetPnLCents != 10 {
		t.Errorf("NetPnL = %d, want 10", s.NetPnLCents)
	}
	if s.MeanPnLCents != 2.5 {
		t.Errorf("Mean = %v, want 2.5", s.MeanPnLCents)
	}
	if s.WinRate != 0.5 {
		t.Errorf("WinRate = %v, want 0.5", s.WinRate)
	}
}

func TestComputeStats_MixedScoredUnscored(t *testing.T) {
	rows := []scoredBet{
		mkScored(5, 10, true),
		{Bet: papertrade.Bet{EdgeCents: 5}}, // unscored
		mkScored(5, -10, false),
	}
	s := computeStats(rows)
	if s.N != 3 {
		t.Errorf("N = %d, want 3", s.N)
	}
	if s.Scored != 2 {
		t.Errorf("Scored = %d, want 2", s.Scored)
	}
	if s.NetPnLCents != 0 {
		t.Errorf("NetPnL = %d, want 0 (10 + -10)", s.NetPnLCents)
	}
}

func TestComputeStats_EmptySlice(t *testing.T) {
	s := computeStats(nil)
	if s.N != 0 || s.Scored != 0 {
		t.Errorf("expected zero stats on empty input")
	}
}

func TestFitEdge_PerfectCalibration(t *testing.T) {
	// Predicted edge = realized PnL exactly → slope=1, intercept=0, R²=1.
	rows := []scoredBet{
		mkScored(-5, -5, false),
		mkScored(0, 0, false),
		mkScored(5, 5, true),
		mkScored(10, 10, true),
		mkScored(20, 20, true),
	}
	f := fitEdge(rows)
	if f.N != 5 {
		t.Fatalf("N = %d, want 5", f.N)
	}
	if math.Abs(f.Slope-1.0) > 1e-9 {
		t.Errorf("slope = %v, want 1.0", f.Slope)
	}
	if math.Abs(f.Intercept) > 1e-9 {
		t.Errorf("intercept = %v, want 0", f.Intercept)
	}
	if math.Abs(f.R2-1.0) > 1e-9 {
		t.Errorf("R² = %v, want 1.0", f.R2)
	}
}

func TestFitEdge_AntiCorrelated(t *testing.T) {
	// Slope -1 → model predicts opposite of what happens.
	rows := []scoredBet{
		mkScored(-5, 5, true),
		mkScored(0, 0, false),
		mkScored(5, -5, false),
		mkScored(10, -10, false),
	}
	f := fitEdge(rows)
	if f.Slope >= 0 {
		t.Errorf("slope = %v, want < 0 for anti-correlated data", f.Slope)
	}
}

func TestCalibration_BinsRealizedRate(t *testing.T) {
	mk := func(p float64, won bool) scoredBet {
		return scoredBet{
			Bet:     papertrade.Bet{ModelP: p},
			Outcome: papertrade.Outcome{Won: won},
			Scored:  true,
		}
	}
	rows := []scoredBet{
		// Bin [0.0, 0.1): 2 bets, 0 wins → 0.0 realized
		mk(0.05, false), mk(0.09, false),
		// Bin [0.5, 0.6): 4 bets, 3 wins → 0.75 realized
		mk(0.50, true), mk(0.55, true), mk(0.59, true), mk(0.51, false),
		// Bin [0.9, 1.0]: 1 bet, 1 win → 1.0 realized (idx clamped)
		mk(1.0, true),
	}
	bins := calibration(rows, 10)
	if got := bins[0].WinRate; got != 0.0 {
		t.Errorf("bin 0 win rate = %v, want 0", got)
	}
	if got := bins[5].WinRate; math.Abs(got-0.75) > 1e-9 {
		t.Errorf("bin 5 win rate = %v, want 0.75", got)
	}
	if got := bins[9].WinRate; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("bin 9 win rate = %v, want 1.0", got)
	}
	// Empty bins must NaN out.
	if !math.IsNaN(bins[1].WinRate) {
		t.Errorf("empty bin 1 should be NaN; got %v", bins[1].WinRate)
	}
}

func TestLeadBucket(t *testing.T) {
	cases := []struct {
		hours float64
		want  string
	}{
		{0, "<6h"}, {5.9, "<6h"},
		{6, "6–12h"}, {11.9, "6–12h"},
		{12, "12–24h"}, {23.9, "12–24h"},
		{24, ">24h"}, {72, ">24h"},
	}
	for _, c := range cases {
		got := leadBucket(papertrade.Bet{LeadHours: c.hours})
		if got != c.want {
			t.Errorf("leadBucket(%v) = %q, want %q", c.hours, got, c.want)
		}
	}
}

func TestMedianAndStdev(t *testing.T) {
	if got := medianInt([]int{1, 2, 3}); got != 2 {
		t.Errorf("median odd = %v, want 2", got)
	}
	if got := medianInt([]int{1, 2, 3, 4}); got != 2.5 {
		t.Errorf("median even = %v, want 2.5", got)
	}
	if got := stdevInt([]int{2, 4, 4, 4, 5, 5, 7, 9}, 5.0); math.Abs(got-2.138) > 0.01 {
		t.Errorf("stdev = %v, want ≈ 2.138 (sample stdev)", got)
	}
}
