package edge

import (
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/mos"
)

// The four helpers below (applyMOS/applyBayes/applyClimate/scoreBacktestRow)
// are the pure per-trade steps of Scan's frame closure. The end-to-end
// Scan tests exercise them wired together; these table-driven unit tests
// pin each branch of each helper in isolation so a future edit can't
// silently flip a skip path or the p_fair last-writer-wins order.

var mamDate = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

func TestApplyMOS(t *testing.T) {
	// A fitted bin with A=0,B=1 leaves μ = ensemble mean; Sigma=5 gives a
	// finite bracket probability so the applied path returns a non-zero,
	// clamped p_fair and hands off (μ,σ) to the Bayesian step.
	fitted := &mos.Coefficients{
		FitModelSet: []string{mos.ModelGFS},
		Bins:        map[string]mos.BinCoeffs{"KNYC|MAM|tmax": {A: 0, B: 1, Sigma: 5}},
	}
	gfsOnly := mos.CanonicalModelSet([]string{mos.ModelGFS})
	pollGFS := &Poll{ModelSet: gfsOnly}
	pollMulti := &Poll{ModelSet: mos.CanonicalModelSet([]string{mos.ModelGFS, mos.ModelECMWF})}
	res := Resolution{Lo: 68, Hi: 72}

	base := mosInputs{
		poll:        pollGFS,
		station:     "KNYC",
		validDate:   mamDate,
		variable:    "tmax",
		resolution:  res,
		sumT:        700, // mean = 700/10 = 70 → μ centred in [68,72)
		nMembers:    10,
		fitModelSet: fitModelSetOf(fitted),
	}

	tests := []struct {
		name            string
		mutate          func(*mosInputs)
		wantApplied     bool
		wantSkipReason  string
		wantCountReason string
		wantPFairPos    bool // pFairMOS strictly > 0
		wantPriorSet    bool // mu/sigma non-zero
	}{
		{
			name:            "no coeffs",
			mutate:          func(in *mosInputs) { in.coeffs = nil },
			wantSkipReason:  "no_coeffs",
			wantCountReason: "",
		},
		{
			name:            "empty fit model set",
			mutate:          func(in *mosInputs) { in.coeffs = fitted; in.fitModelSet = nil },
			wantSkipReason:  "model_set_mismatch:gfsvs",
			wantCountReason: "model_set_mismatch:gfsvs",
		},
		{
			name:            "model set mismatch",
			mutate:          func(in *mosInputs) { in.coeffs = fitted; in.poll = pollMulti },
			wantSkipReason:  "model_set_mismatch:ecmwf+gfsvsgfs",
			wantCountReason: "model_set_mismatch:ecmwf+gfsvsgfs",
		},
		{
			name:           "no bin",
			mutate:         func(in *mosInputs) { in.coeffs = fitted; in.station = "KLAX" },
			wantSkipReason: "no_bin",
		},
		{
			name:         "applied",
			mutate:       func(in *mosInputs) { in.coeffs = fitted },
			wantApplied:  true,
			wantPFairPos: true,
			wantPriorSet: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.logged = map[string]struct{}{}
			tc.mutate(&in)
			got := applyMOS(in)
			if got.applied != tc.wantApplied {
				t.Errorf("applied = %v, want %v", got.applied, tc.wantApplied)
			}
			if got.skipReason != tc.wantSkipReason {
				t.Errorf("skipReason = %q, want %q", got.skipReason, tc.wantSkipReason)
			}
			if got.countReason != tc.wantCountReason {
				t.Errorf("countReason = %q, want %q", got.countReason, tc.wantCountReason)
			}
			if tc.wantPFairPos && !(got.pFairMOS > 0) {
				t.Errorf("pFairMOS = %v, want > 0", got.pFairMOS)
			}
			if tc.wantPriorSet && (got.mu == 0 && got.sigma == 0) {
				t.Errorf("mu/sigma both 0, want the fitted prior handed off")
			}
			if !tc.wantApplied && got.pFairMOS != 0 {
				t.Errorf("pFairMOS = %v, want 0 on a skip path", got.pFairMOS)
			}
		})
	}
}

// fitModelSetOf mirrors how Scan canonicalises MOS.FitModelSet before the
// per-trade loop.
func fitModelSetOf(c *mos.Coefficients) []string {
	if c == nil {
		return nil
	}
	return mos.CanonicalModelSet(c.FitModelSet)
}

