package bayesian

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: equality within floating tolerance.
func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// helper: build a minimal valid kernel for tests with one bin
// preconfigured for KNYC tmax at h=4.
func testKernelTMAX(eDelta, varDelta float64, n int) *Kernel {
	return &Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "test",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmax"): {
				4: {EDelta: eDelta, VarDelta: varDelta, N: n},
			},
		},
	}
}

func TestUpdate_NoKernel_ReturnsPriorOnly(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 78, HoursRemaining: 4}

	got := Update(prior, obs, nil)

	if got.Method != "prior-only" {
		t.Errorf("Method = %q, want %q", got.Method, "prior-only")
	}
	if got.Mu != prior.Mu {
		t.Errorf("Mu = %v, want %v", got.Mu, prior.Mu)
	}
	if got.Sigma != prior.Sigma {
		t.Errorf("Sigma = %v, want %v", got.Sigma, prior.Sigma)
	}
	if got.PriorWeight != 1 || got.ObsWeight != 0 {
		t.Errorf("weights = (%v, %v), want (1, 0)", got.PriorWeight, got.ObsWeight)
	}
}

func TestUpdate_NoEntryForStation_ReturnsPriorOnly(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	// kernel has KNYC/tmax but obs is KMIA/tmax
	k := testKernelTMAX(2.0, 1.0, 100)
	obs := Observation{Station: "KMIA", Variable: "tmax", RunningExtremum: 78, HoursRemaining: 4}

	got := Update(prior, obs, k)

	if got.Method != "prior-only" {
		t.Errorf("Method = %q, want prior-only (no entry for KMIA)", got.Method)
	}
	if got.Mu != prior.Mu || got.Sigma != prior.Sigma {
		t.Errorf("posterior = (%v, %v), want prior (%v, %v)", got.Mu, got.Sigma, prior.Mu, prior.Sigma)
	}
}

func TestUpdate_NoEntryForHour_ReturnsPriorOnly(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	// kernel has KNYC/tmax at h=4 but obs is at h=7
	k := testKernelTMAX(2.0, 1.0, 100)
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 78, HoursRemaining: 7}

	got := Update(prior, obs, k)

	if got.Method != "prior-only" {
		t.Errorf("Method = %q, want prior-only (no entry for h=7)", got.Method)
	}
}

func TestUpdate_HoursRemainingZero_ReturnsObsOnly(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 85.7, HoursRemaining: 0}
	k := testKernelTMAX(2.0, 1.0, 100)

	got := Update(prior, obs, k)

	if got.Method != "obs-only" {
		t.Errorf("Method = %q, want obs-only", got.Method)
	}
	if got.Mu != obs.RunningExtremum {
		t.Errorf("Mu = %v, want %v (running extremum at settlement)", got.Mu, obs.RunningExtremum)
	}
	if got.Sigma != TerminalSigma {
		t.Errorf("Sigma = %v, want TerminalSigma %v", got.Sigma, TerminalSigma)
	}
	if got.PriorWeight != 0 || got.ObsWeight != 1 {
		t.Errorf("weights = (%v, %v), want (0, 1) for obs-only", got.PriorWeight, got.ObsWeight)
	}
}

func TestUpdate_InvalidPrior_ReturnsPriorOnly(t *testing.T) {
	// Defensive: σ ≤ 0 prior should pass through unchanged.
	prior := Prior{Mu: 80, Sigma: 0}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 78, HoursRemaining: 4}
	k := testKernelTMAX(2.0, 1.0, 100)

	got := Update(prior, obs, k)

	if got.Method != "prior-only" {
		t.Errorf("Method = %q, want prior-only (σ=0)", got.Method)
	}
	if got.Mu != prior.Mu || got.Sigma != prior.Sigma {
		t.Errorf("posterior = (%v, %v), want prior pass-through", got.Mu, got.Sigma)
	}
}

