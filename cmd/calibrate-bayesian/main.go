// Command calibrate-bayesian fits the residual kernel used by
// internal/calibration/bayesian: per (station, variable, hours_remaining),
// compute E[Δ] and Var[Δ] where Δ = actual_extremum − running_extremum_at_t.
//
// Algorithm (TODO — implementation follows in a separate commit):
//
//  1. Load truth CSV (NWS CLI bulletins → final TMAX/TMIN per
//     (station, civil_date)).
//  2. Load historical hourly obs (Open-Meteo historical-forecast-api
//     `hourly_2m_temperature`, or METAR/ASOS upgrade later).
//  3. For each (station, civil_date), reconstruct the running max/min
//     trajectory over civil-day hours in the station's local timezone.
//  4. For each hour t ∈ [day_open, day_close], compute R(t) and Δ =
//     final − R(t).
//  5. Group by (station, variable, hours_remaining = day_close − t),
//     compute mean (E[Δ]) and sample variance (Var[Δ]).
//  6. Write a Kernel JSON conforming to the bayesian.KernelSchemaVersion
//     schema.
//
// See ADR-0010 for the math and bin-conditioning rationale, and
// internal/calibration/bayesian for the on-disk schema this binary
// emits.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	obsPath := flag.String("obs", "",
		"path or dir with historical hourly temperature observations "+
			"(e.g. data/calibration/openmeteo-hourly/)")
	truthPath := flag.String("truth", "",
		"path or dir with NWS CLI ground-truth CSVs "+
			"(e.g. data/calibration/obs/)")
	outPath := flag.String("out", "",
		"output Kernel JSON path "+
			"(e.g. data/calibration/bayesian-kernel.json)")
	fitStart := flag.String("fit-start", "",
		"first civil date in fit window, inclusive YYYY-MM-DD")
	fitEnd := flag.String("fit-end", "",
		"last civil date in fit window, inclusive YYYY-MM-DD")
	flag.Parse()

	// Required-flag validation. Empty values fail loudly so the build
	// path (`bin/calibrate-bayesian` with no args) doesn't silently
	// produce an empty kernel or write to "".
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

	// Scaffold path: print intent and exit. The fitting pipeline lands
	// in a follow-up commit gated on ADR-0011's evaluation plan.
	fmt.Fprintln(os.Stderr, "calibrate-bayesian: not yet implemented (ADR-0010 scaffold)")
	fmt.Fprintf(os.Stderr, "  obs:       %s\n", *obsPath)
	fmt.Fprintf(os.Stderr, "  truth:     %s\n", *truthPath)
	fmt.Fprintf(os.Stderr, "  out:       %s\n", *outPath)
	fmt.Fprintf(os.Stderr, "  fit-start: %s\n", *fitStart)
	fmt.Fprintf(os.Stderr, "  fit-end:   %s\n", *fitEnd)
}
