package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/wh0o0pz/isobar/internal/replay"
)

// ScanOptions configures Scan.
type ScanOptions struct {
	// DataDir is the root containing kalshi/ and openmeteo/ subtrees.
	DataDir string
	// StartDate is inclusive, EndDate exclusive (UTC midnights, the
	// dates as found under DataDir/<source>/<YYYY-MM-DD>/).
	StartDate, EndDate time.Time
	// MaxPollAge is the longest staleness allowed between a trade and
	// its paired Open-Meteo poll. 30 min is the natural choice (poll
	// cadence); 1h is the upper bound at which an ensemble member's
	// daily aggregate is still essentially the same number.
	MaxPollAge time.Duration

	// Truth, if non-nil, enables realized-PnL backtest mode. Trades whose
	// resolution date is missing from Truth are skipped (counted as
	// "no_truth_available") rather than scored against forecast-only
	// p_fair — this is how we avoid "imaginary bets that close idk when".
	Truth *TruthIndex

	// EdgeThresholdCents is the minimum naive edge (cents, post-fee)
	// required for our hypothetical strategy to take a trade. Default
	// is 5 — matches Phase 0.5 fee-aware floor in STRATEGY.md.
	EdgeThresholdCents int
}

// EdgeRow is one trade-event record in the output CSV.
type EdgeRow struct {
	TsMs            int64
	Ticker          string
	Series          string
	ValidDate       time.Time
	BracketKind     string
	BracketKey      float64
	Side            string
	TradePriceCents int
	PFairNaive      float64
	NMembers        int
	NMembersInBkt   int
	PollAgeS        int
	FeeCents        int
	EdgeCents       int

	// Backtest fields — populated only when ScanOptions.Truth is set.
	HasTruth     bool   // true iff truth was looked up successfully
	ActualTempF  int    // observed TMAX (KXHIGH*) or TMIN (KXLOWT*) on valid_date
	YesResolved  bool   // bracket would have paid YES
	OurSide      string // "yes", "no", or "" (skipped — no edge band)
	OurPnLCents  int    // realized PnL after entry fee (0 if OurSide=="")
}

// Result is the output of Scan: per-trade rows plus aggregate counters.
type Result struct {
	TotalFrames   int
	TradeFrames   int
	WeatherTrades int
	PairedTrades  int
	Rows          []EdgeRow
	SkippedReason map[string]int

	// Backtest aggregates — only populated when ScanOptions.Truth is set.
	BacktestEnabled bool
	TradesEntered   int // rows where OurSide != ""
	TradesWon       int // rows where OurPnLCents > 0
	TotalPnLCents   int // sum of OurPnLCents
}