// TestUpdate_PrecisionWeightedMean_TMAX is the headline math test: it
// computes the posterior analytically and asserts the implementation
// matches.
//
// Setup: prior μ=80, σ=3 (σ²=9). R(t)=80, E[Δ]=2, VarDelta=1.
// So μ_obs = 80 + 2 = 82, σ²_obs = 1.
//
// Expected:
//
//	1/σ²_post = 1/9 + 1/1 = 10/9  → σ²_post = 0.9, σ_post ≈ 0.9487
//	μ_post    = 0.9 · (80/9 + 82/1) = 0.9 · 90.8888… ≈ 81.8
func TestUpdate_PrecisionWeightedMean_TMAX(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 80, HoursRemaining: 4}
	k := testKernelTMAX(2.0, 1.0, 100)

	got := Update(prior, obs, k)

	if got.Method != "bayes-gauss" {
		t.Fatalf("Method = %q, want bayes-gauss", got.Method)
	}
	const tol = 1e-9
	wantMu := 0.9 * (80.0/9.0 + 82.0)
	if !near(got.Mu, wantMu, tol) {
		t.Errorf("Mu = %v, want %v", got.Mu, wantMu)
	}
	wantSigma := math.Sqrt(0.9)
	if !near(got.Sigma, wantSigma, tol) {
		t.Errorf("Sigma = %v, want %v", got.Sigma, wantSigma)
	}
	// PriorWeight = σ²_post / σ²_prior = 0.9 / 9 = 0.1
	if !near(got.PriorWeight, 0.1, tol) {
		t.Errorf("PriorWeight = %v, want 0.1", got.PriorWeight)
	}
	// ObsWeight = σ²_post / σ²_obs = 0.9 / 1 = 0.9
	if !near(got.ObsWeight, 0.9, tol) {
		t.Errorf("ObsWeight = %v, want 0.9", got.ObsWeight)
	}
}

// TestUpdate_PrecisionWeightedMean_TMIN mirrors the TMAX case with
// E[Δ] < 0. R(t) is the running MIN of the trajectory; the fitter
// emits E[Δ] negative so that R(t) + E[Δ] is the predicted TMIN.
//
// Setup: prior μ=70, σ=3 (σ²=9). R(t)=68 (running min), E[Δ]=-2,
// VarDelta=1. So μ_obs = 68 + (-2) = 66, σ²_obs = 1.
//
// Expected: σ²_post = 0.9, μ_post = 0.9·(70/9 + 66/1) ≈ 66.4.
func TestUpdate_PrecisionWeightedMean_TMIN(t *testing.T) {
	prior := Prior{Mu: 70, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmin", RunningExtremum: 68, HoursRemaining: 5}
	k := &Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "test",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmin"): {5: {EDelta: -2.0, VarDelta: 1.0, N: 100}},
		},
	}

	got := Update(prior, obs, k)

	if got.Method != "bayes-gauss" {
		t.Fatalf("Method = %q, want bayes-gauss", got.Method)
	}
	const tol = 1e-9
	wantMu := 0.9 * (70.0/9.0 + 66.0)
	if !near(got.Mu, wantMu, tol) {
		t.Errorf("Mu = %v, want %v", got.Mu, wantMu)
	}
	// Sanity: posterior is below the prior (the obs pulls it down).
	if got.Mu >= prior.Mu {
		t.Errorf("Mu = %v ≥ prior.Mu %v (expected obs to pull μ down for TMIN)", got.Mu, prior.Mu)
	}
}

