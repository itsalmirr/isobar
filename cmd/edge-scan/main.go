// Command edge-scan walks recorded Phase 0 journals, reconstructs
// orderbooks, pairs each market snapshot to the nearest Open-Meteo
// ensemble poll, and computes fee-aware naive edge per bracket.
//
// This is the Phase 0.5 floor estimate — uncalibrated ensemble fraction
// vs market mid. If naive edge is positive anywhere, calibration only
// helps. If negative everywhere, calibration is load-bearing.
//
// Input:  data/{kalshi,openmeteo}/{DATE}/*.jsonl[.gz]
// Output: CSV to stdout (or --out file)
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/obs"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

func main() {
	dataDir := flag.String("data", "./data", "root data dir")
	date := flag.String("date", "", "YYYY-MM-DD (scans this date's journals; default: all dates)")
	outPath := flag.String("out", "", "CSV output path (default: stdout)")
	maxGap := flag.Duration("max-gap", 60*time.Minute, "max time gap between Kalshi snapshot and Open-Meteo poll for pairing")
	mosPath := flag.String("mos", "", "path to MOS coefficients JSON (from cmd/calibrate-fit); when set, adds calibrated_* columns")
	climDir := flag.String("climate", "", "path to obs CSVs for climate-shrunk baseline (e.g. data/calibration/obs); when set, adds climate_* columns")
	workers := flag.Int("workers", 0, "parallel workers (each processes one UTC date); 0 = runtime.NumCPU()")
	flag.Parse()

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))

	var coeffs *mos.Coefficients
	if *mosPath != "" {
		c, err := loadCoefficients(*mosPath)
		if err != nil {
			logger.Error("load mos coefficients", "path", *mosPath, "err", err)
			os.Exit(1)
		}
		coeffs = c
		logger.Info("mos coefficients loaded", "path", *mosPath, "bins", len(c.Bins))
	}

	var clim *climate.Lookup
	if *climDir != "" {
		clim = climate.New()
		if err := clim.Load(*climDir, market.AllStationCodes()); err != nil {
			logger.Error("load climate", "dir", *climDir, "err", err)
			os.Exit(1)
		}
		logger.Info("climate lookup loaded", "dir", *climDir)
	}

	dates, err := findDates(*dataDir, *date)
	if err != nil {
		logger.Error("find dates", "err", err)
		os.Exit(1)
	}
	if len(dates) == 0 {
		logger.Error("no data found", "dir", *dataDir, "date", *date)
		os.Exit(1)
	}

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath) //nolint:gosec // path is operator-supplied output flag
		if err != nil {
			logger.Error("create output", "err", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	// Schema-version comment first, then CSV header. Downstream parsers
	// must validate the schema line and refuse mismatched versions.
	if _, err := out.Write([]byte(EdgeCSVSchemaComment + "\n")); err != nil {
		logger.Error("write schema header", "err", err)
		os.Exit(1)
	}

	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write(edgeCSVHeader())

	// Parallelize per UTC date — each date is fully independent: it loads
	// its own polls + books, computes edges, and never touches another
	// date's state. We bound concurrency, then write rows back in date
	// order so CSV output stays deterministic across worker counts.
	nWorkers := *workers
	if nWorkers <= 0 {
		nWorkers = runtime.NumCPU()
	}
	if nWorkers > len(dates) {
		nWorkers = len(dates)
	}

	results := make([]dateResult, len(dates))
	sem := make(chan struct{}, nWorkers)
	var wg sync.WaitGroup
	for i, d := range dates {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, date string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = processDate(*dataDir, date, *maxGap, coeffs, clim)
		}(i, d)
	}
	wg.Wait()

	var totalRows, calibratedRows, climateRows int
	for _, r := range results {
		if r.skipReason != "" {
			logger.Warn(r.skipReason, "date", r.date, "err", r.err)
			continue
		}
		for _, row := range r.rows {
			_ = writeEdgeRow(w, r.date, row)
		}
		totalRows += r.totalRows
		calibratedRows += r.calibratedRows
		climateRows += r.climateRows
	}

	logger.Info("edge-scan complete", "rows", totalRows, "calibrated_rows", calibratedRows, "climate_rows", climateRows, "dates", len(dates), "workers", nWorkers)
}

