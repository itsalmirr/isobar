package edge

import (
	"math"
	"testing"
)

func TestResolveBetween(t *testing.T) {
	tests := []struct {
		name     string
		key      float64
		wantLo   float64
		wantHi   float64
		yesTemps []float64
		noTemps  []float64
	}{
		{
			name: "B56.5 covers integer high 56 and 57",
			key:  56.5, wantLo: 55.5, wantHi: 57.5,
			yesTemps: []float64{56, 57, 56.0001, 57.4999},
			noTemps:  []float64{55, 58, 55.4999, 57.5},
		},
		{
			name: "B62.5 covers 62 and 63",
			key:  62.5, wantLo: 61.5, wantHi: 63.5,
			yesTemps: []float64{62, 63}, noTemps: []float64{61, 64},
		},
		{
			name: "B48 (integer key) covers exactly 48",
			key:  48, wantLo: 48, wantHi: 49,
			yesTemps: []float64{48, 48.5, 48.999}, noTemps: []float64{47, 49},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Spec{Kind: BracketBetween, Key: tt.key}.Resolve()
			if got.Lo != tt.wantLo || got.Hi != tt.wantHi {
				t.Fatalf("Resolve() = [%v,%v), want [%v,%v)",
					got.Lo, got.Hi, tt.wantLo, tt.wantHi)
			}
			for _, T := range tt.yesTemps {
				if !got.Contains(T) {
					t.Errorf("expected YES at T=%v in %+v", T, got)
				}
			}
			for _, T := range tt.noTemps {
				if got.Contains(T) {
					t.Errorf("expected NO at T=%v in %+v", T, got)
				}
			}
		})
	}
}

func TestResolveThreshold_DefaultHighSide(t *testing.T) {
	got := Spec{Kind: BracketThreshold, Key: 61}.Resolve()
	if got.Lo != 61 || !math.IsInf(got.Hi, +1) {
		t.Fatalf("default T61 = %+v, want [61, +Inf)", got)
	}
	if !got.Contains(61) || !got.Contains(95) || got.Contains(60) {
		t.Errorf("T61 containment wrong: %+v", got)
	}
}

func TestResolveWithSiblings_LowSideDetected(t *testing.T) {
	siblings := []Spec{
		{Ticker: "KXHIGHNY-26APR29-B58.5", Kind: BracketBetween, Key: 58.5},
		{Ticker: "KXHIGHNY-26APR29-B60.5", Kind: BracketBetween, Key: 60.5},
		{Ticker: "KXHIGHNY-26APR29-B62.5", Kind: BracketBetween, Key: 62.5},
	}
	t56 := Spec{Ticker: "KXHIGHNY-26APR29-T56", Kind: BracketThreshold, Key: 56}
	got := ResolveWithSiblings(t56, siblings)
	if !math.IsInf(got.Lo, -1) || got.Hi != 56 {
		t.Fatalf("T56 with low B58.5 siblings → %+v, want (-Inf, 56)", got)
	}
	if !got.Contains(50) || got.Contains(56) || got.Contains(60) {
		t.Errorf("low-side T56 containment wrong: %+v", got)
	}
}

func TestResolveWithSiblings_HighSideDetected(t *testing.T) {
	siblings := []Spec{
		{Ticker: "KXHIGHCHI-26APR29-B56.5", Kind: BracketBetween, Key: 56.5},
		{Ticker: "KXHIGHCHI-26APR29-B58.5", Kind: BracketBetween, Key: 58.5},
		{Ticker: "KXHIGHCHI-26APR29-B60.5", Kind: BracketBetween, Key: 60.5},
	}
	t61 := Spec{Ticker: "KXHIGHCHI-26APR29-T61", Kind: BracketThreshold, Key: 61}
	got := ResolveWithSiblings(t61, siblings)
	if got.Lo != 61 || !math.IsInf(got.Hi, +1) {
		t.Fatalf("T61 with high B60.5 siblings → %+v, want [61, +Inf)", got)
	}
}

func TestResolveWithSiblings_NoBSiblings_FallsBackToHighDefault(t *testing.T) {
	got := ResolveWithSiblings(
		Spec{Kind: BracketThreshold, Key: 100},
		[]Spec{{Kind: BracketThreshold, Key: 50}},
	)
	if got.Lo != 100 || !math.IsInf(got.Hi, +1) {
		t.Fatalf("fallback = %+v, want [100, +Inf)", got)
	}
}

func TestIsHalfInteger(t *testing.T) {
	cases := map[float64]bool{
		56.5: true, 0.5: true, -3.5: true,
		56: false, 56.4: false, 56.6: false, 0: false,
	}
	for k, want := range cases {
		if got := isHalfInteger(k); got != want {
			t.Errorf("isHalfInteger(%v) = %v, want %v", k, got, want)
		}
	}
}
