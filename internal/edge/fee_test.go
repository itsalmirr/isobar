package edge

import (
	"math"
	"testing"
)

func TestFeePerContractCents(t *testing.T) {
	// Kalshi schedule: ceil(0.07 · price · (1-price) · 100). Spot-checked
	// against a few representative prices.
	tests := []struct {
		price   float64
		wantFee int
	}{
		{0.00, 0}, // P(1-P)=0
		{1.00, 0},
		{0.50, 2}, // ceil(0.07·0.25·100) = ceil(1.75) = 2  — peak fee
		{0.10, 1}, // ceil(0.07·0.09·100) = ceil(0.63) = 1
		{0.90, 1}, // mirror of 0.10
		{0.25, 2}, // ceil(0.07·0.1875·100) = ceil(1.3125) = 2
		{0.75, 2}, // mirror of 0.25
		{0.05, 1}, // ceil(0.7·0.0475) = ceil(0.3325) = 1
		{0.95, 1},
		{0.40, 2}, // ceil(0.07·0.24·100) = ceil(1.68) = 2
	}
	for _, tt := range tests {
		got := FeePerContractCents(tt.price)
		if got != tt.wantFee {
			t.Errorf("FeePerContractCents(%.2f) = %d, want %d", tt.price, got, tt.wantFee)
		}
	}
}

func TestFeePerContractCents_ClampsOutOfRange(t *testing.T) {
	if got := FeePerContractCents(-0.5); got != 0 {
		t.Errorf("negative price clamp = %d, want 0", got)
	}
	if got := FeePerContractCents(1.5); got != 0 {
		t.Errorf("price>1 clamp = %d, want 0 (P=1, P(1-P)=0)", got)
	}
}

func TestEdgeAfterFeeCents(t *testing.T) {
	tests := []struct {
		name     string
		pFair    float64
		price    float64
		wantEdge int
	}{
		{
			name:  "10¢ mispricing at peak fee zone",
			pFair: 0.60, price: 0.50,
			// |60 - 50| = 10¢, fee at price 0.5 = 2¢ → edge = 8¢
			wantEdge: 8,
		},
		{
			name:  "negative edge: 1¢ mispricing eaten by fee",
			pFair: 0.50, price: 0.49,
			// |50-49| = 1¢, fee = ceil(0.07·0.49·0.51·100) = ceil(1.7493) = 2¢ → -1¢
			wantEdge: -1,
		},
		{
			name:  "extreme price low fee, big mispricing",
			pFair: 0.30, price: 0.05,
			// |30-5| = 25¢, fee = 1¢ → 24¢
			wantEdge: 24,
		},
		{
			name:  "no mispricing",
			pFair: 0.42, price: 0.42,
			// 0 - fee(0.42) = ceil(0.07·0.42·0.58·100) = ceil(1.7052) = 2 → -2
			wantEdge: -2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeAfterFeeCents(tt.pFair, tt.price)
			if got != tt.wantEdge {
				t.Errorf("EdgeAfterFeeCents(%.2f, %.2f) = %d, want %d",
					tt.pFair, tt.price, got, tt.wantEdge)
			}
		})
	}
}

// TestFeeMode_BPSAndString locks the constants in.
func TestFeeMode_BPSAndString(t *testing.T) {
	if FeeTaker.BPS() != KalshiTakerFeeBPS {
		t.Errorf("FeeTaker.BPS() = %v, want %v", FeeTaker.BPS(), KalshiTakerFeeBPS)
	}
	if FeeMaker.BPS() != KalshiMakerFeeBPS {
		t.Errorf("FeeMaker.BPS() = %v, want %v", FeeMaker.BPS(), KalshiMakerFeeBPS)
	}
	if FeeTaker.String() != "taker" {
		t.Errorf("FeeTaker.String() = %q, want %q", FeeTaker.String(), "taker")
	}
	if FeeMaker.String() != "maker" {
		t.Errorf("FeeMaker.String() = %q, want %q", FeeMaker.String(), "maker")
	}
	// Kalshi published ratio: maker is exactly 1/4 of taker.
	if got, want := KalshiMakerFeeBPS, KalshiTakerFeeBPS/4; math.Abs(got-want) > 1e-12 {
		t.Errorf("maker/taker ratio = %v / %v = %v, want exactly 1/4",
			KalshiMakerFeeBPS, KalshiTakerFeeBPS, got/KalshiTakerFeeBPS)
	}
}

