package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
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

	// LeadHours is the time from the book-state snapshot to bracket
	// settlement (settlement = midnight local on bracket_date+1). Used
	// for lead-time stratification at analysis time.
	LeadHours float64

	// Calibrated (MOS) fields. NaN when no coefficients are loaded or no
	// matching bin exists.
	CalibratedP            float64
	CalibratedYesEdgeCents float64
	CalibratedNoEdgeCents  float64
	CalibratedMu           float64 // °F, post-bias-correction forecast mean
	CalibratedSigma        float64 // °F, residual stdev of the bin

	// Climate-shrunk-naive baseline. Uses historical bracket frequency
	// from CLI obs as a shrinkage target: p_shrunk = w·climate + (1-w)·naive.
	// NaN when no climate lookup is loaded or no historical observations
	// match the season window. See Pass 3 §4.b.
	ClimateP             float64 // raw climatological frequency, no naive blend
	ClimateShrunkP       float64 // 0.2·climate + 0.8·naive
	ClimateShrunkYesEdge float64
	ClimateShrunkNoEdge  float64
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

// settlementUTC returns the UTC timestamp at which a bracket settles.
// Settlement = end of bracket day in local time = start of (bracket_date+1)
// in local TZ, converted to UTC.
func settlementUTC(bracketDate time.Time, utcOffsetHours int) time.Time {
	endOfDayLocal := bracketDate.Add(24 * time.Hour)
	return endOfDayLocal.Add(time.Duration(-utcOffsetHours) * time.Hour)
}

// edgeFromExtremes is the pure edge-computation kernel: given a parsed
// ticker, the orderbook's mid + spread + ticker-side daily-extreme members,
// the poll's age, optional MOS coefficients, and optional climate lookup,
// return a fully-populated EdgeRow.
//
// When coeffs is nil OR no fitted bin matches, Calibrated* fields are NaN.
// When climate is nil OR no historical match, ClimateP and Climate*Edge
// fields are NaN.
//
// This is the testable core. The caller (computeEdge below) handles the
// I/O of looking up a poll and extracting daily extremes.
func edgeFromExtremes(tk market.Ticker, mid, spread int, members [openmeteo.NumMembers]float64, pollAge time.Duration, leadHours float64, coeffs *mos.Coefficients, clim *climate.Lookup) EdgeRow {
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
		LeadHours:              leadHours,
		CalibratedP:            math.NaN(),
		CalibratedYesEdgeCents: math.NaN(),
		CalibratedNoEdgeCents:  math.NaN(),
		CalibratedMu:           math.NaN(),
		CalibratedSigma:        math.NaN(),
		ClimateP:               math.NaN(),
		ClimateShrunkP:         math.NaN(),
		ClimateShrunkYesEdge:   math.NaN(),
		ClimateShrunkNoEdge:    math.NaN(),
	}

	station, hasStation := locationToStation[tk.Location()]

	// MOS calibration path.
	if coeffs != nil && hasStation {
		mean := ensembleMean(members)
		if !math.IsNaN(mean) {
			if mu, sigma, ok := coeffs.Predict(station, tk.Date, tempVarString(tk.TempVar), mean); ok {
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
			}
		}
	}

	// Climate-shrunk-naive baseline. Per Pass 3 §4.b, this tests whether
	// MOS earns its complexity vs trivial shrinkage.
	if clim != nil && hasStation {
		bracketAbove := tk.Type == market.BracketAbove
		climP := clim.BracketProb(station, tempVarString(tk.TempVar), tk.Date, tk.TempF, bracketAbove)
		if !math.IsNaN(climP) {
			row.ClimateP = climP
			shrunkP := climate.ShrinkP(naiveP, climP, climate.DefaultShrinkage)
			row.ClimateShrunkP = shrunkP
			shrunkCents := shrunkP * 100.0
			row.ClimateShrunkYesEdge = shrunkCents - float64(mid) - float64(feeCents)
			row.ClimateShrunkNoEdge = float64(mid) - shrunkCents - float64(feeCents)
		}
	}
	return row
}

// computeEdge produces an EdgeRow for one (ticker, book) pair using polls
// and (optionally) MOS coefficients + climate lookup. Returns ok=false if
// the row should be skipped: unparseable ticker, one-sided book, no
// matching poll within maxGap, or bracket date outside any poll's
// forecast window.
//
// coeffs and clim may both be nil; when nil, the corresponding CSV
// columns render as empty cells.
func computeEdge(ticker string, book *replay.Book, wall int64, polls []*pollEntry, maxGap time.Duration, coeffs *mos.Coefficients, clim *climate.Lookup) (EdgeRow, bool) {
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
	settle := settlementUTC(tk.Date, tk.UTCOffset())
	bookTime := time.Unix(0, wall).UTC()
	leadHours := settle.Sub(bookTime).Hours()
	return edgeFromExtremes(tk, mid, book.SpreadYesCents(), members, gap, leadHours, coeffs, clim), true
}

// EdgeCSVSchemaVersion identifies the column shape this binary emits.
// Bump on any column add, remove, rename, or semantic change. Downstream
// consumers (analysis scripts, future tools) should validate this and
// refuse to parse mismatched versions.
const EdgeCSVSchemaVersion = "edge-scan/v2"

// EdgeCSVSchemaComment is the leading line emitted before the header.
// Comma-separated CSV parsers tolerate an unparsed-then-skipped header
// line; pandas needs `comment='#'` or `skiprows=1`.
var EdgeCSVSchemaComment = "# schema: " + EdgeCSVSchemaVersion

// edgeCSVHeader returns the header row for edge-scan CSV output.
func edgeCSVHeader() []string {
	return []string{
		"date", "ticker", "series", "location", "bracket_type", "temp_f",
		"market_mid_cents", "market_spread_cents",
		"naive_p", "fee_round_trip_cents",
		"yes_edge_cents", "no_edge_cents",
		"ensemble_min_f", "ensemble_max_f", "ensemble_spread_f",
		"members_in_bracket", "poll_age_seconds",
		"lead_hours",
		// MOS-calibrated columns. Empty when --mos coefficients aren't loaded
		// or no fitted bin matches.
		"calibrated_p", "calibrated_mu_f", "calibrated_sigma_f",
		"calibrated_yes_edge_cents", "calibrated_no_edge_cents",
		// Climate-shrunk-naive baseline columns. Empty when no historical
		// observations match. See Pass 3 §4.b.
		"climate_p", "climate_shrunk_p",
		"climate_shrunk_yes_edge_cents", "climate_shrunk_no_edge_cents",
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
		fmt.Sprintf("%.2f", r.LeadHours),
		fmtNaN(r.CalibratedP, 4),
		fmtNaN(r.CalibratedMu, 1),
		fmtNaN(r.CalibratedSigma, 2),
		fmtNaN(r.CalibratedYesEdgeCents, 2),
		fmtNaN(r.CalibratedNoEdgeCents, 2),
		fmtNaN(r.ClimateP, 4),
		fmtNaN(r.ClimateShrunkP, 4),
		fmtNaN(r.ClimateShrunkYesEdge, 2),
		fmtNaN(r.ClimateShrunkNoEdge, 2),
	})
}
