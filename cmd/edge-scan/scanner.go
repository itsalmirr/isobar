package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

// locationToStation maps the ticker's location code (used in
// config/config.example.yaml) to the CLI station identifier MOS uses.
// LAX uses the airport (KLAX), CHI uses Midway (KMDW per ADR-0007).
var locationToStation = map[string]string{
	"NY":  "KNYC",
	"MIA": "KMIA",
	"LAX": "KLAX",
	"CHI": "KMDW",
}

func tempVarString(v market.TempVar) string {
	if v == market.DailyHigh {
		return "tmax"
	}
	return "tmin"
}

// EdgeRow is one (ticker, book-snapshot, paired-poll) edge observation.
// All "edge" fields are signed cents at the orderbook mid: positive means
// the model's probability beats the market by that many cents per contract
// after round-trip fees.
//
// Calibrated* fields are populated only when --mos coefficients are loaded
// AND a fitted bin exists for the (station, season, variable). Otherwise
// they are NaN and serialise to empty CSV cells.
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

	// Calibrated (MOS) fields. NaN when no coefficients are loaded or no
	// matching bin exists.
	CalibratedP            float64
	CalibratedYesEdgeCents float64
	CalibratedNoEdgeCents  float64
	CalibratedMu           float64 // °F, post-bias-correction forecast mean
	CalibratedSigma        float64 // °F, residual stdev of the bin
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

// ensembleMean returns the arithmetic mean of valid members.
func ensembleMean(members [openmeteo.NumMembers]float64) float64 {
	sum := 0.0
	n := 0
	for _, v := range members {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// edgeFromExtremes is the pure edge-computation kernel: given a parsed
// ticker, the orderbook's mid + spread + ticker-side daily-extreme members,
// the poll's age, and optional MOS coefficients, return a fully-populated
// EdgeRow.
//
// When coeffs is nil OR no fitted bin matches the (station, season, var),
// the Calibrated* fields are NaN. When a bin matches, the calibrated
// probability is Φ((threshold − μ_calibrated) / σ_residual) for "below"
// brackets, 1 − Φ for "above" brackets, and the calibrated edge is
// computed identically to the naive one.
//
// This is the testable core. The caller (computeEdge below) handles the
// I/O of looking up a poll and extracting daily extremes.
func edgeFromExtremes(tk market.Ticker, mid, spread int, members [openmeteo.NumMembers]float64, pollAge time.Duration, coeffs *mos.Coefficients) EdgeRow {
	naiveP, inBracket := naiveProbability(members, tk.Type, tk.TempF)
	feeCents := market.RoundTripFeeCents(mid)
	naiveCents := naiveP * 100.0
	eMin, eMax := ensembleRange(members)

	row := EdgeRow{
		Ticker:                 tk.Raw,
		Series:                 tk.Series,
		Location:               tk.Location(),
		BracketType:            tk.Type.String(),
		TempF:                  tk.TempF,
		MarketMidCents:         mid,
		MarketSpreadCents:      spread,
		NaiveP:                 naiveP,
		FeeRoundTripCents:      feeCents,
		YesEdgeCents:           naiveCents - float64(mid) - float64(feeCents),
		NoEdgeCents:            float64(mid) - naiveCents - float64(feeCents),
		EnsembleMinF:           eMin,
		EnsembleMaxF:           eMax,
		EnsembleSpreadF:        eMax - eMin,
		MembersInBracket:       inBracket,
		PollAgeSeconds:         pollAge.Seconds(),
		CalibratedP:            math.NaN(),
		CalibratedYesEdgeCents: math.NaN(),
		CalibratedNoEdgeCents:  math.NaN(),
		CalibratedMu:           math.NaN(),
		CalibratedSigma:        math.NaN(),
	}

	if coeffs == nil {
		return row
	}
	station, ok := locationToStation[tk.Location()]
	if !ok {
		return row
	}
	mean := ensembleMean(members)
	if math.IsNaN(mean) {
		return row
	}
	mu, sigma, ok := coeffs.Predict(station, tk.Date, tempVarString(tk.TempVar), mean)
	if !ok {
		return row
	}
	var calP float64
	switch tk.Type {
	case market.BracketBelow:
		calP = mos.ProbBelow(tk.TempF, mu, sigma)
	case market.BracketAbove:
		calP = mos.ProbAbove(tk.TempF, mu, sigma)
	}
	calCents := calP * 100.0
	row.CalibratedP = calP
	row.CalibratedYesEdgeCents = calCents - float64(mid) - float64(feeCents)
	row.CalibratedNoEdgeCents = float64(mid) - calCents - float64(feeCents)
	row.CalibratedMu = mu
	row.CalibratedSigma = sigma
	return row
}

// computeEdge produces an EdgeRow for one (ticker, book) pair using polls
// and (optionally) MOS coefficients. Returns ok=false if the row should be
// skipped: unparseable ticker, one-sided book, no matching poll within
// maxGap, or bracket date outside any poll's forecast window.
//
// coeffs may be nil; when nil, EdgeRow.Calibrated* fields are NaN.
func computeEdge(ticker string, book *replay.Book, wall int64, polls []*pollEntry, maxGap time.Duration, coeffs *mos.Coefficients) (EdgeRow, bool) {
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
	return edgeFromExtremes(tk, mid, book.SpreadYesCents(), members, gap, coeffs), true
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
		// MOS-calibrated columns. Empty when --mos coefficients aren't loaded
		// or no fitted bin matches.
		"calibrated_p", "calibrated_mu_f", "calibrated_sigma_f",
		"calibrated_yes_edge_cents", "calibrated_no_edge_cents",
	}
}

// fmtNaN renders math.NaN as the empty string and a float as "%.<prec>f"
// otherwise. Used to emit MOS columns as blank when no bin matched.
func fmtNaN(v float64, prec int) string {
	if math.IsNaN(v) {
		return ""
	}
	return fmt.Sprintf("%.*f", prec, v)
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
		fmtNaN(r.CalibratedP, 4),
		fmtNaN(r.CalibratedMu, 1),
		fmtNaN(r.CalibratedSigma, 2),
		fmtNaN(r.CalibratedYesEdgeCents, 2),
		fmtNaN(r.CalibratedNoEdgeCents, 2),
	})
}
