// Command fillability tests the empirical fillability of paper-trader
// triggers against recorded WS trade prints.
//
// The point of this tool: cmd/paper-trader fires a "would-have-bet"
// when the book mid satisfies a trigger. But the printed mid is rarely
// the achievable fill price at the corner of the book. This tool
// answers the question: of the bets the trader would have fired on
// recorded data, what fraction subsequently traded at-or-better than
// the mid we'd have crossed, within --lookahead?
//
// A "fillable" bet is one where the next --lookahead window contains
// at least one trade print at:
//
//   - YES side: yes_price_cents ≤ mid_at_fire
//   - NO  side: yes_price_cents ≥ mid_at_fire (equivalently
//     no_price_cents ≤ 100 - mid_at_fire)
//
// Output: per-mid-bucket fillability rate. If 8% of mid-1¢ bets are
// fillable, those signals are illusory — they print on no real volume
// and we'd lose the predicted edge to slippage.
//
// Reads: data/kalshi/<DATE>/ + data/openmeteo/<DATE>/. Same input as
// cmd/edge-scan.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/edge"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/papertrade"
	"github.com/wh0o0pz/isobar/internal/replay"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	dataDir := flag.String("data", "./data", "root data dir")
	dateFilter := flag.String("date", "", "YYYY-MM-DD (single date) or empty for all dates")
	climDir := flag.String("climate", "", "obs CSVs dir for climate-shrunk model; empty falls back to naive")
	lookahead := flag.Duration("lookahead", 30*time.Minute, "wait this long after a candidate fire for an at-or-better trade print")
	minEdge := flag.Float64("min-edge", 3.0, "minimum predicted edge cents to register a candidate")
	maxSpread := flag.Int("max-spread", 5, "maximum book spread cents")
	minMid := flag.Int("min-mid", 5, "minimum mid cents (corner filter)")
	maxMid := flag.Int("max-mid", 95, "maximum mid cents (corner filter)")
	minLeadHours := flag.Float64("min-lead-hours", 2.0, "minimum hours from decision to settlement")
	csvOut := flag.String("csv", "", "if set, write per-bet detail to this CSV path")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar fillability %s (%s) built %s\n", version, commit, buildTime)
		return
	}

	var clim *climate.Lookup
	if *climDir != "" {
		clim = climate.New()
		if err := clim.Load(*climDir, market.AllStationCodes()); err != nil {
			fmt.Fprintln(os.Stderr, "load climate:", err)
			os.Exit(1)
		}
	}

	trig := papertrade.Trigger{
		MinEdgeCents:   *minEdge,
		MaxSpreadCents: *maxSpread,
		MinMidCents:    *minMid,
		MaxMidCents:    *maxMid,
		MaxPollAge:     4 * time.Hour,
		MinLeadHours:   *minLeadHours,
		Model:          papertrade.ModelClimateShrunk,
	}

	dates, err := findDates(*dataDir, *dateFilter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "find dates:", err)
		os.Exit(1)
	}
	if len(dates) == 0 {
		fmt.Fprintln(os.Stderr, "no recorded dates under", filepath.Join(*dataDir, "kalshi"))
		os.Exit(1)
	}

	var allCandidates []candidate
	for _, d := range dates {
		cs, err := scanDate(*dataDir, d, trig, clim, *lookahead)
		if err != nil {
			fmt.Fprintln(os.Stderr, "scan", d, ":", err)
			continue
		}
		allCandidates = append(allCandidates, cs...)
		fmt.Fprintf(os.Stderr, "  %s: %d candidates, %d fillable\n", d, len(cs), countFilled(cs))
	}

	report(os.Stdout, allCandidates, *lookahead)

	if *csvOut != "" {
		if err := writeCandidatesCSV(*csvOut, allCandidates); err != nil {
			fmt.Fprintln(os.Stderr, "csv:", err)
			os.Exit(1)
		}
	}
}

// candidate is one would-have-bet observation. The first eight fields
// describe the fire; the last four describe what happened in the
// follow-up window.
type candidate struct {
	Ticker         string
	Series         string
	Location       string
	Side           papertrade.Side
	MidAtFire      int
	SpreadAtFire   int
	EdgeCents      float64
	LeadHours      float64
	FireWall       int64

	Filled       bool
	FirstFillLag time.Duration
	FillCount    int
	BestFillCent int // best YES price observed in window for this candidate's side
}

