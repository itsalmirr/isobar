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
}

// Result is the output of Scan: per-trade rows plus aggregate counters.
type Result struct {
	TotalFrames    int
	TradeFrames    int
	WeatherTrades  int
	PairedTrades   int
	Rows           []EdgeRow
	SkippedReason  map[string]int
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
	if opts.EndDate.Before(opts.StartDate) {
		return nil, errors.New("edge: EndDate before StartDate")
	}

	idx, err := loadPolls(filepath.Join(opts.DataDir, "openmeteo"), opts.StartDate, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("load openmeteo: %w", err)
	}

	res := &Result{SkippedReason: map[string]int{}}

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
			res.SkippedReason["non_weather_ticker"]++
			return nil
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

		priceDollars := msg.priceDollars()
		if priceDollars < 0 {
			res.SkippedReason["bad_trade_price"]++
			return nil
		}

		row := EdgeRow{
			TsMs:            msg.TsMs,
			Ticker:          spec.Ticker,
			Series:          spec.Series,
			ValidDate:       spec.ValidDate,
			BracketKind:     spec.Kind.String(),
			BracketKey:      spec.Key,
			Side:            msg.Side,
			TradePriceCents: int(priceDollars*100 + 0.5),
			PFairNaive:      pFair,
			NMembers:        nMembers,
			NMembersInBkt:   nIn,
			PollAgeS:        int(age.Seconds()),
			FeeCents:        FeePerContractCents(priceDollars),
			EdgeCents:       EdgeAfterFeeCents(pFair, priceDollars),
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
