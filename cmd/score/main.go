// Command score reports paper-trading performance from the joined
// bet ledger + outcome ledger.
//
// Joins each scored Bet to its Outcome by ID and prints, to stdout:
//
//   - Overall: bet count, scored vs unscored, net PnL, win rate,
//     per-bet mean and stdev (a per-bet Sharpe — not annualized; we
//     don't have a stable time-base for annualization yet).
//   - By model:        naive / climate_shrunk / mos_calibrated
//   - By location:     NY / MIA / LAX / CHI
//   - By bracket type: above / below
//   - By lead bucket:  <6h, 6–12h, 12–24h, >24h
//   - Calibration:     bin predicted P into deciles → realized win rate
//   - Edge fit:        linear regression of realized PnL on predicted edge
//
// Phase 0 paper trading produces tens of bets per day, not millions —
// implementation favours readability over throughput. Use --csv DIR to
// also dump the split tables for plotting elsewhere.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/papertrade"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to YAML config (only data_dir is read)")
	lookback := flag.Duration("lookback", 30*24*time.Hour, "scan ledger files this far back from now")
	csvDir := flag.String("csv", "", "if set, also write split tables to this directory as CSVs")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar score %s (%s) built %s\n", version, commit, buildTime)
		return
	}

	// Load .env so config.Load can resolve KALSHI_* into the validated
	// config struct. Score itself doesn't need the credentials, but
	// the shared config validator does.
	if _, err := env.LoadNearest(""); err != nil {
		fmt.Fprintln(os.Stderr, "warn: load .env:", err)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	since := time.Now().Add(-*lookback)
	ledgerDir := filepath.Join(cfg.DataDir, "papertrade")

	scored, err := loadScored(ledgerDir, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}

	r := report(scored)
	r.print(os.Stdout, *lookback, ledgerDir)

	if *csvDir != "" {
		if err := r.writeCSVs(*csvDir); err != nil {
			fmt.Fprintln(os.Stderr, "csv:", err)
			os.Exit(1)
		}
	}
}

// scoredBet pairs a Bet with its Outcome (if any). When Scored is
// false, Outcome is the zero value and excluded from PnL/win-rate
// stats — only the count of unscored bets is surfaced.
type scoredBet struct {
	Bet     papertrade.Bet
	Outcome papertrade.Outcome
	Scored  bool
}

// loadScored joins bets to outcomes by ID. Outcomes are loaded first
// into a map; then bets are walked and zipped. Out-of-window bets
// (those without a matching outcome) still appear in the result with
// Scored=false so the caller can report unscored counts.
func loadScored(ledgerDir string, since time.Time) ([]scoredBet, error) {
	outs := map[string]papertrade.Outcome{}
	if err := papertrade.IterOutcomes(ledgerDir, since, func(o papertrade.Outcome) error {
		outs[o.BetID] = o
		return nil
	}); err != nil {
		return nil, err
	}
	var rows []scoredBet
	if err := papertrade.IterBets(ledgerDir, since, func(b papertrade.Bet) error {
		sb := scoredBet{Bet: b}
		if o, ok := outs[b.ID]; ok {
			sb.Outcome = o
			sb.Scored = true
		}
		rows = append(rows, sb)
		return nil
	}); err != nil {
		return nil, err
	}
	return rows, nil
}

// stats is the reusable bundle of per-split numbers. Sharpe here is
// per-bet mean(pnl) / stddev(pnl) — useful for ranking strategies but
// NOT annualized. PnL units are fee-aware cents per contract.
type stats struct {
	N             int
	Scored        int
	Wins          int
	NetPnLCents   int
	MeanPnLCents  float64
	MedianPnL     float64
	StdevPnL      float64
	WinRate       float64
	PerBetSharpe  float64 // mean / stddev
	BetCount      int     // alias for N to make formatters explicit
	MeanPredEdge  float64 // mean predicted edge cents over scored rows
	EdgeRealized  float64 // mean realized PnL over scored rows (= MeanPnL)
}

