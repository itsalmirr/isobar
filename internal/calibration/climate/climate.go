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
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
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
		f, err := os.Open(path) //nolint:gosec // path is operator-supplied calibration dir
		if err != nil {
			return fmt.Errorf("climate: open %s: %w", path, err)
		}
		r := csv.NewReader(f)
		header, err := r.Read()
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("climate: header %s: %w", path, err)
		}
		col, err := requireColumns(header, "valid_date", "tmax_f", "tmin_f")
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("climate: %s: %w", path, err)
		}
		minCols := maxIndex(col) + 1
		if l.obs[sta] == nil {
			l.obs[sta] = map[string][]obsPoint{}
		}
		for {
			rec, err := r.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("climate: read %s: %w", path, err)
			}
			if len(rec) < minCols {
				_ = f.Close()
				return fmt.Errorf("climate: %s: short record (%d cols, need %d)", path, len(rec), minCols)
			}
			d, err := time.Parse("2006-01-02", rec[col["valid_date"]])
			if err != nil {
				continue
			}
			md := d.Format("01-02")
			if s := rec[col["tmax_f"]]; s != "" {
				if v, e := strconv.ParseFloat(s, 64); e == nil {
					l.obs[sta]["tmax"] = append(l.obs[sta]["tmax"], obsPoint{md, v})
				}
			}
			if s := rec[col["tmin_f"]]; s != "" {
				if v, e := strconv.ParseFloat(s, 64); e == nil {
					l.obs[sta]["tmin"] = append(l.obs[sta]["tmin"], obsPoint{md, v})
				}
			}
		}
		_ = f.Close()
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
	vars, ok := l.byDay[station]
	if !ok {
		return math.NaN()
	}
	table, ok := vars[variable]
	if !ok {
		return math.NaN()
	}
	doy := dayOfYear(target)
	if doy < 1 || doy > 366 {
		return math.NaN()
	}
	window := table[doy-1]
	n := len(window)
	if n == 0 {
		return math.NaN()
	}
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

// ShrinkP combines naive ensemble probability and climate probability
// using `w·climate + (1−w)·naive`. NaN climate falls back to naive
// (i.e. behaves like w=0).
func ShrinkP(naive, climate, weight float64) float64 {
	if math.IsNaN(climate) {
		return naive
	}
	return weight*climate + (1-weight)*naive
}

// requireColumns returns a header→index map containing exactly the
// requested columns, or an error naming any that are missing.
func requireColumns(header []string, names ...string) (map[string]int, error) {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	out := make(map[string]int, len(names))
	var missing []string
	for _, n := range names {
		i, ok := idx[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		out[n] = i
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required columns: %v (got header %v)", missing, header)
	}
	return out, nil
}

// maxIndex returns the largest column index in the map.
func maxIndex(col map[string]int) int {
	m := -1
	for _, i := range col {
		if i > m {
			m = i
		}
	}
	return m
}

// dayOfYear returns 1..366 for the date's month-day (year-independent).
// Treats Feb 29 specially: maps Feb 29 to day 60 same as Mar 1, since
// our climatology spans multiple years and we want all "late February"
// observations to compare equally.
func dayOfYear(t time.Time) int {
	mo, d := int(t.Month()), t.Day()
	return cumDays[mo-1] + d
}

func dayOfYearMD(md string) int {
	t, err := time.Parse("01-02", md)
	if err != nil {
		return 0
	}
	return dayOfYear(t)
}

// circDist is the minimum circular distance in days between two
// year-of-day numbers in [1, 366].
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
// Indexed 0=Jan offset, 1=Feb offset, ..., 11=Dec offset. We treat all
// years as 366 days for circular distance — the off-by-one Feb 28 vs
// Mar 1 is meteorologically negligible at our resolution.
var cumDays = []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
