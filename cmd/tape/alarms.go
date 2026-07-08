// Drop-alarm wiring for cmd/tape (ADR-0011, A1).
//
// internal/journal exposes the OnDropAlarm seam; until A1 every prod
// deploy was accidentally accepting the default slog.Warn fallback and
// running blind to journal drops. This file installs three concrete
// sinks per hook firing:
//
//  1. JournalDropsTotal — a process-wide monotonic atomic counter,
//     exposed via JournalDropsTotal(). Phase 6 (Prometheus) will scrape
//     this; the regression test in main_test.go also reads it.
//  2. A structured slog.Error log carrying the delta drop count, the
//     point-in-time queue depth/capacity, and the sustained-window
//     duration ("since the first drop in this breach").
//  3. A single-line append to <DataDir>/alarms/<YYYY-MM-DD>.jsonl, so
//     drops are observable independent of any external metrics scrape
//     or log pipeline. The file is opened lazily on first alarm,
//     rotated by civil-date (UTC), and closed via the alarmSink.Close
//     in the cmd/tape errgroup-shutdown defer chain.
//
// journal.Open now refuses to construct a Journal with a nil
// OnDropAlarm (ErrAlarmHookRequired). The original ADR-0011 default
// fallback (default-slog-warn) is intentionally gone — the whole point
// of A1 is that nothing accidentally falls back to "alarm goes to a
// log line nobody is watching" anymore.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// journalDropsTotal is the process-wide monotonic counter of journal
// drop events surfaced via OnDropAlarm. Each firing of the hook adds
// the delta of cumulative drops since the previous firing for that
// source — i.e. JournalDropsTotal() is the sum across all sources of
// all drops that have ever crossed the alarm threshold.
//
// Phase 6 will expose this as a Prometheus counter; the seam is the
// hook, this var is the in-process aggregator.
var journalDropsTotal atomic.Uint64

// JournalDropsTotal returns the process-wide monotonic count of
// journal drops observed by the alarm hook. Safe to read from any
// goroutine.
func JournalDropsTotal() uint64 { return journalDropsTotal.Load() }

// alarmSink is the cmd/tape-side sink for journal.OnDropAlarm.
//
// One instance is constructed per cmd/tape process and shared across
// the per-source journals (kalshi, openmeteo). The instance carries:
//
//   - the lazy-opened alarms file (one civil-date-rotated file per
//     process per day);
//   - the per-source "last cumulative drops" map used to compute the
//     delta count for each firing;
//   - a mutex serializing concurrent firings (two journals can fire
//     simultaneously and the file's append-only contract is per-record).
//
// alarmSink methods are safe to call from multiple goroutines.
type alarmSink struct {
	dataDir string
	logger  *slog.Logger

	mu       sync.Mutex
	file     *os.File // lazily opened on first alarm
	dayStamp string   // YYYY-MM-DD of currently-open file; "" if file == nil

	// lastDrops tracks the cumulative drop count for each source as
	// of the previous alarm firing. The delta is the per-firing
	// dropped_count surfaced in logs and the alarms file.
	lastDrops map[string]uint64
}

// newAlarmSink constructs an alarmSink and returns the hook closure
// that journal.Options.OnDropAlarm should be bound to. The closure
// captures the sink (and a *journal.Journal-aware accessor pair for
// queue depth/capacity, supplied by the caller) so each hook firing
// can read the live queue state.
//
// The returned alarmSink.Close MUST be called on process shutdown via
// the existing errgroup-shutdown defer chain so the alarms file is
// fsynced and closed cleanly. Idempotent.
func newAlarmSink(dataDir string, logger *slog.Logger) *alarmSink {
	return &alarmSink{
		dataDir:   dataDir,
		logger:    logger.With("component", "tape-alarm"),
		lastDrops: make(map[string]uint64),
	}
}

// hookFor returns an OnDropAlarm closure for the given source/journal
// pair. The closure reads queue depth/capacity from queueDepth/queueCap
// at fire time so the alarm carries the saturation context.
//
// queueDepth/queueCap are accessor functions rather than ints because
// QueueDepth is a point-in-time gauge (len on a channel) that must be
// read at alarm time, not at hook-construction time.
func (s *alarmSink) hookFor(src string, queueDepth func() int, queueCap func() int) func(string, uint64, time.Duration) {
	return func(hookSrc string, drops uint64, since time.Duration) {
		// hookSrc and src must agree — the hook is bound to a source
		// label at construction time and the journal stamps the same
		// label back through. Cheap sanity check.
		if hookSrc != src {
			s.logger.Warn("tape-alarm: source label mismatch",
				"closure_src", src, "hook_src", hookSrc)
		}
		s.fire(src, drops, since, queueDepth(), queueCap())
	}
}

