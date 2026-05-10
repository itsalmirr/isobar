package edge

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DailyTruth is the ground-truth TMAX/TMIN (°F, integer) reported by
// the NWS CLI bulletin for one (station, civil-date). This is what
// Kalshi resolves on; the calibrate-parse-cli pipeline produces these
// per-station CSVs.
type DailyTruth struct {
	Date  time.Time // UTC midnight on the civil date
	TMaxF int
	TMinF int
}

// TruthIndex is a lookup keyed by (station, civil-date).
type TruthIndex struct {
	byKey map[string]DailyTruth
}

func truthKey(station string, civilDate time.Time) string {
	return station + "|" + civilDate.Format("2006-01-02")
}

// Get returns the truth row for (station, civilDate), or ok=false if
// missing. Civil dates are matched by Y/M/D regardless of timezone.
func (t *TruthIndex) Get(station string, civilDate time.Time) (DailyTruth, bool) {
	if t == nil {
		return DailyTruth{}, false
	}
	v, ok := t.byKey[truthKey(station, civilDate)]
	return v, ok
}

// Len returns the total number of (station, date) rows loaded.
func (t *TruthIndex) Len() int {
	if t == nil {
		return 0
	}
	return len(t.byKey)
}

// LoadTruth reads canonical KSTATION.csv files (output of the
// calibrate-parse-cli parser, per ADR-0007) under dir and returns a
// TruthIndex.
//
// Expected per-file shape (from cmd/calibrate-parse-cli):
//
//	valid_date,tmax_f,tmin_f,issued_at,bulletin_id
//	2024-04-29,70,54,2024-04-30T06:41:00Z,CLIMDW_202404300641.txt
//	...
//
// Only the first three columns are required. Files whose name doesn't
// match KXYZ.csv (where XYZ is 3 letters) are skipped — the *-hourly.csv
// files in the same dir are ASOS-derived feature data, not the canonical
// y-target.
func LoadTruth(dir string) (*TruthIndex, error) {
	idx := &TruthIndex{byKey: map[string]DailyTruth{}}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read truth dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		station, ok := stationFromTruthFilename(name)
		if !ok {
			continue
		}
		path := filepath.Join(dir, name)
		if err := loadTruthFile(idx, station, path); err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
	}
	return idx, nil
}

// stationFromTruthFilename returns the station code for KXYZ.csv files
// (4-letter ICAO with no -hourly suffix), or false otherwise.
func stationFromTruthFilename(name string) (string, bool) {
	if !strings.HasSuffix(name, ".csv") {
		return "", false
	}
	if strings.Contains(name, "-hourly") {
		return "", false
	}
	stem := strings.TrimSuffix(name, ".csv")
	if len(stem) != 4 || stem[0] != 'K' {
		return "", false
	}
	for i := 1; i < 4; i++ {
		c := stem[i]
		if c < 'A' || c > 'Z' {
			return "", false
		}
	}
	return stem, true
}

func loadTruthFile(idx *TruthIndex, station, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate trailing columns

	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	dateCol, maxCol, minCol := -1, -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "valid_date", "date":
			dateCol = i
		case "tmax_f", "tmax":
			maxCol = i
		case "tmin_f", "tmin":
			minCol = i
		}
	}
	if dateCol < 0 || maxCol < 0 || minCol < 0 {
		return fmt.Errorf("missing required columns in %s (header=%v)", path, header)
	}

	for lineNo := 2; ; lineNo++ {
		row, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Surface any non-EOF read failure (malformed quoting,
			// inconsistent column counts under FieldsPerRecord != -1, etc.)
			// — silently truncating truth produces stealthy "missing
			// outcomes" downstream and is worse than failing the load.
			return fmt.Errorf("%s line %d: %w", path, lineNo, err)
		}
		date, err := time.Parse("2006-01-02", strings.TrimSpace(row[dateCol]))
		if err != nil {
			continue
		}
		tmax, err := strconv.Atoi(strings.TrimSpace(row[maxCol]))
		if err != nil {
			continue
		}
		tmin, err := strconv.Atoi(strings.TrimSpace(row[minCol]))
		if err != nil {
			continue
		}
		idx.byKey[truthKey(station, date)] = DailyTruth{
			Date:  date.UTC(),
			TMaxF: tmax,
			TMinF: tmin,
		}
	}
	return nil
}
