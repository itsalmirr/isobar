package main

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/calibration/obscsv"
	"github.com/wh0o0pz/isobar/internal/jsonatomic"
	"github.com/wh0o0pz/isobar/internal/market"
)

// minHRemain and maxHRemain bound the kernel's hours_remaining axis.
// h_remain=0 is handled by Update's obs-only branch (no kernel lookup)
// so the fitter only needs bins ≥ 1. h_remain=24 covers a trade right
// at civil-day-open; beyond that the running extremum is uninformative
// because zero observations have landed yet.
const (
	minHRemain = 1
	maxHRemain = 24
)

// fitOptions is the resolved input set for a Fit run, separated from
// flag parsing so fit_test.go can exercise the algorithm with
// synthetic CSVs.
type fitOptions struct {
	ObsDir    string // dir containing K<STA>-hourly.csv
	TruthDir  string // dir containing K<STA>.csv (CLI truth)
	OutPath   string
	FitStart  time.Time // first civil date, inclusive (UTC midnight; civil-day match is by Y-M-D)
	FitEnd    time.Time // last civil date, inclusive
	Stations  []string  // station codes; empty = all canonical
	FitWindow string    // human-readable label baked into the kernel
}

// hourlyObs is a local alias for the shared HourlyObs type so the
// algorithm below reads naturally; the loader lives in
// internal/calibration/bayesian/hourly.go.
type hourlyObs = bayesian.HourlyObs

// truthRow is one parsed NWS CLI daily report. Tmax/Tmin are integers
// in source but kept as float64 for Δ math.
type truthRow struct {
	Tmax float64
	Tmin float64
}

// runFit drives the per-station, per-variable fit and writes the
// resulting Kernel JSON to opts.OutPath. Returns a summary the
// CLI can log.
func runFit(opts fitOptions) (fitSummary, error) {
	stations := opts.Stations
	if len(stations) == 0 {
		stations = market.AllStationCodes()
	}

	accum := newKernelAccumulator()
	sum := fitSummary{Stations: make(map[string]stationSummary, len(stations))}

	for _, code := range stations {
		tz, err := market.StationTZ(code)
		if err != nil {
			return sum, fmt.Errorf("station %s: %w", code, err)
		}

		truthPath := filepath.Join(opts.TruthDir, code+".csv")
		truth, err := loadTruth(truthPath)
		if err != nil {
			return sum, fmt.Errorf("station %s: truth: %w", code, err)
		}

		hourlyPath := filepath.Join(opts.ObsDir, code+"-hourly.csv")
		hourly, err := bayesian.LoadHourlyCSV(hourlyPath)
		if err != nil {
			return sum, fmt.Errorf("station %s: hourly: %w", code, err)
		}

		stSum := stationSummary{
			TruthRows:  len(truth),
			HourlyRows: len(hourly),
		}
		fitStationVariable(code, "tmax", hourly, truth, tz, opts.FitStart, opts.FitEnd, accum, &stSum)
		fitStationVariable(code, "tmin", hourly, truth, tz, opts.FitStart, opts.FitEnd, accum, &stSum)
		sum.Stations[code] = stSum
	}

	kernel := accum.Finalize(opts.FitWindow, bayesian.MinKernelN)
	if err := writeKernel(opts.OutPath, kernel); err != nil {
		return sum, fmt.Errorf("write kernel: %w", err)
	}

	sum.OutPath = opts.OutPath
	sum.BinCount = countBins(kernel)
	return sum, nil
}

// fitStationVariable walks every civil date in [fitStart, fitEnd] for
// one (station, variable) pair, reconstructs the R(t) trajectory from
// the station's UTC hourly obs, and pushes Δ samples into accum
// keyed by h_remain.
func fitStationVariable(
	station, variable string,
	hourly []hourlyObs,
	truth map[string]truthRow,
	tz *time.Location,
	fitStart, fitEnd time.Time,
	accum *kernelAccumulator,
	stSum *stationSummary,
) {
	for day := fitStart; !day.After(fitEnd); day = day.AddDate(0, 0, 1) {
		dateKey := day.Format("2006-01-02")
		tr, ok := truth[dateKey]
		if !ok {
			stSum.SkippedNoTruth++
			continue
		}
		final := tr.Tmax
		if variable == "tmin" {
			final = tr.Tmin
		}

		// Civil-day window in station-local time, converted to UTC for
		// hourly filtering. time.Date with a non-UTC Location handles
		// DST transitions correctly.
		dayOpenLocal := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, tz)
		dayCloseLocal := dayOpenLocal.AddDate(0, 0, 1)
		dayOpenUTC := dayOpenLocal.UTC()
		dayCloseUTC := dayCloseLocal.UTC()

		// Walk obs in chronological order, building R(t) as we go.
		// At each integer h_remain ∈ [1, 24] we snapshot R(t) and emit
		// Δ = final − R(t).
		var running float64
		init := false
		if variable == "tmax" {
			running = math.Inf(-1)
		} else {
			running = math.Inf(+1)
		}

		// Subset hourly to this civil day (binary-search the entry).
		startIdx := sort.Search(len(hourly), func(i int) bool {
			return !hourly[i].TS.Before(dayOpenUTC)
		})
		obsIdx := startIdx

		// hEmitted tracks which h_remain bins we've already pushed for
		// this day so a multi-obs hour doesn't double-count.
		hEmitted := make(map[int]bool, maxHRemain)

		// Walk forward through the day, advancing obsIdx as time passes.
		// We emit a Δ for each h_remain at the moment we first reach
		// that hour boundary with at least one obs in hand.
		for hRemain := maxHRemain; hRemain >= minHRemain; hRemain-- {
			tBoundary := dayCloseUTC.Add(-time.Duration(hRemain) * time.Hour)

			// Pull in any obs whose timestamp is ≤ tBoundary.
			for obsIdx < len(hourly) && !hourly[obsIdx].TS.After(tBoundary) && hourly[obsIdx].TS.Before(dayCloseUTC) {
				v := hourly[obsIdx].TmpF
				if !math.IsNaN(v) {
					if variable == "tmax" {
						if v > running {
							running = v
						}
					} else {
						if v < running {
							running = v
						}
					}
					init = true
				}
				obsIdx++
			}

			if !init {
				// No observations yet for this day at this hour-boundary.
				// Skip; the kernel doesn't model the cold-start phase.
				continue
			}
			if hEmitted[hRemain] {
				continue
			}
			hEmitted[hRemain] = true
			delta := final - running
			accum.Add(station, variable, hRemain, delta)
		}
		// Only count the (date, variable) pair when it actually
		// contributed Δ samples. A day with truth but no usable hourly
		// obs previously inflated PairsFit, making the summary claim
		// pairs that never reached the kernel.
		if init {
			stSum.PairsFit++
		}
	}
}

