package mos

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// testClock returns a fixed FakeClock anchored at 2026-01-01 UTC so MOS
// Fit produces byte-identical Coefficients given byte-identical pairs.
// One canonical anchor across the file keeps test failures readable.
func testClock() clock.Clock {
	return clock.Fake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

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

	coeffs, err := Fit(pairs, 0, testClock()) // no holdout — use all data for fit
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
	c, err := Fit(pairs, 0, testClock())
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
	_, err := Fit(pairs, 0, testClock())
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
	c, err := Fit(pairs, 0, testClock())
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

// TestFitEmitsSchemaVersion asserts the fitted Coefficients carry the
// current schema version so downstream loaders can validate compatibility.
func TestFitEmitsSchemaVersion(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	pairs := make([]Pair, 50)
	for i := range pairs {
		pairs[i] = Pair{
			Station:   "KNYC",
			ValidDate: startDate.AddDate(0, 0, i%60),
			Variable:  "tmax",
			Forecast:  60 + rng.Float64()*20,
			Observed:  60 + rng.Float64()*20,
		}
	}
	c, err := Fit(pairs, 0, testClock())
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if c.SchemaVersion != CoefficientsSchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", c.SchemaVersion, CoefficientsSchemaVersion)
	}
}

// TestFitUsesHoldoutSigmaWhenSufficient verifies that with ≥
// MinHoldoutForSigma holdout pairs, σ comes from holdout residuals (and is
// labelled "holdout"), not the in-sample training residual stdev. The two
// σ values must differ — otherwise the test is vacuous.
//
// Setup: training pairs come from y = trueA + trueB·x + N(0, σ_train²),
// holdout pairs from the same a, b but with a *larger* noise σ_holdout.
// The fit recovers a, b accurately and the residual stdevs on each set
// are clean estimates of their respective noise scales.
func TestFitUsesHoldoutSigmaWhenSufficient(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 11))
	const (
		trueA, trueB             = 1.5, 0.95
		sigmaTrain, sigmaHoldout = 1.0, 3.0 // 3× larger holdout noise
		nTrainPerDay             = 5
		nHoldoutAll              = 60 // ≥ MinHoldoutForSigma (30)
	)

	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	// Training window: 40 days starting at startDate, each with nTrainPerDay
	// pairs. Holdout window: the trailing 10 days.
	holdoutDays := 10
	trainDays := 40

	var pairs []Pair
	for d := 0; d < trainDays; d++ {
		date := startDate.AddDate(0, 0, d)
		for k := 0; k < nTrainPerDay; k++ {
			x := 55.0 + rng.Float64()*25 // 55-80°F forecast
			y := trueA + trueB*x + rng.NormFloat64()*sigmaTrain
			pairs = append(pairs, Pair{
				Station: "KNYC", ValidDate: date, Variable: "tmax",
				Forecast: x, Observed: y,
			})
		}
	}
	for k := 0; k < nHoldoutAll; k++ {
		date := startDate.AddDate(0, 0, trainDays+(k%holdoutDays))
		x := 55.0 + rng.Float64()*25
		y := trueA + trueB*x + rng.NormFloat64()*sigmaHoldout
		pairs = append(pairs, Pair{
			Station: "KNYC", ValidDate: date, Variable: "tmax",
			Forecast: x, Observed: y,
		})
	}

	c, err := Fit(pairs, holdoutDays, testClock())
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	bc, ok := c.Bins[binKey("KNYC", "MAM", "tmax")]
	if !ok {
		t.Fatalf("MAM bin missing; bins=%v", c.SortedBinKeys())
	}
	if bc.SigmaSource != SigmaSourceHoldout {
		t.Errorf("SigmaSource = %q, want %q", bc.SigmaSource, SigmaSourceHoldout)
	}
	if bc.NHoldout < MinHoldoutForSigma {
		t.Errorf("NHoldout = %d, want ≥ %d", bc.NHoldout, MinHoldoutForSigma)
	}
	// σ should be closer to σ_holdout (3.0) than σ_train (1.0). Loose
	// band to absorb sample noise — what matters is direction.
	if math.Abs(bc.Sigma-sigmaHoldout) > 0.8 {
		t.Errorf("Sigma = %v, want ≈ %v (holdout σ); training σ was %v",
			bc.Sigma, sigmaHoldout, sigmaTrain)
	}
	if bc.Sigma < 1.5 {
		t.Errorf("Sigma = %v looks like training-σ (%v), not holdout-σ (%v)",
			bc.Sigma, sigmaTrain, sigmaHoldout)
	}
}