// TestFeePerContractCents_TakerParity ensures the default no-mode
// callers see exactly the same behaviour as the mode-aware caller
// invoked with FeeTaker — i.e., adding the mode-aware variant did
// not regress any existing taker calculation.
func TestFeePerContractCents_TakerParity(t *testing.T) {
	for p := 0.0; p <= 1.0+1e-9; p += 0.01 {
		legacy := FeePerContractCents(p)
		modal := FeePerContractCentsMode(p, FeeTaker)
		if legacy != modal {
			t.Errorf("at p=%.2f: legacy=%d vs FeeTaker=%d", p, legacy, modal)
		}
	}
}

// TestMakerFeePerContractCents_AtMidrange spot-checks the maker fee
// against the published schedule at common prices.
func TestMakerFeePerContractCents_AtMidrange(t *testing.T) {
	tests := []struct {
		price   float64
		wantFee int
	}{
		{0.50, 1}, // ceil(0.0175·0.25·100) = ceil(0.4375) = 1
		{0.40, 1}, // ceil(0.0175·0.24·100) = ceil(0.42) = 1
		{0.10, 1}, // ceil(0.0175·0.09·100) = ceil(0.1575) = 1  — still 1¢ due to ceil
		{0.05, 1}, // ceil(0.0175·0.0475·100) = ceil(0.08312) = 1 — minimum 1¢
		{0.00, 0},
		{1.00, 0},
		// At a wide enough corner the maker fee is exactly 0 — there's no
		// such P in (0,1) given the ceil()-floor of 1, but the boundary
		// cases above cover it. Keep this spot-check tight.
	}
	for _, tt := range tests {
		got := FeePerContractCentsMode(tt.price, FeeMaker)
		if got != tt.wantFee {
			t.Errorf("maker fee at p=%.2f = %d, want %d", tt.price, got, tt.wantFee)
		}
	}
}

// TestMakerFee_NeverExceedsTaker is a property test: for any price,
// the maker fee must be ≤ taker fee. ceil() rounding can introduce
// equality at small prices but never inversion.
func TestMakerFee_NeverExceedsTaker(t *testing.T) {
	for p := 0.0; p <= 1.0+1e-9; p += 0.001 {
		taker := FeePerContractCentsMode(p, FeeTaker)
		maker := FeePerContractCentsMode(p, FeeMaker)
		if maker > taker {
			t.Errorf("at p=%.3f: maker=%d > taker=%d (impossible)", p, maker, taker)
		}
	}
}

// TestEdgeAfterFeeCents_MakerWidensEdge confirms that switching to
// maker fees does not narrow edge (it can only widen or keep equal,
// since maker fee ≤ taker fee at every price).
func TestEdgeAfterFeeCents_MakerWidensEdge(t *testing.T) {
	tests := []struct{ pFair, price float64 }{
		{0.60, 0.50}, {0.55, 0.49}, {0.30, 0.05}, {0.50, 0.42}, {0.10, 0.07},
	}
	for _, tt := range tests {
		takerEdge := EdgeAfterFeeCentsMode(tt.pFair, tt.price, FeeTaker)
		makerEdge := EdgeAfterFeeCentsMode(tt.pFair, tt.price, FeeMaker)
		if makerEdge < takerEdge {
			t.Errorf("p=%.2f price=%.2f: maker edge %d < taker edge %d", tt.pFair, tt.price, makerEdge, takerEdge)
		}
	}
}