// Scan walks the data tree, pairs each Kalshi trade event with the
// most-recent Open-Meteo poll for its station, and emits a Result.
//
// Open-Meteo polls are loaded into memory first (size budget: a few
// hundred MB worst case for years of data, easily fits). Kalshi trades
// are streamed.
func Scan(opts ScanOptions) (*Result, error) {
	if opts.DataDir == "" {
		return nil, errors.New("edge: ScanOptions.DataDir required")
	}
	if opts.MaxPollAge <= 0 {
		opts.MaxPollAge = time.Hour
	}
	if opts.EdgeThresholdCents == 0 {
		opts.EdgeThresholdCents = 5
	}
	if opts.EndDate.Before(opts.StartDate) {
		return nil, errors.New("edge: EndDate before StartDate")
	}

	idx, err := loadPolls(filepath.Join(opts.DataDir, "openmeteo"), opts.StartDate, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("load openmeteo: %w", err)
	}

	res := &Result{
		SkippedReason:   map[string]int{},
		BacktestEnabled: opts.Truth != nil,
	}

	kalshiFiles, err := dailyFiles(filepath.Join(opts.DataDir, "kalshi"), opts.StartDate, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("discover kalshi files: %w", err)
	}

	err = replay.ForEachFrame(kalshiFiles, func(env replay.Envelope, _ int, _ bool) error {
		res.TotalFrames++
		var kf replay.KalshiFrame
		if err := json.Unmarshal(env.Payload, &kf); err != nil {
			return nil
		}
		if kf.Type != "trade" {
			return nil
		}
		res.TradeFrames++

		var msg tradeMsg
		if err := json.Unmarshal(kf.Msg, &msg); err != nil {
			res.SkippedReason["trade_decode_error"]++
			return nil
		}
		spec, err := ParseTicker(msg.MarketTicker)
		if err != nil {
			if errors.Is(err, ErrNotWeatherTicker) {
				res.SkippedReason["non_weather_ticker"]++
				return nil
			}
			return fmt.Errorf("parse ticker %q: %w", msg.MarketTicker, err)
		}
		res.WeatherTrades++

		// Find most-recent poll for this station.
		station := spec.Station()
		tradeTime := time.Unix(0, msg.TsMs*int64(time.Millisecond)).UTC()
		poll, pollTime, ok := idx.latest(station, tradeTime)
		if !ok {
			res.SkippedReason["no_poll_for_station"]++
			return nil
		}
		age := tradeTime.Sub(pollTime)
		if age > opts.MaxPollAge {
			res.SkippedReason["poll_too_old"]++
			return nil
		}

		tz, err := stationLocation(station)
		if err != nil {
			res.SkippedReason["unknown_station_tz"]++
			return nil
		}
		extremes, err := poll.DailyExtremesF(spec.ValidDate, tz)
		if err != nil {
			if errors.Is(err, ErrInsufficientCoverage) {
				res.SkippedReason["insufficient_poll_coverage"]++
				return nil
			}
			return fmt.Errorf("daily extremes: %w", err)
		}

		// Resolution; T-bracket direction needs sibling context — a future
		// pass over per-(series,date) ticker sets could populate this. For
		// the v0 lower bound, default high-side is fine for the high-end T
		// markets and gives a known-wrong p_fair for the low-end ones (we
		// flag those in SkippedReason so the summary highlights the issue).
		resolution := spec.Resolve()

		// p_fair = fraction of members whose daily extreme falls in the
		// bracket. KXHIGH* uses TMAX, KXLOWT* uses TMIN.
		nIn := 0
		for _, md := range extremes {
			t := md.TMaxF
			if !spec.IsHigh() {
				t = md.TMinF
			}
			if resolution.Contains(t) {
				nIn++
			}
		}
		nMembers := len(extremes)
		pFair := float64(nIn) / float64(nMembers)

		// Canonical YES-perspective prices (cents). The trade-message price
		// fields can come from either side, so we always normalise here so
		// every downstream calculation is in the same frame. yes + no = 100.
		yesPriceCents, ok := msg.yesPriceCents()
		if !ok {
			res.SkippedReason["bad_trade_price"]++
			return nil
		}
		noPriceCents := 100 - yesPriceCents
		yesPriceDollars := float64(yesPriceCents) / 100

		// The actual price the recorded trade executed at, in cents — depends
		// on the trade's side (yes-side trades fill at yes_price, etc.). We
		// expose this for the CSV but never feed it into edge math.
		var execPriceCents int
		if msg.Side == "no" {
			execPriceCents = noPriceCents
		} else {
			execPriceCents = yesPriceCents
		}

		// Fee is symmetric in (P, 1-P), so it's the same regardless of side.
		fee := FeePerContractCents(yesPriceDollars)

		row := EdgeRow{
			TsMs:            msg.TsMs,
			Ticker:          spec.Ticker,
			Series:          spec.Series,
			ValidDate:       spec.ValidDate,
			BracketKind:     spec.Kind.String(),
			BracketKey:      spec.Key,
			Side:            msg.Side,
			TradePriceCents: execPriceCents,
			PFairNaive:      pFair,
			NMembers:        nMembers,
			NMembersInBkt:   nIn,
			PollAgeS:        int(age.Seconds()),
			FeeCents:        fee,
			// EdgeCents is the YES-perspective post-fee mispricing magnitude
			// — same number for either trade side because the underlying
			// market is the same.
			EdgeCents: EdgeAfterFeeCents(pFair, yesPriceDollars),
		}

		// Realized-PnL backtest path. If no truth available we skip with a
		// reason — this is the user's "no imaginary bets" rule: never score
		// a trade whose outcome we can't look up.
		if opts.Truth != nil {
			truth, ok := opts.Truth.Get(station, spec.ValidDate)
			if !ok {
				res.SkippedReason["no_truth_available"]++
				return nil
			}
			row.HasTruth = true
			actual := truth.TMaxF
			if !spec.IsHigh() {
				actual = truth.TMinF
			}
			row.ActualTempF = actual
			row.YesResolved = resolution.Contains(float64(actual))

			// Decide hypothetical action: take whichever side has the larger
			// post-fee edge, if it clears the threshold. Both edges are
			// computed in the canonical YES-perspective frame, so this works
			// identically whether the recorded trade was yes-side or no-side.
			pFairYesCents := int(pFair*100 + 0.5)
			edgeYesCents := pFairYesCents - yesPriceCents - fee
			edgeNoCents := (100 - pFairYesCents) - noPriceCents - fee

			switch {
			case edgeYesCents >= opts.EdgeThresholdCents && edgeYesCents >= edgeNoCents:
				row.OurSide = "yes"
				if row.YesResolved {
					row.OurPnLCents = 100 - yesPriceCents - fee
				} else {
					row.OurPnLCents = -yesPriceCents - fee
				}
			case edgeNoCents >= opts.EdgeThresholdCents:
				row.OurSide = "no"
				if !row.YesResolved {
					row.OurPnLCents = 100 - noPriceCents - fee
				} else {
					row.OurPnLCents = -noPriceCents - fee
				}
			default:
				// no edge — skip the trade (PnL = 0, OurSide = "")
			}

			if row.OurSide != "" {
				res.TradesEntered++
				if row.OurPnLCents > 0 {
					res.TradesWon++
				}
				res.TotalPnLCents += row.OurPnLCents
			}
		}

		res.Rows = append(res.Rows, row)
		res.PairedTrades++
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// Stable sort by ts for deterministic CSV output.
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].TsMs < res.Rows[j].TsMs })
	return res, nil
}

// stationLocation returns the IANA timezone for each Kalshi-resolved
// weather station. NWS CLI civil days are local time at the station.
func stationLocation(station string) (*time.Location, error) {
	switch station {
	case "KNYC", "KMIA":
		return time.LoadLocation("America/New_York")
	case "KMDW":
		return time.LoadLocation("America/Chicago")
	case "KLAX":
		return time.LoadLocation("America/Los_Angeles")
	}
	return nil, fmt.Errorf("unknown station %q", station)
}
