package market

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type BracketType int

const (
	BracketAbove BracketType = iota // T-prefix: YES if temp >= threshold
	BracketBelow                    // B-prefix: YES if temp <= threshold
)

func (b BracketType) String() string {
	if b == BracketAbove {
		return "above"
	}
	return "below"
}

type TempVar int

const (
	DailyHigh TempVar = iota // KXHIGH series
	DailyLow                 // KXLOWT series
)

type Ticker struct {
	Raw     string
	Series  string
	Date    time.Time
	Type    BracketType
	TempF   float64
	TempVar TempVar
}

// Location returns the Open-Meteo location name for this ticker's series.
var seriesToLocation = map[string]string{
	"KXHIGHNY":  "NY",
	"KXHIGHMIA": "MIA",
	"KXHIGHLAX": "LAX",
	"KXHIGHCHI": "CHI",
	"KXLOWTNYC": "NY",
	"KXLOWTMIA": "MIA",
	"KXLOWTLAX": "LAX",
	"KXLOWTCHI": "CHI",
}

// UTCOffset returns hours behind UTC for each location during US daylight
// saving time (March–November). Edge-scan runs on April–May data.
var locationUTCOffset = map[string]int{
	"NY":  -4, // EDT
	"MIA": -4, // EDT
	"LAX": -7, // PDT
	"CHI": -5, // CDT
}

func (t Ticker) Location() string { return seriesToLocation[t.Series] }

func (t Ticker) UTCOffset() int { return locationUTCOffset[t.Location()] }

func ParseTicker(raw string) (Ticker, error) {
	parts := strings.SplitN(raw, "-", 3)
	if len(parts) != 3 {
		return Ticker{}, fmt.Errorf("market: bad ticker %q: want SERIES-DATE-TYPE", raw)
	}
	series := parts[0]

	var tv TempVar
	switch {
	case strings.HasPrefix(series, "KXHIGH"):
		tv = DailyHigh
	case strings.HasPrefix(series, "KXLOWT"):
		tv = DailyLow
	default:
		return Ticker{}, fmt.Errorf("market: unknown series prefix in %q", raw)
	}

	if _, ok := seriesToLocation[series]; !ok {
		return Ticker{}, fmt.Errorf("market: unknown series %q", series)
	}

	d, err := parseKalshiDate(parts[1])
	if err != nil {
		return Ticker{}, fmt.Errorf("market: bad date in %q: %w", raw, err)
	}

	if len(parts[2]) < 2 {
		return Ticker{}, fmt.Errorf("market: bad bracket in %q", raw)
	}
	var bt BracketType
	switch parts[2][0] {
	case 'T':
		bt = BracketAbove
	case 'B':
		bt = BracketBelow
	default:
		return Ticker{}, fmt.Errorf("market: unknown bracket type %q in %q", parts[2][0:1], raw)
	}

	temp, err := strconv.ParseFloat(parts[2][1:], 64)
	if err != nil {
		return Ticker{}, fmt.Errorf("market: bad temp in %q: %w", raw, err)
	}

	return Ticker{
		Raw:     raw,
		Series:  series,
		Date:    d,
		Type:    bt,
		TempF:   temp,
		TempVar: tv,
	}, nil
}

var kalshiMonths = map[string]time.Month{
	"JAN": time.January, "FEB": time.February, "MAR": time.March,
	"APR": time.April, "MAY": time.May, "JUN": time.June,
	"JUL": time.July, "AUG": time.August, "SEP": time.September,
	"OCT": time.October, "NOV": time.November, "DEC": time.December,
}

// parseKalshiDate parses "26APR22" → 2026-04-22.
func parseKalshiDate(s string) (time.Time, error) {
	if len(s) < 7 {
		return time.Time{}, fmt.Errorf("too short: %q", s)
	}
	yy, err := strconv.Atoi(s[:2])
	if err != nil {
		return time.Time{}, err
	}
	mon, ok := kalshiMonths[s[2:5]]
	if !ok {
		return time.Time{}, fmt.Errorf("unknown month %q", s[2:5])
	}
	dd, err := strconv.Atoi(s[5:])
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(2000+yy, mon, dd, 0, 0, 0, 0, time.UTC), nil
}
