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

// TestScan_BacktestRealizedPnL: one trade where naive strategy says
// "buy NO" and the actual outcome confirms NO. Should produce a
// positive realized PnL.
func TestScan_BacktestRealizedPnL(t *testing.T) {
	dir := t.TempDir()
	pollPayload := synthPoll(t)
	pollTime := time.Date(2026, 4, 29, 7, 30, 0, 0, time.UTC)
	tradeTime := pollTime.Add(20 * time.Minute)

	if err := writeJournal(t, filepath.Join(dir, "openmeteo", "2026-04-29"), "om.jsonl",
		[]envelope{{
			Wall: pollTime.UnixNano(), Source: "openmeteo", Kind: "rest:NY", Payload: pollPayload,
		}}); err != nil {
		t.Fatal(err)
	}

	// Trade: KXHIGHNY-26APR29-B62.5 at yes_price=25 (so no_price=75).
	// Synthetic poll members all have TMAX ~80°F → p_fair = 0 for [61.5, 63.5).
	// edge_yes = 0 - 25 - fee(.25)=2 = -27 (negative, skip yes)
	// edge_no  = 100 - 75 - 2 = 23 (positive, take NO at 75¢)
	// Truth: TMAX=80°F → YES did NOT resolve → NO wins.
	// PnL_no = (100 - 75) - 2 = 23¢.
	tradeMsgRaw, _ := json.Marshal(map[string]interface{}{
		"market_ticker": "KXHIGHNY-26APR29-B62.5",
		"yes_price":     25,
		"no_price":      75,
		"side":          "yes",
		"count":         1,
		"ts_ms":         tradeTime.UnixMilli(),
	})
	frame, _ := json.Marshal(map[string]interface{}{
		"type": "trade", "msg": json.RawMessage(tradeMsgRaw),
	})
	if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"), "k.jsonl",
		[]envelope{{
			Wall: tradeTime.UnixNano(), Source: "kalshi", Kind: "ws", Payload: frame,
		}}); err != nil {
		t.Fatal(err)
	}

	// Truth file at <dir>/truth/KNYC.csv with TMAX=80, TMIN=40 for Apr 29.
	truthDir := filepath.Join(dir, "truth")
	if err := os.MkdirAll(truthDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(truthDir, "KNYC.csv"),
		[]byte("valid_date,tmax_f,tmin_f,issued_at,bulletin_id\n"+
			"2026-04-29,80,40,2026-04-30T06:00:00Z,CLINYC_test.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	truth, err := LoadTruth(truthDir)
	if err != nil {
		t.Fatalf("LoadTruth: %v", err)
	}
	if truth.Len() != 1 {
		t.Fatalf("truth.Len = %d, want 1", truth.Len())
	}

	res, err := Scan(ScanOptions{
		DataDir:            dir,
		StartDate:          time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		EndDate:            time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		MaxPollAge:         time.Hour,
		Truth:              truth,
		EdgeThresholdCents: 5,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !res.BacktestEnabled {
		t.Fatal("BacktestEnabled false")
	}
	if res.TradesEntered != 1 {
		t.Fatalf("TradesEntered = %d, want 1, skipped=%v", res.TradesEntered, res.SkippedReason)
	}
	if res.TradesWon != 1 {
		t.Errorf("TradesWon = %d, want 1", res.TradesWon)
	}
	if res.TotalPnLCents != 23 {
		t.Errorf("TotalPnLCents = %d, want 23", res.TotalPnLCents)
	}
	row := res.Rows[0]
	if !row.HasTruth || row.ActualTempF != 80 || row.YesResolved {
		t.Errorf("row truth fields wrong: %+v", row)
	}
	if row.OurSide != "no" || row.OurPnLCents != 23 {
		t.Errorf("row hypothetical action wrong: side=%q pnl=%d", row.OurSide, row.OurPnLCents)
	}
}

// TestScan_BacktestNoTruthSkipped: trade for a date outside the truth
// window should be counted as "no_truth_available" and not entered.
func TestScan_BacktestNoTruthSkipped(t *testing.T) {
	dir := t.TempDir()
	pollPayload := synthPoll(t)
	pollTime := time.Date(2026, 4, 29, 7, 30, 0, 0, time.UTC)
	tradeTime := pollTime.Add(20 * time.Minute)

	if err := writeJournal(t, filepath.Join(dir, "openmeteo", "2026-04-29"), "om.jsonl",
		[]envelope{{Wall: pollTime.UnixNano(), Source: "openmeteo", Kind: "rest:NY", Payload: pollPayload}}); err != nil {
		t.Fatal(err)
	}
	// Trade for Apr 30 valid_date — in poll coverage, but truth only has Apr 29.
	tradeMsgRaw, _ := json.Marshal(map[string]interface{}{
		"market_ticker": "KXHIGHNY-26APR30-B62.5",
		"yes_price":     25, "no_price": 75, "side": "yes", "count": 1,
		"ts_ms": tradeTime.UnixMilli(),
	})
	frame, _ := json.Marshal(map[string]interface{}{"type": "trade", "msg": json.RawMessage(tradeMsgRaw)})
	if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"), "k.jsonl",
		[]envelope{{Wall: tradeTime.UnixNano(), Source: "kalshi", Kind: "ws", Payload: frame}}); err != nil {
		t.Fatal(err)
	}
	truthDir := filepath.Join(dir, "truth")
	_ = os.MkdirAll(truthDir, 0o755)
	_ = os.WriteFile(filepath.Join(truthDir, "KNYC.csv"),
		[]byte("valid_date,tmax_f,tmin_f\n2026-04-29,80,40\n"), 0o644)
	truth, _ := LoadTruth(truthDir)

	res, err := Scan(ScanOptions{
		DataDir:    dir,
		StartDate:  time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		MaxPollAge: time.Hour,
		Truth:      truth,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.SkippedReason["no_truth_available"] != 1 {
		t.Errorf("no_truth_available = %d, want 1, full=%v",
			res.SkippedReason["no_truth_available"], res.SkippedReason)
	}
	if res.TradesEntered != 0 {
		t.Errorf("TradesEntered = %d, want 0", res.TradesEntered)
	}
}

// TestScan_LowSideTBracketResolution_KXLOWT: a KXLOWTNYC-T56 trade in a
// market whose B-bracket siblings live above T56 must resolve as a
// LOW-side threshold (TMIN < 56), not the default high-side. Verifies that
// Scan groups specs by (series, valid_date) and feeds the siblings into
// ResolveWithSiblings before computing p_fair.
//
// Setup: synthetic poll has TMIN ~40°F across all three members. With the
// (broken) default high-side resolution [56, +Inf), p_fair would be 0/3 = 0
// (no TMIN ≥ 56). With sibling-aware low-side resolution (-Inf, 56),
// p_fair is 3/3 = 1.0 (every TMIN < 56). The test asserts the latter.
func TestScan_LowSideTBracketResolution_KXLOWT(t *testing.T) {
	dir := t.TempDir()
	pollPayload := synthPoll(t)
	pollTime := time.Date(2026, 4, 29, 7, 30, 0, 0, time.UTC)
	tradeTime := pollTime.Add(20 * time.Minute)

	if err := writeJournal(t, filepath.Join(dir, "openmeteo", "2026-04-29"), "om.jsonl",
		[]envelope{{Wall: pollTime.UnixNano(), Source: "openmeteo", Kind: "rest:NY", Payload: pollPayload}}); err != nil {
		t.Fatal(err)
	}

	// Three trade frames: one T56 (low-side) and two B-bracket siblings B58.5,
	// B60.5 — both above T56 so ResolveWithSiblings should flip T56 to low.
	// We use side=yes, yes_price=50 (mid-market) and the price isn't
	// load-bearing for the assertion; only resolution + p_fair are.
	for _, ticker := range []string{
		"KXLOWTNYC-26APR29-T56",
		"KXLOWTNYC-26APR29-B58.5",
		"KXLOWTNYC-26APR29-B60.5",
	} {
		raw, _ := json.Marshal(map[string]interface{}{
			"market_ticker": ticker,
			"yes_price":     50,
			"no_price":      50,
			"side":          "yes",
			"ts_ms":         tradeTime.UnixMilli(),
		})
		frame, _ := json.Marshal(map[string]interface{}{
			"type": "trade", "msg": json.RawMessage(raw),
		})
		if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"),
			"k-"+ticker+".jsonl",
			[]envelope{{Wall: tradeTime.UnixNano(), Source: "kalshi", Kind: "ws", Payload: frame}}); err != nil {
			t.Fatal(err)
		}
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
	// Find the T56 row.
	var got *EdgeRow
	for i := range res.Rows {
		if res.Rows[i].Ticker == "KXLOWTNYC-26APR29-T56" {
			got = &res.Rows[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("T56 row missing; rows=%d skipped=%v", len(res.Rows), res.SkippedReason)
	}
	// Synthetic poll TMINs (members 00/01/02): 4°C/5°C/6°C → 39.2/41.0/42.8°F.
	// Sibling-aware low-side resolution (-Inf, 56) → all 3 are inside → p_fair = 1.0.
	// Buggy default high-side [56, +Inf) → 0/3 = 0. We assert 1.0.
	if got.PFairNaive != 1.0 {
		t.Errorf("KXLOWT T56 PFairNaive = %v, want 1.0 (low-side resolution detected via siblings; default high-side would give 0)",
			got.PFairNaive)
	}
	if got.NMembersInBkt != 3 {
		t.Errorf("KXLOWT T56 NMembersInBkt = %d, want 3", got.NMembersInBkt)
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
