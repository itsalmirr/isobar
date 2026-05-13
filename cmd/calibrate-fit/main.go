// Command calibrate-fit pairs CLI ground-truth daily TMAX/TMIN with
// historical GFS deterministic forecasts, fits MOS bias correction per
// (station, season, variable), and writes coefficients to JSON.
//
// Inputs:
//
//	data/calibration/forecast/K{STA}.csv (gfs_tmax_f, gfs_tmin_f)
//	data/calibration/obs/K{STA}.csv      (tmax_f,     tmin_f)
//
// Output: data/calibration/mos-coefficients.json
//
// See ADR-0008 for the deterministic-only first-pass scope.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/obs"
)

func main() {
	forecastDir := flag.String("forecast-dir", "data/calibration/forecast", "directory of K{STA}.csv forecasts")
	obsDir := flag.String("obs-dir", "data/calibration/obs", "directory of K{STA}.csv observations")
	outPath := flag.String("out", "data/calibration/mos-coefficients.json", "output JSON path")
	holdoutDays := flag.Int("holdout-days", 90, "trailing N days of pairs reserved for OOS holdout (not used in fit)")
	flag.Parse()

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pairs, err := loadAllPairs(ctx, *forecastDir, *obsDir, logger)
	if err != nil {
		logger.Error("load pairs", "err", err)
		os.Exit(1)
	}
	logger.Info("loaded pairs", "n", len(pairs))

	coeffs, err := mos.Fit(pairs, *holdoutDays, clock.Wall())
	if err != nil {
		logger.Error("fit", "err", err)
		os.Exit(1)
	}
	// Current calibration sources GFS deterministic forecasts only (see
	// readForecastCSV: it reads gfs_tmax_f / gfs_tmin_f columns). Stamp
	// the model set so the scanner can refuse to apply these coefficients
	// to a multi-model poll without us having to remember.
	coeffs.FitModelSet = []string{mos.ModelGFS}

	if err := writeJSON(*outPath, coeffs); err != nil {
		logger.Error("write", "err", err)
		os.Exit(1)
	}

	printSummary(coeffs)
	logger.Info("wrote MOS coefficients", "path", *outPath, "bins", len(coeffs.Bins))
}

// loadAllPairs reads every (station, variable) forecast/observed CSV pair
// in the calibration tree and returns them as a flat slice of mos.Pair.
// Days where either side is missing are dropped silently.
func loadAllPairs(ctx context.Context, fcDir, obsDir string, logger interface{ Info(string, ...any) }) ([]mos.Pair, error) {
	var all []mos.Pair
	for _, sta := range market.AllStationCodes() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fc, err := readForecastCSV(filepath.Join(fcDir, sta+".csv"))
		if err != nil {
			return nil, fmt.Errorf("forecast %s: %w", sta, err)
		}
		obs, err := readObsCSV(filepath.Join(obsDir, sta+".csv"))
		if err != nil {
			return nil, fmt.Errorf("obs %s: %w", sta, err)
		}
		paired := pairByDate(sta, fc, obs)
		logger.Info("paired station", "station", sta, "fc_rows", len(fc), "obs_rows", len(obs), "paired", len(paired))
		all = append(all, paired...)
	}
	return all, nil
}

type forecastRow struct {
	Date         time.Time
	TmaxF, TminF float64
}

type obsRow struct {
	Date         time.Time
	TmaxF, TminF *float64 // nil iff source CSV had empty cell
}

func readForecastCSV(path string) ([]forecastRow, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied calibration dir
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	col, err := requireColumns(header, "valid_date", "gfs_tmax_f", "gfs_tmin_f")
	if err != nil {
		return nil, fmt.Errorf("forecast %s: %w", path, err)
	}
	minCols := maxIndex(col) + 1
	out := []forecastRow{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) < minCols {
			return nil, fmt.Errorf("forecast %s: short record (%d cols, need %d)", path, len(rec), minCols)
		}
		d, err := time.Parse("2006-01-02", rec[col["valid_date"]])
		if err != nil {
			continue
		}
		hi, err1 := strconv.ParseFloat(rec[col["gfs_tmax_f"]], 64)
		lo, err2 := strconv.ParseFloat(rec[col["gfs_tmin_f"]], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, forecastRow{Date: d, TmaxF: hi, TminF: lo})
	}
	return out, nil
}

