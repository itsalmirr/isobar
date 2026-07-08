package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
)

// bayesScanFixture stands up the full Bayesian-update scan environment:
// a synthetic poll for NY, one trade frame for the given ticker, MOS
// coefficients that match (KNYC, MAM, tmax), a KNYC-hourly.csv obs file
// with one same-day observation, and an in-memory kernel covering every
// h_remain bin so the update path can't fall back for lack of a bin.
//
// The trade timestamp is 2026-04-29 07:50 UTC = 03:50 EDT — i.e. on the
// station-local civil day 2026-04-29.
func bayesScanFixture(t *testing.T, ticker string) ScanOptions {
	t.Helper()
	dir := t.TempDir()
	pollTime := time.Date(2026, 4, 29, 7, 30, 0, 0, time.UTC)
	tradeTime := pollTime.Add(20 * time.Minute)

	if err := writeJournal(t, filepath.Join(dir, "openmeteo", "2026-04-29"), "om.jsonl",
		[]envelope{{Wall: pollTime.UnixNano(), Source: "openmeteo", Kind: "rest:NY", Payload: synthPoll(t)}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"market_ticker": ticker,
		"yes_price":     10,
		"no_price":      90,
		"side":          "yes",
		"ts_ms":         tradeTime.UnixMilli(),
	})
	frame, _ := json.Marshal(map[string]interface{}{
		"type": "trade", "msg": json.RawMessage(raw),
	})
	if err := writeJournal(t, filepath.Join(dir, "kalshi", "2026-04-29"), "k.jsonl",
		[]envelope{{Wall: tradeTime.UnixNano(), Source: "kalshi", Kind: "ws", Payload: frame}}); err != nil {
		t.Fatal(err)
	}

	// One hourly obs inside the Apr 29 NY civil day, before trade time.
	obsDir := filepath.Join(dir, "obs")
	if err := os.MkdirAll(obsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(obsDir, "KNYC-hourly.csv"),
		[]byte("station,valid,tmpf\nKNYC,2026-04-29 05:00,50.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hourly, err := bayesian.LoadHourlyIndex(obsDir)
	if err != nil {
		t.Fatalf("LoadHourlyIndex: %v", err)
	}

	// Kernel with every h_remain bin populated so a missing bin can
	// never be the reason the update is skipped.
	bins := map[int]bayesian.Residual{}
	for h := 1; h <= 24; h++ {
		bins[h] = bayesian.Residual{EDelta: 10, VarDelta: 4, N: 10}
	}
	kernel := &bayesian.Kernel{
		SchemaVersion: bayesian.KernelSchemaVersion,
		FitWindow:     "test",
		Entries:       map[string]map[int]bayesian.Residual{"KNYC/tmax": bins},
	}

	coeffs := &mos.Coefficients{
		FitModelSet: []string{mos.ModelGFS},
		Bins: map[string]mos.BinCoeffs{
			"KNYC|MAM|tmax": {A: 0, B: 1, Sigma: 2},
		},
	}

	return ScanOptions{
		DataDir:        dir,
		StartDate:      time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		EndDate:        time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		MaxPollAge:     time.Hour,
		MOS:            coeffs,
		BayesianKernel: kernel,
		HourlyObs:      hourly,
	}
}

// TestScan_BayesAppliesOnSameCivilDay is the positive control: a trade
// on the bracket's own resolution day (2026-04-29, station-local) with
// same-day obs and a populated kernel must produce the posterior update.
func TestScan_BayesAppliesOnSameCivilDay(t *testing.T) {
	opts := bayesScanFixture(t, "KXHIGHNY-26APR29-B82.5")
	res, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1, skipped=%v", len(res.Rows), res.SkippedReason)
	}
	row := res.Rows[0]
	if !row.MOSApplied {
		t.Fatalf("MOSApplied = false; fixture broken (skip=%q)", row.SkippedReason)
	}
	if !row.BayesApplied {
		t.Fatalf("BayesApplied = false (reason=%q), want true for a same-day trade", row.BayesSkipReason)
	}
	if row.BayesRunningTempF != 50 {
		t.Errorf("BayesRunningTempF = %v, want 50 (the single obs)", row.BayesRunningTempF)
	}
	// Trade at 07:50Z; NY civil day closes 2026-04-30 04:00Z →
	// 20h10m remaining → ceil = 21.
	if row.BayesHRemain != 21 {
		t.Errorf("BayesHRemain = %d, want 21", row.BayesHRemain)
	}
	if row.PFair != row.PFairBayes {
		t.Errorf("PFair = %v, want PFairBayes %v when the update applied", row.PFair, row.PFairBayes)
	}
	if res.BayesApplied != 1 {
		t.Errorf("res.BayesApplied = %d, want 1", res.BayesApplied)
	}
}

// TestScan_BayesSkipsWrongCivilDay is the regression guard for the
// wrong-day poisoning bug: a NEXT-day bracket (valid 2026-04-30) traded
// on 2026-04-29 must NOT receive a posterior update — the running
// extremum available at trade time belongs to Apr 29, not to the day
// the bracket resolves on. Before the fix, Scan folded today's running
// max into tomorrow's TMAX estimate (with the tight late-day kernel
// variance), silently corrupting p_fair for every day-ahead trade.
func TestScan_BayesSkipsWrongCivilDay(t *testing.T) {
	opts := bayesScanFixture(t, "KXHIGHNY-26APR30-B82.5")
	res, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1, skipped=%v", len(res.Rows), res.SkippedReason)
	}
	row := res.Rows[0]
	if !row.MOSApplied {
		t.Fatalf("MOSApplied = false; fixture broken (skip=%q)", row.SkippedReason)
	}
	if row.BayesApplied {
		t.Fatalf("BayesApplied = true for a next-day bracket — today's R(t) must not update tomorrow's estimate")
	}
	if row.BayesSkipReason != "wrong_civil_day" {
		t.Errorf("BayesSkipReason = %q, want %q", row.BayesSkipReason, "wrong_civil_day")
	}
	if row.PFair != row.PFairMOS {
		t.Errorf("PFair = %v, want MOS prior %v when the update is skipped", row.PFair, row.PFairMOS)
	}
	if res.BayesApplied != 0 {
		t.Errorf("res.BayesApplied = %d, want 0", res.BayesApplied)
	}
}