// TestFitFlagsTrainingFallbackWhenHoldoutTooSmall verifies the negative
// path: with < MinHoldoutForSigma holdout pairs (or none), σ stays at the
// training-residual stdev and SigmaSource is "training-fallback" so a
// downstream consumer can see it was the conservative path.
func TestFitFlagsTrainingFallbackWhenHoldoutTooSmall(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 13))
	const trueA, trueB, trueSigma = 0.5, 1.0, 1.2
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")

	// 60 training pairs in April-May (MAM), no holdout configured at all
	// (holdoutDays = 0).
	pairs := make([]Pair, 60)
	for i := range pairs {
		date := startDate.AddDate(0, 0, i%50)
		x := 60.0 + rng.Float64()*15
		y := trueA + trueB*x + rng.NormFloat64()*trueSigma
		pairs[i] = Pair{
			Station: "KNYC", ValidDate: date, Variable: "tmax",
			Forecast: x, Observed: y,
		}
	}
	c, err := Fit(pairs, 0, testClock())
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	bc, ok := c.Bins[binKey("KNYC", "MAM", "tmax")]
	if !ok {
		t.Fatalf("bin missing")
	}
	// With holdoutDays=0, all pairs land in train (or holdout subset is
	// strictly less than MinHoldoutForSigma=30). Either way SigmaSource
	// must be training-fallback.
	if bc.NHoldout >= MinHoldoutForSigma {
		t.Fatalf("test bug: NHoldout=%d ≥ %d — adjust fixture",
			bc.NHoldout, MinHoldoutForSigma)
	}
	if bc.SigmaSource != SigmaSourceTrainingFallback {
		t.Errorf("SigmaSource = %q, want %q",
			bc.SigmaSource, SigmaSourceTrainingFallback)
	}
	if math.Abs(bc.Sigma-trueSigma) > 0.3 {
		t.Errorf("Sigma = %v, want ≈ %v (training residual stdev)",
			bc.Sigma, trueSigma)
	}
}

// TestFitHoldoutBoundary pins the train/holdout split exactly:
// holdoutDays=N reserves precisely the final N civil dates, and
// holdoutDays=0 reserves nothing. The previous boundary
// (maxDate − N·24h, inclusive) held out N+1 days — with N=0 the final
// day was silently diverted out of training even though callers asked
// for "no holdout".
func TestFitHoldoutBoundary(t *testing.T) {
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	mkPairs := func(days, perDay int) []Pair {
		var pairs []Pair
		for d := 0; d < days; d++ {
			for k := 0; k < perDay; k++ {
				x := 60.0 + float64(k)
				pairs = append(pairs, Pair{
					Station: "KNYC", ValidDate: startDate.AddDate(0, 0, d),
					Variable: "tmax", Forecast: x, Observed: x + 1 + 0.1*float64(k%3),
				})
			}
		}
		return pairs
	}

	t.Run("holdoutDays=0 keeps every pair in training", func(t *testing.T) {
		const days, perDay = 20, 3
		c, err := Fit(mkPairs(days, perDay), 0, testClock())
		if err != nil {
			t.Fatalf("Fit: %v", err)
		}
		bc := c.Bins[binKey("KNYC", "MAM", "tmax")]
		if bc.NTrain != days*perDay {
			t.Errorf("NTrain = %d, want %d (all pairs)", bc.NTrain, days*perDay)
		}
		if bc.NHoldout != 0 {
			t.Errorf("NHoldout = %d, want 0", bc.NHoldout)
		}
	})

	t.Run("holdoutDays=5 reserves exactly the final 5 dates", func(t *testing.T) {
		const days, perDay, holdout = 20, 3, 5
		c, err := Fit(mkPairs(days, perDay), holdout, testClock())
		if err != nil {
			t.Fatalf("Fit: %v", err)
		}
		bc := c.Bins[binKey("KNYC", "MAM", "tmax")]
		if bc.NTrain != (days-holdout)*perDay {
			t.Errorf("NTrain = %d, want %d", bc.NTrain, (days-holdout)*perDay)
		}
		if bc.NHoldout != holdout*perDay {
			t.Errorf("NHoldout = %d, want %d", bc.NHoldout, holdout*perDay)
		}
		// Window labels bracket the split: training ends the day before
		// the holdout starts.
		if want := startDate.AddDate(0, 0, days-holdout).Format("2006-01-02"); c.HoldoutWindowStart != want {
			t.Errorf("HoldoutWindowStart = %s, want %s", c.HoldoutWindowStart, want)
		}
		if want := startDate.AddDate(0, 0, days-holdout-1).Format("2006-01-02"); c.TrainingWindowEnd != want {
			t.Errorf("TrainingWindowEnd = %s, want %s", c.TrainingWindowEnd, want)
		}
	})
}

