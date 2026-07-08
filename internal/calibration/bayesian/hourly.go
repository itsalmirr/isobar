package bayesian

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/market"
)

// HourlyObs is one IEM ASOS surface-temperature observation. TS is in
// UTC (the fetcher in scripts/calibration-fetch-obs.sh passes
// tz=Etc/UTC); TmpF is the air temperature in °F.
type HourlyObs struct {
	TS   time.Time
	TmpF float64
}

// LoadHourlyCSV parses an IEM ASOS CSV with columns (station, valid, tmpf).
// Missing values ("M"), trace ("T") and blank tmpf are dropped. Rows
// with unparseable timestamps are dropped silently — same best-effort
// policy as internal/edge.LoadTruth and the rest of the calibration
// loaders. Returns rows sorted by Ts ascending.
func LoadHourlyCSV(path string) ([]HourlyObs, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied calibration path
	if err != nil {
		return nil, fmt.Errorf("hourly: open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	var out []HourlyObs
	first := true
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hourly: read %s: %w", path, err)
		}
		if first {
			first = false
			continue
		}
		if len(rec) < 3 {
			continue
		}
		ts, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(rec[2])
		if raw == "" || raw == "M" || raw == "T" {
			continue
		}
		tmpf, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		out = append(out, HourlyObs{TS: ts.UTC(), TmpF: tmpf})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

// HourlyIndex is a station-keyed cache of pre-sorted hourly obs plus
// timezone metadata. Built once at scan startup so per-trade R(t)
// lookups are O(log n) binary-search + O(k) walk forward through at
// most ~24 obs.
type HourlyIndex struct {
	byStation map[string]stationHourly
}

type stationHourly struct {
	Obs []HourlyObs    // sorted by Ts ascending
	TZ  *time.Location // station-local civil-day timezone
}

// LoadHourlyIndex scans dir for K<STA>-hourly.csv files matching the
// canonical station registry (market.AllStationCodes) and returns a
// ready-to-use index. A missing file for a known station is not a
// fatal error — RunningExtremum will simply return ok=false for that
// station — but a present-but-unparseable file is.
func LoadHourlyIndex(dir string) (*HourlyIndex, error) {
	codes := market.AllStationCodes()
	idx := &HourlyIndex{byStation: make(map[string]stationHourly, len(codes))}
	for _, code := range codes {
		tz, err := market.StationTZ(code)
		if err != nil {
			return nil, fmt.Errorf("hourly: %w", err)
		}
		path := filepath.Join(dir, code+"-hourly.csv")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			continue
		}
		obs, err := LoadHourlyCSV(path)
		if err != nil {
			return nil, err
		}
		idx.byStation[code] = stationHourly{Obs: obs, TZ: tz}
	}
	return idx, nil
}

// RunningExtremum returns R(t) at time asOf for (station, variable),
// the integer hours_remaining to civil-day close, and ok=false when
// the station has no obs in the civil-day window up to asOf (cold
// start).
//
// variable must be "tmax" or "tmin"; anything else returns ok=false.
//
// The civil day window is [day_open(station_local), day_close), where
// day_open is asOf converted to station-local TZ, truncated to
// midnight. R(t) is the max (for tmax) or min (for tmin) over all
// HourlyObs.TS ∈ [day_open_utc, asOf]. h_remain is clamped to [0,
// 24] — the maximum bin we fit during kernel construction.
func (h *HourlyIndex) RunningExtremum(station, variable string, asOf time.Time) (float64, int, bool) {
	if h == nil {
		return 0, 0, false
	}
	st, ok := h.byStation[station]
	if !ok {
		return 0, 0, false
	}
	if variable != "tmax" && variable != "tmin" {
		return 0, 0, false
	}

	asOfLocal := asOf.In(st.TZ)
	dayOpenLocal := time.Date(asOfLocal.Year(), asOfLocal.Month(), asOfLocal.Day(), 0, 0, 0, 0, st.TZ)
	dayCloseLocal := dayOpenLocal.AddDate(0, 0, 1)
	dayOpenUTC := dayOpenLocal.UTC()
	dayCloseUTC := dayCloseLocal.UTC()

	if asOf.Before(dayOpenUTC) || !asOf.Before(dayCloseUTC) {
		// asOf is outside the civil-day window we identified — shouldn't
		// happen in practice (asOf was used to derive the day), but guard
		// against TZ rounding by refusing to extrapolate.
		return 0, 0, false
	}

	// Binary-search to find the first obs ≥ dayOpenUTC.
	startIdx := sort.Search(len(st.Obs), func(i int) bool {
		return !st.Obs[i].TS.Before(dayOpenUTC)
	})

	var running float64
	if variable == "tmax" {
		running = -1e9 // any plausible tmpf is greater than this
	} else {
		running = 1e9
	}
	seen := false
	for i := startIdx; i < len(st.Obs); i++ {
		o := st.Obs[i]
		if o.TS.After(asOf) {
			break
		}
		if variable == "tmax" {
			if o.TmpF > running {
				running = o.TmpF
			}
		} else {
			if o.TmpF < running {
				running = o.TmpF
			}
		}
		seen = true
	}
	if !seen {
		return 0, 0, false
	}

	// h_remain = ceil((dayClose - asOf) / 1h), clamped to [0, 24]. We
	// use ceil so a trade at 23:30 local maps to h_remain=1, not 0
	// — h_remain=0 is reserved for "settlement reached", handled by
	// Update's obs-only branch.
	remaining := dayCloseUTC.Sub(asOf)
	hRemain := int((remaining + time.Hour - 1) / time.Hour) // ceil
	if hRemain < 0 {
		hRemain = 0
	}
	if hRemain > 24 {
		hRemain = 24
	}
	return running, hRemain, true
}
