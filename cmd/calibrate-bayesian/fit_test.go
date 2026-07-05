package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/market"
)

// TestOnlineStat covers Welford's invariants vs a closed-form reference.
func TestOnlineStat(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		s := onlineStat{}
		mean, variance := s.MeanVar()
		assert.Equal(t, 0.0, mean)
		assert.Equal(t, 0.0, variance)
	})
	t.Run("single sample → variance 0", func(t *testing.T) {
		s := onlineStat{}
		s.Update(5.0)
		mean, variance := s.MeanVar()
		assert.InDelta(t, 5.0, mean, 1e-12)
		assert.Equal(t, 0.0, variance)
	})
	t.Run("matches closed-form mean+variance", func(t *testing.T) {
		samples := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
		s := onlineStat{}
		for _, x := range samples {
			s.Update(x)
		}
		gotMean, gotVar := s.MeanVar()
		// closed-form: mean=5.5, sample variance (N-1)=9.166666...
		assert.InDelta(t, 5.5, gotMean, 1e-12)
		assert.InDelta(t, 9.166666666666666, gotVar, 1e-9)
		assert.Equal(t, 10, s.N)
	})
}

// TestStationIANA pins the four canonical mappings via the shared
// market.StationTZ helper; refusing unknowns keeps the fitter honest if
// the registry grows without a corresponding tz entry.
func TestStationIANA(t *testing.T) {
	cases := map[string]string{
		"KNYC": "America/New_York",
		"KMIA": "America/New_York",
		"KLAX": "America/Los_Angeles",
		"KMDW": "America/Chicago",
	}
	for code, want := range cases {
		got, err := market.StationTZ(code)
		require.NoError(t, err)
		assert.Equal(t, want, got.String(), "station %s", code)
	}
	_, err := market.StationTZ("KORD")
	assert.Error(t, err, "unknown station must error")
}

// TestLoadTruth_RoundTrip covers the schema-tolerant parse + the
// best-effort skip of malformed rows.
func TestLoadTruth_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "KNYC.csv")
	csv := "valid_date,tmax_f,tmin_f,issued_at,bulletin_id\n" +
		"2024-04-29,87,63,2024-04-30T06:18:00Z,CLINYC_202404300618.txt\n" +
		"2024-04-30,66,52,2024-05-01T06:21:00Z,CLINYC_202405010621.txt\n" +
		"bogus,row,here,,\n" +
		"2024-05-01,M,M,,\n" + // missing temps → skipped
		"2024-05-02,72,58\n" // short trailing cols → kept
	require.NoError(t, os.WriteFile(path, []byte(csv), 0o600))

	tr, err := loadTruth(path)
	require.NoError(t, err)
	assert.Len(t, tr, 3)
	assert.Equal(t, 87.0, tr["2024-04-29"].Tmax)
	assert.Equal(t, 63.0, tr["2024-04-29"].Tmin)
	assert.Equal(t, 58.0, tr["2024-05-02"].Tmin)
	assert.NotContains(t, tr, "bogus")
	assert.NotContains(t, tr, "2024-05-01")
}

// TestKernelAccumulator_Finalize verifies the MinKernelN drop and the
// schema-version stamp.
func TestKernelAccumulator_Finalize(t *testing.T) {
	a := newKernelAccumulator()
	// 5 samples in bin tmax/h=12 → kept
	for i := 1; i <= 5; i++ {
		a.Add("KNYC", "tmax", 12, float64(i))
	}
	// 3 samples in bin tmax/h=6 → dropped (below MinKernelN)
	for i := 1; i <= 3; i++ {
		a.Add("KNYC", "tmax", 6, 0)
	}
	k := a.Finalize("test-window", bayesian.MinKernelN)
	assert.Equal(t, bayesian.KernelSchemaVersion, k.SchemaVersion)
	assert.Equal(t, "test-window", k.FitWindow)
	require.Contains(t, k.Entries, "KNYC/tmax")
	bin := k.Entries["KNYC/tmax"]
	assert.Contains(t, bin, 12)
	assert.NotContains(t, bin, 6)
	r := bin[12]
	assert.Equal(t, 5, r.N)
	assert.InDelta(t, 3.0, r.EDelta, 1e-12) // mean of 1..5
}

