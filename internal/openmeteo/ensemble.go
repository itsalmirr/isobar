package openmeteo

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

const NumMembers = 31 // control + 30 perturbations

// EnsemblePoll is one decoded Open-Meteo ensemble API response.
type EnsemblePoll struct {
	Wall     int64                 // unix nanoseconds from journal envelope
	Location string                // "NY", "MIA", etc. (from envelope Kind "rest:NY")
	Times    []string              // ISO 8601 UTC timestamps, len = forecast_days * 24
	Members  [NumMembers][]float64 // hourly temperature_2m per member, °C
}

// DailyExtremes returns the [NumMembers] daily max and min temperatures in
// °F for the given local calendar date. utcOffsetHours is negative for
// west-of-UTC (e.g. -4 for EDT). Returns an error if the date isn't
// covered by the forecast window.
func (p *EnsemblePoll) DailyExtremes(date time.Time, utcOffsetHours int) (maxF [NumMembers]float64, minF [NumMembers]float64, err error) {
	startUTC := date.Add(time.Duration(-utcOffsetHours) * time.Hour)
	endUTC := startUTC.Add(24 * time.Hour)

	startIdx, endIdx := -1, -1
	for i, ts := range p.Times {
		t, e := time.Parse("2006-01-02T15:04", ts)
		if e != nil {
			continue
		}
		if startIdx < 0 && !t.Before(startUTC) {
			startIdx = i
		}
		if t.Before(endUTC) {
			endIdx = i + 1
		}
	}
	if startIdx < 0 || endIdx <= startIdx {
		return maxF, minF, fmt.Errorf("ensemble: date %s not in forecast window", date.Format("2006-01-02"))
	}

	for m := range NumMembers {
		if len(p.Members[m]) < endIdx {
			return maxF, minF, fmt.Errorf("ensemble: member %d has %d values, need %d", m, len(p.Members[m]), endIdx)
		}
		hi := math.Inf(-1)
		lo := math.Inf(1)
		for i := startIdx; i < endIdx; i++ {
			v := p.Members[m][i]
			if math.IsNaN(v) {
				continue
			}
			if v > hi {
				hi = v
			}
			if v < lo {
				lo = v
			}
		}
		maxF[m] = cToF(hi)
		minF[m] = cToF(lo)
	}
	return maxF, minF, nil
}

func cToF(c float64) float64 { return c*9.0/5.0 + 32.0 }

// ParseEnsemblePoll decodes a raw Open-Meteo JSON response payload into
// an EnsemblePoll. The payload is the Envelope.Payload from a recorded
// journal frame.
func ParseEnsemblePoll(payload json.RawMessage) (*EnsemblePoll, error) {
	var resp struct {
		Hourly json.RawMessage `json:"hourly"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("ensemble: unmarshal top: %w", err)
	}
	if resp.Hourly == nil {
		return nil, fmt.Errorf("ensemble: no hourly section")
	}

	hourly := make(map[string]json.RawMessage)
	if err := json.Unmarshal(resp.Hourly, &hourly); err != nil {
		return nil, fmt.Errorf("ensemble: unmarshal hourly: %w", err)
	}

	var times []string
	if raw, ok := hourly["time"]; ok {
		if err := json.Unmarshal(raw, &times); err != nil {
			return nil, fmt.Errorf("ensemble: unmarshal times: %w", err)
		}
	}

	poll := &EnsemblePoll{Times: times}

	control, err := parseNullableFloats(hourly["temperature_2m"])
	if err != nil {
		return nil, fmt.Errorf("ensemble: parse control: %w", err)
	}
	poll.Members[0] = control

	for i := 1; i <= 30; i++ {
		key := "temperature_2m_member" + fmt.Sprintf("%02d", i)
		raw, ok := hourly[key]
		if !ok {
			return nil, fmt.Errorf("ensemble: missing %s", key)
		}
		vals, err := parseNullableFloats(raw)
		if err != nil {
			return nil, fmt.Errorf("ensemble: parse %s: %w", key, err)
		}
		poll.Members[i] = vals
	}

	return poll, nil
}

// parseNullableFloats decodes a JSON array that may contain null entries.
func parseNullableFloats(raw json.RawMessage) ([]float64, error) {
	var mixed []json.RawMessage
	if err := json.Unmarshal(raw, &mixed); err != nil {
		return nil, err
	}
	out := make([]float64, len(mixed))
	for i, v := range mixed {
		s := string(v)
		if s == "null" {
			out[i] = math.NaN()
			continue
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		out[i] = f
	}
	return out, nil
}