// dateResult holds one worker's per-date output. rows are written by the
// main goroutine in input-date order; skipReason is the log message kind
// when the date couldn't be processed (empty when processing succeeded).
type dateResult struct {
	date           string
	rows           []EdgeRow
	totalRows      int
	calibratedRows int
	climateRows    int
	skipReason     string
	err            error
}

// processDate is the per-date compute kernel run by each worker. It loads
// the date's ensemble polls and reconstructed orderbooks, then computes
// one EdgeRow per (ticker, book-snapshot, paired-poll). All inputs are
// read-only from the worker's perspective: coeffs/clim are loaded once
// and shared by reference; date directories don't overlap.
func processDate(dataDir, date string, maxGap time.Duration, coeffs *mos.Coefficients, clim *climate.Lookup) dateResult {
	out := dateResult{date: date}
	polls, err := loadEnsemblePolls(dataDir, date)
	if err != nil {
		out.skipReason = "load ensemble polls"
		out.err = err
		return out
	}
	if len(polls) == 0 {
		out.skipReason = "no ensemble polls"
		return out
	}
	books, walls, err := loadBooks(dataDir, date)
	if err != nil {
		out.skipReason = "load books"
		out.err = err
		return out
	}
	for ticker, book := range books {
		row, ok := computeEdge(ticker, book, walls[ticker], polls, maxGap, coeffs, clim)
		if !ok {
			continue
		}
		out.rows = append(out.rows, row)
		out.totalRows++
		if !isNaN(row.CalibratedP) {
			out.calibratedRows++
		}
		if !isNaN(row.ClimateShrunkP) {
			out.climateRows++
		}
	}
	return out
}

func loadCoefficients(path string) (*mos.Coefficients, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied flag
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var c mos.Coefficients
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, err
	}
	// Schema-version check: refuse to load coefficients from a different
	// version of the algorithm. Old files with no version field (pre-v1)
	// also fail closed; refit with current binary.
	if c.SchemaVersion != mos.CoefficientsSchemaVersion {
		return nil, fmt.Errorf("mos coefficients schema mismatch: got %q, want %q (refit with current cmd/calibrate-fit)", c.SchemaVersion, mos.CoefficientsSchemaVersion)
	}
	return &c, nil
}

func isNaN(v float64) bool { return v != v }

// findDates returns date directory names (YYYY-MM-DD) under dataDir/kalshi/.
func findDates(dataDir, dateFilter string) ([]string, error) {
	if dateFilter != "" {
		return []string{dateFilter}, nil
	}
	kalshiRoot := filepath.Join(dataDir, "kalshi")
	entries, err := os.ReadDir(kalshiRoot)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 10 {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// loadBooks walks a date's Kalshi journals and returns the end-of-window
// book per ticker plus the wall timestamp of the last book-affecting frame.
func loadBooks(dataDir, date string) (map[string]*replay.Book, map[string]int64, error) {
	files, err := replay.ListJournals(filepath.Join(dataDir, "kalshi", date))
	if err != nil {
		return nil, nil, err
	}
	books := map[string]*replay.Book{}
	walls := map[string]int64{}
	_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		var f replay.KalshiFrame
		if err := sonic.Unmarshal(env.Payload, &f); err != nil {
			return nil //nolint:nilerr // skip malformed frame, don't abort the scan
		}
		replay.ApplyFrameToBooks(books, walls, env, f)
		return nil
	})
	return books, walls, nil
}

// loadEnsemblePolls reads all Open-Meteo journal frames for a date and
// returns parsed polls indexed by location.
func loadEnsemblePolls(dataDir, date string) ([]*pollEntry, error) {
	dir := filepath.Join(dataDir, "openmeteo", date)
	files, err := replay.ListJournals(dir)
	if err != nil {
		return nil, err
	}
	var polls []*pollEntry
	_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		loc := strings.TrimPrefix(env.Kind, "rest:")
		if loc == "" || loc == env.Kind {
			return nil
		}
		poll, err := openmeteo.ParseEnsemblePoll(env.Payload)
		if err != nil {
			return nil //nolint:nilerr // skip malformed poll, don't abort the scan
		}
		poll.Wall = env.Wall
		poll.Location = loc
		polls = append(polls, &pollEntry{poll: poll, loc: loc, wall: env.Wall})
		return nil
	})
	return polls, nil
}
