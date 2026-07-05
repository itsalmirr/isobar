// Package edge provides the offline tape-scanner that walks recorded
// Kalshi trade events, pairs them to Open-Meteo ensemble polls, and
// computes naive p_fair / fee / edge per trade for Phase 0.5 review.
//
// Intentional scope: this is the *naive* lower bound. p_fair is the
// raw fraction of GFS ensemble members predicting the bracket outcome —
// no NGR, no ECMWF blend, no T−3h Bayes, no conformal interval. If
// edge density appears at this bound, calibration only improves it.
package edge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is the structured parse of a Kalshi weather-bracket ticker.
//
// Examples that produce these fields:
//
//	"KXHIGHNY-26APR29-B62.5"  → Series=KXHIGHNY,  ValidDate=2026-04-29, Kind=BracketBetween, Key=62.5
//	"KXHIGHCHI-26APR29-T61"   → Series=KXHIGHCHI, ValidDate=2026-04-29, Kind=BracketThreshold, Key=61
//	"KXLOWTNYC-26MAY01-B48"   → Series=KXLOWTNYC, ValidDate=2026-05-01, Kind=BracketBetween, Key=48
type Spec struct {
	Ticker    string
	Series    string
	ValidDate time.Time // UTC midnight on the resolution civil date
	Kind      BracketKind
	Key       float64
}

// BracketKind discriminates the suffix family. The actual temperature-range
// semantics live in [Resolution] (computed separately) — this enum only
// captures the lexical kind.
type BracketKind int

const (
	BracketUnknown BracketKind = iota
	// BracketBetween: ticker suffix "B<num>", typically a midpoint label
	// for a contiguous bracket. Examples: B62.5, B48, B85.5.
	BracketBetween
	// BracketThreshold: ticker suffix "T<num>", typically a one-sided
	// threshold market (e.g. high ≥ N or low ≤ N). Examples: T61, T56.
	BracketThreshold
)

func (k BracketKind) String() string {
	switch k {
	case BracketBetween:
		return "B"
	case BracketThreshold:
		return "T"
	}
	return "?"
}

// ErrNotWeatherTicker is returned for tickers that don't look like a
// weather-bracket ticker (wrong prefix, wrong shape, etc.). The scanner
// uses errors.Is on this to silently skip non-weather frames rather
// than aborting.
var ErrNotWeatherTicker = errors.New("edge: not a weather-bracket ticker")

// validSeries are the Kalshi weather series we know about. Recorded
// data outside this set is skipped, not parsed.
var validSeries = map[string]bool{
	"KXHIGHNY": true, "KXHIGHMIA": true, "KXHIGHLAX": true, "KXHIGHCHI": true,
	"KXLOWTNYC": true, "KXLOWTMIA": true, "KXLOWTLAX": true, "KXLOWTCHI": true,
}

// monthAbbrev maps Kalshi's 3-letter month codes (uppercase) to
// time.Month. Non-matches return zero, surfaced as a parse error.
var monthAbbrev = map[string]time.Month{
	"JAN": time.January, "FEB": time.February, "MAR": time.March,
	"APR": time.April, "MAY": time.May, "JUN": time.June,
	"JUL": time.July, "AUG": time.August, "SEP": time.September,
	"OCT": time.October, "NOV": time.November, "DEC": time.December,
}