// computeStats reduces a slice of scoredBets to a stats bundle. Only
// bets with Scored=true contribute to PnL aggregates; all bets in the
// slice contribute to N (so callers can split by group and still see
// the unscored rate).
func computeStats(rows []scoredBet) stats {
	s := stats{N: len(rows), BetCount: len(rows)}
	if len(rows) == 0 {
		return s
	}
	pnls := make([]int, 0, len(rows))
	predEdges := make([]float64, 0, len(rows))
	for _, r := range rows {
		if !r.Scored {
			continue
		}
		s.Scored++
		s.NetPnLCents += r.Outcome.PnLCents
		if r.Outcome.Won {
			s.Wins++
		}
		pnls = append(pnls, r.Outcome.PnLCents)
		predEdges = append(predEdges, r.Bet.EdgeCents)
	}
	if s.Scored == 0 {
		return s
	}
	s.MeanPnLCents = float64(s.NetPnLCents) / float64(s.Scored)
	s.WinRate = float64(s.Wins) / float64(s.Scored)
	s.MedianPnL = medianInt(pnls)
	s.StdevPnL = stdevInt(pnls, s.MeanPnLCents)
	if s.StdevPnL > 0 {
		s.PerBetSharpe = s.MeanPnLCents / s.StdevPnL
	}
	s.MeanPredEdge = meanFloat(predEdges)
	s.EdgeRealized = s.MeanPnLCents
	return s
}

// reportData is the full set of split tables we'll print.
type reportData struct {
	overall     stats
	byModel     map[string]stats
	byLocation  map[string]stats
	byBracket   map[string]stats
	byLead      map[string]stats // bucket key, e.g. "12–24h"
	calibration []calibBin
	edgeFit     edgeFitResult
}

// calibBin is one decile of the calibration plot: predicted P bin
// boundaries and the realized win rate within.
type calibBin struct {
	BinLo   float64
	BinHi   float64
	N       int
	WinRate float64
}

// edgeFitResult is the linear-regression diagnostic for predicted
// edge cents → realized PnL cents. Slope of 1 + intercept of 0 means
// edges are dollar-accurate. Slope <<1 means we systematically
// over-predict; negative slope means the prediction is anti-correlated
// with realized P&L (model is broken).
type edgeFitResult struct {
	N         int
	Slope     float64
	Intercept float64
	R2        float64
}

func report(rows []scoredBet) reportData {
	r := reportData{
		overall:    computeStats(rows),
		byModel:    map[string]stats{},
		byLocation: map[string]stats{},
		byBracket:  map[string]stats{},
		byLead:     map[string]stats{},
	}

	splitBy(rows, func(b papertrade.Bet) string { return string(b.Model) }, r.byModel)
	splitBy(rows, func(b papertrade.Bet) string { return b.Location }, r.byLocation)
	splitBy(rows, func(b papertrade.Bet) string { return b.BracketType }, r.byBracket)
	splitBy(rows, leadBucket, r.byLead)

	r.calibration = calibration(rows, 10)
	r.edgeFit = fitEdge(rows)
	return r
}

// splitBy groups rows by keyFn(b) and writes computeStats(group) to
// dst[k]. Groups with zero rows are not emitted.
func splitBy(rows []scoredBet, keyFn func(papertrade.Bet) string, dst map[string]stats) {
	groups := map[string][]scoredBet{}
	for _, r := range rows {
		k := keyFn(r.Bet)
		if k == "" {
			continue
		}
		groups[k] = append(groups[k], r)
	}
	for k, g := range groups {
		dst[k] = computeStats(g)
	}
}

// leadBucket maps a Bet's lead_hours into a coarse bucket label so
// the splits remain readable when bet volume is low.
func leadBucket(b papertrade.Bet) string {
	switch {
	case b.LeadHours < 6:
		return "<6h"
	case b.LeadHours < 12:
		return "6–12h"
	case b.LeadHours < 24:
		return "12–24h"
	default:
		return ">24h"
	}
}

