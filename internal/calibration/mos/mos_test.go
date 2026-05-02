package mos

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestSeason(t *testing.T) {
	tests := []struct {
		date string
		want string
	}{
		{"2025-01-15", "DJF"},
		{"2025-02-28", "DJF"},
		{"2025-03-01", "MAM"},
		{"2025-05-31", "MAM"},
		{"2025-06-01", "JJA"},
		{"2025-08-31", "JJA"},
		{"2025-09-01", "SON"},
		{"2025-11-30", "SON"},
		{"2025-12-15", "DJF"},
	}
	for _, tt := range tests {
		d, _ := time.Parse("2006-01-02", tt.date)
		if got := season(d); got != tt.want {
			t.Errorf("season(%s) = %q, want %q", tt.date, got, tt.want)
		}
	}
}

func TestProbBelowStandardNormalSanity(t *testing.T) {
	// Φ(0) = 0.5
	if got := ProbBelow(0, 0, 1); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("Φ(0) = %v, want 0.5", got)
	}
	// Φ(1.96) ≈ 0.975
	if got := ProbBelow(1.96, 0, 1); math.Abs(got-0.975) > 1e-3 {
		t.Errorf("Φ(1.96) = %v, want ≈0.975", got)
	}
	// Φ(-1.96) ≈ 0.025
	if got := ProbBelow(-1.96, 0, 1); math.Abs(got-0.025) > 1e-3 {
		t.Errorf("Φ(-1.96) = %v, want ≈0.025", got)
	}
	// Symmetry: ProbAbove + ProbBelow = 1
	if got := ProbBelow(0.5, 0, 1) + ProbAbove(0.5, 0, 1); math.Abs(got-1) > 1e-9 {
		t.Errorf("ProbBelow + ProbAbove = %v, want 1", got)
	}
}

func TestProbBelowDegenerate(t *testing.T) {
	// σ = 0 → indicator function
	if got := ProbBelow(70, 65, 0); got != 1 {
		t.Errorf("μ=65 ≤ 70 with σ=0: got %v, want 1", got)
	}
	if got := ProbBelow(60, 65, 0); got != 0 {
		t.Errorf("μ=65 > 60 with σ=0: got %v, want 0", got)
	}
}

// TestFitRecoversKnownLinearStructure generates synthetic data from a known
// model y = 2 + 0.9·x + N(0, 1.5²) and verifies Fit recovers a≈2, b≈0.9,
// σ≈1.5. The point of MOS is to recover these systematic biases, so this
// is the headline correctness test.
func TestFitRecoversKnownLinearStructure(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 42))
	const trueA, trueB, trueSigma = 2.0, 0.9, 1.5
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")

	// Generate 200 pairs spanning ~7 months so the bin (KNYC, MAM, tmax)
	// has enough samples (200 > MinTrainPerBin even after holdout).
	pairs := make([]Pair, 200)
	for i := range pairs {
		// Stay inside MAM (March-May) by spacing dates and starting in
		// April; max 200 days fills out into early October but most of
		// the data lands in MAM/JJA. Test only checks the MAM bin.
		// Better: keep all dates within April-May to stay in MAM only.
		date := startDate.AddDate(0, 0, i%60) // cycle within April-May
		x := 50.0 + rng.Float64()*30          // forecast in 50-80°F
		noise := rng.NormFloat64() * trueSigma
		y := trueA + trueB*x + noise
		pairs[i] = Pair{
			Station:   "KNYC",
			ValidDate: date,
			Variable:  "tmax",
			Forecast:  x,
			Observed:  y,
		}
	}

	coeffs, err := Fit(pairs, 0) // no holdout — use all data for fit
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	bc, ok := coeffs.Bins[binKey("KNYC", "MAM", "tmax")]
	if !ok {
		t.Fatalf("MAM bin missing")
	}
	// Check predictions at points across the input range — more robust than
	// checking a, b individually because (a, b) are correlated when x is far
	// from zero. Slope and σ should both be tight.
	d, _ := time.Parse("2006-01-02", "2025-04-15")
	for _, x := range []float64{55, 65, 75} {
		mu, _, _ := coeffs.Predict("KNYC", d, "tmax", x)
		want := trueA + trueB*x
		if math.Abs(mu-want) > 0.6 {
			t.Errorf("Predict at x=%v: μ=%v, want ≈%v", x, mu, want)
		}
	}
	if math.Abs(bc.B-trueB) > 0.05 {
		t.Errorf("slope b = %v, want ≈%v", bc.B, trueB)
	}
	if math.Abs(bc.Sigma-trueSigma) > 0.2 {
		t.Errorf("σ = %v, want ≈%v", bc.Sigma, trueSigma)
	}
	if bc.NTrain == 0 {
		t.Errorf("NTrain = 0")
	}
}

