// Command calibrate-parse-cli reads a per-station NWS CLI bulletin zip and
// writes daily TMAX/TMIN as CSV (the canonical y-target for NGR per
// ADR-0007). A calibration-fitness gap report is printed to stderr.
//
// Input:  data/calibration/cli/K<STA>.zip  (from scripts/calibration-fetch-cli.sh)
// Output: data/calibration/obs/K<STA>.csv  (sorted by valid_date ascending)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wh0o0pz/isobar/internal/calibration/cliparse"
	"github.com/wh0o0pz/isobar/internal/obs"
)

func main() {
	zipPath := flag.String("zip", "", "path to K<STATION>.zip (required)")
	outPath := flag.String("out", "", "CSV output path (default: data/calibration/obs/K<STATION>.csv)")
	start := flag.String("start", "", "window start, YYYY-MM-DD inclusive (required)")
	end := flag.String("end", "", "window end, YYYY-MM-DD inclusive (required)")
	flag.Parse()

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))

	if *zipPath == "" || *start == "" || *end == "" {
		fmt.Fprintln(os.Stderr, "usage: calibrate-parse-cli --zip <path> --start YYYY-MM-DD --end YYYY-MM-DD [--out <path>]")
		os.Exit(2)
	}
	if *outPath == "" {
		*outPath = inferOutPath(*zipPath)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := cliparse.Run(ctx, cliparse.Options{
		ZipPath: *zipPath,
		OutPath: *outPath,
		Start:   *start,
		End:     *end,
		Logger:  logger,
	}); err != nil {
		logger.Error("parse failed", "err", err)
		os.Exit(1)
	}
}

// inferOutPath: data/calibration/cli/KORD.zip → data/calibration/obs/KORD.csv.
func inferOutPath(zipPath string) string {
	base := strings.TrimSuffix(filepath.Base(zipPath), ".zip") + ".csv"
	return filepath.Join("data", "calibration", "obs", base)
}