// TestKernelAccumulator_RoundTripsLoadKernel asserts the on-disk
// emit is compatible with bayesian.LoadKernel — the contract the
// fitter is shipping.
func TestKernelAccumulator_RoundTripsLoadKernel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.json")
	a := newKernelAccumulator()
	// Two bins, both above MinKernelN.
	for i := 1; i <= 10; i++ {
		a.Add("KNYC", "tmax", 12, float64(i))
		a.Add("KMIA", "tmin", 6, float64(-i)) // negative Δ for tmin
	}
	k := a.Finalize("rt", bayesian.MinKernelN)
	require.NoError(t, writeKernel(path, k))

	got, err := bayesian.LoadKernel(path)
	require.NoError(t, err)
	assert.Equal(t, "rt", got.FitWindow)
	require.Contains(t, got.Entries, "KNYC/tmax")
	require.Contains(t, got.Entries, "KMIA/tmin")
	assert.InDelta(t, 5.5, got.Entries["KNYC/tmax"][12].EDelta, 1e-9)
	assert.InDelta(t, -5.5, got.Entries["KMIA/tmin"][6].EDelta, 1e-9)
}

// TestFitStationVariable_TmaxTrajectory uses a synthetic temperature
// trajectory with known TMAX to verify the running-max + Δ emission
// at every h_remain boundary.
//
// Fixture: NYC, civil date 2024-05-01. Local day = [2024-05-01 00:00
// EDT, 2024-05-02 00:00 EDT) = [04:00Z, 04:00Z next].
// Hourly trajectory: temperature rises 50 → 70 then drops 70 → 60
// across the day. TMAX truth = 70.
func TestFitStationVariable_TmaxTrajectory(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// Build hourly obs spanning 04:00Z..04:00Z+1day at integer-hour boundaries.
	dayOpenUTC := time.Date(2024, 5, 1, 4, 0, 0, 0, time.UTC)
	temps := []float64{
		50, 52, 54, 56, 58, 60, // hours 00-05 local (04-09Z), warming
		62, 65, 68, 70, 70, 70, // hours 06-11 local (10-15Z), peak
		68, 66, 64, 62, 60, 58, // hours 12-17 local (16-21Z), cooling
		56, 54, 52, 51, 50, 50, // hours 18-23 local (22Z-03Z+1)
	}
	var hourly []hourlyObs
	for i, v := range temps {
		hourly = append(hourly, hourlyObs{
			TS:   dayOpenUTC.Add(time.Duration(i) * time.Hour),
			TmpF: v,
		})
	}
	truth := map[string]truthRow{"2024-05-01": {Tmax: 70, Tmin: 50}}

	accum := newKernelAccumulator()
	st := stationSummary{}
	fitStationVariable("KNYC", "tmax", hourly, truth, tz,
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		accum, &st)

	assert.Equal(t, 1, st.PairsFit)
	assert.Equal(t, 0, st.SkippedNoTruth)

	bin := accum.bins["KNYC/tmax"]
	require.NotNil(t, bin)
	// Sanity:
	// h_remain=24 → t = dayClose - 24h = dayOpen. running has hour-0 obs (50). Δ = 70 - 50 = 20.
	require.Contains(t, bin, 24)
	gotMean24, _ := bin[24].MeanVar()
	assert.InDelta(t, 20.0, gotMean24, 1e-9)
	// h_remain=12 → t = dayOpen + 12h. running has seen hours 0..11 incl. peak (70). Δ = 0.
	require.Contains(t, bin, 12)
	gotMean12, _ := bin[12].MeanVar()
	assert.InDelta(t, 0.0, gotMean12, 1e-9)
	// h_remain=1 → t = dayClose - 1h. running has full day = 70. Δ = 0.
	require.Contains(t, bin, 1)
	gotMean1, _ := bin[1].MeanVar()
	assert.InDelta(t, 0.0, gotMean1, 1e-9)
}

