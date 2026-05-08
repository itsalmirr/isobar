package main

import (
	"math"
	"testing"

	"github.com/wh0o0pz/isobar/internal/papertrade"
)

func mk(side papertrade.Side, mid, pnl int, won, scored bool) candidate {
	return candidate{
		Side:     side,
		MidCents: mid,
		PnLCents: pnl,
		Won:      won,
		Scored:   scored,
	}
}

func TestCompute_AllScored(t *testing.T) {
	cs := []candidate{
		mk(papertrade.SideYes, 30, 66, true, true),
		mk(papertrade.SideYes, 30, -34, false, true),
		mk(papertrade.SideNo, 60, 36, true, true),
		mk(papertrade.SideNo, 60, -64, false, true),
	}
	s := compute(cs)
	if s.N != 4 || s.Scored != 4 {
		t.Errorf("N/Scored = %d/%d, want 4/4", s.N, s.Scored)
	}
	if s.Wins != 2 {
		t.Errorf("wins = %d, want 2", s.Wins)
	}
	if s.NetPnLCents != 4 {
		t.Errorf("net = %d, want 4 (66-34+36-64)", s.NetPnLCents)
	}
	if math.Abs(s.MeanPnLCents-1.0) > 1e-9 {
		t.Errorf("mean = %v, want 1.0", s.MeanPnLCents)
	}
	if s.WinRate != 0.5 {
		t.Errorf("win_rate = %v, want 0.5", s.WinRate)
	}
}

func TestCompute_MixedScoredUnscored(t *testing.T) {
	cs := []candidate{
		mk(papertrade.SideYes, 30, 66, true, true),
		mk(papertrade.SideYes, 30, 0, false, false), // not scored
		mk(papertrade.SideYes, 30, -34, false, true),
	}
	s := compute(cs)
	if s.N != 3 {
		t.Errorf("N = %d, want 3", s.N)
	}
	if s.Scored != 2 {
		t.Errorf("Scored = %d, want 2", s.Scored)
	}
	if s.NetPnLCents != 32 {
		t.Errorf("net = %d, want 32 (66-34, unscored excluded)", s.NetPnLCents)
	}
}

func TestCompute_EmptyOrUnscoredOnly(t *testing.T) {
	if s := compute(nil); s.N != 0 || s.Scored != 0 {
		t.Errorf("nil → expected zero stats")
	}
	cs := []candidate{
		mk(papertrade.SideYes, 30, 0, false, false),
		mk(papertrade.SideYes, 30, 0, false, false),
	}
	s := compute(cs)
	if s.N != 2 || s.Scored != 0 || s.MeanPnLCents != 0 {
		t.Errorf("all-unscored → expected N=2, Scored=0, mean=0; got %+v", s)
	}
}

func TestCompute_PerBetSharpe(t *testing.T) {
	// Three wins of +66, three losses of -34 → mean = 16, stdev ≈ 50.
	// sharpe ≈ 0.32. (Sample stdev uses N-1.)
	cs := []candidate{
		mk(papertrade.SideYes, 30, 66, true, true),
		mk(papertrade.SideYes, 30, 66, true, true),
		mk(papertrade.SideYes, 30, 66, true, true),
		mk(papertrade.SideYes, 30, -34, false, true),
		mk(papertrade.SideYes, 30, -34, false, true),
		mk(papertrade.SideYes, 30, -34, false, true),
	}
	s := compute(cs)
	if math.Abs(s.MeanPnLCents-16.0) > 1e-6 {
		t.Errorf("mean = %v, want 16", s.MeanPnLCents)
	}
	if math.Abs(s.WinRate-0.5) > 1e-9 {
		t.Errorf("win_rate = %v, want 0.5", s.WinRate)
	}
	if s.PerBetSharpe <= 0 || s.PerBetSharpe > 1.0 {
		t.Errorf("sharpe = %v, want positive and < 1.0", s.PerBetSharpe)
	}
}
