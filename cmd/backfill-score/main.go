// Command backfill-score answers the actual question: of the bets the
// paper-trader would have fired on recorded data, what fraction won?
//
// Reads the per-candidate CSV produced by cmd/fillability, looks up
// each candidate's Kalshi market resolution via REST, computes paper
// P&L using the same fee-aware math as cmd/settle, and reports:
//
//   - Overall: candidates, settled, win rate, mean PnL, Sharpe.
//   - Filled subset (the bets we'd actually have transacted): same
//     stats. This is the only number that matters for the
//     "would we have made money?" question.
//   - Splits by mid bucket / predicted edge bucket / location / side.
//
// One Kalshi GET per candidate, rate-limited by the kalshi.Client to
// ~1 req/s. 444 candidates ≈ 7 minutes wall-clock.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/kalshi"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/papertrade"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to YAML config (Kalshi creds)")
	candPath := flag.String("candidates", "", "fillability CSV (required)")
	outPath := flag.String("out", "", "scored CSV output (default: stdout-summary only)")
	limit := flag.Int("limit", 0, "process only the first N candidates (0 = all)")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar backfill-score %s (%s) built %s\n", version, commit, buildTime)
		return
	}
	if *candPath == "" {
		fmt.Fprintln(os.Stderr, "candidates CSV required (-candidates)")
		os.Exit(2)
	}

	if _, err := env.LoadNearest(""); err != nil {
		fmt.Fprintln(os.Stderr, "warn: load .env:", err)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	pemBytes, src, err := loadKalshiPEM(cfg.Kalshi)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kalshi creds:", err)
		os.Exit(1)
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pemBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kalshi signer:", err)
		os.Exit(1)
	}
	rest := kalshi.NewClient(cfg.Kalshi.APIBase, signer)
	_ = src

	cands, err := readCandidates(*candPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read candidates:", err)
		os.Exit(1)
	}
	if *limit > 0 && *limit < len(cands) {
		cands = cands[:*limit]
	}
	fmt.Fprintf(os.Stderr, "loaded %d candidates from %s\n", len(cands), *candPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scored := scoreAll(ctx, rest, cands)

	if *outPath != "" {
		if err := writeScoredCSV(*outPath, scored); err != nil {
			fmt.Fprintln(os.Stderr, "write csv:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote scored CSV → %s\n", *outPath)
	}

	report(os.Stdout, scored)
}

// candidate mirrors the fillability CSV row, plus Kalshi resolution
// and computed P&L once scored.
type candidate struct {
	Ticker          string
	Series          string
	Location        string
	Side            papertrade.Side
	MidCents        int
	SpreadCents     int
	EdgeCents       float64
	LeadHours       float64
	FireWall        int64
	SettlementUTC   string
	Filled          bool
	FirstFillLagS   float64
	FillCount       int
	BestFillCents   int

	// Filled in by scoreAll.
	KalshiStatus string
	KalshiResult string
	Resolution   papertrade.Resolution
	Won          bool
	PnLCents     int
	Scored       bool
	LookupErr    string
}

func readCandidates(path string) ([]candidate, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied input path
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	hdr, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, c := range hdr {
		idx[strings.TrimSpace(c)] = i
	}
	required := []string{"ticker", "series", "location", "side", "mid_cents", "edge_cents", "filled"}
	for _, c := range required {
		if _, ok := idx[c]; !ok {
			return nil, fmt.Errorf("required column %q missing", c)
		}
	}

	var out []candidate
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		c := candidate{
			Ticker:        row[idx["ticker"]],
			Series:        row[idx["series"]],
			Location:      row[idx["location"]],
			Side:          papertrade.Side(row[idx["side"]]),
			MidCents:      atoiOr0(row[idx["mid_cents"]]),
			SpreadCents:   atoiOr0(rowOr(row, idx, "spread_cents")),
			EdgeCents:     atofOr0(row[idx["edge_cents"]]),
			LeadHours:     atofOr0(rowOr(row, idx, "lead_hours")),
			FireWall:      atoi64Or0(rowOr(row, idx, "fire_unix_nano")),
			SettlementUTC: rowOr(row, idx, "settlement_utc"),
			Filled:        row[idx["filled"]] == "1",
			FirstFillLagS: atofOr0(rowOr(row, idx, "first_fill_lag_seconds")),
			FillCount:     atoiOr0(rowOr(row, idx, "fill_count")),
			BestFillCents: atoiOr0(rowOr(row, idx, "best_fill_cents")),
		}
		out = append(out, c)
	}
	return out, nil
}

