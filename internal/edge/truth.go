package edge

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/obscsv"
)

// DailyTruth is the ground-truth TMAX/TMIN (°F, integer) reported by
// the NWS CLI bulletin for one (station, civil-date). This is what
// Kalshi resolves on; the calibrate-parse-cli pipeline produces these
// per-station CSVs.
type DailyTruth struct {
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
	// Truth is the sole input to the realized-PnL backtest, so this is the
	// best-effort/lenient side of the shared reader: ragged rows are skipped
	// rather than rejected, but a mid-file csv.ParseError still fails loudly
	// (obscsv fail-loud contract) — a malformed byte must not silently
	// truncate the tail of a season and skew every PnL/win-rate number
	// downstream.
	rows, err := obscsv.ReadFile(path, obscsv.Options{
		Alias:   obscsv.EdgeAliases,
		Require: []string{obscsv.ColDate, obscsv.ColTmax, obscsv.ColTmin},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, row := range rows {
		// DailyTruth carries integer temps and rejects non-integer cells
		// (Atoi refuses "70.0") — Kalshi resolves on integer TMAX/TMIN, so a
		// decimal truth cell is a corpus error we skip rather than round.
		tmax, err := strconv.Atoi(row.TmaxRaw)
		if err != nil {
			continue
		}
		tmin, err := strconv.Atoi(row.TminRaw)
		if err != nil {
			continue
		}
		idx.byKey[truthKey(station, row.Date)] = DailyTruth{
			TMaxF: tmax,
			TMinF: tmin,
		}
	}
	return nil
}
