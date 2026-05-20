// Command calibrate-bayesian fits the residual kernel used by
// internal/calibration/bayesian: per (station, variable, hours_remaining),
// compute E[Δ] and Var[Δ] where Δ = actual_extremum − running_extremum_at_t.
//
// Algorithm (implemented in fit.go):
//
//  1. Load truth CSV (NWS CLI bulletins → final TMAX/TMIN per
//     (station, civil_date)).
//  2. Load historical hourly obs (IEM ASOS METAR/SPECI in UTC).
//  3. For each (station, civil_date), reconstruct the running max/min
//     trajectory over civil-day hours in the station's local timezone.
//  4. For each h_remain ∈ [1, 24], snapshot R(t) at t = day_close −
//     h_remain hours and compute Δ = final − R(t).
//  5. Aggregate Δ samples by (station, variable, h_remain) with
//     Welford's online algorithm (mean, sample variance).
//  6. Emit a Kernel JSON conforming to the bayesian.KernelSchemaVersion
//     schema, dropping bins with N < bayesian.MinKernelN.
//
// See ADR-0010 for the math and bin-conditioning rationale, and
// internal/calibration/bayesian for the on-disk schema this binary
// emits.
//
// Held-out evaluation: this binary only fits the kernel. The
// Brier-on-holdout gate per ADR-0010's follow-up evaluation harness
// is the next step before wiring into edge-scan; see
// cmd/calibrate-bayesian-eval (planned).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	obsPath := flag.String("obs", "",
		"path to dir with historical hourly ASOS CSVs "+
			"(K<STA>-hourly.csv, e.g. data/calibration/obs/)")
	truthPath := flag.String("truth", "",
		"path to dir with NWS CLI ground-truth CSVs "+
			"(K<STA>.csv, e.g. data/calibration/obs/)")
	outPath := flag.String("out", "",
		"output Kernel JSON path "+
			"(e.g. data/calibration/bayesian-kernel.json)")
	fitStart := flag.String("fit-start", "",
		"first civil date in fit window, inclusive YYYY-MM-DD")
	fitEnd := flag.String("fit-end", "",
		"last civil date in fit window, inclusive YYYY-MM-DD")
	flag.Parse()

	var missing []string
	if *obsPath == "" {
		missing = append(missing, "-obs")
	}
	if *truthPath == "" {
		missing = append(missing, "-truth")
	}
	if *outPath == "" {
		missing = append(missing, "-out")
	}
	if *fitStart == "" {
		missing = append(missing, "-fit-start")
	}
	if *fitEnd == "" {
		missing = append(missing, "-fit-end")
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "calibrate-bayesian: missing required flag(s): %v\n", missing)
		flag.Usage()
		os.Exit(2)
	}

	start, err := time.Parse("2006-01-02", *fitStart)
	if err != nil {
		die(fmt.Errorf("fit-start: %w", err))
	}
	end, err := time.Parse("2006-01-02", *fitEnd)
	if err != nil {
		die(fmt.Errorf("fit-end: %w", err))
	}
	if end.Before(start) {
		die(fmt.Errorf("fit-end %s before fit-start %s", *fitEnd, *fitStart))
	}

	opts := fitOptions{
		ObsDir:    *obsPath,
		TruthDir:  *truthPath,
		OutPath:   *outPath,
		FitStart:  start,
		FitEnd:    end,
		FitWindow: fmt.Sprintf("%s..%s", *fitStart, *fitEnd),
	}

	summary, err := runFit(opts)
	if err != nil {
		die(err)
	}

	fmt.Printf("# calibrate-bayesian %s\n", opts.FitWindow)
	fmt.Printf("kernel:    %s\n", summary.OutPath)
	fmt.Printf("bins:      %d (station/variable × h_remain entries with N ≥ MinKernelN)\n", summary.BinCount)
	fmt.Println("per-station:")
	for code, st := range summary.Stations {
		fmt.Printf("  %s: truth=%d hourly=%d pairs_fit=%d skipped_no_truth=%d\n",
			code, st.TruthRows, st.HourlyRows, st.PairsFit, st.SkippedNoTruth)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "calibrate-bayesian:", err)
	os.Exit(1)
}