// fire is the inner work of one alarm event: compute delta, bump the
// process counter, emit the structured log line, append to the alarms
// file. Serialized via s.mu so concurrent firings from different
// journals don't interleave file writes or corrupt lastDrops.
func (s *alarmSink) fire(src string, cumulativeDrops uint64, since time.Duration, depth, capacity int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.lastDrops[src]
	delta := cumulativeDrops
	if cumulativeDrops > prev {
		delta = cumulativeDrops - prev
	}
	s.lastDrops[src] = cumulativeDrops
	journalDropsTotal.Add(delta)

	now := time.Now().UTC()
	sinceLastDropMs := since.Milliseconds()

	// (1) Structured log — error level, per spec.
	s.logger.Error("journal: drop-alarm fired",
		"src", src,
		"dropped_count", delta,
		"queue_depth", depth,
		"queue_capacity", capacity,
		"since_last_drop_ms", sinceLastDropMs,
	)

	// (2) Alarms file write — best-effort. A file-system error here
	// must not propagate as a panic (we're inside a hook the journal
	// invokes under recover, but defense-in-depth: don't trip the
	// recover counter on a transient EIO). Log+continue.
	if err := s.appendAlarmRecordLocked(now, src, delta, sinceLastDropMs, depth); err != nil {
		s.logger.Error("tape-alarm: alarms-file write failed; structured log still emitted",
			"err", err, "src", src)
	}
}

// appendAlarmRecordLocked appends one JSON record to today's alarms
// file, opening/rotating as needed. Caller must hold s.mu.
//
// File layout: <DataDir>/alarms/<YYYY-MM-DD>.jsonl, one record per
// line, append-only, NDJSON-friendly.
func (s *alarmSink) appendAlarmRecordLocked(now time.Time, src string, dropped uint64, sinceLastDropMs int64, depth int) error {
	day := now.Format("2006-01-02")
	if s.dayStamp != day {
		// Date rolled or first write: rotate.
		if err := s.rotateLocked(day); err != nil {
			return err
		}
	}
	rec := struct {
		TS              string `json:"ts"`
		Src             string `json:"src"`
		Dropped         uint64 `json:"dropped"`
		QueueDepth      int    `json:"queue_depth"`
		SinceLastDropMs int64  `json:"since_last_drop_ms"`
	}{
		TS:              now.Format(time.RFC3339Nano),
		Src:             src,
		Dropped:         dropped,
		QueueDepth:      depth,
		SinceLastDropMs: sinceLastDropMs,
	}
	b, err := json.Marshal(&rec)
	if err != nil {
		return fmt.Errorf("marshal alarm record: %w", err)
	}
	b = append(b, '\n')
	if _, err := s.file.Write(b); err != nil {
		return fmt.Errorf("write alarms file: %w", err)
	}
	// Per-write sync — alarms are low-volume (rate-limited by the
	// edge-trigger debounce + sustain window) and operators want
	// durability above all here. The sync cost is dwarfed by the
	// breach event the operator is about to investigate.
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("fsync alarms file: %w", err)
	}
	return nil
}

// rotateLocked closes the current alarms file (if any), creates the
// alarms/ directory if needed, and opens today's file in append mode.
// Caller must hold s.mu.
func (s *alarmSink) rotateLocked(day string) error {
	if s.file != nil {
		_ = s.file.Sync()
		_ = s.file.Close()
		s.file = nil
	}
	alarmsDir := filepath.Join(s.dataDir, "alarms")
	if err := os.MkdirAll(alarmsDir, 0o750); err != nil { //nolint:gosec // alarmsDir is built from operator-configured DataDir, not user input
		return fmt.Errorf("mkdir %s: %w", alarmsDir, err)
	}
	path := filepath.Join(alarmsDir, day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // path is built from configured DataDir + civil date, not user input
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	s.file = f
	s.dayStamp = day
	return nil
}

// Close flushes and closes the alarms file. Safe to call when no file
// is open; idempotent. Wired through the cmd/tape errgroup-shutdown
// defer chain — see run() in main.go.
func (s *alarmSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	syncErr := s.file.Sync()
	closeErr := s.file.Close()
	s.file = nil
	s.dayStamp = ""
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
