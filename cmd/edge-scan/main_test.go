package main

import (
	"strings"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/edge"
)

// fixtureResult builds a small Result with one entered/won trade and one
// skipped trade, enough to exercise every section of the report writers.
func fixtureResult() *edge.Result {
	return &edge.Result{
		TotalFrames:        100,
		TradeFrames:        40,
		WeatherTrades:      20,
		PairedTrades:       2,
		SkippedReason:      map[string]int{"no_poll_for_station": 3},
		BacktestEnabled:    true,
		TradesEntered:      1,
		TradesWon:          1,
		TotalPnLCents:      37,
		MOSEnabled:         true,
		MOSApplied:         1,
		FeeMode:            edge.FeeTaker,
		EntryFilterName:    "h4",
		EdgeThresholdCents: 7,
		Rows: []edge.EdgeRow{
			{
				TSMs:            1000,
				Ticker:          "KXHIGHCHI-26APR29-B62.5",
				ValidDate:       time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
				TradePriceCents: 40,
				EdgeCents:       9,
				HasTruth:        true,
				OurSide:         "yes",
				OurPnLCents:     37,
			},
			{
				TSMs:      2000,
				Ticker:    "KXHIGHMIA-26APR29-B85.5",
				ValidDate: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
				EdgeCents: 2,
			},
		},
	}
}

func TestWriteCountsPrefixesEveryLine(t *testing.T) {
	res := fixtureResult()

	var plain, md strings.Builder
	writeCounts(&summaryWriter{w: &plain}, "", res)
	writeCounts(&summaryWriter{w: &md}, "- ", res)

	plainLines := strings.Split(strings.TrimRight(plain.String(), "\n"), "\n")
	mdLines := strings.Split(strings.TrimRight(md.String(), "\n"), "\n")
	if len(plainLines) != len(mdLines) {
		t.Fatalf("line count mismatch: stdout %d vs markdown %d", len(plainLines), len(mdLines))
	}
	for i := range plainLines {
		if "- "+plainLines[i] != mdLines[i] {
			t.Errorf("line %d diverged:\n  stdout:   %q\n  markdown: %q", i, plainLines[i], mdLines[i])
		}
	}
	joined := plain.String()
	for _, want := range []string{"frames seen:        100", "paired trades:      2", "MOS-calibrated:     1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("counts output missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "climate-shrunk") || strings.Contains(joined, "Bayesian-updated") {
		t.Errorf("disabled model sections must be omitted, got:\n%s", joined)
	}
}

func TestWriteTopBracketsUsesRunThreshold(t *testing.T) {
	res := fixtureResult()

	var buf strings.Builder
	writeTopBrackets(&summaryWriter{w: &buf}, res)
	out := buf.String()

	if !strings.Contains(out, "edge ≥ 7¢") {
		t.Errorf("heading must echo the run threshold 7¢, got:\n%s", out)
	}
	if !strings.Contains(out, "KXHIGHCHI-26APR29-B62.5") {
		t.Errorf("ticker at 9¢ edge must clear the 7¢ threshold, got:\n%s", out)
	}
	if strings.Contains(out, "KXHIGHMIA-26APR29-B85.5") {
		t.Errorf("ticker at 2¢ edge must not clear the 7¢ threshold, got:\n%s", out)
	}
}

func TestWriteBacktestSectionTotals(t *testing.T) {
	res := fixtureResult()

	var buf strings.Builder
	writeBacktestSection(&summaryWriter{w: &buf}, res)
	out := buf.String()

	for _, want := range []string{
		"- trades entered:    1",
		"- win rate:          100.0%",
		"- total realized:    +37¢  ($+0.37)",
		"| 2026-04-29 | 1 | 1 | 100.0% | +37 | $+0.37 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("backtest section missing %q in:\n%s", want, out)
		}
	}
}

func TestWriteBacktestSectionNoEntries(t *testing.T) {
	res := fixtureResult()
	res.TradesEntered = 0
	res.TradesWon = 0
	res.TotalPnLCents = 0

	var buf strings.Builder
	writeBacktestSection(&summaryWriter{w: &buf}, res)
	out := buf.String()

	if !strings.Contains(out, "no trades cleared the entry threshold") {
		t.Errorf("empty backtest must print the no-entries note, got:\n%s", out)
	}
	if strings.Contains(out, "win rate") || strings.Contains(out, "Daily PnL") {
		t.Errorf("empty backtest must omit rate/daily sections, got:\n%s", out)
	}
}
