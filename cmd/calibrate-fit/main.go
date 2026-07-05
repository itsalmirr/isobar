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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/calibration/obscsv"
	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/jsonatomic"
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

// forecastAliases projects the forecast CSV's gfs_* columns onto the
// canonical obscsv Row: Row.Tmax/Tmin carry the GFS deterministic
// forecast high/low rather than observations.
var forecastAliases = obscsv.Aliases{
	obscsv.ColDate: {"valid_date"},
	obscsv.ColTmax: {"gfs_tmax_f"},
	obscsv.ColTmin: {"gfs_tmin_f"},
}

func readForecastCSV(path string) ([]forecastRow, error) {
	rows, err := obscsv.ReadFile(path, obscsv.Options{
		Alias:      forecastAliases,
		Require:    []string{obscsv.ColDate, obscsv.ColTmax, obscsv.ColTmin},
		StrictRows: true,
	})
	if err != nil {
		return nil, fmt.Errorf("forecast %s: %w", path, err)
	}
	out := make([]forecastRow, 0, len(rows))
	for _, row := range rows {
		// A row missing either forecast temp carries no usable pair; skip it
		// (same policy as the previous both-or-nothing ParseFloat parse).
		if !row.TmaxPresent || !row.TminPresent {
			continue
		}
		out = append(out, forecastRow{Date: row.Date, TmaxF: row.Tmax, TminF: row.Tmin})
	}
	return out, nil
}

func readObsCSV(path string) ([]obsRow, error) {
	rows, err := obscsv.ReadFile(path, obscsv.Options{
		Alias:      obscsv.StrictAliases,
		Require:    []string{obscsv.ColDate, obscsv.ColTmax, obscsv.ColTmin},
		StrictRows: true,
	})
	if err != nil {
		return nil, fmt.Errorf("obs %s: %w", path, err)
	}
	out := make([]obsRow, 0, len(rows))
	for _, row := range rows {
		// nil iff the source cell was empty/unparseable — pairByDate treats a
		// nil temperature as "no observation" and drops that (date, variable).
		o := obsRow{Date: row.Date}
		if row.TmaxPresent {
			v := row.Tmax
			o.TmaxF = &v
		}
		if row.TminPresent {
			v := row.Tmin
			o.TminF = &v
		}
		out = append(out, o)
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

func writeJSON(path string, c mos.Coefficients) error {
	return jsonatomic.WriteIndent(path, c)
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
