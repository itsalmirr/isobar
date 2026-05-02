package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"time"

	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

// EdgeRow is one (ticker, book-snapshot, paired-poll) edge observation.
// All "edge" fields are signed cents at the orderbook mid: positive means
// the model's naive probability beats the market by that many cents per
// contract after round-trip fees.
type EdgeRow struct {
	Ticker            string
	Series            string
	Location          string
	BracketType       string
	TempF             float64
	MarketMidCents    int
	MarketSpreadCents int
	NaiveP            float64
	FeeRoundTripCents int
	YesEdgeCents      float64
	NoEdgeCents       float64
	EnsembleMinF      float64
	EnsembleMaxF      float64
	EnsembleSpreadF   float64
	MembersInBracket  int
	PollAgeSeconds    float64
}

// pollEntry is an ensemble poll with metadata for time-based pairing.
type pollEntry struct {
	poll *openmeteo.EnsemblePoll
	loc  string
	wall int64
}

// nearestPoll finds the poll closest in time to wall, restricted to loc.
// Returns nil + math.MaxInt64 duration if no poll matches the location.
func nearestPoll(polls []*pollEntry, loc string, wall int64) (*openmeteo.EnsemblePoll, time.Duration) {
	var best *openmeteo.EnsemblePoll
	bestGap := time.Duration(math.MaxInt64)
	for _, pe := range polls {
		if pe.loc != loc {
			continue
		}
		gap := time.Duration(abs64(pe.wall - wall))
		if gap < bestGap {
			bestGap = gap
			best = pe.poll
		}
	}
	return best, bestGap
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// naiveProbability returns P(bracket-condition) as fraction of valid ensemble
// members satisfying it, plus the in-bracket count.
func naiveProbability(members [openmeteo.NumMembers]float64, bt market.BracketType, threshold float64) (float64, int) {
	count := 0
	valid := 0
	for _, v := range members {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		valid++
		switch bt {
		case market.BracketAbove:
			if v >= threshold {
				count++
			}
		case market.BracketBelow:
			if v <= threshold {
				count++
			}
		}
	}
	if valid == 0 {
		return 0, 0
	}
	return float64(count) / float64(valid), count
}

// ensembleRange returns (min, max) of valid (non-NaN, non-Inf) members.
// Returns (+Inf, -Inf) if all members are invalid.
func ensembleRange(members [openmeteo.NumMembers]float64) (float64, float64) {
	lo := math.Inf(1)
	hi := math.Inf(-1)
	for _, v := range members {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// edgeFromExtremes is the pure edge-computation kernel: given a parsed
// ticker, the orderbook's mid + spread + ticker-side daily-extreme members,
// and the poll's age, return a fully-populated EdgeRow.
//
// This is the testable core. The caller (computeEdge below) handles the
// I/O of looking up a poll and extracting daily extremes.
func edgeFromExtremes(tk market.Ticker, mid, spread int, members [openmeteo.NumMembers]float64, pollAge time.Duration) EdgeRow {
	naiveP, inBracket := naiveProbability(members, tk.Type, tk.TempF)
	feeCents := market.RoundTripFeeCents(mid)
	naiveCents := naiveP * 100.0
	eMin, eMax := ensembleRange(members)

	return EdgeRow{
		Ticker:            tk.Raw,
		Series:            tk.Series,
		Location:          tk.Location(),
		BracketType:       tk.Type.String(),
		TempF:             tk.TempF,
		MarketMidCents:    mid,
		MarketSpreadCents: spread,
		NaiveP:            naiveP,
		FeeRoundTripCents: feeCents,
		YesEdgeCents:      naiveCents - float64(mid) - float64(feeCents),
		NoEdgeCents:       float64(mid) - naiveCents - float64(feeCents),
		EnsembleMinF:      eMin,
		EnsembleMaxF:      eMax,
		EnsembleSpreadF:   eMax - eMin,
		MembersInBracket:  inBracket,
		PollAgeSeconds:    pollAge.Seconds(),
	}
}

// computeEdge produces an EdgeRow for one (ticker, book) pair using polls.
// Returns ok=false if the row should be skipped:
//   - unparseable ticker (non-weather series, malformed)
//   - one-sided book (mid undefined)
//   - no matching poll within maxGap for the ticker's location
//   - bracket date outside any poll's forecast window
func computeEdge(ticker string, book *replay.Book, wall int64, polls []*pollEntry, maxGap time.Duration) (EdgeRow, bool) {
	tk, err := market.ParseTicker(ticker)
	if err != nil {
		return EdgeRow{}, false
	}
	mid := book.MidYesCents()
	if mid <= 0 || mid >= 100 {
		return EdgeRow{}, false
	}
	poll, gap := nearestPoll(polls, tk.Location(), wall)
	if poll == nil || gap > maxGap {
		return EdgeRow{}, false
	}
	maxTemps, minTemps, err := poll.DailyExtremes(tk.Date, tk.UTCOffset())
	if err != nil {
		return EdgeRow{}, false
	}
	var members [openmeteo.NumMembers]float64
	switch tk.TempVar {
	case market.DailyHigh:
		members = maxTemps
	case market.DailyLow:
		members = minTemps
	}
	return edgeFromExtremes(tk, mid, book.SpreadYesCents(), members, gap), true
}

// edgeCSVHeader returns the header row for edge-scan CSV output.
func edgeCSVHeader() []string {
	return []string{
		"date", "ticker", "series", "location", "bracket_type", "temp_f",
		"market_mid_cents", "market_spread_cents",
		"naive_p", "fee_round_trip_cents",
		"yes_edge_cents", "no_edge_cents",
		"ensemble_min_f", "ensemble_max_f", "ensemble_spread_f",
		"members_in_bracket", "poll_age_seconds",
	}
}

// writeEdgeRow writes one EdgeRow as a CSV line under the given date.
func writeEdgeRow(w *csv.Writer, date string, r EdgeRow) error {
	return w.Write([]string{
		date, r.Ticker, r.Series, r.Location, r.BracketType,
		fmt.Sprintf("%.1f", r.TempF),
		fmt.Sprintf("%d", r.MarketMidCents),
		fmt.Sprintf("%d", r.MarketSpreadCents),
		fmt.Sprintf("%.4f", r.NaiveP),
		fmt.Sprintf("%d", r.FeeRoundTripCents),
		fmt.Sprintf("%.2f", r.YesEdgeCents),
		fmt.Sprintf("%.2f", r.NoEdgeCents),
		fmt.Sprintf("%.1f", r.EnsembleMinF),
		fmt.Sprintf("%.1f", r.EnsembleMaxF),
		fmt.Sprintf("%.1f", r.EnsembleSpreadF),
		fmt.Sprintf("%d", r.MembersInBracket),
		fmt.Sprintf("%.0f", r.PollAgeSeconds),
	})
}