// TestEntryFilterAdmits_None_AndEmpty: both treat the predicate as
// no-op (admit everything). This is the contract used by callers
// that don't specify a filter.
func TestEntryFilterAdmits_None_AndEmpty(t *testing.T) {
	cases := []struct {
		series string
		price  int
	}{
		{"KXHIGHNY", 50}, {"KXHIGHNY", 5}, {"KXLOWTNYC", 50}, {"KXLOWTLAX", 91},
	}
	for _, c := range cases {
		if !entryFilterAdmits("", c.series, c.price) {
			t.Errorf("empty filter must admit %s @ %d", c.series, c.price)
		}
		if !entryFilterAdmits("none", c.series, c.price) {
			t.Errorf("'none' filter must admit %s @ %d", c.series, c.price)
		}
	}
}

// TestEntryFilterAdmits_H4 locks in the v4 filter contract per
// reports/phase0.5/2026-05-19-v4-h4-filter.md §"The H4 filter":
//
//   admit IFF series ≠ KXHIGHNY AND (KXLOWT* OR price ≤ 9 OR price ≥ 91)
func TestEntryFilterAdmits_H4(t *testing.T) {
	tests := []struct {
		name   string
		series string
		price  int
		admit  bool
		why    string
	}{
		// KXHIGHNY is the unconditional exclusion.
		{"NY HIGH never admitted", "KXHIGHNY", 50, false, "excluded by series"},
		{"NY HIGH at deep no", "KXHIGHNY", 3, false, "still excluded — series wins"},
		{"NY HIGH at deep yes", "KXHIGHNY", 97, false, "still excluded"},
		{"NY HIGH at low-temp prefix", "KXHIGHNY", 5, false, "still excluded"},

		// KXLOWT* (low-temp brackets) admitted at any price.
		{"NY LOW midrange admitted", "KXLOWTNYC", 50, true, "low-temp series"},
		{"MIA LOW admitted", "KXLOWTMIA", 60, true, "low-temp series"},
		{"LAX LOW deep yes", "KXLOWTLAX", 95, true, "low-temp series (also extreme)"},
		{"CHI LOW deep no", "KXLOWTCHI", 3, true, "low-temp series (also extreme)"},

		// Non-NY HIGHs admitted only at extreme price.
		{"MIA HIGH midrange rejected", "KXHIGHMIA", 50, false, "midrange, not extreme, not low-temp"},
		{"MIA HIGH deep no admitted", "KXHIGHMIA", 5, true, "extreme OTM"},
		{"MIA HIGH deep yes admitted", "KXHIGHMIA", 95, true, "extreme OTM"},
		{"LAX HIGH boundary 9 admitted", "KXHIGHLAX", 9, true, "boundary: ≤9¢ is extreme"},
		{"LAX HIGH boundary 91 admitted", "KXHIGHLAX", 91, true, "boundary: ≥91¢ is extreme"},
		{"LAX HIGH boundary 10 rejected", "KXHIGHLAX", 10, false, "just above extreme band"},
		{"LAX HIGH boundary 90 rejected", "KXHIGHLAX", 90, false, "just below extreme band"},

		// CHI HIGH midrange rejected.
		{"CHI HIGH midrange rejected", "KXHIGHCHI", 55, false, "midrange, not extreme, not low-temp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := entryFilterAdmits("h4", tt.series, tt.price)
			if got != tt.admit {
				t.Errorf("entryFilterAdmits(h4, %s, %d) = %v, want %v (%s)",
					tt.series, tt.price, got, tt.admit, tt.why)
			}
		})
	}
}

// TestEntryFilterAdmits_UnknownFilter_FallsThrough documents the
// silent-fallthrough behaviour so a future change that flips the
// default to "reject everything" is loudly visible to callers.
//
// The fallthrough is a deliberate safety choice (typos shouldn't
// disable trading), but the scanner is expected to log a one-shot
// warning when this branch is hit — that's tested separately at
// scan_test.go because it requires capturing the slog handler.
func TestEntryFilterAdmits_UnknownFilter_FallsThrough(t *testing.T) {
	if !entryFilterAdmits("h5", "KXHIGHNY", 50) {
		t.Error("unknown filter name must admit (fallthrough), to avoid silently disabling trading on a typo")
	}
	if !entryFilterAdmits("typo", "KXLOWTNYC", 50) {
		t.Error("unknown filter name must admit")
	}
}