// scanDate replays one date's recorded data, applies the same Decide
// logic as cmd/paper-trader, and tracks fills against the resulting
// candidates. Two passes: pass 1 builds candidates by replaying frames
// and triggering Decide; pass 2 walks the same frames and counts
// trade prints that satisfy each candidate's fill condition within the
// lookahead window.
//
// Memory: candidates are O(distinct (ticker, side, bracket-date)) per
// day — typically a few hundred — and trade prints are streamed
// (per-frame check, no buffering).
func scanDate(dataDir, date string, trig papertrade.Trigger, clim *climate.Lookup, lookahead time.Duration) ([]candidate, error) {
	polls, err := loadPollsByLoc(dataDir, date)
	if err != nil {
		return nil, fmt.Errorf("load polls: %w", err)
	}
	if len(polls) == 0 {
		return nil, fmt.Errorf("no polls for %s", date)
	}

	files, err := replay.ListJournals(filepath.Join(dataDir, "kalshi", date))
	if err != nil {
		return nil, fmt.Errorf("list journals: %w", err)
	}

	// Pass 1: walk frames, maintain books, evaluate trigger, collect
	// candidates. Dedup on (ticker, side, bracket_date) so one ticker
	// can produce at most one YES candidate and one NO candidate per
	// scan window — same as the live trader.
	books := map[string]*replay.Book{}
	walls := map[string]int64{}
	seen := map[string]struct{}{}
	cands := []candidate{}
	lastMid := map[string]int{}
	lastSpread := map[string]int{}

	_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		var f replay.KalshiFrame
		if err := sonic.Unmarshal(env.Payload, &f); err != nil {
			return nil
		}
		if f.Type != "orderbook_snapshot" && f.Type != "orderbook_delta" {
			return nil
		}
		ticker := f.MarketTicker()
		if ticker == "" {
			return nil
		}
		replay.ApplyFrameToBooks(books, walls, env, f)
		book := books[ticker]
		if book == nil {
			return nil
		}
		mid := book.MidYesCents()
		spread := book.SpreadYesCents()
		// Skip redundant evaluations — same as live trader.
		if pm, ok := lastMid[ticker]; ok && pm == mid {
			if ps, ok := lastSpread[ticker]; ok && ps == spread {
				return nil
			}
		}
		lastMid[ticker] = mid
		lastSpread[ticker] = spread

		tk, perr := market.ParseTicker(ticker)
		if perr != nil {
			return nil
		}
		loc := tk.Location()
		latestPoll := pollAtOrBefore(polls[loc], env.Wall)
		if latestPoll == nil {
			return nil
		}

		bet, _, ok := papertrade.Decide(papertrade.Snapshot{
			Ticker:      ticker,
			MidCents:    mid,
			SpreadCents: spread,
			Poll:        latestPoll.poll,
			PollWall:    latestPoll.wall,
			DecidedAt:   env.Wall,
		}, trig, clim)
		if !ok {
			return nil
		}
		key := fmt.Sprintf("%s|%s|%s", ticker, bet.Side, bet.BracketDate)
		if _, dup := seen[key]; dup {
			return nil
		}
		seen[key] = struct{}{}
		cands = append(cands, candidate{
			Ticker:       bet.Ticker,
			Series:       bet.Series,
			Location:     bet.Location,
			Side:         bet.Side,
			MidAtFire:    bet.MidCents,
			SpreadAtFire: bet.SpreadCents,
			EdgeCents:    bet.EdgeCents,
			LeadHours:    bet.LeadHours,
			FireWall:     env.Wall,
		})
		return nil
	})

	if len(cands) == 0 {
		return cands, nil
	}

	// Pass 2: index candidates by ticker so trade frames can update
	// every open candidate for that ticker in O(open) per print.
	byTicker := map[string][]int{}
	for i, c := range cands {
		byTicker[c.Ticker] = append(byTicker[c.Ticker], i)
	}

	_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		var f replay.KalshiFrame
		if err := sonic.Unmarshal(env.Payload, &f); err != nil {
			return nil
		}
		if f.Type != "trade" {
			return nil
		}
		var t tradeMsg
		if err := sonic.Unmarshal(f.Msg, &t); err != nil {
			return nil
		}
		ticker := t.MarketTicker
		if ticker == "" {
			return nil
		}
		idxs, ok := byTicker[ticker]
		if !ok {
			return nil
		}
		yesPriceCents := dollarsToCents(t.YesPriceDollars)
		if yesPriceCents <= 0 {
			return nil
		}
		for _, i := range idxs {
			c := &cands[i]
			lag := time.Duration(env.Wall - c.FireWall)
			if lag < 0 || lag > lookahead {
				continue
			}
			matched := false
			switch c.Side {
			case papertrade.SideYes:
				if yesPriceCents <= c.MidAtFire {
					matched = true
					if c.BestFillCent == 0 || yesPriceCents < c.BestFillCent {
						c.BestFillCent = yesPriceCents
					}
				}
			case papertrade.SideNo:
				// NO buys at no_price_cents ≤ 100-mid → equivalently
				// yes prints AT OR ABOVE mid (the seller of YES at mid
				// or higher creates the buyer-of-NO opportunity).
				if yesPriceCents >= c.MidAtFire {
					matched = true
					if yesPriceCents > c.BestFillCent {
						c.BestFillCent = yesPriceCents
					}
				}
			}
			if matched {
				if !c.Filled {
					c.Filled = true
					c.FirstFillLag = lag
				}
				c.FillCount++
			}
		}
		return nil
	})

	return cands, nil
}