// TestFitStationVariable_TminTrajectory mirrors the tmax test for
// running-min semantics. Cold morning, warm afternoon, cold late
// night. TMIN should be hit early in the day.
func TestFitStationVariable_TminTrajectory(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	dayOpenUTC := time.Date(2024, 5, 1, 4, 0, 0, 0, time.UTC)
	temps := []float64{
		55, 53, 51, 50, 50, 52, // hours 00-05 local, TMIN reached at hour 3 (=07Z) at 50°F
		56, 60, 65, 70, 72, 74,
		76, 75, 73, 70, 66, 62,
		58, 56, 55, 54, 53, 52,
	}
	var hourly []hourlyObs
	for i, v := range temps {
		hourly = append(hourly, hourlyObs{
			TS:   dayOpenUTC.Add(time.Duration(i) * time.Hour),
			TmpF: v,
		})
	}
	truth := map[string]truthRow{"2024-05-01": {Tmax: 76, Tmin: 50}}

	accum := newKernelAccumulator()
	st := stationSummary{}
	fitStationVariable("KNYC", "tmin", hourly, truth, tz,
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		accum, &st)

	bin := accum.bins["KNYC/tmin"]
	require.NotNil(t, bin)
	// h_remain=24 → t = dayOpen. running has only hour 0 (55). Δ = TMIN(50) - 55 = -5.
	gotMean24, _ := bin[24].MeanVar()
	assert.InDelta(t, -5.0, gotMean24, 1e-9)
	// h_remain=20 → t = dayOpen + 4h. running has hours 0..3 incl. 50. Δ = 50 - 50 = 0.
	gotMean20, _ := bin[20].MeanVar()
	assert.InDelta(t, 0.0, gotMean20, 1e-9)
	// h_remain=1 → end of day. Δ = 0.
	gotMean1, _ := bin[1].MeanVar()
	assert.InDelta(t, 0.0, gotMean1, 1e-9)
}

// TestFitStationVariable_NoTruth_Skips verifies the "no truth" branch
// is counted and produces no kernel samples.
func TestFitStationVariable_NoTruth_Skips(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	hourly := []hourlyObs{{
		TS:   time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC),
		TmpF: 65,
	}}
	accum := newKernelAccumulator()
	st := stationSummary{}
	fitStationVariable("KNYC", "tmax", hourly, map[string]truthRow{}, tz,
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		accum, &st)

	assert.Equal(t, 1, st.SkippedNoTruth)
	assert.Equal(t, 0, st.PairsFit)
	assert.Empty(t, accum.bins)
}

// TestFitStationVariable_DST_FallBack ensures civil-day reconstruction
// works across a DST transition where the local day has 25 hours.
// 2024-11-03 is "fall back" Sunday in America/New_York: 02:00 EDT
// repeats as 01:00 EST. The civil day is 25 hours long; obs collected
// in UTC should still bucket correctly.
func TestFitStationVariable_DST_FallBack(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// 2024-11-03 local day = 2024-11-03 00:00 EDT (= 2024-11-03 04:00 UTC)
	// to 2024-11-04 00:00 EST (= 2024-11-04 05:00 UTC). 25-hour day.
	dayOpenUTC := time.Date(2024, 11, 3, 4, 0, 0, 0, time.UTC)
	var hourly []hourlyObs
	for i := 0; i < 25; i++ {
		hourly = append(hourly, hourlyObs{
			TS:   dayOpenUTC.Add(time.Duration(i) * time.Hour),
			TmpF: 50 + float64(i), // monotonically rising for predictability
		})
	}
	truth := map[string]truthRow{"2024-11-03": {Tmax: 74, Tmin: 50}}

	accum := newKernelAccumulator()
	st := stationSummary{}
	fitStationVariable("KNYC", "tmax", hourly, truth, tz,
		time.Date(2024, 11, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 11, 3, 0, 0, 0, 0, time.UTC),
		accum, &st)

	assert.Equal(t, 1, st.PairsFit)
	bin := accum.bins["KNYC/tmax"]
	require.NotNil(t, bin)

	// On a 25-hour DST fall-back day, the kernel axis is still
	// hRemain ∈ [1, 24] — every hour boundary in that range falls
	// inside the local day. The fitter must:
	//   - not panic
	//   - emit a Δ for every hRemain ∈ [1, 24] (we have obs at every hour)
	//   - keep Δ monotonically non-increasing as hRemain shrinks (more
	//     obs absorbed → running max approaches final TMAX)
	for h := minHRemain; h <= maxHRemain; h++ {
		require.Contains(t, bin, h, "missing h_remain=%d on DST day", h)
	}
	prev := math.Inf(+1)
	for h := maxHRemain; h >= minHRemain; h-- {
		got, _ := bin[h].MeanVar()
		assert.LessOrEqual(t, got, prev+1e-9,
			"Δ should be non-increasing as h_remain shrinks; h=%d got=%v prev=%v", h, got, prev)
		prev = got
	}
	// Sanity: final Δ at hRemain=1 should be ≤ 1 (running max within 1°F of TMAX).
	got1, _ := bin[1].MeanVar()
	assert.LessOrEqual(t, got1, 1.0+1e-9, "Δ at h_remain=1 should be small; got %v", got1)
}

