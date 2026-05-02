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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/obs"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

func main() {
	dataDir := flag.String("data", "./data", "root data dir")
	date := flag.String("date", "", "YYYY-MM-DD (scans this date's journals; default: all dates)")
	outPath := flag.String("out", "", "CSV output path (default: stdout)")
	maxGap := flag.Duration("max-gap", 60*time.Minute, "max time gap between Kalshi snapshot and Open-Meteo poll for pairing")
	flag.Parse()

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))

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
		f, err := os.Create(*outPath)
		if err != nil {
			logger.Error("create output", "err", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	w := csv.NewWriter(out)
	defer w.Flush()
	_ = w.Write(edgeCSVHeader())

	var totalRows int
	for _, d := range dates {
		polls, err := loadEnsemblePolls(*dataDir, d)
		if err != nil {
			logger.Warn("load ensemble polls", "date", d, "err", err)
			continue
		}
		if len(polls) == 0 {
			logger.Warn("no ensemble polls", "date", d)
			continue
		}

		books, walls, err := loadBooks(*dataDir, d)
		if err != nil {
			logger.Warn("load books", "date", d, "err", err)
			continue
		}

		for ticker, book := range books {
			row, ok := computeEdge(ticker, book, walls[ticker], polls, *maxGap)
			if !ok {
				continue
			}
			_ = writeEdgeRow(w, d, row)
			totalRows++
		}
	}

	logger.Info("edge-scan complete", "rows", totalRows, "dates", len(dates))
}

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
		if err := json.Unmarshal(env.Payload, &f); err != nil {
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