// tradeMsg is the slice of a Kalshi WS trade frame's msg we care about.
type tradeMsg struct {
	MarketTicker    string `json:"market_ticker"`
	YesPriceDollars string `json:"yes_price_dollars"`
}

// pollEntry pairs a parsed poll with its received wall stamp.
type pollEntry struct {
	poll *openmeteo.EnsemblePoll
	wall int64
}

// pollAtOrBefore returns the most recent poll whose wall ≤ target,
// using a linear scan over the location's poll history. Lists are
// short (a poll every ~30 minutes ⇒ ~50 polls/day) so a sort+binary-
// search would be over-engineering.
func pollAtOrBefore(polls []pollEntry, target int64) *pollEntry {
	var best *pollEntry
	for i := range polls {
		if polls[i].wall > target {
			break
		}
		best = &polls[i]
	}
	return best
}

// loadPollsByLoc reads all Open-Meteo journal frames for a date and
// returns parsed polls indexed by location.
func loadPollsByLoc(dataDir, date string) (map[string][]pollEntry, error) {
	dir := filepath.Join(dataDir, "openmeteo", date)
	files, err := replay.ListJournals(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]pollEntry{}
	_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		loc := strings.TrimPrefix(env.Kind, "rest:")
		if loc == "" || loc == env.Kind {
			return nil
		}
		poll, err := openmeteo.ParseEnsemblePoll(env.Payload)
		if err != nil {
			return nil
		}
		poll.Wall = env.Wall
		poll.Location = loc
		out[loc] = append(out[loc], pollEntry{poll: poll, wall: env.Wall})
		return nil
	})
	return out, nil
}

func findDates(dataDir, dateFilter string) ([]string, error) {
	if dateFilter != "" {
		return []string{dateFilter}, nil
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "kalshi"))
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

func dollarsToCents(s string) int {
	// "0.3900" → 39
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Manual conversion to keep this tiny and avoid import-cycle into market.
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return 0
	}
	dollars := atoi(parts[0])
	frac := parts[1]
	if len(frac) > 2 {
		frac = frac[:2]
	} else if len(frac) == 1 {
		frac += "0"
	} else if len(frac) == 0 {
		frac = "00"
	}
	cents := atoi(frac)
	return dollars*100 + cents
}