// calibration bins predicted-P into n equal-width deciles and reports
// the realized win rate per bin. Bins with zero scored rows are
// emitted with N=0 + WinRate=NaN so the bin grid is uniform.
func calibration(rows []scoredBet, n int) []calibBin {
	bins := make([]calibBin, n)
	for i := range bins {
		bins[i].BinLo = float64(i) / float64(n)
		bins[i].BinHi = float64(i+1) / float64(n)
	}
	for _, r := range rows {
		if !r.Scored {
			continue
		}
		p := r.Bet.ModelP
		idx := int(p * float64(n))
		if idx >= n {
			idx = n - 1
		}
		if idx < 0 {
			idx = 0
		}
		bins[idx].N++
		if r.Outcome.Won {
			bins[idx].WinRate++ // running win count, divided below
		}
	}
	for i := range bins {
		if bins[i].N == 0 {
			bins[i].WinRate = math.NaN()
			continue
		}
		bins[i].WinRate /= float64(bins[i].N)
	}
	return bins
}

// fitEdge runs ordinary least squares on (x=predicted_edge_cents,
// y=realized_pnl_cents). Returns slope, intercept, R². With a
// well-calibrated model, slope ≈ 1.0 and intercept ≈ 0.
func fitEdge(rows []scoredBet) edgeFitResult {
	xs, ys := []float64{}, []float64{}
	for _, r := range rows {
		if !r.Scored {
			continue
		}
		xs = append(xs, r.Bet.EdgeCents)
		ys = append(ys, float64(r.Outcome.PnLCents))
	}
	res := edgeFitResult{N: len(xs)}
	if res.N < 2 {
		return res
	}
	mx, my := meanFloat(xs), meanFloat(ys)
	var num, den, ss float64
	for i := range xs {
		dx := xs[i] - mx
		dy := ys[i] - my
		num += dx * dy
		den += dx * dx
		ss += dy * dy
	}
	if den == 0 {
		return res
	}
	res.Slope = num / den
	res.Intercept = my - res.Slope*mx
	if ss > 0 {
		res.R2 = (num * num) / (den * ss)
	}
	return res
}

func medianInt(xs []int) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	cp := append([]int(nil), xs...)
	sort.Ints(cp)
	n := len(cp)
	if n%2 == 1 {
		return float64(cp[n/2])
	}
	return float64(cp[n/2-1]+cp[n/2]) / 2.0
}

