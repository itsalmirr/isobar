// Command edge-scan walks a recorded isobar tape, pairs every Kalshi
// trade event with the most-recent Open-Meteo poll, computes naive p_fair
// plus optional MOS and climate-shrunk baselines for each weather-bracket
// trade, and emits a per-trade edge CSV plus a summary report.
//
// This is the Phase 0.5 verification path: no ECMWF blend and no T−3h Bayes.
// MOS and climate-shrunk probabilities are explicit columns so downstream
// reports can compare each baseline against market-implied probability.
//
// Output goes under <data>/calibration/edge-scan/ by default.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/edge"
	"github.com/wh0o0pz/isobar/internal/market"
)

func main() {
	dataDir := flag.String("data", "./data", "data root containing kalshi/ and openmeteo/")
	outDir := flag.String("out", "", "output dir (default: <data>/calibration/edge-scan)")
	startStr := flag.String("start", "", "first date to scan, inclusive YYYY-MM-DD (UTC)")
	endStr := flag.String("end", "", "last date to scan, inclusive YYYY-MM-DD (UTC)")
	maxAgeStr := flag.String("max-poll-age", "1h", "max staleness between trade and paired poll")
	truthDir := flag.String("truth", "", "(optional) dir with KSTATION.csv ground-truth files; "+
		"enables realized-PnL backtest. Trades with no truth row are skipped.")
	edgeThresh := flag.Int("edge-threshold-cents", 5,
		"min naive edge (post-fee, cents) required to enter a hypothetical trade")
	mosPath := flag.String("mos", "",
		"(optional) path to MOS coefficients JSON; when set p_fair uses the "+
			"calibrated estimator instead of naive ensemble-fraction")
	climateDir := flag.String("climate", "",
		"(optional) dir with KSTATION.csv observation history; emits climate-shrunk baseline columns")
	bayesKernelPath := flag.String("bayesian-kernel", "",
		"(optional) path to a Bayesian kernel JSON produced by cmd/calibrate-bayesian. "+
			"When set together with -hourly-obs, p_fair switches from the MOS prior to "+
			"a precision-weighted posterior that folds in the running extremum at trade time (ADR-0010).")
	hourlyObsDir := flag.String("hourly-obs", "",
		"(optional) dir with K<STA>-hourly.csv ASOS observations (UTC timestamps); "+
			"required when -bayesian-kernel is set")
	entryFilter := flag.String("entry-filter", "",
		"(optional) named entry-decision filter beyond the edge threshold. "+
			"Currently 'h4' (Phase 0.5 v4 finding: exclude KXHIGHNY; require "+
			"low-temp bracket OR extreme-OTM price band). Empty = no filter.")
	makerFees := flag.Bool("maker-fees", false,
		"if set, score every trade with the Kalshi MAKER fee schedule "+
			"(1/4 of taker, per the published rate) instead of the default "+
			"taker schedule. This is a fee-elimination upper-bound diagnostic; "+
			"the backtest does not simulate maker fill probability. See "+
			"docs/BACKTEST.md \"Pitfalls\" for the model limits.")
	flag.Parse()

	if *startStr == "" || *endStr == "" {
		fmt.Fprintln(os.Stderr, "edge-scan: -start and -end required")
		flag.Usage()
		os.Exit(2)
	}
	start, err := time.Parse("2006-01-02", *startStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "edge-scan: bad -start %q: %v\n", *startStr, err)
		os.Exit(2)
	}
	end, err := time.Parse("2006-01-02", *endStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "edge-scan: bad -end %q: %v\n", *endStr, err)
		os.Exit(2)
	}
	maxAge, err := time.ParseDuration(*maxAgeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "edge-scan: bad -max-poll-age %q: %v\n", *maxAgeStr, err)
		os.Exit(2)
	}

	if *outDir == "" {
		*outDir = filepath.Join(*dataDir, "calibration", "edge-scan")
	}
	if mkdirErr := os.MkdirAll(*outDir, 0o750); mkdirErr != nil {
		fmt.Fprintf(os.Stderr, "edge-scan: mkdir %s: %v\n", *outDir, mkdirErr)
		os.Exit(1)
	}

	feeMode := edge.FeeTaker
	if *makerFees {
		feeMode = edge.FeeMaker
	}
	scanOpts := edge.ScanOptions{
		DataDir:            *dataDir,
		StartDate:          start,
		EndDate:            end.AddDate(0, 0, 1), // make end inclusive on the CLI
		MaxPollAge:         maxAge,
		EdgeThresholdCents: *edgeThresh,
		EntryFilter:        *entryFilter,
		FeeMode:            feeMode,
	}
	if *truthDir != "" {
		truth, loadErr := edge.LoadTruth(*truthDir)
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load truth:", loadErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "loaded truth: %d (station,date) rows from %s\n", truth.Len(), *truthDir)
		scanOpts.Truth = truth
	}
	if *mosPath != "" {
		coeffs, loadErr := mos.LoadCoefficients(*mosPath)
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load mos:", loadErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "loaded MOS coefficients: %d bins from %s (fitted %s)\n",
			len(coeffs.Bins), *mosPath, coeffs.FittedAt.Format(time.RFC3339))
		scanOpts.MOS = &coeffs
	}
	if *climateDir != "" {
		clim := climate.New()
		if loadErr := clim.Load(*climateDir, market.AllStationCodes()); loadErr != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load climate:", loadErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "loaded climate observations from %s\n", *climateDir)
		scanOpts.Climate = clim
	}
	if *bayesKernelPath != "" {
		if *hourlyObsDir == "" {
			fmt.Fprintln(os.Stderr, "edge-scan: -bayesian-kernel requires -hourly-obs")
			os.Exit(2)
		}
		kernel, loadErr := bayesian.LoadKernel(*bayesKernelPath)
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load bayesian kernel:", loadErr)
			os.Exit(1)
		}
		idx, loadErr := bayesian.LoadHourlyIndex(*hourlyObsDir)
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load hourly obs:", loadErr)
			os.Exit(1)
		}
		nBins := 0
		for _, bin := range kernel.Entries {
			nBins += len(bin)
		}
		fmt.Fprintf(os.Stderr, "loaded Bayesian kernel: %d bins (window=%s) from %s\n",
			nBins, kernel.FitWindow, *bayesKernelPath)
		fmt.Fprintf(os.Stderr, "loaded hourly obs index from %s\n", *hourlyObsDir)
		scanOpts.BayesianKernel = kernel
		scanOpts.HourlyObs = idx
	}

	res, err := edge.Scan(scanOpts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "edge-scan:", err)
		os.Exit(1)
	}

	csvPath := filepath.Join(*outDir, fmt.Sprintf("edges-%s_%s.csv", *startStr, *endStr))
	if err := writeCSV(csvPath, res.Rows); err != nil {
		fmt.Fprintln(os.Stderr, "edge-scan: write csv:", err)
		os.Exit(1)
	}
	mdPath := filepath.Join(*outDir, fmt.Sprintf("summary-%s_%s.md", *startStr, *endStr))
	if err := writeSummary(mdPath, *startStr, *endStr, res); err != nil {
		fmt.Fprintln(os.Stderr, "edge-scan: write summary:", err)
		os.Exit(1)
	}

	fmt.Printf("# edge-scan %s..%s\n", *startStr, *endStr)
	fmt.Printf("fee mode:           %s\n", res.FeeMode)
	if res.EntryFilterName != "" {
		fmt.Printf("entry filter:       %s\n", res.EntryFilterName)
	}
	fmt.Printf("frames seen:        %d\n", res.TotalFrames)
	fmt.Printf("trade frames:       %d\n", res.TradeFrames)
	fmt.Printf("weather trades:     %d\n", res.WeatherTrades)
	fmt.Printf("paired trades:      %d  (%.1f%% of weather)\n",
		res.PairedTrades, pct(res.PairedTrades, res.WeatherTrades))
	if res.MOSEnabled {
		fmt.Printf("MOS-calibrated:     %d  (%.1f%% of paired; rest fell back to naive)\n",
			res.MOSApplied, pct(res.MOSApplied, res.PairedTrades))
	}
	if res.ClimateEnabled {
		fmt.Printf("climate-shrunk:     %d  (%.1f%% of paired)\n",
			res.ClimateApplied, pct(res.ClimateApplied, res.PairedTrades))
	}
	if res.BayesEnabled {
		fmt.Printf("Bayesian-updated:   %d  (%.1f%% of paired; rest fell back to MOS/naive)\n",
			res.BayesApplied, pct(res.BayesApplied, res.PairedTrades))
	}
	if res.BacktestEnabled {
		fmt.Printf("\n# realized-PnL backtest (entry threshold = %d¢ post-fee)\n", *edgeThresh)
		fmt.Printf("trades entered:     %d  (%.1f%% of paired)\n",
			res.TradesEntered, pct(res.TradesEntered, res.PairedTrades))
		fmt.Printf("trades won:         %d  (%.1f%% win rate)\n",
			res.TradesWon, pct(res.TradesWon, res.TradesEntered))
		fmt.Printf("total realized PnL: %+d¢  ($%+.2f)\n",
			res.TotalPnLCents, float64(res.TotalPnLCents)/100)
	}
	fmt.Printf("\noutputs:\n  %s\n  %s\n", csvPath, mdPath)
}