// TestUpdate_VarianceFloor_PreventsCollapse asserts that a kernel
// reporting Var[Δ] = 0.01 is clamped to MinObsVariance, preventing
// the posterior from collapsing to a near-point estimate.
func TestUpdate_VarianceFloor_PreventsCollapse(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 80, HoursRemaining: 4}
	k := testKernelTMAX(2.0, 0.01, 100) // VarDelta tiny

	got := Update(prior, obs, k)

	if got.Method != "bayes-gauss" {
		t.Fatalf("Method = %q, want bayes-gauss", got.Method)
	}
	// With clamp: σ²_obs = MinObsVariance = 0.25.
	// 1/σ²_post = 1/9 + 1/0.25 = 0.111… + 4 = 4.111…
	// σ²_post = 1/4.111… ≈ 0.2432, σ_post ≈ 0.4932
	wantPrecPost := 1.0/9.0 + 1.0/MinObsVariance
	wantVarPost := 1.0 / wantPrecPost
	wantSigma := math.Sqrt(wantVarPost)
	if !near(got.Sigma, wantSigma, 1e-9) {
		t.Errorf("Sigma = %v, want %v (var floor should have engaged)", got.Sigma, wantSigma)
	}
	// Defensive check: without the floor, σ_post would be √(1/(1/9 + 100)) ≈
	// √0.00990 ≈ 0.0995. The floor must keep us well above that.
	if got.Sigma < 0.3 {
		t.Errorf("Sigma = %v collapsed below 0.3°F — var floor did not engage", got.Sigma)
	}
}

// TestUpdate_WeightsSumProperly verifies the algebraic identity
// PriorWeight + ObsWeight = 1 in the standard bayes-gauss case.
// This follows from σ²_post / σ²_prior + σ²_post / σ²_obs =
// σ²_post · (1/σ²_prior + 1/σ²_obs) = σ²_post · (1/σ²_post) = 1.
func TestUpdate_WeightsSumProperly(t *testing.T) {
	cases := []struct {
		name     string
		priorMu  float64
		priorSig float64
		eDelta   float64
		varDelta float64
		R        float64
	}{
		{"prior-tight", 80, 1.0, 2, 4, 78},
		{"obs-tight", 80, 5.0, 1, 0.5, 79},
		{"balanced", 70, 2.0, -1, 4, 71},
		{"very-uncertain", 60, 10, 0.5, 9, 59},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prior := Prior{Mu: tc.priorMu, Sigma: tc.priorSig}
			obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: tc.R, HoursRemaining: 3}
			k := testKernelTMAX(tc.eDelta, tc.varDelta, 100)
			// Need to put entry at h=3 specifically.
			k.Entries[EntryKey("KNYC", "tmax")] = map[int]Residual{
				3: {EDelta: tc.eDelta, VarDelta: tc.varDelta, N: 100},
			}

			got := Update(prior, obs, k)
			sum := got.PriorWeight + got.ObsWeight
			if !near(sum, 1, 1e-12) {
				t.Errorf("PriorWeight + ObsWeight = %v, want 1 (within 1e-12)", sum)
			}
		})
	}
}

// TestUpdate_PosteriorTighterThanPrior asserts the sanity invariant
// that any Bayes-gauss update should produce σ_post ≤ σ_prior. (Adding
// information cannot increase uncertainty.)
func TestUpdate_PosteriorTighterThanPrior(t *testing.T) {
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 80, HoursRemaining: 4}
	k := testKernelTMAX(0, 1.0, 100) // σ_obs = 1
	got := Update(prior, obs, k)
	if got.Sigma > prior.Sigma {
		t.Errorf("σ_post = %v > σ_prior = %v (bayes-gauss must tighten)", got.Sigma, prior.Sigma)
	}
}

func TestLoadKernel_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	want := Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "2024-04-01..2026-05-04",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmax"): {
				3: {EDelta: 0.5, VarDelta: 1.2, N: 100},
				4: {EDelta: 0.8, VarDelta: 1.5, N: 100},
			},
			EntryKey("KMIA", "tmin"): {
				6: {EDelta: -0.4, VarDelta: 0.9, N: 80},
			},
		},
	}
	raw, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := LoadKernel(path)
	if err != nil {
		t.Fatalf("LoadKernel: %v", err)
	}
	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", got.SchemaVersion, want.SchemaVersion)
	}
	if got.FitWindow != want.FitWindow {
		t.Errorf("FitWindow = %q, want %q", got.FitWindow, want.FitWindow)
	}
	r := got.Entries[EntryKey("KNYC", "tmax")][3]
	if !near(r.EDelta, 0.5, 1e-9) || !near(r.VarDelta, 1.2, 1e-9) || r.N != 100 {
		t.Errorf("KNYC/tmax/h=3 = %+v, want EDelta=0.5 VarDelta=1.2 N=100", r)
	}
}