// loadTruth parses a CLI truth CSV produced by cmd/calibrate-parse-cli.
// Schema: valid_date,tmax_f,tmin_f,issued_at,bulletin_id (last two
// ignored). Rows with a missing/unparseable temp are skipped silently —
// same best-effort policy as internal/edge.LoadTruth. Column resolution is
// by header NAME via the shared obscsv reader: the previous positional
// rec[0]/rec[1]/rec[2] parse would have silently misread the instant the
// CLI parser reordered its columns.
func loadTruth(path string) (map[string]truthRow, error) {
	rows, err := obscsv.ReadFile(path, obscsv.Options{
		Alias:   obscsv.StrictAliases,
		Require: []string{obscsv.ColDate, obscsv.ColTmax, obscsv.ColTmin},
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make(map[string]truthRow, len(rows))
	for _, row := range rows {
		if !row.TmaxPresent || !row.TminPresent {
			continue
		}
		out[row.Date.Format("2006-01-02")] = truthRow{Tmax: row.Tmax, Tmin: row.Tmin}
	}
	return out, nil
}

// kernelAccumulator collects Δ samples into per-bin online
// (count, mean, M2) triples. Welford's algorithm is used so we never
// hold the raw sample list in memory — important when the fit window
// is 2 years × 4 stations × 2 variables × 24 bins.
type kernelAccumulator struct {
	bins map[string]map[int]*onlineStat
}

func newKernelAccumulator() *kernelAccumulator {
	return &kernelAccumulator{bins: make(map[string]map[int]*onlineStat)}
}

func (a *kernelAccumulator) Add(station, variable string, hRemain int, delta float64) {
	key := bayesian.EntryKey(station, variable)
	bin, ok := a.bins[key]
	if !ok {
		bin = make(map[int]*onlineStat)
		a.bins[key] = bin
	}
	s, ok := bin[hRemain]
	if !ok {
		s = &onlineStat{}
		bin[hRemain] = s
	}
	s.Update(delta)
}

// Finalize produces a Kernel from the accumulator, dropping bins with
// fewer than minN observations (LoadKernel will reject them anyway).
func (a *kernelAccumulator) Finalize(fitWindow string, minN int) bayesian.Kernel {
	out := bayesian.Kernel{
		SchemaVersion: bayesian.KernelSchemaVersion,
		FitWindow:     fitWindow,
		Entries:       make(map[string]map[int]bayesian.Residual, len(a.bins)),
	}
	for outer, bin := range a.bins {
		kept := make(map[int]bayesian.Residual)
		for h, s := range bin {
			if s.N < minN {
				continue
			}
			mean, variance := s.MeanVar()
			kept[h] = bayesian.Residual{
				EDelta:   mean,
				VarDelta: variance,
				N:        s.N,
			}
		}
		if len(kept) == 0 {
			continue
		}
		out.Entries[outer] = kept
	}
	return out
}

// onlineStat is Welford's running mean + variance. Hot enough that
// the alloc-free struct shape matters at fit scale (~140k Δs).
type onlineStat struct {
	N    int
	Mean float64
	M2   float64
}

func (s *onlineStat) Update(x float64) {
	s.N++
	delta := x - s.Mean
	s.Mean += delta / float64(s.N)
	delta2 := x - s.Mean
	s.M2 += delta * delta2
}

// MeanVar returns the sample mean and sample variance (N-1
// denominator). Returns (mean, 0) when N < 2 to avoid divide-by-zero;
// LoadKernel's MinKernelN guard makes that branch unreachable in
// finalized output but it keeps unit tests from panicking on small
// fixtures.
func (s *onlineStat) MeanVar() (float64, float64) {
	if s.N < 2 {
		return s.Mean, 0
	}
	return s.Mean, s.M2 / float64(s.N-1)
}

// writeKernel marshals the kernel and writes via tmp+rename for atomicity.
func writeKernel(path string, k bayesian.Kernel) error {
	return jsonatomic.WriteIndent(path, k)
}

// stationSummary is the per-station fit report.
type stationSummary struct {
	TruthRows      int
	HourlyRows     int
	PairsFit       int // (date, variable) pairs that contributed ≥1 Δ sample
	SkippedNoTruth int
}

// fitSummary is the top-level fit report logged by main().
type fitSummary struct {
	Stations map[string]stationSummary
	OutPath  string
	BinCount int
}

// countBins returns the total number of (station/variable, h_remain)
// entries in the kernel — useful for the summary log.
func countBins(k bayesian.Kernel) int {
	n := 0
	for _, bin := range k.Entries {
		n += len(bin)
	}
	return n
}
