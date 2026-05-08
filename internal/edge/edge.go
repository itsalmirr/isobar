// Package edge holds the pure edge-computation kernel shared by
// cmd/edge-scan (offline replay) and cmd/paper-trader (live).
//
// "Edge" here is the fee-aware difference between a probability estimate
// (naive ensemble fraction, optionally MOS-calibrated, optionally
// climate-shrunk) and the orderbook midpoint of a Kalshi binary-outcome
// market, expressed in cents per contract. Positive YesEdgeCents means
// the model thinks YES is underpriced by that many cents after round-trip
// fees; positive NoEdgeCents is the symmetric statement for NO.
//
// The kernel is intentionally I/O-free: no journal reads, no WS, no time
// pairing. Callers feed in the ticker, the orderbook mid+spread, the
// ticker-side daily-extreme ensemble members, and (optionally) MOS
// coefficients + climate lookup. Edge-scan layers replay/poll-pairing on
// top; paper-trader layers live WS/poll handling on top.
package edge

import (
	"math"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
)

// Row is one (ticker, book-snapshot, ensemble-poll) edge observation.
// All "edge" fields are signed cents at the orderbook mid: positive means
// the model's probability beats the market by that many cents per
// contract after round-trip fees.
//
// Calibrated* fields are populated only when MOS coefficients are passed
// AND a fitted bin exists for the (station, season, variable). Otherwise
// they are NaN. Climate* fields follow the same pattern with a climate
// lookup. NaN-handling is left to the caller (CSV emit, ledger record,
// etc).
type Row struct {
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
	LeadHours         float64

	// Calibrated (MOS) fields. NaN when no coefficients are loaded or no
	// matching bin exists.
	CalibratedP            float64
	CalibratedYesEdgeCents float64
	CalibratedNoEdgeCents  float64
	CalibratedMu           float64 // °F, post-bias-correction forecast mean
	CalibratedSigma        float64 // °F, residual stdev of the bin

	// Climate-shrunk-naive baseline. Uses historical bracket frequency
	// from CLI obs as a shrinkage target: p_shrunk = w·climate + (1−w)·naive.
	// NaN when no climate lookup is loaded or no historical observations
	// match the season window.
	ClimateP             float64 // raw climatological frequency, no naive blend
	ClimateShrunkP       float64 // 0.2·climate + 0.8·naive
	ClimateShrunkYesEdge float64
	ClimateShrunkNoEdge  float64
}

// FromExtremes is the pure edge-computation kernel: given a parsed
// ticker, the orderbook's mid + spread + ticker-side daily-extreme
// members, the poll's age, optional MOS coefficients, and optional
// climate lookup, return a fully-populated Row.
//
// When coeffs is nil OR no fitted bin matches, Calibrated* fields are
// NaN. When clim is nil OR no historical match, ClimateP and Climate*
// fields are NaN.
func FromExtremes(tk market.Ticker, mid, spread int, members [openmeteo.NumMembers]float64, pollAge time.Duration, leadHours float64, coeffs *mos.Coefficients, clim *climate.Lookup) Row {
	naiveP, inBracket := NaiveProbability(members, tk.Type, tk.TempF)
	feeCents := market.RoundTripFeeCents(mid)
	naiveCents := naiveP * 100.0
	eMin, eMax := EnsembleRange(members)

	row := Row{
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

	station, hasStation := stationCodeFor(tk.Location())

	// MOS calibration path.
	if coeffs != nil && hasStation {
		mean := EnsembleMean(members)
		if !math.IsNaN(mean) {
			if mu, sigma, ok := coeffs.Predict(station, tk.Date, TempVarString(tk.TempVar), mean); ok {
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

	// Climate-shrunk-naive baseline.
	if clim != nil && hasStation {
		bracketAbove := tk.Type == market.BracketAbove
		climP := clim.BracketProb(station, TempVarString(tk.TempVar), tk.Date, tk.TempF, bracketAbove)
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

// NaiveProbability returns P(bracket-condition) as fraction of valid
// ensemble members satisfying it, plus the in-bracket count. NaN/Inf
// members are skipped.
func NaiveProbability(members [openmeteo.NumMembers]float64, bt market.BracketType, threshold float64) (float64, int) {
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

// EnsembleRange returns (min, max) of valid (non-NaN, non-Inf) members.
// Returns (+Inf, -Inf) if all members are invalid.
func EnsembleRange(members [openmeteo.NumMembers]float64) (float64, float64) {
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

// EnsembleMean returns the arithmetic mean of valid members, or NaN if
// none are valid.
func EnsembleMean(members [openmeteo.NumMembers]float64) float64 {
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

// SettlementUTC returns the UTC timestamp at which a bracket settles.
// Settlement = end of bracket day in local time = start of (bracket_date+1)
// in local TZ, converted to UTC.
func SettlementUTC(bracketDate time.Time, utcOffsetHours int) time.Time {
	endOfDayLocal := bracketDate.Add(24 * time.Hour)
	return endOfDayLocal.Add(time.Duration(-utcOffsetHours) * time.Hour)
}

// TempVarString maps a market.TempVar to its CLI/MOS string ("tmax" or
// "tmin").
func TempVarString(v market.TempVar) string {
	if v == market.DailyHigh {
		return "tmax"
	}
	return "tmin"
}

// stationCodeFor returns the CLI station code (KNYC etc.) for a ticker's
// location tag, via the canonical registry in internal/market.
func stationCodeFor(loc string) (string, bool) {
	s, ok := market.StationByLocation(loc)
	if !ok {
		return "", false
	}
	return s.Code, true
}
