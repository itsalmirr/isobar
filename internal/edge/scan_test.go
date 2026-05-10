package edge

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestScan_EndToEndSynthetic stands up a synthetic data tree with one
// Open-Meteo poll and one Kalshi trade, runs Scan, and asserts the
// per-trade edge row was produced with the expected p_fair.
func TestScan_EndToEndSynthetic(t *testing.T) {
	dir := t.TempDir()

	pollPayload := synthPoll(t)
	pollTime := time.Date(2026, 4, 29, 7, 30, 0, 0, time.UTC)
	tradeTime := pollTime.Add(20 * time.Minute)

	// Open-Meteo journal: one frame for NY.
	if err := writeJournal(t, filepath.Join(dir, "openmeteo", "2026-04-29"), "openmeteo-073000.jsonl",
		[]envelope{{
			Wall:    pollTime.UnixNano(),
			Source:  "openmeteo",
			Kind:    "rest:NY",
			Payload: pollPayload,
		}},
	); err != nil {
		t.Fatal(err)
	}

	// Kalshi journal: one trade frame for KXHIGHNY-26APR29-B62.5
	// at trade price 0.45 (45¢), side=yes.
	tradeMsgRaw, _ := json.Marshal(map[string]interface{}{
		"market_ticker": "KXHIGHNY-26APR29-B62.5",
		"yes_price":     45,
		"no_price":      55,
		"side":          "yes",
		"count":         3,
		"ts_ms":         tradeTime.UnixMilli(),
	})
	frame, _ := json.Marshal(map[string]interface{}{
		"type": "trade",
		"msg":  json.RawMessage(tradeMsgRaw),
	})
	if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"), "kalshi-073500.jsonl",
		[]envelope{{
			Wall:    tradeTime.UnixNano(),
			Source:  "kalshi",
			Kind:    "ws",
			Payload: frame,
		}},
	); err != nil {
		t.Fatal(err)
	}

	res, err := Scan(ScanOptions{
		DataDir:    dir,
		StartDate:  time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		MaxPollAge: time.Hour,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.WeatherTrades != 1 {
		t.Errorf("WeatherTrades = %d, want 1", res.WeatherTrades)
	}
	if res.PairedTrades != 1 {
		t.Errorf("PairedTrades = %d, want 1, skipped=%v", res.PairedTrades, res.SkippedReason)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	row := res.Rows[0]
	if row.Ticker != "KXHIGHNY-26APR29-B62.5" {
		t.Errorf("Ticker = %q", row.Ticker)
	}
	if row.TradePriceCents != 45 {
		t.Errorf("TradePriceCents = %d, want 45", row.TradePriceCents)
	}
	if row.NMembers != 3 {
		t.Errorf("NMembers = %d, want 3 (control+2 perturbations)", row.NMembers)
	}
	// In synthPoll, Apr 29 EDT max temps (°C) for the three members:
	// control=27, mem01=28, mem02=29 → in °F: 80.6, 82.4, 84.2.
	// Bracket B62.5 covers [61.5, 63.5)°F. None of those highs fall in.
	// So p_fair = 0, edge = |0 - 45| - fee(0.45) = 45 - 2 = 43.
	if row.PFairNaive != 0 {
		t.Errorf("PFairNaive = %v, want 0 (highs 80–84°F all outside [61.5, 63.5))", row.PFairNaive)
	}
	if row.EdgeCents != 43 {
		t.Errorf("EdgeCents = %d, want 43 (|0 - 45c| - 2c fee)", row.EdgeCents)
	}
}

func TestScan_NonWeatherTradesSkipped(t *testing.T) {
	dir := t.TempDir()

	tradeMsg, _ := json.Marshal(map[string]interface{}{
		"market_ticker": "KXBOGUS-26APR29-T100",
		"yes_price":     50,
		"side":          "yes",
		"ts_ms":         time.Now().UnixMilli(),
	})
	frame, _ := json.Marshal(map[string]interface{}{
		"type": "trade",
		"msg":  json.RawMessage(tradeMsg),
	})
	if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"), "kalshi-1.jsonl",
		[]envelope{{Wall: time.Now().UnixNano(), Source: "kalshi", Kind: "ws", Payload: frame}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "openmeteo", "2026-04-29"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(ScanOptions{
		DataDir:    dir,
		StartDate:  time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		MaxPollAge: time.Hour,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.WeatherTrades != 0 {
		t.Errorf("WeatherTrades = %d, want 0", res.WeatherTrades)
	}
	if res.SkippedReason["non_weather_ticker"] != 1 {
		t.Errorf("non_weather_ticker count = %d, want 1, full=%v",
			res.SkippedReason["non_weather_ticker"], res.SkippedReason)
	}
}

// envelope mirrors internal/replay.Envelope for fixture writing.
type envelope struct {
	Wall    int64           `json:"t"`
	Mono    int64           `json:"m"`
	Source  string          `json:"src"`
	Kind    string          `json:"k"`
	Payload json.RawMessage `json:"p,omitempty"`
}

func writeJournal(t *testing.T, dir, name string, envs []envelope) error {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range envs {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// Compile-time assert gzip is reachable so the test file imports it
// when we extend tests to cover .gz reads in a follow-up.
var _ = gzip.BestSpeed
