// Package climate computes climatological bracket probabilities from
// historical CLI observations. Used as a baseline against MOS calibration
// per Pass 3 §4.b: if MOS doesn't beat naive-shrunk-to-climate in the
// tradeable midrange, the MOS package isn't earning its complexity.
//
// The shrinkage formula is `p_shrunk = w·climate_p + (1−w)·naive_p`,
// with default w=0.2. Climate_p is the historical empirical frequency
// of the bracket condition, computed per (station, variable) over the
// full ground-truth window, optionally narrowed to a season window
// around the target date.
package climate

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/obscsv"
)

// DefaultShrinkage is the climate weight in `p_shrunk = w·climate_p + (1−w)·naive_p`.
// 0.2 follows the Pass 3 recommendation; treat as a hyperparameter to be
// tuned post-gate if the baseline beats MOS.
const DefaultShrinkage = 0.2

// Lookup is an in-memory store of historical observations keyed by
// (station, variable, day-of-year). Variable is "tmax" or "tmin";
// values are °F.
//
// At Load time the raw observation list is materialised into a
// pre-computed lookup: for each day-of-year (1..366), a sorted slice
// of values from observations within ±SeasonWindowDays. This collapses
// BracketProb from a 730-element linear scan + circular-distance
// filter to a binary search over a ~60-day-window slice, ~50-100x
// faster on production data and constant-time as the history grows.
type Lookup struct {
	obs map[string]map[string][]obsPoint // raw, retained for tests + loadability
	// byDay[station][variable][dayOfYear-1] is the sorted ascending
	// list of observed values from days within ±SeasonWindowDays of
	// dayOfYear (circular calendar). Built by Compile, called at the
	// end of Load.
	byDay map[string]map[string][366][]float64
}

type obsPoint struct {
	monthDay string  // "MM-DD"
	value    float64 // °F
}

// New returns an empty Lookup. Use Load to populate from CSVs.
func New() *Lookup {
	return &Lookup{
		obs:   map[string]map[string][]obsPoint{},
		byDay: map[string]map[string][366][]float64{},
	}
}

// Load reads obs CSVs (data/calibration/obs/K{STA}.csv) for the given
// stations and populates the Lookup. CSV schema:
//
//	valid_date,tmax_f,tmin_f,issued_at,bulletin_id
//
// Days with empty tmax_f or tmin_f are dropped from that variable's
// historical set but still count toward the other variable. Required
// columns (valid_date, tmax_f, tmin_f) are validated against the header;
// a missing column fails loudly rather than silently reading column 0.
func (l *Lookup) Load(obsDir string, stations []string) error {
	for _, sta := range stations {
		path := filepath.Join(obsDir, sta+".csv")
		rows, err := obscsv.ReadFile(path, obscsv.Options{
			Alias:      obscsv.StrictAliases,
			Require:    []string{obscsv.ColDate, obscsv.ColTmax, obscsv.ColTmin},
			StrictRows: true,
		})
		if err != nil {
			return fmt.Errorf("climate: %s: %w", path, err)
		}
		if l.obs[sta] == nil {
			l.obs[sta] = map[string][]obsPoint{}
		}
		for _, row := range rows {
			md := row.Date.Format("01-02")
			// An empty/unparseable tmax_f cell drops that variable's sample
			// but still lets the same row contribute to tmin, and vice versa.
			if row.TmaxPresent {
				l.obs[sta]["tmax"] = append(l.obs[sta]["tmax"], obsPoint{md, row.Tmax})
			}
			if row.TminPresent {
				l.obs[sta]["tmin"] = append(l.obs[sta]["tmin"], obsPoint{md, row.Tmin})
			}
		}
	}
	l.compile()
	return nil
}

// compile builds the per-day-of-year sorted-value tables from the raw
// obs slices. Called automatically at the end of Load.
func (l *Lookup) compile() {
	for sta, vars := range l.obs {
		if l.byDay[sta] == nil {
			l.byDay[sta] = map[string][366][]float64{}
		}
		for v, pts := range vars {
			// Group observations by their day-of-year first.
			perDay := make(map[int][]float64, 366)
			for _, p := range pts {
				doy := dayOfYearMD(p.monthDay)
				perDay[doy] = append(perDay[doy], p.value)
			}
			// For each target day, gather values from days within
			// ±SeasonWindowDays (circular). Sort once for binary search.
			var table [366][]float64
			for target := 1; target <= 366; target++ {
				var window []float64
				for d, vs := range perDay {
					if circDist(target, d) <= SeasonWindowDays {
						window = append(window, vs...)
					}
				}
				sort.Float64s(window)
				table[target-1] = window
			}
			l.byDay[sta][v] = table
		}
	}
}

// SeasonWindowDays is half-width of the calendar window used to restrict
// climatology to the season around the target date. ±30 days = ~2-month
// window centered on the target's month-day. 0 disables restriction.
const SeasonWindowDays = 30

