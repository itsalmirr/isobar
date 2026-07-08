package journal

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// BenchmarkJournalWrite measures the producer-side enqueue cost. The
// recorder hot path: every WS frame goes through this. The select-default
// drop semantics keep this O(1) even when the consumer is slow.
func BenchmarkJournalWrite(b *testing.B) {
	dir := b.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j, err := Open(Options{
		Dir:          dir,
		Source:       "bench",
		MaxFileBytes: 1 << 30, // huge — we don't want rotation noise
		MaxFileAge:   1 * time.Hour,
		FlushEvery:   1 * time.Second,
		QueueSize:    1 << 16,
		OnDropAlarm:  NoopAlarmHook,
	}, logger)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = j.Close() }()

	// Realistic Kalshi delta payload size (~250 bytes).
	payload := json.RawMessage(`{"type":"orderbook_delta","sid":1,"seq":12345,"msg":{"market_ticker":"KXHIGHNY-26APR22-T64","market_id":"abc-123","price_dollars":"0.4500","delta_fp":"1.00","side":"yes","ts":"2026-04-22T15:30:00.000Z","ts_ms":1745335800000}}`)
	frame := Frame{
		Stamp:   clock.Stamp{Wall: time.Now().UnixNano(), Mono: 0},
		Source:  "kalshi",
		Kind:    "ws",
		Payload: payload,
	}
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		j.Write(frame)
	}
}