// ParseTicker splits a Kalshi weather-bracket ticker into its three
// components (series, date, bracket suffix) and returns a Spec.
//
// The format is `<SERIES>-<YYMMM><DD>-<KIND><KEY>` where:
//   - SERIES is one of [validSeries]
//   - YY is two-digit year (interpreted in the 2000s)
//   - MMM is uppercase 3-letter month
//   - DD is two-digit day
//   - KIND is "B" or "T" (other letters → ErrNotWeatherTicker)
//   - KEY is a decimal number (may include ".5")
//
// Returns [ErrNotWeatherTicker] for shapes we don't trade against; any
// other error indicates a parse-level problem on a ticker we expected
// to recognise (a real bug, not noise).
func ParseTicker(ticker string) (Spec, error) {
	parts := strings.Split(ticker, "-")
	if len(parts) != 3 {
		return Spec{}, fmt.Errorf("%w: want 3 dash-segments, got %d (%q)",
			ErrNotWeatherTicker, len(parts), ticker)
	}

	series := parts[0]
	if !validSeries[series] {
		return Spec{}, fmt.Errorf("%w: unknown series %q", ErrNotWeatherTicker, series)
	}

	date, err := parseKalshiDate(parts[1])
	if err != nil {
		return Spec{}, fmt.Errorf("ticker %q: %w", ticker, err)
	}

	kind, key, err := parseBracketSuffix(parts[2])
	if err != nil {
		return Spec{}, fmt.Errorf("ticker %q: %w", ticker, err)
	}

	return Spec{
		Ticker:    ticker,
		Series:    series,
		ValidDate: date,
		Kind:      kind,
		Key:       key,
	}, nil
}

// parseKalshiDate parses "26APR29" into time.Date(2026, April, 29, 0,0,0,0, UTC).
func parseKalshiDate(s string) (time.Time, error) {
	if len(s) != 7 {
		return time.Time{}, fmt.Errorf("%w: date segment %q want 7 chars",
			ErrNotWeatherTicker, s)
	}
	yy, err := strconv.Atoi(s[0:2])
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: bad year in %q", ErrNotWeatherTicker, s)
	}
	month, ok := monthAbbrev[s[2:5]]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: bad month in %q", ErrNotWeatherTicker, s)
	}
	dd, err := strconv.Atoi(s[5:7])
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: bad day in %q", ErrNotWeatherTicker, s)
	}
	if dd < 1 || dd > 31 {
		return time.Time{}, fmt.Errorf("%w: day out of range in %q", ErrNotWeatherTicker, s)
	}
	return time.Date(2000+yy, month, dd, 0, 0, 0, 0, time.UTC), nil
}

// parseBracketSuffix splits "B62.5" → (BracketBetween, 62.5) etc.
func parseBracketSuffix(s string) (BracketKind, float64, error) {
	if len(s) < 2 {
		return BracketUnknown, 0, fmt.Errorf("%w: empty bracket suffix", ErrNotWeatherTicker)
	}
	var kind BracketKind
	switch s[0] {
	case 'B':
		kind = BracketBetween
	case 'T':
		kind = BracketThreshold
	default:
		return BracketUnknown, 0, fmt.Errorf("%w: unknown bracket prefix %q",
			ErrNotWeatherTicker, s[:1])
	}
	key, err := strconv.ParseFloat(s[1:], 64)
	if err != nil {
		return BracketUnknown, 0, fmt.Errorf("%w: bad bracket key %q",
			ErrNotWeatherTicker, s[1:])
	}
	return kind, key, nil
}

// Station returns the ASOS-style station code Kalshi resolves on for a
// given series. The Chicago series intentionally returns KMDW (Midway),
// not KORD (O'Hare) — see ADR-0007 and commit 35d5622.
func (s Spec) Station() string {
	switch s.Series {
	case "KXHIGHNY", "KXLOWTNYC":
		return "KNYC"
	case "KXHIGHMIA", "KXLOWTMIA":
		return "KMIA"
	case "KXHIGHLAX", "KXLOWTLAX":
		return "KLAX"
	case "KXHIGHCHI", "KXLOWTCHI":
		return "KMDW"
	}
	return ""
}

// IsHigh reports whether the series resolves on the daily high (vs low).
// KXHIGH* are highs; KXLOWT* are lows.
func (s Spec) IsHigh() bool {
	return strings.HasPrefix(s.Series, "KXHIGH")
}

// Variable returns the calibration variable the series resolves on:
// "tmax" for KXHIGH* (daily high), "tmin" for KXLOWT* (daily low).
// This is the key the MOS, climate, and Bayesian layers bin by.
func (s Spec) Variable() string {
	if s.IsHigh() {
		return "tmax"
	}
	return "tmin"
}