// TestLoadCoefficients_LegacyFileBackCompat: a Coefficients JSON written
// before FitModelSet existed must still load — defaulting to ["gfs"] —
// rather than erroring. Without this back-compat, every existing
// calibration artifact on disk would silently lose its fitted model.
func TestLoadCoefficients_LegacyFileBackCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.json")
	// Hand-roll a JSON with the v1 schema version and NO fit_model_set
	// key, mimicking a file written before this field was added.
	body := []byte(`{
		"schema_version": "isobar.mos/v1",
		"fitted_at": "2025-01-01T00:00:00Z",
		"training_window_end": "2024-12-31",
		"holdout_window_start": "2025-01-01",
		"bins": {
			"KNYC|MAM|tmax": {"a": 1.0, "b": 1.0, "sigma": 2.0, "n_train": 30, "n_holdout": 5}
		}
	}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCoefficients(path)
	if err != nil {
		t.Fatalf("LoadCoefficients: %v", err)
	}
	if len(c.FitModelSet) != 1 || c.FitModelSet[0] != ModelGFS {
		t.Errorf("FitModelSet = %v, want [%q] (legacy back-compat default)",
			c.FitModelSet, ModelGFS)
	}
}

// TestLoadCoefficients_RoundTripsModelSet: a Coefficients with an
// explicit FitModelSet (e.g. ["gfs"] written by today's calibrate-fit)
// must round-trip through JSON without dropping or mutating the field.
func TestLoadCoefficients_RoundTripsModelSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modern.json")
	orig := Coefficients{
		SchemaVersion:      CoefficientsSchemaVersion,
		FittedAt:           time.Now().UTC().Truncate(time.Second),
		TrainingWindowEnd:  "2024-12-31",
		HoldoutWindowStart: "2025-01-01",
		FitModelSet:        []string{ModelGFS},
		Bins: map[string]BinCoeffs{
			"KNYC|MAM|tmax": {A: 0.5, B: 1.0, Sigma: 2.0, NTrain: 100, NHoldout: 30, SigmaSource: SigmaSourceHoldout},
		},
	}
	body, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(path, body, 0o600); werr != nil {
		t.Fatal(werr)
	}
	got, err := LoadCoefficients(path)
	if err != nil {
		t.Fatalf("LoadCoefficients: %v", err)
	}
	if len(got.FitModelSet) != 1 || got.FitModelSet[0] != ModelGFS {
		t.Errorf("FitModelSet = %v, want [%q]", got.FitModelSet, ModelGFS)
	}
}

func TestCanonicalModelSet(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, []string{}},
		{[]string{"gfs"}, []string{"gfs"}},
		{[]string{"gfs", "ecmwf"}, []string{"ecmwf", "gfs"}},
		{[]string{"gfs", "gfs", "ecmwf"}, []string{"ecmwf", "gfs"}}, // dedupe
		{[]string{"", "gfs"}, []string{"gfs"}},                      // skip empty
	}
	for _, tc := range cases {
		got := CanonicalModelSet(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("CanonicalModelSet(%v) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("CanonicalModelSet(%v) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestModelSetEqual(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"gfs"}, []string{"gfs"}, true},
		{[]string{"gfs"}, []string{"ecmwf"}, false},
		{[]string{"ecmwf", "gfs"}, []string{"ecmwf", "gfs"}, true},
		{[]string{"gfs"}, []string{"ecmwf", "gfs"}, false},
		{[]string{}, []string{}, true},
	}
	for _, tc := range cases {
		if got := ModelSetEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("ModelSetEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
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

// TestFit_DeterministicAcrossCalls is the A8 regression: with identical
// pairs and an identical fake clock, two Fit calls must produce
// byte-identical JSON. Before this fix FittedAt was time.Now().UTC()
// inside Fit, so two back-to-back fits diverged by tens of nanoseconds —
// enough to break any artifact-diffing reproducibility check.
//
// The JSON map iteration order in encoding/json sorts keys alphabetically,
// so Bins won't perturb the comparison even though it is a map.
func TestFit_DeterministicAcrossCalls(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 456))
	startDate, _ := time.Parse("2006-01-02", "2024-04-01")
	pairs := make([]Pair, 80)
	for i := range pairs {
		date := startDate.AddDate(0, 0, i%50)
		x := 60.0 + rng.Float64()*15
		y := 0.5 + 1.0*x + rng.NormFloat64()*1.2
		pairs[i] = Pair{
			Station: "KNYC", ValidDate: date, Variable: "tmax",
			Forecast: x, Observed: y,
		}
	}

	// Reuse the same fake clock across both fits — the point is that
	// Fit reads from the injected clock, not from time.Now, so identical
	// inputs produce identical outputs.
	clk := testClock()

	c1, err := Fit(pairs, 0, clk)
	if err != nil {
		t.Fatalf("Fit (1st): %v", err)
	}
	c2, err := Fit(pairs, 0, clk)
	if err != nil {
		t.Fatalf("Fit (2nd): %v", err)
	}

	// FittedAt is the field that used to drift; cheap explicit check.
	if !c1.FittedAt.Equal(c2.FittedAt) {
		t.Errorf("FittedAt drifted: %v vs %v", c1.FittedAt, c2.FittedAt)
	}

	b1, err := json.Marshal(c1)
	if err != nil {
		t.Fatalf("marshal c1: %v", err)
	}
	b2, err := json.Marshal(c2)
	if err != nil {
		t.Fatalf("marshal c2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("Fit output diverged across calls with identical inputs.\n  c1=%s\n  c2=%s", b1, b2)
	}
}

// TestFit_PanicsOnNilClock documents the explicit-parameter discipline:
// passing nil panics rather than silently defaulting to time.Now. Tests
// that need a clock construct clock.Fake; production constructs clock.Wall.
func TestFit_PanicsOnNilClock(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("Fit(_, _, nil) did not panic")
		}
	}()
	_, _ = Fit([]Pair{{Station: "KNYC", Variable: "tmax", Forecast: 60, Observed: 62, ValidDate: time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)}}, 0, nil)
}
