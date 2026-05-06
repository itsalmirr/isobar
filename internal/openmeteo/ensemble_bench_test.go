package openmeteo

import (
	"fmt"
	"testing"
	"time"
)

// buildEnsemblePayload returns a synthetic Open-Meteo response of
// realistic shape: 31 members × hourly_steps timesteps for temperature_2m.
// Real responses are ~200KB; this builds a comparable size.
func buildEnsemblePayload(hours int) []byte {
	times := "["
	for h := 0; h < hours; h++ {
		if h > 0 {
			times += ","
		}
		t := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC).Add(time.Duration(h) * time.Hour)
		times += `"` + t.Format("2006-01-02T15:04") + `"`
	}
	times += "]"

	vals := "["
	for h := 0; h < hours; h++ {
		if h > 0 {
			vals += ","
		}
		vals += fmt.Sprintf("%.2f", 10.0+float64(h%24)*0.5)
	}
	vals += "]"

	body := `{"hourly":{"time":` + times + `,"temperature_2m":` + vals
	for m := 1; m <= 30; m++ {
		body += fmt.Sprintf(`,"temperature_2m_member%02d":%s`, m, vals)
	}
	body += "}}"
	return []byte(body)
}

// BenchmarkParseEnsemblePoll measures decoding one recorded ensemble
// poll. Called once per poll during edge-scan.
func BenchmarkParseEnsemblePoll(b *testing.B) {
	payload := buildEnsemblePayload(72)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		_, err := ParseEnsemblePoll(payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDailyExtremes measures the per-(member, lookup) max/min
// extraction with timezone windowing. Called once per (poll, ticker)
// in edge-scan.
func BenchmarkDailyExtremes(b *testing.B) {
	payload := buildEnsemblePayload(72)
	poll, err := ParseEnsemblePoll(payload)
	if err != nil {
		b.Fatal(err)
	}
	target, _ := time.Parse("2006-01-02", "2026-04-22")
	b.ResetTimer()
	for range b.N {
		_, _, _ = poll.DailyExtremes(target, -4)
	}
}