func stdevInt(xs []int, mean float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var ss float64
	for _, x := range xs {
		d := float64(x) - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func meanFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// --- output ----------------------------------------------------------------

func (r reportData) print(w *os.File, lookback time.Duration, dir string) {
	fmt.Fprintf(w, "isobar score — paper-trade performance\n")
	fmt.Fprintf(w, "ledger:    %s\n", dir)
	fmt.Fprintf(w, "lookback:  %s\n\n", lookback)

	printOverall(w, r.overall)
	printSplit(w, "By model", r.byModel)
	printSplit(w, "By location", r.byLocation)
	printSplit(w, "By bracket type", r.byBracket)
	printSplit(w, "By lead-hour bucket", r.byLead)
	printCalibration(w, r.calibration)
	printEdgeFit(w, r.edgeFit)

	if r.overall.Scored == 0 {
		fmt.Fprintln(w, "\n(!) No scored bets in window. Run cmd/settle to reconcile bets against Kalshi resolutions.")
	}
}

func printOverall(w *os.File, s stats) {
	fmt.Fprintln(w, "Overall")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  bets\tscored\tunscored\tnet_pnl_¢\tmean_pnl_¢\tmedian_¢\tstdev_¢\twin_rate\tper_bet_sharpe\n")
	fmt.Fprintf(tw, "  %d\t%d\t%d\t%d\t%.2f\t%.1f\t%.2f\t%.3f\t%.3f\n",
		s.N, s.Scored, s.N-s.Scored, s.NetPnLCents, s.MeanPnLCents,
		s.MedianPnL, s.StdevPnL, s.WinRate, s.PerBetSharpe)
	tw.Flush()
	fmt.Fprintln(w)
}

func printSplit(w *os.File, title string, m map[string]stats) {
	fmt.Fprintln(w, title)
	if len(m) == 0 {
		fmt.Fprintln(w, "  (no rows)")
		fmt.Fprintln(w)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  group\tbets\tscored\tnet_¢\tmean_¢\twin_rate\tsharpe\n")
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := m[k]
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%.2f\t%.3f\t%.3f\n",
			k, s.N, s.Scored, s.NetPnLCents, s.MeanPnLCents, s.WinRate, s.PerBetSharpe)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printCalibration(w *os.File, bins []calibBin) {
	fmt.Fprintln(w, "Calibration (predicted P → realized win rate)")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  bin\tn\twin_rate\n")
	for _, b := range bins {
		wr := "—"
		if !math.IsNaN(b.WinRate) {
			wr = fmt.Sprintf("%.3f", b.WinRate)
		}
		fmt.Fprintf(tw, "  [%.1f–%.1f)\t%d\t%s\n", b.BinLo, b.BinHi, b.N, wr)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printEdgeFit(w *os.File, f edgeFitResult) {
	fmt.Fprintln(w, "Edge fit: realized_pnl_¢ ≈ slope · predicted_edge_¢ + intercept")
	if f.N < 2 {
		fmt.Fprintln(w, "  (need ≥ 2 scored bets)")
		fmt.Fprintln(w)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  n\tslope\tintercept\tR²\n")
	fmt.Fprintf(tw, "  %d\t%.3f\t%.2f\t%.3f\n", f.N, f.Slope, f.Intercept, f.R2)
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Slope ≈ 1.0 + intercept ≈ 0 → edges are dollar-correct.")
	fmt.Fprintln(w, "  Slope << 1 → we systematically over-predict edge.")
	fmt.Fprintln(w, "  Slope < 0 → prediction anti-correlates with realized PnL (model is broken).")
	fmt.Fprintln(w)
}

// --- CSV (optional) --------------------------------------------------------

func (r reportData) writeCSVs(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeSplitCSV(filepath.Join(dir, "by_model.csv"), r.byModel); err != nil {
		return err
	}
	if err := writeSplitCSV(filepath.Join(dir, "by_location.csv"), r.byLocation); err != nil {
		return err
	}
	if err := writeSplitCSV(filepath.Join(dir, "by_bracket.csv"), r.byBracket); err != nil {
		return err
	}
	if err := writeSplitCSV(filepath.Join(dir, "by_lead.csv"), r.byLead); err != nil {
		return err
	}
	return writeCalibrationCSV(filepath.Join(dir, "calibration.csv"), r.calibration)
}

func writeSplitCSV(path string, m map[string]stats) error {
	f, err := os.Create(path) //nolint:gosec // path is operator-supplied output dir
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"group", "bets", "scored", "net_pnl_cents", "mean_pnl_cents", "median_pnl", "stdev_pnl", "win_rate", "per_bet_sharpe", "mean_pred_edge_cents"})
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := m[k]
		_ = w.Write([]string{
			k,
			strconv.Itoa(s.N), strconv.Itoa(s.Scored),
			strconv.Itoa(s.NetPnLCents),
			fmtFloat(s.MeanPnLCents),
			fmtFloat(s.MedianPnL),
			fmtFloat(s.StdevPnL),
			fmtFloat(s.WinRate),
			fmtFloat(s.PerBetSharpe),
			fmtFloat(s.MeanPredEdge),
		})
	}
	return nil
}

func writeCalibrationCSV(path string, bins []calibBin) error {
	f, err := os.Create(path) //nolint:gosec // path is operator-supplied output dir
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"bin_lo", "bin_hi", "n", "realized_win_rate"})
	for _, b := range bins {
		wr := ""
		if !math.IsNaN(b.WinRate) {
			wr = fmtFloat(b.WinRate)
		}
		_ = w.Write([]string{
			strconv.FormatFloat(b.BinLo, 'f', 1, 64),
			strconv.FormatFloat(b.BinHi, 'f', 1, 64),
			strconv.Itoa(b.N),
			wr,
		})
	}
	return nil
}

func fmtFloat(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
}