// scoreAll calls Kalshi REST once per candidate with the client's
// internal rate limiter doing the throttling. Errors are recorded
// per-row rather than failing the whole run; a flaky network shouldn't
// cost an hour of progress.
func scoreAll(ctx context.Context, rest *kalshi.Client, cands []candidate) []candidate {
	out := make([]candidate, len(cands))
	for i, c := range cands {
		select {
		case <-ctx.Done():
			out[i] = c
			out[i].LookupErr = ctx.Err().Error()
			continue
		default:
		}
		mkt, err := rest.GetMarket(ctx, c.Ticker)
		c.KalshiStatus = mkt.Status
		c.KalshiResult = mkt.Result
		if err != nil {
			c.LookupErr = err.Error()
			out[i] = c
			continue
		}
		res := papertrade.ClassifyResolution(mkt.Status, mkt.Result)
		c.Resolution = res
		if res != papertrade.ResolutionUnsettled {
			fee := market.RoundTripFeeCents(c.MidCents)
			b := papertrade.Bet{
				Side:              c.Side,
				MidCents:          c.MidCents,
				FeeRoundTripCents: fee,
			}
			won, pnl := papertrade.PnLFromBet(b, res)
			c.Won = won
			c.PnLCents = pnl
			c.Scored = true
		}
		out[i] = c
		// Progress chirp every 50 rows.
		if (i+1)%50 == 0 {
			fmt.Fprintf(os.Stderr, "  scored %d/%d (%s)\n", i+1, len(cands), c.Ticker)
		}
	}
	return out
}

// stats holds aggregate numbers for a slice of candidates. Sharpe is
// per-bet mean(pnl)/stddev(pnl), not annualized — useful for ranking
// strategies but not directly comparable to capital-market figures.
type stats struct {
	N            int     // total in subset
	Scored       int     // had a Kalshi resolution
	Wins         int
	NetPnLCents  int
	MeanPnLCents float64
	StdevPnL     float64
	WinRate      float64
	PerBetSharpe float64
}

func compute(cs []candidate) stats {
	s := stats{N: len(cs)}
	if len(cs) == 0 {
		return s
	}
	pnls := make([]int, 0, len(cs))
	for _, c := range cs {
		if !c.Scored {
			continue
		}
		s.Scored++
		s.NetPnLCents += c.PnLCents
		if c.Won {
			s.Wins++
		}
		pnls = append(pnls, c.PnLCents)
	}
	if s.Scored == 0 {
		return s
	}
	s.MeanPnLCents = float64(s.NetPnLCents) / float64(s.Scored)
	s.WinRate = float64(s.Wins) / float64(s.Scored)
	if s.Scored > 1 {
		var ss float64
		for _, p := range pnls {
			d := float64(p) - s.MeanPnLCents
			ss += d * d
		}
		s.StdevPnL = math.Sqrt(ss / float64(s.Scored-1))
		if s.StdevPnL > 0 {
			s.PerBetSharpe = s.MeanPnLCents / s.StdevPnL
		}
	}
	return s
}

func report(w *os.File, scored []candidate) {
	fmt.Fprintf(w, "isobar backfill-score — historical paper-bet performance\n")
	fmt.Fprintf(w, "candidates: %d\n", len(scored))

	// Headline panes: overall, then filled-only.
	all := compute(scored)
	filled := []candidate{}
	for _, c := range scored {
		if c.Filled {
			filled = append(filled, c)
		}
	}
	fl := compute(filled)

	fmt.Fprintln(w)
	printPane(w, "All candidates", all)
	printPane(w, "Filled subset (the bets we'd actually transact)", fl)

	// Bucket splits — only meaningful when we have scored bets. Run
	// over the FILLED subset since unfilled candidates carry no real
	// P&L impact.
	fmt.Fprintln(w, "Filled-subset splits below:")
	fmt.Fprintln(w)
	printBucketed(w, "By mid bucket", filled,
		[]bucketDef{{1, 5}, {5, 15}, {15, 30}, {30, 70}, {70, 85}, {85, 95}, {95, 100}},
		func(c candidate) int { return c.MidCents }, "mid_¢")
	printBucketed(w, "By predicted-edge bucket", filled,
		[]bucketDef{{3, 5}, {5, 10}, {10, 20}, {20, 50}, {50, 100}},
		func(c candidate) int { return int(c.EdgeCents) }, "edge_¢")
	printGroupSplit(w, "By location", filled, func(c candidate) string { return c.Location })
	printGroupSplit(w, "By side", filled, func(c candidate) string { return string(c.Side) })

	// Lookup errors (network / rate-limit / market-not-found).
	errs := 0
	for _, c := range scored {
		if c.LookupErr != "" {
			errs++
		}
	}
	if errs > 0 {
		fmt.Fprintf(w, "(!) %d candidates had Kalshi lookup errors; rerun to retry\n", errs)
	}
}