// BracketProb returns the empirical frequency of the bracket condition
// from historical observations for (station, variable), restricted to a
// ±SeasonWindowDays calendar window around target.
//
// For "above": fraction of observed values ≥ threshold.
// For "below": fraction of observed values ≤ threshold.
//
// Returns NaN if no historical observations match the season window.
//
// Implementation: O(log N) via binary search over a pre-sorted per-day
// window slice, built at Load time. See Lookup.compile.
func (l *Lookup) BracketProb(station, variable string, target time.Time, threshold float64, bracketAbove bool) float64 {
	window, ok := l.seasonWindow(station, variable, target)
	if !ok {
		return math.NaN()
	}
	n := len(window)
	// window is sorted ascending. For "above": count of values ≥ threshold
	// = n - first-index-≥-threshold. For "below": count of values ≤
	// threshold = last-index-≤-threshold + 1 = first-index-> threshold.
	if bracketAbove {
		i := sort.SearchFloat64s(window, threshold) // first index with value ≥ threshold
		return float64(n-i) / float64(n)
	}
	// "below": values ≤ threshold. Use first index with value > threshold.
	i := sort.Search(n, func(k int) bool { return window[k] > threshold })
	return float64(i) / float64(n)
}

// RangeProb returns the empirical frequency of historical observations inside
// the half-open bracket [lo, hi). Open-ended ranges are supported with +/-Inf.
func (l *Lookup) RangeProb(station, variable string, target time.Time, lo, hi float64) float64 {
	window, ok := l.seasonWindow(station, variable, target)
	if !ok {
		return math.NaN()
	}
	n := len(window)
	if lo >= hi {
		return math.NaN()
	}

	start := 0
	if !math.IsInf(lo, -1) {
		start = sort.SearchFloat64s(window, lo)
	}
	end := n
	if !math.IsInf(hi, +1) {
		end = sort.Search(n, func(k int) bool { return window[k] >= hi })
	}
	if end < start {
		return 0
	}
	return float64(end-start) / float64(n)
}

// seasonWindow returns the pre-sorted ±SeasonWindowDays value window for
// (station, variable) at target's day-of-year, or ok=false when the
// lookup holds no matching data (probability callers return NaN).
func (l *Lookup) seasonWindow(station, variable string, target time.Time) ([]float64, bool) {
	vars, ok := l.byDay[station]
	if !ok {
		return nil, false
	}
	table, ok := vars[variable]
	if !ok {
		return nil, false
	}
	doy := dayOfYear(target)
	if doy < 1 || doy > 366 {
		return nil, false
	}
	window := table[doy-1]
	if len(window) == 0 {
		return nil, false
	}
	return window, true
}

// ShrinkP combines naive ensemble probability and climate probability
// using `w·climate + (1−w)·naive`. NaN climate falls back to naive
// (i.e. behaves like w=0).
func ShrinkP(naive, climate, weight float64) float64 {
	if math.IsNaN(climate) {
		return naive
	}
	return weight*climate + (1-weight)*naive
}

// dayOfYear returns 1..366, leap-year-aware. In leap years, Feb 29 is
// day 60 and Mar 1 is day 61. In non-leap years, Mar 1 is day 60 (no
// Feb 29 slot occurs). This ISO-style mapping disambiguates Feb 29
// observations from Mar 1 in the climate corpus, avoiding the silent
// aliasing the original non-leap-year cumDays scheme produced.
//
// Storage path: obs are keyed via dayOfYearMD, which parses month-day
// against year 0000 (which Go's calendar treats as leap), so every
// distinct month-day gets a unique slot in [1, 366]: Feb 29 → 60,
// Mar 1 → 61, Feb 28 → 59, etc. Query path uses this function on the
// caller-supplied target date; for non-leap year targets after Feb,
// the DOY is one less than the storage slot for the same calendar day,
// but the ±SeasonWindowDays window absorbs that single-day offset (a
// meteorologically negligible shift at our resolution).
func dayOfYear(t time.Time) int {
	mo, d := int(t.Month()), t.Day()
	base := cumDays[mo-1] + d
	if isLeapYear(t.Year()) && mo > 2 {
		base++
	}
	return base
}

// dayOfYearMD parses a "MM-DD" string and returns its day-of-year using
// the leap-year-canonical mapping (Go parses missing-year layouts as
// year 0000, which is a leap year). This guarantees Feb 29 → 60 and
// Mar 1 → 61 for storage, regardless of the original obs year.
func dayOfYearMD(md string) int {
	t, err := time.Parse("01-02", md)
	if err != nil {
		return 0
	}
	return dayOfYear(t)
}

// isLeapYear reports whether y is a Gregorian leap year (divisible by
// 4, except centuries not divisible by 400).
func isLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

// circDist is the minimum circular distance in days between two
// day-of-year numbers in [1, 366]. The calendar is treated as a
// 366-day cycle so Feb 29 (day 60) and Mar 1 (day 61) are adjacent
// and Dec 31 (day 366) wraps to Jan 1 (day 1) with distance 1.
func circDist(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	if d > 183 {
		return 366 - d
	}
	return d
}

// cumDays[i] is the number of days before month i+1 in a non-leap year.
// Indexed 0=Jan offset, 1=Feb offset, ..., 11=Dec offset. Leap-year
// adjustment (+1 for months after February) is applied in dayOfYear.
var cumDays = []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
