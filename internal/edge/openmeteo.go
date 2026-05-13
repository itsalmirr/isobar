package edge

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Poll is a decoded Open-Meteo GFS-ensemble poll. The recorder stores
// raw JSON in the journal; this struct is what the edge scanner sees
// after decoding.
//
// Members maps member-ID → hourly temperature_2m series (°C). The
// control / deterministic forecast is keyed as "00" (Open-Meteo serves
// it without a `_member` suffix); the 30 perturbations are "01" through
// "30". Phase 0 expects 31 members for gfs_seamless ensemble.
type Poll struct {
	Latitude    float64
	Longitude   float64
	HourlyTimes []time.Time          // UTC, parallel to each Members value
	Members     map[string][]float64 // memberID ("00" + "01".."30") → hourly °C
}

// MemberCount returns the number of members successfully decoded.
func (p *Poll) MemberCount() int { return len(p.Members) }

// MemberIDs returns all member IDs sorted lexicographically.
func (p *Poll) MemberIDs() []string {
	ids := make([]string, 0, len(p.Members))
	for id := range p.Members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// openMeteoRaw is the on-the-wire shape we decode. The recorder requested
// timezone=UTC so all times in the response are UTC ISO-8601 strings.
type openMeteoRaw struct {
	Latitude  float64                    `json:"latitude"`
	Longitude float64                    `json:"longitude"`
	Hourly    map[string]json.RawMessage `json:"hourly"`
}

// ParsePoll decodes a raw Open-Meteo ensemble JSON payload into a Poll.
//
// We only consume the `hourly` block. Daily aggregates are computed by
// [DailyExtremesF] using a station-local timezone, because Kalshi's
// CLI bulletins resolve on local civil days, not UTC days.
//
// Members are extracted by name pattern: "temperature_2m" → "00"
// (control), "temperature_2m_memberNN" → "NN".
func ParsePoll(raw []byte) (*Poll, error) {
	var om openMeteoRaw
	if err := json.Unmarshal(raw, &om); err != nil {
		return nil, fmt.Errorf("openmeteo decode: %w", err)
	}
	if om.Hourly == nil {
		return nil, fmt.Errorf("openmeteo: response missing 'hourly' block")
	}

	timesRaw, ok := om.Hourly["time"]
	if !ok {
		return nil, fmt.Errorf("openmeteo: 'hourly.time' missing")
	}
	var timeStrs []string
	if err := json.Unmarshal(timesRaw, &timeStrs); err != nil {
		return nil, fmt.Errorf("openmeteo: decode hourly.time: %w", err)
	}
	times := make([]time.Time, len(timeStrs))
	for i, s := range timeStrs {
		t, err := parseHourlyTime(s)
		if err != nil {
			return nil, fmt.Errorf("openmeteo: parse hourly.time[%d]=%q: %w", i, s, err)
		}
		times[i] = t
	}

	members := map[string][]float64{}
	for k, v := range om.Hourly {
		id, ok := matchTemperatureMember(k)
		if !ok {
			continue
		}
		var vals []float64
		if err := json.Unmarshal(v, &vals); err != nil {
			// Tolerate non-numeric arrays (some members can be all-null
			// in early forecasts when GFS hasn't published yet).
			continue
		}
		if len(vals) != len(times) {
			return nil, fmt.Errorf("openmeteo: member %q has %d values, want %d",
				id, len(vals), len(times))
		}
		members[id] = vals
	}

	if len(members) == 0 {
		return nil, fmt.Errorf("openmeteo: no temperature_2m members found in payload")
	}

	return &Poll{
		Latitude:    om.Latitude,
		Longitude:   om.Longitude,
		HourlyTimes: times,
		Members:     members,
	}, nil
}

// matchTemperatureMember returns the member ID for an Open-Meteo hourly
// field name, or false if the field isn't a temperature_2m series we care
// about.
//
// Single-model responses (e.g. ?models=gfs_seamless alone):
//
//	"temperature_2m"               → "00"   (control)
//	"temperature_2m_memberNN"      → "NN"   (perturbations 01..N)
//
// Multi-model responses (e.g. ?models=gfs_seamless,ecmwf_ifs025) suffix
// every series with the model tag, and we prefix the model into the
// member ID so members from different models don't collide:
//
//	"temperature_2m_ncep_gefs_seamless"          → "ncep_gefs_seamless:00"
//	"temperature_2m_memberNN_ncep_gefs_seamless" → "ncep_gefs_seamless:NN"
//	"temperature_2m_ecmwf_ifs025_ensemble"        → "ecmwf_ifs025_ensemble:00"
//	"temperature_2m_memberNN_ecmwf_ifs025_ensemble" → "ecmwf_ifs025_ensemble:NN"
//
// Aggregators ("temperature_2m_max", "_min", and their member/model
// variants) and other variables (precipitation, etc.) return false.
func matchTemperatureMember(field string) (string, bool) {
	const base = "temperature_2m"
	if !strings.HasPrefix(field, base) {
		return "", false
	}
	rest := field[len(base):]

	// Reject aggregators before pattern-matching members.
	if strings.HasPrefix(rest, "_max") || strings.HasPrefix(rest, "_min") {
		return "", false
	}

	// Single-model control.
	if rest == "" {
		return "00", true
	}

	// Member: "_memberNN" optionally followed by "_<model>".
	if strings.HasPrefix(rest, "_member") {
		mr := rest[len("_member"):]
		if len(mr) < 2 || !isAllDigits(mr[:2]) {
			return "", false
		}
		num := mr[:2]
		tail := mr[2:]
		if tail == "" {
			return num, true
		}
		if !strings.HasPrefix(tail, "_") {
			return "", false
		}
		return tail[1:] + ":" + num, true
	}

	// Multi-model control: "_<model>".
	if strings.HasPrefix(rest, "_") {
		return rest[1:] + ":00", true
	}
	return "", false
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// parseHourlyTime parses Open-Meteo's hourly time string. With
// `timezone=UTC` requested, the format is "2006-01-02T15:04" (no
// seconds, no zone offset). We treat it as UTC.
func parseHourlyTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04", s, time.UTC)
}

// MinHourlySamplesPerMember is the minimum number of in-window hourly
// samples a member needs for its daily extremes to be reliable. At the
// 6h/12h forecast horizon, individual members can be partially missing
// even when the poll as a whole is healthy. 12 (half a day) is the
// floor below which TMAX/TMIN are too noisy to feed downstream.
const MinHourlySamplesPerMember = 12

// DailyExtremesF returns the per-member {TMAX, TMIN} in °F for a
// station-local civil date.
//
// civilDate is the resolution date as on the Kalshi ticker (e.g.
// 2026-04-29 for KXHIGHNY-26APR29-...). loc is the IANA timezone of
// the station (e.g. America/New_York for KNYC). The window is
// [civilDate 00:00 loc, civilDate 24:00 loc) — half-open right.
//
// Members with fewer than [MinHourlySamplesPerMember] in-window samples
// are skipped (and surfaced via the second return of
// [DailyExtremesFWithSkip]). The scanner uses the surviving member subset
// as long as ≥1 member remains; previously a single sparse member aborted
// the entire poll, which silently lost ~30 trades on the partial-coverage
// edge of the forecast window.
//
// Returns ErrInsufficientCoverage only if every member was skipped or
// the poll has zero members.
func (p *Poll) DailyExtremesF(civilDate time.Time, loc *time.Location) (
	map[string]MemberDaily, error,
) {
	out, _, err := p.dailyExtremesFInternal(civilDate, loc)
	return out, err
}

// DailyExtremesFWithSkip is the diagnostics-aware variant of
// DailyExtremesF. The second return value is the number of members
// dropped for failing the MinHourlySamplesPerMember check; the scanner
// uses this to surface "members_skipped" in its result counters without
// changing the per-trade pricing math (which always uses len(out) as
// nMembers).
func (p *Poll) DailyExtremesFWithSkip(civilDate time.Time, loc *time.Location) (
	map[string]MemberDaily, int, error,
) {
	return p.dailyExtremesFInternal(civilDate, loc)
}

// dailyExtremesFInternal is the shared implementation; the second return
// is the number of members skipped for insufficient coverage.
func (p *Poll) dailyExtremesFInternal(civilDate time.Time, loc *time.Location) (
	map[string]MemberDaily, int, error,
) {
	if loc == nil {
		return nil, 0, fmt.Errorf("openmeteo: nil timezone location")
	}
	startLocal := time.Date(civilDate.Year(), civilDate.Month(), civilDate.Day(),
		0, 0, 0, 0, loc)
	startUTC := startLocal.UTC()
	endUTC := startLocal.Add(24 * time.Hour).UTC()

	out := map[string]MemberDaily{}
	skipped := 0
	for id, series := range p.Members {
		var (
			minC, maxC = math.Inf(+1), math.Inf(-1)
			n          = 0
		)
		for i, t := range p.HourlyTimes {
			if t.Before(startUTC) || !t.Before(endUTC) {
				continue
			}
			v := series[i]
			if math.IsNaN(v) {
				continue
			}
			if v < minC {
				minC = v
			}
			if v > maxC {
				maxC = v
			}
			n++
		}
		if n < MinHourlySamplesPerMember {
			skipped++
			continue
		}
		out[id] = MemberDaily{
			TMaxF: cToF(maxC),
			TMinF: cToF(minC),
			N:     n,
		}
	}
	if len(out) == 0 {
		return nil, skipped, fmt.Errorf("%w: all %d members below %d in-window samples",
			ErrInsufficientCoverage, skipped, MinHourlySamplesPerMember)
	}
	return out, skipped, nil
}

// MemberDaily is the per-ensemble-member daily TMAX/TMIN for one civil
// date, in °F (matching CLI bulletin units), plus the sample count
// (1 hour each) used to compute it.
type MemberDaily struct {
	TMaxF float64
	TMinF float64
	N     int
}

// ErrInsufficientCoverage signals that an Open-Meteo poll didn't cover
// enough hours of the target civil date (e.g. the bracket is for "today"
// but the poll happened mid-morning so we only have 14 in-window hours).
//
// The scanner treats this as "skip this trade-poll pairing" rather than a
// hard failure.
var ErrInsufficientCoverage = errFmt("openmeteo: insufficient hourly coverage")

type errFmt string

func (e errFmt) Error() string { return string(e) }

func cToF(c float64) float64 { return c*9.0/5.0 + 32.0 }