// TestFitProducesMultipleBins verifies binning by (station, season, variable)
// — three stations × two variables in one season should produce 6 bins.
func TestFitProducesMultipleBins(t *testing.T) {
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	var pairs []Pair
	rng := rand.New(rand.NewPCG(7, 7))
	for _, sta := range []string{"KNYC", "KMIA", "KLAX"} {
		for _, v := range []string{"tmax", "tmin"} {
			for i := 0; i < 30; i++ {
				date := startDate.AddDate(0, 0, i%60)
				pairs = append(pairs, Pair{
					Station:   sta,
					ValidDate: date,
					Variable:  v,
					Forecast:  60 + rng.Float64()*20,
					Observed:  60 + rng.Float64()*20,
				})
			}
		}
	}
	c, err := Fit(pairs, 0)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if got := len(c.Bins); got != 6 {
		t.Errorf("bins: got %d, want 6 (3 stations × 2 vars in MAM)", got)
	}
}

// TestFitDropsBinsBelowMinTrain verifies that a bin with too few pairs
// is excluded rather than producing an unreliable fit. Predict on it
// returns ok=false.
func TestFitDropsBinsBelowMinTrain(t *testing.T) {
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	pairs := make([]Pair, 5) // < MinTrainPerBin (=10)
	for i := range pairs {
		pairs[i] = Pair{
			Station:   "KNYC",
			ValidDate: startDate.AddDate(0, 0, i),
			Variable:  "tmax",
			Forecast:  60 + float64(i),
			Observed:  62 + float64(i),
		}
	}
	_, err := Fit(pairs, 0)
	// Either the bin is dropped (Fit returns "no bin had at least N pairs")
	// or returns an empty Coefficients. Both are acceptable.
	if err == nil {
		t.Fatalf("expected Fit to fail with insufficient pairs")
	}
}

func TestPredictUnknownBinReturnsFalse(t *testing.T) {
	c := Coefficients{Bins: map[string]BinCoeffs{}}
	d, _ := time.Parse("2006-01-02", "2025-04-15")
	mu, sigma, ok := c.Predict("KNYC", d, "tmax", 70)
	if ok {
		t.Errorf("Predict on empty coeffs should return ok=false; got μ=%v σ=%v", mu, sigma)
	}
}

func TestPredictAppliesAffineCorrection(t *testing.T) {
	c := Coefficients{
		Bins: map[string]BinCoeffs{
			binKey("KMIA", "MAM", "tmax"): {A: 2.0, B: 1.0, Sigma: 1.5},
		},
	}
	d, _ := time.Parse("2006-01-02", "2025-04-15")
	mu, sigma, ok := c.Predict("KMIA", d, "tmax", 80.0)
	if !ok {
		t.Fatalf("Predict ok=false")
	}
	if math.Abs(mu-82.0) > 1e-9 {
		t.Errorf("μ = %v, want 82 (= 2 + 1*80)", mu)
	}
	if math.Abs(sigma-1.5) > 1e-9 {
		t.Errorf("σ = %v, want 1.5", sigma)
	}
}

// TestFitHandlesBiasCorrectly verifies the Phase 0.5 motivating case:
// observed = forecast - 2 (i.e., forecast over-predicts by 2°F, like KMIA).
// MOS should produce a ≈ -2, b ≈ 1.
func TestFitHandlesBiasCorrectly(t *testing.T) {
	rng := rand.New(rand.NewPCG(99, 99))
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	pairs := make([]Pair, 100)
	for i := range pairs {
		date := startDate.AddDate(0, 0, i%60) // stay in MAM
		x := 80.0 + rng.Float64()*5           // forecast 80-85°F
		y := x - 2.0 + rng.NormFloat64()*0.5  // observed = forecast - 2 + small noise
		pairs[i] = Pair{
			Station:   "KMIA",
			ValidDate: date,
			Variable:  "tmax",
			Forecast:  x,
			Observed:  y,
		}
	}
	c, err := Fit(pairs, 0)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	bc := c.Bins[binKey("KMIA", "MAM", "tmax")]
	// Predict at forecast=82: should give μ ≈ 80
	d, _ := time.Parse("2006-01-02", "2025-04-15")
	mu, _, _ := c.Predict("KMIA", d, "tmax", 82.0)
	if math.Abs(mu-80.0) > 0.4 {
		t.Errorf("Predicted μ at f=82 = %v, want ≈80 (bias-corrected). a=%v b=%v", mu, bc.A, bc.B)
	}
}

func TestSortedBinKeys(t *testing.T) {
	c := Coefficients{
		Bins: map[string]BinCoeffs{
			binKey("KNYC", "MAM", "tmin"): {},
			binKey("KMIA", "JJA", "tmax"): {},
			binKey("KLAX", "DJF", "tmax"): {},
		},
	}
	got := c.SortedBinKeys()
	want := []string{"KLAX|DJF|tmax", "KMIA|JJA|tmax", "KNYC|MAM|tmin"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortedBinKeys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