func atoi(s string) int {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

// --- reporting -------------------------------------------------------------

type bucket struct {
	Lo, Hi int // mid cents range, inclusive lo, exclusive hi
	N      int
	Filled int
}

func report(w *os.File, cands []candidate, lookahead time.Duration) {
	fmt.Fprintf(w, "isobar fillability — empirical fill rate over recorded data\n")
	fmt.Fprintf(w, "lookahead: %s\n", lookahead)
	fmt.Fprintf(w, "candidates: %d total\n\n", len(cands))
	if len(cands) == 0 {
		return
	}

	totalFilled := countFilled(cands)
	fmt.Fprintf(w, "Overall fillability: %d/%d = %.1f%%\n",
		totalFilled, len(cands), 100*float64(totalFilled)/float64(len(cands)))

	// Lag stats over the filled subset.
	var lags []time.Duration
	for _, c := range cands {
		if c.Filled {
			lags = append(lags, c.FirstFillLag)
		}
	}
	if len(lags) > 0 {
		sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
		fmt.Fprintf(w, "First-fill lag (filled subset): median %s, p90 %s, max %s\n\n",
			lags[len(lags)/2].Round(time.Second),
			lags[len(lags)*9/10].Round(time.Second),
			lags[len(lags)-1].Round(time.Second),
		)
	} else {
		fmt.Fprintln(w)
	}

	midBuckets := []bucket{
		{Lo: 1, Hi: 5}, {Lo: 5, Hi: 15}, {Lo: 15, Hi: 30},
		{Lo: 30, Hi: 70}, {Lo: 70, Hi: 85}, {Lo: 85, Hi: 95}, {Lo: 95, Hi: 100},
	}
	bucketize(cands, midBuckets, func(c candidate) int { return c.MidAtFire })
	printBuckets(w, "By mid bucket", midBuckets, "mid_¢")

	edgeBuckets := []bucket{
		{Lo: 3, Hi: 5}, {Lo: 5, Hi: 10}, {Lo: 10, Hi: 20},
		{Lo: 20, Hi: 50}, {Lo: 50, Hi: 100},
	}
	bucketize(cands, edgeBuckets, func(c candidate) int { return int(c.EdgeCents) })
	printBuckets(w, "By predicted-edge bucket", edgeBuckets, "edge_¢")

	// By location split.
	byLoc := map[string]*bucket{}
	for _, c := range cands {
		b, ok := byLoc[c.Location]
		if !ok {
			b = &bucket{}
			byLoc[c.Location] = b
		}
		b.N++
		if c.Filled {
			b.Filled++
		}
	}
	fmt.Fprintln(w, "By location")
	keys := make([]string, 0, len(byLoc))
	for k := range byLoc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := byLoc[k]
		fmt.Fprintf(w, "  %-4s  n=%-5d  filled=%-5d  rate=%.1f%%\n",
			k, b.N, b.Filled, 100*safeDiv(b.Filled, b.N))
	}
	fmt.Fprintln(w)

	// Side split.
	yesB, noB := bucket{}, bucket{}
	for _, c := range cands {
		var b *bucket
		if c.Side == papertrade.SideYes {
			b = &yesB
		} else {
			b = &noB
		}
		b.N++
		if c.Filled {
			b.Filled++
		}
	}
	fmt.Fprintf(w, "By side\n")
	fmt.Fprintf(w, "  yes   n=%-5d  filled=%-5d  rate=%.1f%%\n", yesB.N, yesB.Filled, 100*safeDiv(yesB.Filled, yesB.N))
	fmt.Fprintf(w, "  no    n=%-5d  filled=%-5d  rate=%.1f%%\n", noB.N, noB.Filled, 100*safeDiv(noB.Filled, noB.N))
}

func bucketize(cands []candidate, buckets []bucket, key func(candidate) int) {
	for _, c := range cands {
		v := key(c)
		for i := range buckets {
			if v >= buckets[i].Lo && v < buckets[i].Hi {
				buckets[i].N++
				if c.Filled {
					buckets[i].Filled++
				}
				break
			}
		}
	}
}

func printBuckets(w *os.File, title string, buckets []bucket, axisLabel string) {
	fmt.Fprintf(w, "%s\n", title)
	fmt.Fprintf(w, "  %-12s  %-6s  %-6s  rate\n", axisLabel, "n", "filled")
	for _, b := range buckets {
		if b.N == 0 {
			continue
		}
		fmt.Fprintf(w, "  [%2d–%2d)        %-6d  %-6d  %.1f%%\n",
			b.Lo, b.Hi, b.N, b.Filled, 100*safeDiv(b.Filled, b.N))
	}
	fmt.Fprintln(w)
}

func safeDiv(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func countFilled(cs []candidate) int {
	n := 0
	for _, c := range cs {
		if c.Filled {
			n++
		}
	}
	return n
}

// settlementUTCFor returns the settlement UTC for a parsed candidate's
// ticker — used by writeCandidatesCSV when laying out diagnostic output.
func settlementUTCFor(c candidate) time.Time {
	tk, err := market.ParseTicker(c.Ticker)
	if err != nil {
		return time.Time{}
	}
	return edge.SettlementUTC(tk.Date, tk.UTCOffset())
}

func writeCandidatesCSV(path string, cands []candidate) error {
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
		"fire_unix_nano", "settlement_utc",
		"filled", "first_fill_lag_seconds", "fill_count", "best_fill_cents",
	})
	for _, c := range cands {
		settle := settlementUTCFor(c)
		settleS := ""
		if !settle.IsZero() {
			settleS = settle.Format(time.RFC3339)
		}
		filled := "0"
		if c.Filled {
			filled = "1"
		}
		_ = w.Write([]string{
			c.Ticker, c.Series, c.Location, string(c.Side),
			fmt.Sprint(c.MidAtFire), fmt.Sprint(c.SpreadAtFire),
			fmt.Sprintf("%.2f", c.EdgeCents),
			fmt.Sprintf("%.2f", c.LeadHours),
			fmt.Sprint(c.FireWall),
			settleS,
			filled,
			fmt.Sprintf("%.2f", c.FirstFillLag.Seconds()),
			fmt.Sprint(c.FillCount),
			fmt.Sprint(c.BestFillCent),
		})
	}
	return nil
}
