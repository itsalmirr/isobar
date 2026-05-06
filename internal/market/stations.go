package market

// Station is the canonical record for one weather observing site we
// trade brackets on. Single source of truth for code, location tag,
// coordinates, timezone offset, Kalshi series names, and NWS CLI PIL.
//
// Adding a new station: append to allStations, add the location tag
// to the appropriate series prefix mappings, and verify
// scripts/calibration-fetch-*.sh include the same set.
type Station struct {
	// Code is the WFO/airport identifier used by NWS CLI bulletins
	// and by data/calibration/obs CSV filenames (e.g. "KNYC").
	Code string

	// Location is the short tag used by Open-Meteo poll Kind
	// ("rest:NY") and by config/config.example.yaml entries.
	Location string

	// City is the human-readable name (for log messages, reports).
	City string

	// Lat, Lon are the point used for Open-Meteo polling. For Chicago
	// this is KMDW (Midway) per ADR-0007, NOT KORD (O'Hare).
	Lat, Lon float64

	// UTCOffsetHours is the local-civil-time offset during US daylight
	// time (March-November). All four stations are in DST during the
	// April-October recording window. Re-validate before recording in
	// the November-March standard-time window.
	UTCOffsetHours int

	// HighSeries and LowSeries are the Kalshi series prefixes that
	// resolve on this station's daily TMAX/TMIN.
	HighSeries, LowSeries string

	// CLIPil is the AFOS PIL prefix for this station's NWS CLI
	// bulletin (e.g. "CLINYC"). Matches what scripts/calibration-fetch-cli.sh
	// requests from IEM.
	CLIPil string
}

// allStations is the registry. Consumers should use the lookup
// functions below rather than accessing this slice directly so that
// the canonical ordering and filter semantics are stable.
var allStations = []Station{
	{
		Code: "KNYC", Location: "NY", City: "New York",
		Lat: 40.7794, Lon: -73.9692, UTCOffsetHours: -4,
		HighSeries: "KXHIGHNY", LowSeries: "KXLOWTNYC",
		CLIPil: "CLINYC",
	},
	{
		Code: "KMIA", Location: "MIA", City: "Miami",
		Lat: 25.7617, Lon: -80.1918, UTCOffsetHours: -4,
		HighSeries: "KXHIGHMIA", LowSeries: "KXLOWTMIA",
		CLIPil: "CLIMIA",
	},
	{
		Code: "KLAX", Location: "LAX", City: "Los Angeles",
		Lat: 33.9416, Lon: -118.4085, UTCOffsetHours: -7,
		HighSeries: "KXHIGHLAX", LowSeries: "KXLOWTLAX",
		CLIPil: "CLILAX",
	},
	{
		Code: "KMDW", Location: "CHI", City: "Chicago",
		Lat: 41.7861, Lon: -87.7522, UTCOffsetHours: -5,
		HighSeries: "KXHIGHCHI", LowSeries: "KXLOWTCHI",
		CLIPil: "CLIMDW",
	},
}

// AllStations returns a copy of the canonical station list. The order
// matches the configuration file and is stable across calls.
func AllStations() []Station {
	out := make([]Station, len(allStations))
	copy(out, allStations)
	return out
}

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

// StationByLocation looks up a Station by its short location tag
// ("NY", "MIA", "LAX", "CHI").
func StationByLocation(loc string) (Station, bool) {
	for _, s := range allStations {
		if s.Location == loc {
			return s, true
		}
	}
	return Station{}, false
}

// StationBySeries looks up the Station that this Kalshi series
// resolves on. Accepts either the high or low series for a station.
// Returns ok=false for unrecognised prefixes (which is also how
// non-weather tickers fall through).
func StationBySeries(series string) (Station, bool) {
	for _, s := range allStations {
		if s.HighSeries == series || s.LowSeries == series {
			return s, true
		}
	}
	return Station{}, false
}
