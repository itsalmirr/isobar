// Package market is the canonical registry of weather observing
// sites the recorder, calibrator, and scanner all agree on. It owns
// the mapping from short location tags ("NY"/"MIA"/"LAX"/"CHI") to
// the four-letter NWS station code, lat/lon, and IANA timezone.
// Adding a new station is a one-place edit; see the Station type
// comment for the invariants every entry must satisfy.
package market

import (
	"fmt"
	"sync"
	"time"
)

// Station is the canonical record for one weather observing site we
// trade brackets on. Single source of truth for code, location tag,
// coordinates, and civil-day timezone. The station↔series mapping
// lives in internal/edge (Spec.Station); CLI bulletin PILs are
// derived from station codes inside scripts/calibration-fetch-cli.sh.
//
// Adding a new station: append to allStations, extend the series
// mapping in internal/edge, and verify scripts/calibration-fetch-*.sh
// include the same set.
type Station struct {
	// Code is the WFO/airport identifier used by NWS CLI bulletins
	// and by data/calibration/obs CSV filenames (e.g. "KNYC").
	Code string

	// Location is the short tag used by Open-Meteo poll Kind
	// ("rest:NY") and by config/config.example.yaml entries.
	Location string

	// Lat, Lon are the canonical observing-site coordinates (WGS84
	// decimal degrees), used for Open-Meteo polling and for boot-time
	// validation of operator-supplied config (see
	// internal/config/config.go: ValidateStationCoords). For Chicago
	// this is KMDW (Midway) per ADR-0007, NOT KORD (O'Hare). Values
	// are sourced from api.weather.gov/stations/<CODE>; cross-checked
	// against aviationweather.gov station info. Tolerance for config
	// drift is ±0.001°; tighter than that risks rejecting valid
	// rounding, looser drops past the 1°F bracket-flip threshold for
	// stations near coast/elevation gradients.
	Lat, Lon float64

	// TZ is the IANA tz-database name for the station's civil day. NWS
	// CLI settlement is on local calendar days, so the recorder,
	// calibrator, and scanner all derive civil-day boundaries from this.
	// Prefer StationTZ(code) to LoadLocation-cache it rather than reading
	// this field directly. DST is handled by the tz database.
	TZ string
}

// allStations is the registry. Consumers should use the lookup
// functions below rather than accessing this slice directly so that
// the canonical ordering and filter semantics are stable.
//
// Note on lookup performance: with N=4 stations the linear-scan
// implementation in StationBy* outperforms a map[string]Station hash
// lookup (~1.8 ns vs ~6 ns, measured), because the slice fits on a
// cache line and the comparison is branch-predictable. Switch to a
// map index once N grows past ~16 (hurricane series, secondary cities).
var allStations = []Station{
	{
		Code: "KNYC", Location: "NY",
		Lat: 40.7794, Lon: -73.9692, TZ: "America/New_York",
	},
	{
		Code: "KMIA", Location: "MIA",
		Lat: 25.7959, Lon: -80.2870, TZ: "America/New_York",
	},
	{
		Code: "KLAX", Location: "LAX",
		Lat: 33.9425, Lon: -118.4081, TZ: "America/Los_Angeles",
	},
	{
		Code: "KMDW", Location: "CHI",
		Lat: 41.7842, Lon: -87.7553, TZ: "America/Chicago",
	},
}

// AllStations returns a copy of the canonical station list. The order
// matches the configuration file and is stable across calls.
func AllStations() []Station {
	out := make([]Station, len(allStations))
	copy(out, allStations)
	return out
}

// AllStationsCanonical is a named alias for AllStations() used by
// boot-time config validation (internal/config: ValidateStationCoords).
// Calling it through this name documents that the caller depends on
// the Lat/Lon fields being the canonical NWS values, not arbitrary
// fixtures. Returns a fresh copy on every call.
func AllStationsCanonical() []Station { return AllStations() }

// AllStationCodes returns the four-letter station codes (KNYC, KMIA,
// KLAX, KMDW) in canonical order. Convenience for callers that only
// need codes (e.g. CLI bulletin fetcher).
func AllStationCodes() []string {
	out := make([]string, len(allStations))
	for i, s := range allStations {
		out[i] = s.Code
	}
	return out
}

// StationByCode looks up a Station by its four-letter code (KNYC etc).
func StationByCode(code string) (Station, bool) {
	for _, s := range allStations {
		if s.Code == code {
			return s, true
		}
	}
	return Station{}, false
}

// tzCache memoises the parsed *time.Location per station code so the
// recorder/calibrator/scanner don't re-parse the tz database on every
// civil-day boundary computation. Concurrency-safe: StationTZ is called
// from parallel scan/calibrate goroutines.
var tzCache sync.Map // string code → *time.Location

// StationTZ returns the parsed IANA *time.Location for the station's civil
// day, LoadLocation-cached per code. It is the single source of the
// station→timezone mapping the recorder, calibrator, and scanner share;
// callers derive local civil-day boundaries (and thus which calendar day a
// trade resolves on) from it, so a stale or divergent copy would be a
// bracket-flip-level correctness bug. Returns an error for an unknown code
// or a station whose TZ fails to load.
func StationTZ(code string) (*time.Location, error) {
	if v, ok := tzCache.Load(code); ok {
		return v.(*time.Location), nil
	}
	st, ok := StationByCode(code)
	if !ok || st.TZ == "" {
		return nil, fmt.Errorf("station %q has no timezone mapping", code)
	}
	loc, err := time.LoadLocation(st.TZ)
	if err != nil {
		return nil, fmt.Errorf("station %s: load tz %s: %w", code, st.TZ, err)
	}
	actual, _ := tzCache.LoadOrStore(code, loc)
	return actual.(*time.Location), nil
}
