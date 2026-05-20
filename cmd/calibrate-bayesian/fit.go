package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
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

// stationIANA maps WFO codes to their IANA timezone. Used to convert
// the truth's local civil-date window into a UTC range we can filter
// hourly obs on. Mirrors internal/edge/scan.go: stationLocation; kept
// local here so the fitter doesn't import internal/edge (and to be
// explicit that the four-station registry is the same).
func stationIANA(code string) (string, error) {
	switch code {
	case "KNYC", "KMIA":
		return "America/New_York", nil
	case "KMDW":
		return "America/Chicago", nil
	case "KLAX":
		return "America/Los_Angeles", nil
	}
	return "", fmt.Errorf("station %q has no timezone mapping", code)
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
		loc, err := stationIANA(code)
		if err != nil {
			return sum, fmt.Errorf("station %s: %w", code, err)
		}
		tz, err := time.LoadLocation(loc)
		if err != nil {
			return sum, fmt.Errorf("station %s: load tz %s: %w", code, loc, err)
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
			return !hourly[i].Ts.Before(dayOpenUTC)
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
			for obsIdx < len(hourly) && !hourly[obsIdx].Ts.After(tBoundary) && hourly[obsIdx].Ts.Before(dayCloseUTC) {
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
		stSum.PairsFit++
	}
}

// loadTruth parses a CLI truth CSV produced by cmd/calibrate-parse-cli.
// Schema: valid_date,tmax_f,tmin_f,issued_at,bulletin_id (last two
// ignored). Rows with non-integer temps are skipped silently — same
// best-effort policy as internal/edge.LoadTruth.
func loadTruth(path string) (map[string]truthRow, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied calibration path
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate trailing-col variance
	out := make(map[string]truthRow)
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if first {
			first = false
			continue
		}
		if len(rec) < 3 {
			continue
		}
		date := strings.TrimSpace(rec[0])
		if _, err := time.Parse("2006-01-02", date); err != nil {
			continue
		}
		tmax, err1 := strconv.ParseFloat(strings.TrimSpace(rec[1]), 64)
		tmin, err2 := strconv.ParseFloat(strings.TrimSpace(rec[2]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[date] = truthRow{Tmax: tmax, Tmin: tmin}
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

// writeKernel marshals the kernel and writes via tmp+rename for
// atomicity (matching cmd/calibrate-fit's convention).
func writeKernel(path string, k bayesian.Kernel) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // operator-supplied output path
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(k); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// stationSummary is the per-station fit report.
type stationSummary struct {
	TruthRows      int
	HourlyRows     int
	PairsFit       int // (date, variable) pairs that had truth + ≥1 obs
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
