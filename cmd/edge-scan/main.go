// Command edge-scan walks a recorded isobar tape, pairs every Kalshi
// trade event with the most-recent Open-Meteo poll, computes a *naive*
// p_fair = ensemble_member_fraction for each weather-bracket trade, and
// emits a per-trade edge CSV plus a summary report.
//
// This is the Phase 0.5 lower bound: no calibration, no ECMWF blend,
// no T−3h Bayes. If positive expected-value trades exist at this
// floor, calibration improves on it. If they don't, calibration is
// the only path to having any.
//
// Output goes under <data>/calibration/edge-scan/ by default.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/wh0o0pz/isobar/internal/edge"
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
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "edge-scan: mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}

	scanOpts := edge.ScanOptions{
		DataDir:            *dataDir,
		StartDate:          start,
		EndDate:            end.AddDate(0, 0, 1), // make end inclusive on the CLI
		MaxPollAge:         maxAge,
		EdgeThresholdCents: *edgeThresh,
	}
	if *truthDir != "" {
		truth, err := edge.LoadTruth(*truthDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "edge-scan: load truth:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "loaded truth: %d (station,date) rows from %s\n", truth.Len(), *truthDir)
		scanOpts.Truth = truth
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
	if err := writeSummary(mdPath, *startStr, *endStr, *edgeThresh, res); err != nil {
		fmt.Fprintln(os.Stderr, "edge-scan: write summary:", err)
		os.Exit(1)
	}

	fmt.Printf("# edge-scan %s..%s\n", *startStr, *endStr)
	fmt.Printf("frames seen:        %d\n", res.TotalFrames)
	fmt.Printf("trade frames:       %d\n", res.TradeFrames)
	fmt.Printf("weather trades:     %d\n", res.WeatherTrades)
	fmt.Printf("paired trades:      %d  (%.1f%% of weather)\n",
		res.PairedTrades, pct(res.PairedTrades, res.WeatherTrades))
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
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{
		"ts_ms", "ticker", "series", "valid_date", "bracket_kind", "bracket_key",
		"side", "trade_price_cents", "p_fair_naive", "n_members", "n_members_in_bkt",
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
	return w.Error()
}

func writeSummary(path, startStr, endStr string, edgeThresholdCents int, res *edge.Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# edge-scan summary — %s..%s\n\n", startStr, endStr)
	fmt.Fprintf(f, "## Counts\n\n")
	fmt.Fprintf(f, "- frames seen:        %d\n", res.TotalFrames)
	fmt.Fprintf(f, "- trade frames:       %d\n", res.TradeFrames)
	fmt.Fprintf(f, "- weather trades:     %d\n", res.WeatherTrades)
	fmt.Fprintf(f, "- paired trades:      %d  (%.1f%% of weather)\n",
		res.PairedTrades, pct(res.PairedTrades, res.WeatherTrades))

	// Realized PnL section — only present when truth was loaded.
	if res.BacktestEnabled {
		fmt.Fprintf(f, "\n## Realized PnL backtest\n\n")
		fmt.Fprintf(f, "- trades entered:    %d\n", res.TradesEntered)
		fmt.Fprintf(f, "- trades won:        %d\n", res.TradesWon)
		if res.TradesEntered > 0 {
			fmt.Fprintf(f, "- win rate:          %.1f%%\n", pct(res.TradesWon, res.TradesEntered))
			fmt.Fprintf(f, "- total realized:    %+d¢  ($%+.2f)\n",
				res.TotalPnLCents, float64(res.TotalPnLCents)/100)
			fmt.Fprintf(f, "- avg PnL per trade: %+.2f¢\n",
				float64(res.TotalPnLCents)/float64(res.TradesEntered))
		} else {
			fmt.Fprintln(f, "- _no trades cleared the entry threshold — naive p_fair offers no edge band_")
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
			fmt.Fprintf(f, "\n### Daily PnL (by bracket valid_date)\n\n")
			fmt.Fprintln(f, "| valid_date | trades | wins | win_rate | pnl_cents | pnl_$ |")
			fmt.Fprintln(f, "|---|---:|---:|---:|---:|---:|")
			days := make([]string, 0, len(dayPnL))
			for d := range dayPnL {
				days = append(days, d)
			}
			sort.Strings(days)
			for _, d := range days {
				fmt.Fprintf(f, "| %s | %d | %d | %.1f%% | %+d | $%+.2f |\n",
					d, dayTrades[d], dayWins[d], pct(dayWins[d], dayTrades[d]),
					dayPnL[d], float64(dayPnL[d])/100)
			}
		}
	}
	fmt.Fprintf(f, "\n## Skipped reasons\n\n")
	if len(res.SkippedReason) == 0 {
		fmt.Fprintln(f, "_(none — every weather trade was paired)_")
	} else {
		for _, r := range sortedKeys(res.SkippedReason) {
			fmt.Fprintf(f, "- %-30s  %d\n", r, res.SkippedReason[r])
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
	fmt.Fprintf(f, "\n## Edge distribution (cents)\n\n| edge_cents | trades |\n|---:|---:|\n")
	for _, k := range keys {
		fmt.Fprintf(f, "| %4d | %6d |\n", k, hist[k])
	}

	// Trades with edge ≥ threshold post-fee, by bracket.
	type bktTotal struct {
		key   string
		count int
	}
	bkt := map[string]int{}
	for _, r := range res.Rows {
		if r.EdgeCents >= edgeThresholdCents {
			bkt[r.Ticker]++
		}
	}
	var bktList []bktTotal
	for k, v := range bkt {
		bktList = append(bktList, bktTotal{k, v})
	}
	sort.Slice(bktList, func(i, j int) bool { return bktList[i].count > bktList[j].count })
	fmt.Fprintf(f, "\n## Top brackets with edge ≥ %d¢ post-fee\n\n", edgeThresholdCents)
	if len(bktList) == 0 {
		fmt.Fprintf(f, "_(no trade has positive edge ≥ %d¢ — naive p_fair offers no edge band over fees)_\n",
			edgeThresholdCents)
	} else {
		fmt.Fprintln(f, "| ticker | trades |\n|---|---:|")
		for i, bt := range bktList {
			if i >= 30 {
				break
			}
			fmt.Fprintf(f, "| %s | %d |\n", bt.key, bt.count)
		}
	}
	return nil
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
