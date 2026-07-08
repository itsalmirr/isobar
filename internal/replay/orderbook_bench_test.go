package replay

import (
	"encoding/json"
	"testing"
)

// BenchmarkApplySnapshot measures the cost of replacing the entire book
// state from a snapshot frame. This runs once per ticker per WS reconnect
// + Kalshi snapshot interval (~30s). On 96 subscribed tickers that's
// ~3-10 calls/sec at peak; far from a hot path. Tracked anyway as a
// regression baseline.
func BenchmarkApplySnapshot(b *testing.B) {
	msg := json.RawMessage(`{
		"market_ticker":"KXHIGHNY-26APR22-T64",
		"yes_dollars_fp":[["0.4500","100.00"],["0.4400","50.00"],["0.4300","25.00"],["0.4200","10.00"],["0.4100","5.00"]],
		"no_dollars_fp":[["0.5200","75.00"],["0.5300","40.00"],["0.5400","20.00"],["0.5500","12.00"],["0.5600","6.00"]]
	}`)
	book := NewBook()
	b.ResetTimer()
	for range b.N {
		_ = book.ApplySnapshot(msg)
	}
}

// BenchmarkApplyDelta measures the cost of one orderbook-delta frame.
// At 28 frames/sec sustained (production observed) and ~99% deltas, this
// is the recorder's dominant cost for any consumer that decodes (analyze,
// edge-scan). Edge-scan replays ~14M frames per scan, all here.
func BenchmarkApplyDelta(b *testing.B) {
	book := NewBook()
	_ = book.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4500","100.00"]],
		"no_dollars_fp":[["0.5200","75.00"]]
	}`))
	deltaPlus := json.RawMessage(`{"price_dollars":"0.4500","delta_fp":"5.00","side":"yes"}`)
	deltaMinus := json.RawMessage(`{"price_dollars":"0.4500","delta_fp":"-3.00","side":"yes"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			_ = book.ApplyDelta(deltaPlus)
		} else {
			_ = book.ApplyDelta(deltaMinus)
		}
	}
}

// BenchmarkApplyFrameToBooks is the kernel that walks a live tape.
// Includes the json.Unmarshal of KalshiFrame + dispatch. Should match
// real-tape pacing closely.
func BenchmarkApplyFrameToBooks(b *testing.B) {
	books := map[string]*Book{}
	walls := map[string]int64{}
	snapshot := KalshiFrame{
		Type: "orderbook_snapshot",
		Msg:  json.RawMessage(`{"market_ticker":"KXHIGHNY-26APR22-T64","yes_dollars_fp":[["0.4500","100"]],"no_dollars_fp":[["0.5200","75"]]}`),
	}
	delta := KalshiFrame{
		Type: "orderbook_delta",
		Msg:  json.RawMessage(`{"market_ticker":"KXHIGHNY-26APR22-T64","price_dollars":"0.4500","delta_fp":"1","side":"yes"}`),
	}
	env := Envelope{Wall: 1000}
	// Prime with the snapshot so deltas have a book.
	_, _ = ApplyFrameToBooks(books, walls, env, snapshot)
	b.ResetTimer()
	for range b.N {
		_, _ = ApplyFrameToBooks(books, walls, env, delta)
	}
}

// BenchmarkBookMidYesCents measures the lookup cost on the end-of-window
// state, called once per (ticker, scan) in edge-scan.
func BenchmarkBookMidYesCents(b *testing.B) {
	book := NewBook()
	_ = book.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4500","100"],["0.4400","50"],["0.4300","25"]],
		"no_dollars_fp":[["0.5200","75"],["0.5300","40"],["0.5400","20"]]
	}`))
	b.ResetTimer()
	for range b.N {
		_ = book.MidYesCents()
	}
}
