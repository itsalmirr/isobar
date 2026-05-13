package edge

import (
	"math"
	"sync"
	"sync/atomic"
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

// TestResolveBracketConvention_LockedIn captures the bracket convention
// in a single regression test, closing the previous FIXME. Each case
// here came directly from observed Kalshi weather-bracket markets;
// changing the convention (e.g. switching B<K.5> to a 1°F instead of
// 2°F bracket) must trip at least one of these assertions.
//
// The "tiling invariant" sub-test verifies that the four B-brackets of
// a representative market cover the integer-temperature range without
// gap or overlap, which is the empirical anchor for the convention:
// summed market prices across all brackets must be ~100¢ if and only if
// the union of bracket Resolutions tiles the integer line under their
// span. See resolution.go header comment for the full evidence chain.
func TestResolveBracketConvention_LockedIn(t *testing.T) {
	// Per-bracket invariants — derived from the resolution.go header,
	// any drift here means the convention shifted.
	cases := []struct {
		name string
		spec Spec
		lo   float64
		hi   float64
	}{
		{
			name: "B-bracket with half-integer key spans 2°F",
			spec: Spec{Kind: BracketBetween, Key: 58.5},
			lo:   57.5, hi: 59.5,
		},
		{
			name: "B-bracket with integer key spans 1°F",
			spec: Spec{Kind: BracketBetween, Key: 48},
			lo:   48, hi: 49,
		},
		{
			name: "T-bracket defaults to high-side [K, +Inf)",
			spec: Spec{Kind: BracketThreshold, Key: 61},
			lo:   61, hi: math.Inf(+1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec.Resolve()
			if got.Lo != tc.lo {
				t.Errorf("Lo = %v, want %v", got.Lo, tc.lo)
			}
			if got.Hi != tc.hi {
				t.Errorf("Hi = %v, want %v", got.Hi, tc.hi)
			}
		})
	}

	// Tiling invariant: the four B-brackets {B56.5, B58.5, B60.5, B62.5}
	// cover integer highs 56..63 with no gap and no overlap. If the
	// B<K.5> convention ever changes from 2°F to anything else, an
	// integer in this range will either hit no bracket (gap) or two
	// brackets (overlap), failing here. The integers 55 and 64 are
	// intentionally checked as "covered by zero B-brackets" — they
	// must fall to the flanking T-brackets in a real market.
	t.Run("four half-integer B-brackets tile their span", func(t *testing.T) {
		resolutions := []Resolution{
			Spec{Kind: BracketBetween, Key: 56.5}.Resolve(),
			Spec{Kind: BracketBetween, Key: 58.5}.Resolve(),
			Spec{Kind: BracketBetween, Key: 60.5}.Resolve(),
			Spec{Kind: BracketBetween, Key: 62.5}.Resolve(),
		}
		countHits := func(tempF int) int {
			hits := 0
			for _, r := range resolutions {
				if r.Contains(float64(tempF)) {
					hits++
				}
			}
			return hits
		}
		// In-span integers must hit exactly one bracket.
		for tempF := 56; tempF <= 63; tempF++ {
			if h := countHits(tempF); h != 1 {
				t.Errorf("integer temp %d°F covered by %d brackets, want 1", tempF, h)
			}
		}
		// Flanking integers must hit zero B-brackets (covered by T's).
		for _, tempF := range []int{54, 55, 64, 65} {
			if h := countHits(tempF); h != 0 {
				t.Errorf("flanking temp %d°F covered by %d B-brackets, want 0", tempF, h)
			}
		}
	})
}

// resetResolveUnknownState restores the package-level counter, the
// once gate, and the logger to a clean state. Tests that exercise the
// unreachable BracketUnknown branch must call this in t.Cleanup so
// they don't bleed state into each other (the package-global counter
// and once-pointer are shared across the test binary).
func resetResolveUnknownState(t *testing.T, replacement func(format string, args ...any)) {
	t.Helper()
	prevCounter := resolveUnknownBranchTotal.Load()
	prevOnce := resolveUnknownWarnOnce
	prevLogger := resolveUnknownLogger

	resolveUnknownBranchTotal.Store(0)
	resolveUnknownWarnOnce = &sync.Once{}
	resolveUnknownLogger = replacement

	t.Cleanup(func() {
		resolveUnknownBranchTotal.Store(prevCounter)
		resolveUnknownWarnOnce = prevOnce
		resolveUnknownLogger = prevLogger
	})
}

// TestResolve_UnknownKind_IncrementsCounter verifies that the defensive
// default branch in Spec.Resolve() — unreachable from any production
// caller, since ParseTicker never returns a BracketUnknown-kinded Spec
// on a non-error path — increments the package counter when hit, and
// returns NaN bounds whose Contains() is false for any input.
func TestResolve_UnknownKind_IncrementsCounter(t *testing.T) {
	resetResolveUnknownState(t, func(_ string, _ ...any) {})

	if got := ResolveUnknownBranchTotal(); got != 0 {
		t.Fatalf("baseline counter = %d, want 0 after reset", got)
	}

	got := Spec{Kind: BracketUnknown, Ticker: "TEST-UNKNOWN"}.Resolve()

	if !math.IsNaN(got.Lo) || !math.IsNaN(got.Hi) {
		t.Errorf("Lo/Hi = (%v, %v), want both NaN", got.Lo, got.Hi)
	}
	// Contains() must be vacuously false for any finite input.
	for _, tempF := range []float64{0, 30, 60, 90, 120} {
		if got.Contains(tempF) {
			t.Errorf("Contains(%v) on NaN-bounded Resolution = true, want false", tempF)
		}
	}
	if c := ResolveUnknownBranchTotal(); c != 1 {
		t.Errorf("counter after 1 call = %d, want 1", c)
	}

	// Second invocation increments again — the counter is unbounded,
	// not gated by Once (only the log is gated).
	_ = Spec{Kind: BracketUnknown, Ticker: "TEST-UNKNOWN-2"}.Resolve()
	if c := ResolveUnknownBranchTotal(); c != 2 {
		t.Errorf("counter after 2 calls = %d, want 2", c)
	}
}

// TestResolve_UnknownKind_WarnOncePerProcess verifies that the warn log
// fires exactly once per process even when the unknown-kind branch is
// hit many times — so a future regression doesn't flood logs.
func TestResolve_UnknownKind_WarnOncePerProcess(t *testing.T) {
	var logCount atomic.Uint64
	resetResolveUnknownState(t, func(_ string, _ ...any) {
		logCount.Add(1)
	})

	for i := 0; i < 5; i++ {
		_ = Spec{Kind: BracketUnknown}.Resolve()
	}

	if got := logCount.Load(); got != 1 {
		t.Errorf("warn log fired %d times, want exactly 1", got)
	}
	if got := ResolveUnknownBranchTotal(); got != 5 {
		t.Errorf("counter = %d, want 5 (every hit counts, only log is gated)", got)
	}
}

// TestResolveUnknownBranchTotal_Accessor verifies the public accessor
// reads the same atomic underlying the increment.
func TestResolveUnknownBranchTotal_Accessor(t *testing.T) {
	resetResolveUnknownState(t, func(_ string, _ ...any) {})
	if got := ResolveUnknownBranchTotal(); got != 0 {
		t.Fatalf("baseline = %d, want 0", got)
	}
	_ = Spec{Kind: BracketUnknown}.Resolve()
	if got := ResolveUnknownBranchTotal(); got != 1 {
		t.Errorf("after 1 hit = %d, want 1", got)
	}
}