func TestApplyBayes(t *testing.T) {
	res := Resolution{Lo: 68, Hi: 72}
	tests := []struct {
		name           string
		in             bayesInputs
		wantApplied    bool
		wantSkipReason string
	}{
		{
			name:           "no kernel or obs is a silent no-op",
			in:             bayesInputs{kernel: nil, obs: nil, mosApplied: true, resolution: res},
			wantApplied:    false,
			wantSkipReason: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyBayes(tc.in)
			if got.applied != tc.wantApplied {
				t.Errorf("applied = %v, want %v", got.applied, tc.wantApplied)
			}
			if got.skipReason != tc.wantSkipReason {
				t.Errorf("skipReason = %q, want %q", got.skipReason, tc.wantSkipReason)
			}
			if !got.applied && got.pFairBayes != 0 {
				t.Errorf("pFairBayes = %v, want 0 when not applied", got.pFairBayes)
			}
		})
	}
}

func TestApplyClimate(t *testing.T) {
	res := Resolution{Lo: 68, Hi: 72}
	tests := []struct {
		name        string
		in          climateInputs
		wantApplied bool
		wantNaN     bool // climateP/pFairClimate must be NaN
	}{
		{
			name:        "nil lookup yields NaN, not applied",
			in:          climateInputs{lookup: nil, resolution: res, pFairNaive: 0.5, price: 0.5},
			wantApplied: false,
			wantNaN:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyClimate(tc.in)
			if got.applied != tc.wantApplied {
				t.Errorf("applied = %v, want %v", got.applied, tc.wantApplied)
			}
			if tc.wantNaN {
				if !isNaN(got.climateP) || !isNaN(got.pFairClimate) {
					t.Errorf("climateP=%v pFairClimate=%v, want both NaN", got.climateP, got.pFairClimate)
				}
			}
			if !got.applied && got.edgeClimateCents != 0 {
				t.Errorf("edgeClimateCents = %d, want 0 when not applied", got.edgeClimateCents)
			}
		})
	}
}

func isNaN(f float64) bool { return f != f }

func TestScoreBacktestRow(t *testing.T) {
	// TMAX=71 lands inside [68,72) → bracket resolves YES. p_fair is high,
	// so with a cheap YES price the YES edge clears the threshold and the
	// realized PnL is the winning payout.
	truth := &TruthIndex{byKey: map[string]DailyTruth{
		truthKey("KNYC", mamDate): {TMaxF: 71, TMinF: 40},
	}}
	res := Resolution{Lo: 68, Hi: 72}

	base := backtestInputs{
		truth:         truth,
		station:       "KNYC",
		validDate:     mamDate,
		series:        "KXHIGHNYC",
		isHigh:        true,
		resolution:    res,
		feeMode:       FeeTaker,
		edgeThreshold: 5,
	}

	tests := []struct {
		name        string
		mutate      func(*backtestInputs)
		wantOK      bool
		wantSide    string
		wantYesRes  bool
		wantPnLSign int // -1 neg, 0 zero, +1 pos
	}{
		{
			name:        "no truth for station",
			mutate:      func(in *backtestInputs) { in.station = "KLAX" },
			wantOK:      false,
			wantSide:    "",
			wantPnLSign: 0,
		},
		{
			name:        "yes edge wins",
			mutate:      func(in *backtestInputs) { in.pFair = 0.95; in.price = 0.20; in.tradePrice = 20 },
			wantOK:      true,
			wantSide:    "yes",
			wantYesRes:  true,
			wantPnLSign: 1,
		},
		{
			name:        "no edge wins (bracket resolves YES → NO loses)",
			mutate:      func(in *backtestInputs) { in.pFair = 0.05; in.price = 0.80; in.tradePrice = 80 },
			wantOK:      true,
			wantSide:    "no",
			wantYesRes:  true,
			wantPnLSign: -1,
		},
		{
			name:        "no clearing edge → no trade",
			mutate:      func(in *backtestInputs) { in.pFair = 0.50; in.price = 0.50; in.tradePrice = 50 },
			wantOK:      true,
			wantSide:    "",
			wantYesRes:  true,
			wantPnLSign: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			got, ok := scoreBacktestRow(in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.ourSide != tc.wantSide {
				t.Errorf("ourSide = %q, want %q", got.ourSide, tc.wantSide)
			}
			if got.yesResolved != tc.wantYesRes {
				t.Errorf("yesResolved = %v, want %v", got.yesResolved, tc.wantYesRes)
			}
			sign := 0
			switch {
			case got.ourPnLCents > 0:
				sign = 1
			case got.ourPnLCents < 0:
				sign = -1
			}
			if sign != tc.wantPnLSign {
				t.Errorf("ourPnLCents = %d (sign %d), want sign %d", got.ourPnLCents, sign, tc.wantPnLSign)
			}
		})
	}
}
