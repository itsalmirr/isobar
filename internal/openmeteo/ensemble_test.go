package openmeteo

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestParseEnsemblePoll(t *testing.T) {
	// Minimal synthetic response mimicking Open-Meteo shape.
	payload := []byte(`{
		"latitude": 40.75,
		"longitude": -74.0,
		"hourly": {
			"time": ["2026-04-22T00:00","2026-04-22T01:00","2026-04-22T02:00",
			         "2026-04-22T03:00","2026-04-22T04:00","2026-04-22T05:00",
			         "2026-04-22T06:00","2026-04-22T07:00","2026-04-22T08:00",
			         "2026-04-22T09:00","2026-04-22T10:00","2026-04-22T11:00",
			         "2026-04-22T12:00","2026-04-22T13:00","2026-04-22T14:00",
			         "2026-04-22T15:00","2026-04-22T16:00","2026-04-22T17:00",
			         "2026-04-22T18:00","2026-04-22T19:00","2026-04-22T20:00",
			         "2026-04-22T21:00","2026-04-22T22:00","2026-04-22T23:00",
			         "2026-04-23T00:00","2026-04-23T01:00","2026-04-23T02:00",
			         "2026-04-23T03:00","2026-04-23T04:00","2026-04-23T05:00",
			         "2026-04-23T06:00","2026-04-23T07:00","2026-04-23T08:00",
			         "2026-04-23T09:00","2026-04-23T10:00","2026-04-23T11:00",
			         "2026-04-23T12:00","2026-04-23T13:00","2026-04-23T14:00",
			         "2026-04-23T15:00","2026-04-23T16:00","2026-04-23T17:00",
			         "2026-04-23T18:00","2026-04-23T19:00","2026-04-23T20:00",
			         "2026-04-23T21:00","2026-04-23T22:00","2026-04-23T23:00"],` +
		buildMembersJSON(48, 10.0, 20.0) + `
		}
	}`)

	poll, err := ParseEnsemblePoll(payload)
	if err != nil {
		t.Fatalf("ParseEnsemblePoll: %v", err)
	}
	if len(poll.Times) != 48 {
		t.Fatalf("times len = %d, want 48", len(poll.Times))
	}
	if len(poll.Members[0]) != 48 {
		t.Fatalf("control len = %d, want 48", len(poll.Members[0]))
	}

	// Test DailyExtremes for Apr 22, UTC-4 (EDT / NY).
	// Local Apr 22 = UTC Apr 22 04:00 → Apr 23 04:00.
	maxF, minF, err := poll.DailyExtremes(
		time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), -4)
	if err != nil {
		t.Fatalf("DailyExtremes: %v", err)
	}

	// All members have the same synthetic data: linear ramp 10→20°C over
	// 48h. The local-date window (UTC 04:00–28:00) captures hours 4–27
	// of the first day. Min at hour 4: ~11.67°C, max at hour 27: ~16.46°C.
	// Converted to °F.
	for m := range NumMembers {
		if math.IsInf(maxF[m], 0) || math.IsInf(minF[m], 0) {
			t.Errorf("member %d: got inf", m)
		}
		if maxF[m] <= minF[m] {
			t.Errorf("member %d: max %.1f <= min %.1f", m, maxF[m], minF[m])
		}
	}
}

func TestCToF(t *testing.T) {
	tests := []struct {
		c, f float64
	}{
		{0, 32}, {100, 212}, {-40, -40}, {20, 68},
	}
	for _, tt := range tests {
		got := cToF(tt.c)
		if math.Abs(got-tt.f) > 0.01 {
			t.Errorf("cToF(%v) = %v, want %v", tt.c, got, tt.f)
		}
	}
}

// buildMembersJSON creates the "temperature_2m" + "temperature_2m_member01"
// through "_member30" keys with a linear ramp from lo to hi °C.
func buildMembersJSON(n int, lo, hi float64) string {
	vals := "["
	for i := range n {
		if i > 0 {
			vals += ","
		}
		v := lo + (hi-lo)*float64(i)/float64(n-1)
		vals += fmt.Sprintf("%.2f", v)
	}
	vals += "]"

	s := `"temperature_2m":` + vals
	for m := 1; m <= 30; m++ {
		s += fmt.Sprintf(`,"temperature_2m_member%02d":%s`, m, vals)
	}
	return s
}