func readObsCSV(path string) ([]obsRow, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied calibration dir
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	col, err := requireColumns(header, "valid_date", "tmax_f", "tmin_f")
	if err != nil {
		return nil, fmt.Errorf("obs %s: %w", path, err)
	}
	minCols := maxIndex(col) + 1
	out := []obsRow{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) < minCols {
			return nil, fmt.Errorf("obs %s: short record (%d cols, need %d)", path, len(rec), minCols)
		}
		d, err := time.Parse("2006-01-02", rec[col["valid_date"]])
		if err != nil {
			continue
		}
		row := obsRow{Date: d}
		if s := rec[col["tmax_f"]]; s != "" {
			if v, e := strconv.ParseFloat(s, 64); e == nil {
				row.TmaxF = &v
			}
		}
		if s := rec[col["tmin_f"]]; s != "" {
			if v, e := strconv.ParseFloat(s, 64); e == nil {
				row.TminF = &v
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// pairByDate joins forecast and observation rows by date for one station,
// emitting one mos.Pair per (date, variable) where both sides have data.
func pairByDate(station string, fc []forecastRow, ob []obsRow) []mos.Pair {
	obByDate := make(map[string]obsRow, len(ob))
	for _, o := range ob {
		obByDate[o.Date.Format("2006-01-02")] = o
	}
	var out []mos.Pair
	for _, f := range fc {
		key := f.Date.Format("2006-01-02")
		o, ok := obByDate[key]
		if !ok {
			continue
		}
		if o.TmaxF != nil {
			out = append(out, mos.Pair{
				Station: station, ValidDate: f.Date, Variable: "tmax",
				Forecast: f.TmaxF, Observed: *o.TmaxF,
			})
		}
		if o.TminF != nil {
			out = append(out, mos.Pair{
				Station: station, ValidDate: f.Date, Variable: "tmin",
				Forecast: f.TminF, Observed: *o.TminF,
			})
		}
	}
	return out
}

// requireColumns returns a header→index map containing exactly the
// requested columns, or an error naming any that are missing. This
// closes the latent bug where map[string]int returned 0 (zero value)
// for absent keys, silently reading column 0 instead of failing loudly.
func requireColumns(header []string, names ...string) (map[string]int, error) {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	out := make(map[string]int, len(names))
	var missing []string
	for _, n := range names {
		i, ok := idx[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		out[n] = i
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required columns: %v (got header %v)", missing, header)
	}
	return out, nil
}

// maxIndex returns the largest column index in the map. Used to validate
// that records have at least that many fields.
func maxIndex(col map[string]int) int {
	m := -1
	for _, i := range col {
		if i > m {
			m = i
		}
	}
	return m
}

func writeJSON(path string, c mos.Coefficients) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // path is operator-supplied output flag
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printSummary(c mos.Coefficients) {
	fmt.Println()
	fmt.Println("MOS coefficients")
	fmt.Printf("  fitted_at: %s\n", c.FittedAt.Format(time.RFC3339))
	fmt.Printf("  training window ends: %s\n", c.TrainingWindowEnd)
	fmt.Printf("  holdout window starts: %s\n", c.HoldoutWindowStart)
	fmt.Println()
	fmt.Printf("  %-20s  %6s  %6s  %6s  %6s  %6s\n", "bin", "n_train", "n_hold", "a", "b", "sigma")
	fmt.Println("  " + dashes(60))
	for _, k := range c.SortedBinKeys() {
		bc := c.Bins[k]
		fmt.Printf("  %-20s  %6d  %6d  %+6.2f  %+6.3f  %6.2f\n",
			k, bc.NTrain, bc.NHoldout, bc.A, bc.B, bc.Sigma)
	}
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}