func TestLoadKernel_SchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	bad := Kernel{
		SchemaVersion: "isobar.bayesian/v0", // wrong version
		FitWindow:     "test",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmax"): {4: {EDelta: 0, VarDelta: 1, N: 10}},
		},
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadKernel(path)
	if err == nil {
		t.Fatalf("LoadKernel: nil err on wrong schema_version")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("err = %q, want one mentioning schema_version", err.Error())
	}
}

func TestLoadKernel_RejectsLowN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	bad := Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "test",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmax"): {
				4: {EDelta: 0, VarDelta: 1, N: 2}, // N < MinKernelN
			},
		},
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadKernel(path)
	if err == nil {
		t.Fatalf("LoadKernel: nil err on low N")
	}
	// The error must name the bin so an operator can fix it. We check
	// for both the station/variable and the hour value in the message.
	msg := err.Error()
	if !strings.Contains(msg, "KNYC/tmax") {
		t.Errorf("err = %q, want one naming the bin key KNYC/tmax", msg)
	}
	if !strings.Contains(msg, "h=4") {
		t.Errorf("err = %q, want one naming h=4", msg)
	}
}

func TestLoadKernel_RejectsNegativeVariance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	bad := Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "test",
		Entries: map[string]map[int]Residual{
			EntryKey("KNYC", "tmax"): {
				4: {EDelta: 0, VarDelta: -0.1, N: 100}, // negative variance
			},
		},
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadKernel(path)
	if err == nil {
		t.Fatalf("LoadKernel: nil err on negative VarDelta")
	}
	if !strings.Contains(err.Error(), "var_delta") {
		t.Errorf("err = %q, want one mentioning var_delta", err.Error())
	}
}

// TestLoadKernel_AcceptsEmptyEntries documents the chosen behavior:
// an empty entries map is allowed and yields prior-only for every
// Update call. This is the natural identity for an unfitted kernel
// (e.g. cold-start before cmd/calibrate-bayesian has run) and makes
// the integration path safe-by-default.
func TestLoadKernel_AcceptsEmptyEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	empty := Kernel{
		SchemaVersion: KernelSchemaVersion,
		FitWindow:     "test",
		Entries:       map[string]map[int]Residual{},
	}
	raw, _ := json.Marshal(empty)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	k, err := LoadKernel(path)
	if err != nil {
		t.Fatalf("LoadKernel: %v (empty entries should be allowed)", err)
	}
	// And every Update should fall through to prior-only.
	prior := Prior{Mu: 80, Sigma: 3}
	obs := Observation{Station: "KNYC", Variable: "tmax", RunningExtremum: 78, HoursRemaining: 4}
	if got := Update(prior, obs, k); got.Method != "prior-only" {
		t.Errorf("Update with empty kernel: Method = %q, want prior-only", got.Method)
	}
}

func TestLoadKernel_RejectsNullEntriesMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	// Hand-write JSON with entries: null to hit the explicit nil-map check.
	body := `{"schema_version":"` + KernelSchemaVersion + `","fit_window":"test","entries":null}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadKernel(path)
	if err == nil {
		t.Fatalf("LoadKernel: nil err on null entries map")
	}
	if !strings.Contains(err.Error(), "entries map is null") {
		t.Errorf("err = %q, want one mentioning the null entries map", err.Error())
	}
}

func TestLoadKernel_MissingFile(t *testing.T) {
	_, err := LoadKernel("/nonexistent/path/kernel.json")
	if err == nil {
		t.Fatalf("LoadKernel: nil err on missing file")
	}
}

func TestLoadKernel_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadKernel(path)
	if err == nil {
		t.Fatalf("LoadKernel: nil err on malformed json")
	}
}

func TestEntryKey(t *testing.T) {
	if got := EntryKey("KNYC", "tmax"); got != "KNYC/tmax" {
		t.Errorf("EntryKey = %q, want KNYC/tmax", got)
	}
}