type bucketDef struct{ Lo, Hi int }

func printPane(w *os.File, title string, s stats) {
	fmt.Fprintln(w, title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  n\tscored\twins\twin_rate\tnet_pnl_¢\tmean_pnl_¢\tstdev_¢\tper_bet_sharpe\n")
	fmt.Fprintf(tw, "  %d\t%d\t%d\t%.3f\t%d\t%.2f\t%.2f\t%.3f\n",
		s.N, s.Scored, s.Wins, s.WinRate, s.NetPnLCents, s.MeanPnLCents, s.StdevPnL, s.PerBetSharpe)
	tw.Flush()
	fmt.Fprintln(w)
}

func printBucketed(w *os.File, title string, cs []candidate, defs []bucketDef, key func(candidate) int, axisLabel string) {
	fmt.Fprintf(w, "%s\n", title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  %s\tn\twins\twin_rate\tnet_¢\tmean_¢\n", axisLabel)
	for _, d := range defs {
		bucket := []candidate{}
		for _, c := range cs {
			v := key(c)
			if v >= d.Lo && v < d.Hi {
				bucket = append(bucket, c)
			}
		}
		if len(bucket) == 0 {
			continue
		}
		s := compute(bucket)
		fmt.Fprintf(tw, "  [%2d–%2d)\t%d\t%d\t%.3f\t%d\t%.2f\n",
			d.Lo, d.Hi, s.N, s.Wins, s.WinRate, s.NetPnLCents, s.MeanPnLCents)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printGroupSplit(w *os.File, title string, cs []candidate, keyFn func(candidate) string) {
	fmt.Fprintf(w, "%s\n", title)
	groups := map[string][]candidate{}
	for _, c := range cs {
		k := keyFn(c)
		groups[k] = append(groups[k], c)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  group\tn\twins\twin_rate\tnet_¢\tmean_¢\n")
	for _, k := range keys {
		s := compute(groups[k])
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%.3f\t%d\t%.2f\n",
			k, s.N, s.Wins, s.WinRate, s.NetPnLCents, s.MeanPnLCents)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func writeScoredCSV(path string, cs []candidate) error {
	f, err := os.Create(path) //nolint:gosec // operator-supplied output path
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{
		"ticker", "series", "location", "side",
		"mid_cents", "spread_cents", "edge_cents", "lead_hours",
		"filled", "first_fill_lag_seconds", "fill_count", "best_fill_cents",
		"kalshi_status", "kalshi_result", "resolution",
		"scored", "won", "pnl_cents", "lookup_err",
	})
	for _, c := range cs {
		_ = w.Write([]string{
			c.Ticker, c.Series, c.Location, string(c.Side),
			itoa(c.MidCents), itoa(c.SpreadCents), ftoa(c.EdgeCents), ftoa(c.LeadHours),
			boolStr(c.Filled), ftoa(c.FirstFillLagS), itoa(c.FillCount), itoa(c.BestFillCents),
			c.KalshiStatus, c.KalshiResult, string(c.Resolution),
			boolStr(c.Scored), boolStr(c.Won), itoa(c.PnLCents), c.LookupErr,
		})
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func atoiOr0(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func atoi64Or0(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

func atofOr0(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func rowOr(row []string, idx map[string]int, col string) string {
	if i, ok := idx[col]; ok && i < len(row) {
		return row[i]
	}
	return ""
}

func itoa(v int) string  { return strconv.Itoa(v) }
func ftoa(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
}
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func loadKalshiPEM(k config.Kalshi) ([]byte, string, error) {
	if k.PrivateKey != "" {
		return []byte(k.PrivateKey), "inline (KALSHI_PRIVATE_KEY)", nil
	}
	if k.PrivateKeyPath == "" {
		return nil, "", errors.New("kalshi: no PEM source configured")
	}
	b, err := os.ReadFile(k.PrivateKeyPath)
	if err != nil {
		return nil, "", fmt.Errorf("kalshi: read private key %s: %w", k.PrivateKeyPath, err)
	}
	return b, "file (" + k.PrivateKeyPath + ")", nil
}

