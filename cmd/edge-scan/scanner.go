package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/edge"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

// EdgeRow is the per-row CSV view of an offline scan. The compute kernel
// lives in internal/edge.Row; this is a type alias kept for source
// stability of callers that reference the old name.
type EdgeRow = edge.Row

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

// edgeFromExtremes wraps internal/edge.FromExtremes for source stability;
// the kernel was lifted to the shared package so cmd/paper-trader can
// reuse it without a build-graph cycle through cmd/edge-scan.
func edgeFromExtremes(tk market.Ticker, mid, spread int, members [openmeteo.NumMembers]float64, pollAge time.Duration, leadHours float64, coeffs *mos.Coefficients, clim *climate.Lookup) EdgeRow {
	return edge.FromExtremes(tk, mid, spread, members, pollAge, leadHours, coeffs, clim)
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
	settle := edge.SettlementUTC(tk.Date, tk.UTCOffset())
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