func writeCSV(path string, rows []edge.EdgeRow) error {
	f, err := os.Create(path) //nolint:gosec // path is operator-supplied output flag
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)

	if err := w.Write([]string{
		"ts_ms", "ticker", "series", "valid_date", "bracket_kind", "bracket_key",
		"side", "trade_price_cents",
		"p_fair_naive", "p_fair_mos", "p_fair", "mos_applied",
		"climate_p", "p_fair_climate_shrunk", "climate_applied", "edge_climate_shrunk_cents",
		"p_fair_bayes", "bayes_applied", "bayes_skip_reason",
		"bayes_running_temp_f", "bayes_h_remain", "bayes_obs_weight",
		"n_members", "n_members_in_bkt",
		"poll_age_s", "fee_cents", "edge_cents",
		"has_truth", "actual_temp_f", "yes_resolved", "our_side", "our_pnl_cents",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		actual := ""
		if r.HasTruth {
			actual = strconv.Itoa(r.ActualTempF)
		}
		edgeClimate := ""
		if r.ClimateApplied {
			edgeClimate = strconv.Itoa(r.EdgeClimateCents)
		}
		bayesRunning := ""
		bayesObsW := ""
		bayesHRem := ""
		if r.BayesApplied {
			bayesRunning = strconv.FormatFloat(r.BayesRunningTempF, 'f', 2, 64)
			bayesObsW = strconv.FormatFloat(r.BayesObsWeight, 'f', 4, 64)
			bayesHRem = strconv.Itoa(r.BayesHRemain)
		}
		if err := w.Write([]string{
			strconv.FormatInt(r.TsMs, 10),
			r.Ticker,
			r.Series,
			r.ValidDate.Format("2006-01-02"),
			r.BracketKind,
			strconv.FormatFloat(r.BracketKey, 'f', -1, 64),
			r.Side,
			strconv.Itoa(r.TradePriceCents),
			strconv.FormatFloat(r.PFairNaive, 'f', 4, 64),
			strconv.FormatFloat(r.PFairMOS, 'f', 4, 64),
			strconv.FormatFloat(r.PFair, 'f', 4, 64),
			boolStr(r.MOSApplied),
			formatFloatOrBlank(r.ClimateP, 4),
			formatFloatOrBlank(r.PFairClimate, 4),
			boolStr(r.ClimateApplied),
			edgeClimate,
			strconv.FormatFloat(r.PFairBayes, 'f', 4, 64),
			boolStr(r.BayesApplied),
			r.BayesSkipReason,
			bayesRunning,
			bayesHRem,
			bayesObsW,
			strconv.Itoa(r.NMembers),
			strconv.Itoa(r.NMembersInBkt),
			strconv.Itoa(r.PollAgeS),
			strconv.Itoa(r.FeeCents),
			strconv.Itoa(r.EdgeCents),
			boolStr(r.HasTruth),
			actual,
			boolStr(r.YesResolved),
			r.OurSide,
			strconv.Itoa(r.OurPnLCents),
		}); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeSummary(path, startStr, endStr string, res *edge.Result) error {
	f, err := os.Create(path) //nolint:gosec // path is operator-supplied output flag
	if err != nil {
		return err
	}

	w := &summaryWriter{w: f}
	w.printf("# edge-scan summary — %s..%s\n\n", startStr, endStr)
	w.printf("## Run configuration\n\n")
	w.printf("- fee mode:           **%s** (`KalshiTakerFeeBPS=0.07` / `KalshiMakerFeeBPS=0.0175`)\n", res.FeeMode)
	if res.EntryFilterName != "" {
		w.printf("- entry filter:       **%s**\n", res.EntryFilterName)
	} else {
		w.printf("- entry filter:       _none_ (no further restriction beyond edge threshold)\n")
	}
	w.printf("\n## Counts\n\n")
	w.printf("- frames seen:        %d\n", res.TotalFrames)
	w.printf("- trade frames:       %d\n", res.TradeFrames)
	w.printf("- weather trades:     %d\n", res.WeatherTrades)
	w.printf("- paired trades:      %d  (%.1f%% of weather)\n",
		res.PairedTrades, pct(res.PairedTrades, res.WeatherTrades))
	if res.MOSEnabled {
		w.printf("- MOS-calibrated:     %d  (%.1f%% of paired; rest fell back to naive)\n",
			res.MOSApplied, pct(res.MOSApplied, res.PairedTrades))
	}
	if res.ClimateEnabled {
		w.printf("- climate-shrunk:     %d  (%.1f%% of paired)\n",
			res.ClimateApplied, pct(res.ClimateApplied, res.PairedTrades))
	}
	if res.BayesEnabled {
		w.printf("- Bayesian-updated:   %d  (%.1f%% of paired; rest fell back to MOS/naive)\n",
			res.BayesApplied, pct(res.BayesApplied, res.PairedTrades))
	}

	// Realized PnL section — only present when truth was loaded.
	if res.BacktestEnabled {
		w.printf("\n## Realized PnL backtest\n\n")
		w.printf("- trades entered:    %d\n", res.TradesEntered)
		w.printf("- trades won:        %d\n", res.TradesWon)
		if res.TradesEntered > 0 {
			w.printf("- win rate:          %.1f%%\n", pct(res.TradesWon, res.TradesEntered))
			w.printf("- total realized:    %+d¢  ($%+.2f)\n",
				res.TotalPnLCents, float64(res.TotalPnLCents)/100)
			w.printf("- avg PnL per trade: %+.2f¢\n",
				float64(res.TotalPnLCents)/float64(res.TradesEntered))
		} else {
			w.println("- _no trades cleared the entry threshold — naive p_fair offers no edge band_")
		}

		// Daily PnL breakdown.
		dayPnL := map[string]int{}
		dayTrades := map[string]int{}
		dayWins := map[string]int{}
		for _, r := range res.Rows {
			if r.OurSide == "" {
				continue
			}
			d := r.ValidDate.Format("2006-01-02")
			dayPnL[d] += r.OurPnLCents
			dayTrades[d]++
			if r.OurPnLCents > 0 {
				dayWins[d]++
			}
		}
		if len(dayPnL) > 0 {
			w.printf("\n### Daily PnL (by bracket valid_date)\n\n")
			w.println("| valid_date | trades | wins | win_rate | pnl_cents | pnl_$ |")
			w.println("|---|---:|---:|---:|---:|---:|")
			days := make([]string, 0, len(dayPnL))
			for d := range dayPnL {
				days = append(days, d)
			}
			sort.Strings(days)
			for _, d := range days {
				w.printf("| %s | %d | %d | %.1f%% | %+d | $%+.2f |\n",
					d, dayTrades[d], dayWins[d], pct(dayWins[d], dayTrades[d]),
					dayPnL[d], float64(dayPnL[d])/100)
			}
		}
	}
	w.printf("\n## Skipped reasons\n\n")
	if len(res.SkippedReason) == 0 {
		w.println("_(none — every weather trade was paired)_")
	} else {
		for _, r := range sortedKeys(res.SkippedReason) {
			w.printf("- %-30s  %d\n", r, res.SkippedReason[r])
		}
	}

	// Edge histogram in 1¢ buckets.
	hist := map[int]int{}
	for _, r := range res.Rows {
		hist[r.EdgeCents]++
	}
	keys := make([]int, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	w.printf("\n## Edge distribution (cents)\n\n| edge_cents | trades |\n|---:|---:|\n")
	for _, k := range keys {
		w.printf("| %4d | %6d |\n", k, hist[k])
	}

	// Trades with edge ≥ 5¢ post-fee, by bracket.
	type bktTotal struct {
		key   string
		count int
	}
	bkt := map[string]int{}
	for _, r := range res.Rows {
		if r.EdgeCents >= 5 {
			bkt[r.Ticker]++
		}
	}
	var bktList []bktTotal
	for k, v := range bkt {
		bktList = append(bktList, bktTotal{k, v})
	}
	sort.Slice(bktList, func(i, j int) bool { return bktList[i].count > bktList[j].count })
	w.printf("\n## Top brackets with edge ≥ 5¢ post-fee\n\n")
	if len(bktList) == 0 {
		w.println("_(no trade has positive edge ≥ 5¢ — naive p_fair offers no edge band over fees)_")
	} else {
		w.println("| ticker | trades |\n|---|---:|")
		for i, bt := range bktList {
			if i >= 30 {
				break
			}
			w.printf("| %s | %d |\n", bt.key, bt.count)
		}
	}
	if w.err != nil {
		_ = f.Close()
		return w.err
	}
	return f.Close()
}

type summaryWriter struct {
	w   io.Writer
	err error
}

func (w *summaryWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.w, format, args...)
}

func (w *summaryWriter) println(args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.w, args...)
}

func pct(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return 100 * float64(num) / float64(den)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func formatFloatOrBlank(v float64, prec int) string {
	if math.IsNaN(v) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}