// TestFitStationVariable_ColdStart_SkipsEmptyHours verifies that
// h_remain bins which precede the first observation of the day get
// no sample (we don't model "no info yet" as Δ=NaN, we just skip).
func TestFitStationVariable_ColdStart_SkipsEmptyHours(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// Only one obs at 14:00 local (18:00Z). Bins h_remain=24..11 should be empty;
	// h_remain=10..1 should record running max = 70.
	hourly := []hourlyObs{{
		TS:   time.Date(2024, 5, 1, 18, 0, 0, 0, time.UTC),
		TmpF: 70,
	}}
	truth := map[string]truthRow{"2024-05-01": {Tmax: 75, Tmin: 50}}

	accum := newKernelAccumulator()
	st := stationSummary{}
	fitStationVariable("KNYC", "tmax", hourly, truth, tz,
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		accum, &st)

	bin := accum.bins["KNYC/tmax"]
	require.NotNil(t, bin)
	assert.NotContains(t, bin, 24, "no obs before this boundary → no sample")
	assert.NotContains(t, bin, 12, "no obs before this boundary → no sample")
	assert.Contains(t, bin, 10, "first obs at 14h local = 10h before close")
	gotMean10, _ := bin[10].MeanVar()
	assert.InDelta(t, 5.0, gotMean10, 1e-9, "Δ = 75 - 70 = 5")
}

// TestRunFit_Integration end-to-ends the pipeline with fixture files
// on disk: writes truth + hourly CSVs, runs the fitter, loads the
// kernel back, asserts the math.
func TestRunFit_Integration(t *testing.T) {
	dir := t.TempDir()

	// Truth: 7 days at KNYC, integer TMAX/TMIN.
	truthRows := "valid_date,tmax_f,tmin_f\n"
	for d := 1; d <= 7; d++ {
		truthRows += fmt.Sprintf("2024-05-%02d,70,50\n", d)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC.csv"), []byte(truthRows), 0o600))

	// Hourly: each day = 24 obs in a tent shape, 50°F → 70°F (hour 12)
	// → 50°F. TMAX = 70 hit at the noon hour, then mirrored down.
	hourly := "station,valid,tmpf\n"
	for d := 1; d <= 7; d++ {
		for h := 0; h < 24; h++ {
			ts := time.Date(2024, 5, d, 4+h, 0, 0, 0, time.UTC).Format("2006-01-02 15:04")
			var tmp float64
			if h <= 12 {
				tmp = 50.0 + float64(h)*20.0/12.0
			} else {
				tmp = 70.0 - float64(h-12)*20.0/12.0
			}
			hourly += fmt.Sprintf("NYC,%s,%.2f\n", ts, tmp)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"), []byte(hourly), 0o600))

	out := filepath.Join(dir, "kernel.json")
	opts := fitOptions{
		ObsDir:    dir,
		TruthDir:  dir,
		OutPath:   out,
		FitStart:  time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		FitEnd:    time.Date(2024, 5, 7, 0, 0, 0, 0, time.UTC),
		Stations:  []string{"KNYC"},
		FitWindow: "test-integration",
	}
	summary, err := runFit(opts)
	require.NoError(t, err)
	assert.Equal(t, 1, len(summary.Stations))
	assert.Equal(t, 7, summary.Stations["KNYC"].TruthRows)
	assert.Equal(t, 7*24, summary.Stations["KNYC"].HourlyRows)
	assert.Equal(t, 14, summary.Stations["KNYC"].PairsFit) // 7 days × 2 vars

	k, err := bayesian.LoadKernel(out)
	require.NoError(t, err)
	require.Contains(t, k.Entries, "KNYC/tmax")
	require.Contains(t, k.Entries, "KNYC/tmin")
	// Variance across 7 identical V-shaped days should be ~0 at every h_remain.
	for _, r := range k.Entries["KNYC/tmax"] {
		assert.LessOrEqual(t, r.VarDelta, 1e-6,
			"identical fixture days should have ~0 sample variance, got %v", r.VarDelta)
		assert.GreaterOrEqual(t, r.N, bayesian.MinKernelN)
	}

	// Marshal-back round-trip via stdlib for shape sanity.
	raw, err := os.ReadFile(out) //nolint:gosec // test reads its own temp output
	require.NoError(t, err)
	var back bayesian.Kernel
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, bayesian.KernelSchemaVersion, back.SchemaVersion)
}
