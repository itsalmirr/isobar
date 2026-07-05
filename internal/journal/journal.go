// Package journal implements a per-source append-only JSONL writer with
// rotation, batched fsync, and a non-blocking enqueue API.
//
// Design contract:
//   - Producers (WS receivers, REST pollers) call Write. Write never blocks;
//     if the internal queue is full the frame is dropped and a counter is
//     incremented. The recorder must never apply backpressure to a market
//     data feed.
//   - A single owner goroutine drains the queue, encodes envelopes to a
//     bufio-backed file, flushes + fsyncs every FlushEvery, and rotates by
//     size or age. On rotate, files are optionally gzipped.
//   - Payload bytes are copied on enqueue so callers may safely reuse their
//     read buffers (this matters for coder/websocket which re-uses the
//     read-buffer between Read calls).
//
// Backpressure visibility (ADR-0011):
//   - On every drop, a monotonic atomic counter increments and (every Nth
//     drop) a sampled slog.Debug record fires. Read the counter via
//     Dropped().
//   - The writer goroutine samples the counter once per second; when the
//     drop rate has been at or above AlarmThreshold drops/sec continuously
//     for AlarmSustain, the OnDropAlarm hook is invoked exactly once per
//     crossing edge. Hook invocation runs under a deferred recover; a
//     panicking hook bumps AlarmHookPanicTotal() and is logged but
//     cannot kill the writer goroutine.
//   - OnDropAlarm is REQUIRED. ADR-0011 originally specified a default
//     slog.Warn fallback; that fallback was deleted in A1 because every
//     prod deploy of cmd/tape was running blind to journal drops by
//     accepting the default. Open() now returns ErrAlarmHookRequired
//     if Options.OnDropAlarm is nil. Tests can pass NoopAlarmHook.
//   - The legacy 60-second "frames dropped" warn line is demoted to an
//     info-level cumulative heartbeat; the alarm hook is now the primary
//     operator signal.
//
// The on-disk shape is stable JSONL: one envelope per line, NDJSON-friendly.
// ADR-0011 does NOT bump the on-disk format — instrumentation is in-process
// only and tapes remain replayable by any pre-existing reader.
package journal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// Frame is one record submitted by a producer. Payload should be the raw
// source bytes (e.g., a WS frame body) — not decoded — so that future
// schema changes do not invalidate previously recorded data.
type Frame struct {
	Stamp   clock.Stamp
	Source  string
	Kind    string
	Payload json.RawMessage
}

// EnvelopeSchemaVersion is stamped into every envelope's "v" field at
// write time. It identifies the on-disk shape so a future reader can
// switch behavior based on schema generation. Readers tolerate the
// field's absence (frames written before this constant existed decode
// with v == "" and are re-labeled at read time — see
// internal/replay.EnvelopeSchemaLegacyV0). Reader-side migration
// policy beyond "tolerate absence" is held for Phase 2.
const EnvelopeSchemaVersion = "isobar.journal/v1"

// envelope is the on-disk JSON shape: flat, compact, NDJSON-friendly.
type envelope struct {
	Wall    int64           `json:"t"`
	Mono    int64           `json:"m"`
	Source  string          `json:"src"`
	Kind    string          `json:"k"`
	Version string          `json:"v"`
	Payload json.RawMessage `json:"p,omitempty"`
}

// Options configure the Journal.
//
// The four backpressure-visibility fields (OnDropAlarm, AlarmThreshold,
// AlarmSustain, SampleEvery) are documented end-to-end in
// docs/decisions/0011-journal-backpressure.md. Zero values pick safe
// defaults so journal.Options{Dir,Source} remains a working configuration.
type Options struct {
	Dir              string
	Source           string
	MaxFileBytes     int64
	MaxFileAge       time.Duration
	FlushEvery       time.Duration
	QueueSize        int
	CompressOnRotate bool

	// OnDropAlarm is invoked at most once per crossing edge when the
	// drop rate has been at or above AlarmThreshold drops/sec for at
	// least AlarmSustain. The hook receives the source label, the
	// cumulative drop count, and the duration since the first drop in
	// the current sustained window.
	//
	// OnDropAlarm is REQUIRED. Open() returns ErrAlarmHookRequired if
	// this field is nil. ADR-0011 originally specified a default
	// slog.Warn fallback; A1 deleted that fallback because every prod
	// cmd/tape run was accidentally accepting it. The hook is now the
	// drop-visibility seam — wire it to a real sink (Prometheus, an
	// alarms file, alertmanager). Tests that don't care about drop
	// events may pass NoopAlarmHook.
	OnDropAlarm func(src string, drops uint64, since time.Duration)

	// OnWriteError is invoked on every failed encode/write/flush/fsync on
	// the writer path, carrying the source label and the running
	// write-error count. Unlike OnDropAlarm it is OPTIONAL and nil-tolerant:
	// a nil hook is skipped. It exists because the ADR-0011 drop counter
	// and OnDropAlarm only observe queue-full drops on the enqueue side; a
	// full disk or read-only filesystem drains the queue normally (never
	// full → no drop alarm) while every frame is logged-and-discarded on
	// the write side, re-introducing exactly the silent-loss failure mode
	// ADR-0011 was built to eliminate. This hook (plus WriteErrorsTotal())
	// is the parallel seam for that path; it deliberately does NOT
	// multiplex onto OnDropAlarm, whose delta accounting and alarms-file
	// schema are built around queue-drop counting.
	OnWriteError func(src string, writeErrors uint64, err error)

	// AlarmThreshold is the minimum sustained drops/sec rate that arms
	// the alarm. 0 → 1 drop/sec.
	AlarmThreshold uint64

	// AlarmSustain is how long the rate must stay at or above
	// AlarmThreshold before OnDropAlarm fires. 0 → 60s.
	AlarmSustain time.Duration

	// SampleEvery controls the per-drop debug-log sampling cadence:
	// every Nth dropped frame produces a slog.Debug record. 0 → 1024.
	// Set to math.MaxUint64 to effectively disable the sampled log.
	SampleEvery uint64
}

// dropLogSampleEvery is the default sampling cadence for the per-drop
// debug log. ADR-0011 §3 picks 1024 — high enough that an at-debug
// crank does not flood stderr during a typical burst, low enough that a
// 5-minute investigation window will see hits.
const dropLogSampleEvery uint64 = 1024

// Journal is a per-source append-only writer with rotation and batched fsync.
type Journal struct {
	opts   Options
	logger *slog.Logger

	in       chan envelope
	closed   chan struct{}
	doneOnce sync.Once
	done     chan struct{}

	// dropped is the monotonic per-instance drop counter. Exposed via
	// Dropped(). ADR-0011 §5 keeps this name for back-compat with the
	// existing accessor and the health-check.sh operator script.
	dropped atomic.Uint64

	// writeErrors is the monotonic per-instance count of encode/write/
	// flush/fsync failures on the writer path. Exposed via
	// WriteErrorsTotal(). It is the write-side twin of dropped: dropped
	// tracks queue-full losses on enqueue, writeErrors tracks losses that
	// happen after a frame has been dequeued (ENOSPC, read-only FS, a
	// closed fd). Kept separate so the ADR-0011 drop-alarm accounting and
	// the alarms-file schema stay uncontaminated by write-path failures.
	writeErrors atomic.Uint64

	// Alarm machinery. droppedAtLastTick is the counter value sampled
	// at the previous 1-second alarm tick; the delta is the per-second
	// drop rate. alarmActive is the edge-trigger debounce — true while
	// the hook has been fired and the rate has not cleared back below
	// threshold. alarmFirstSeenNanos is the unix-nano timestamp of the
	// first second in the current sustained-breach window; (now - that)
	// is the "since" duration passed to the hook.
	droppedAtLastTick   atomic.Uint64
	alarmActive         atomic.Bool
	alarmFirstSeenNanos atomic.Int64

	// alarmHookPanics counts how many times a user-supplied OnDropAlarm
	// callback has panicked. ADR-0011 §Consequences pins this as a
	// guarded failure mode: a buggy hook must not be able to kill the
	// writer goroutine and silently turn off all journaling. Read via
	// AlarmHookPanicTotal().
	alarmHookPanics atomic.Uint64

	// Resolved option values (post-default-fill). Kept on the struct so
	// the run goroutine and Write don't re-read opts on every call.
	sampleEvery    uint64
	alarmThreshold uint64
	alarmSustain   time.Duration
	onDropAlarm    func(src string, drops uint64, since time.Duration)
	onWriteError   func(src string, writeErrors uint64, err error)
}

// ErrAlarmHookRequired is returned by Open when Options.OnDropAlarm
// is nil. ADR-0011 §A1 deleted the default-slog-warn fallback because
// every prod cmd/tape run was accidentally accepting it; the hook is
// now a startup-time prerequisite, the same way Dir and Source are.
// Tests that don't exercise drop events should pass NoopAlarmHook.
var ErrAlarmHookRequired = errors.New("journal: Options.OnDropAlarm required (use NoopAlarmHook for tests)")

// errNoOpenFile is the sentinel handed to the write-error path when a
// frame is dequeued but there is no open file to write it to — the state
// left behind when a re-open after a size rotate fails. It lets the
// writer count the lost frame rather than nil-deref bw.Write.
var errNoOpenFile = errors.New("journal: no open file (re-open after rotate failed)")

// NoopAlarmHook is a placeholder hook that does nothing. Use it in
// tests that don't care about drop events; production code must wire
// a real sink (Prometheus increment, alarms file, alertmanager push).
func NoopAlarmHook(src string, drops uint64, since time.Duration) {}

// Open creates a Journal and starts its writer goroutine. Stop with Close.
// Returns ErrAlarmHookRequired if Options.OnDropAlarm is nil.
func Open(opts Options, logger *slog.Logger) (*Journal, error) {
	if opts.Dir == "" || opts.Source == "" {
		return nil, errors.New("journal: Dir and Source required")
	}
	if opts.OnDropAlarm == nil {
		return nil, ErrAlarmHookRequired
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 8192
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = 250 * time.Millisecond
	}
	if opts.SampleEvery == 0 {
		opts.SampleEvery = dropLogSampleEvery
	}
	if opts.AlarmThreshold == 0 {
		opts.AlarmThreshold = 1
	}
	if opts.AlarmSustain <= 0 {
		opts.AlarmSustain = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	scopedLogger := logger.With("component", "journal", "src", opts.Source)

	j := &Journal{
		opts:           opts,
		logger:         scopedLogger,
		in:             make(chan envelope, opts.QueueSize),
		closed:         make(chan struct{}),
		done:           make(chan struct{}),
		sampleEvery:    opts.SampleEvery,
		alarmThreshold: opts.AlarmThreshold,
		alarmSustain:   opts.AlarmSustain,
		onDropAlarm:    opts.OnDropAlarm,
		onWriteError:   opts.OnWriteError,
	}

	j.logger.Info("journal: drop-alarm sink installed")

	go j.run()
	return j, nil
}

// Write enqueues a frame. It never blocks; if the queue is full the frame
// is dropped and the dropped counter incremented (a periodic warn log
// surfaces the count).
//
// Payload bytes are copied so callers may reuse their read buffer.
func (j *Journal) Write(f Frame) {
	var p json.RawMessage
	if len(f.Payload) > 0 {
		p = make(json.RawMessage, len(f.Payload))
		copy(p, f.Payload)
	}
	e := envelope{
		Wall:    f.Stamp.Wall,
		Mono:    f.Stamp.Mono,
		Source:  f.Source,
		Kind:    f.Kind,
		Version: EnvelopeSchemaVersion,
		Payload: p,
	}
	select {
	case j.in <- e:
	default:
		n := j.dropped.Add(1)
		// Sampled debug log: every Nth drop emits one structured
		// record. At debug level this is off in prod; an operator can
		// crank to debug for 5 minutes to localize a burst without
		// flooding stderr. ADR-0011 §3.
		if j.sampleEvery > 0 && n%j.sampleEvery == 0 {
			j.logger.Debug("journal: frame dropped (queue full)",
				"src", j.opts.Source,
				"total_drops", n,
				"queue_depth", len(j.in),
				"queue_capacity", cap(j.in),
				"kind", f.Kind,
			)
		}
	}
}

// Dropped returns the number of frames dropped due to a full queue.
// ADR-0011 §5 preserves this name for the operator health-check.sh
// script and existing test assertions.
func (j *Journal) Dropped() uint64 { return j.dropped.Load() }

// WriteErrorsTotal returns the number of encode/write/flush/fsync
// failures on the writer path. This is the write-side counterpart to
// Dropped(): a persistent disk-full or read-only-filesystem condition
// drains the queue without ever filling it, so Dropped() stays flat
// while WriteErrorsTotal() climbs. Operators and the OnWriteError hook
// use this as the primary signal that the tape is silently lossy on the
// durability-critical path.
func (j *Journal) WriteErrorsTotal() uint64 { return j.writeErrors.Load() }

// AlarmHookPanicTotal returns the monotonic count of OnDropAlarm
// invocations that panicked. Reads are atomic and safe from any
// goroutine. ADR-0011 §Consequences guards the writer goroutine
// against a buggy hook; this accessor lets tests, future Prometheus
// scrape, and operators observe how often the guard tripped.
func (j *Journal) AlarmHookPanicTotal() uint64 { return j.alarmHookPanics.Load() }

// QueueDepth returns the current number of envelopes queued for the
// writer. Reads are racy by nature (len on a channel) but useful as a
// point-in-time gauge — operator dashboards and the cmd/tape alarm
// hook log this alongside QueueCapacity so a drop event carries the
// "how saturated were we" context.
func (j *Journal) QueueDepth() int { return len(j.in) }

// QueueCapacity returns the static capacity of the writer queue
// (== Options.QueueSize after default-fill). Constant for the life
// of the Journal.
func (j *Journal) QueueCapacity() int { return cap(j.in) }

// Close stops the writer goroutine, draining the queue and fsyncing the
// current file. Safe to call from any goroutine; idempotent.
func (j *Journal) Close() error {
	j.doneOnce.Do(func() { close(j.closed) })
	<-j.done
	return nil
}

// Wait blocks until the writer goroutine exits or ctx is cancelled.
// Useful in tests; not required in normal use.
func (j *Journal) Wait(ctx context.Context) {
	select {
	case <-j.done:
	case <-ctx.Done():
	}
}

// Internal ticker intervals. Kept as package-level vars (not Options
// fields) so they remain invisible to normal callers but swappable in
// tests that would otherwise pay real-time seconds to exercise
// age-based rotation, the alarm crossing path, or the cumulative-drop
// heartbeat.
var (
	rotateCheckInterval = 15 * time.Second
	dropReportInterval  = 60 * time.Second
	// alarmCheckInterval is the cadence at which the run goroutine
	// samples the drop counter and decides whether the alarm hook
	// should fire. ADR-0011 §4 picks 1s; tests swap this down to ms.
	alarmCheckInterval = 1 * time.Second
)

// SetAlarmCheckIntervalForTest swaps the alarm-check tick interval
// for cross-package tests (notably cmd/tape's regression harness for
// A1). The previous value is returned so callers can restore it on
// cleanup. NOT safe to call concurrently with Open; intended for
// test-time setup before any Journal exists.
//
// Production code MUST NOT call this. It exists only because tests in
// cmd/tape need to exercise the alarm crossing path without paying
// the 1-second default tick.
func SetAlarmCheckIntervalForTest(d time.Duration) (prev time.Duration) {
	prev = alarmCheckInterval
	alarmCheckInterval = d
	return prev
}

// run is the single owner of the on-disk file.
func (j *Journal) run() {
	defer close(j.done)

	var (
		f       *os.File
		bw      *bufio.Writer
		opened  time.Time
		written int64
		encBuf  bytes.Buffer
	)
	encoder := json.NewEncoder(&encBuf)
	encoder.SetEscapeHTML(false)

	open := func() error {
		path, err := newPath(j.opts.Dir, j.opts.Source)
		if err != nil {
			return err
		}
		nf, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // journal paths are built from our own Dir+timestamp, not user input
		if err != nil {
			return err
		}
		f = nf
		bw = bufio.NewWriterSize(f, 64<<10)
		opened = time.Now()
		written = 0
		j.logger.Info("journal opened", "path", path)
		return nil
	}

	flushAndSync := func() {
		if bw == nil || f == nil {
			return
		}
		if err := bw.Flush(); err != nil {
			j.logger.Error("flush failed", "err", err)
			j.recordWriteError(err)
			return
		}
		if err := f.Sync(); err != nil {
			j.logger.Error("fsync failed", "err", err)
			j.recordWriteError(err)
		}
	}

	rotate := func() {
		if f == nil {
			return
		}
		flushAndSync()
		oldPath := f.Name()
		_ = f.Close()
		f, bw = nil, nil

		if !j.opts.CompressOnRotate {
			return
		}
		if err := gzipFile(oldPath); err != nil {
			j.logger.Warn("gzip on rotate failed; keeping plain file", "path", oldPath, "err", err)
			return
		}
		if err := os.Remove(oldPath); err != nil {
			j.logger.Warn("remove plain after gzip failed", "path", oldPath, "err", err)
		}
	}

	writeOne := func(e envelope) {
		// A failed re-open after a size rotate leaves bw==nil while the
		// run loop keeps draining the queue (see maybeRotateBySize); guard
		// here so a dequeued frame cannot nil-deref bw.Write and kill the
		// writer goroutine. Mirrors the same guards in flushAndSync and
		// rotate. A nil bw means we have no destination, so the frame is a
		// write loss and is counted as one.
		if bw == nil {
			j.recordWriteError(errNoOpenFile)
			return
		}
		encBuf.Reset()
		if err := encoder.Encode(&e); err != nil {
			j.logger.Error("encode failed", "err", err)
			j.recordWriteError(err)
			return
		}
		// json.Encoder appends a newline already; bw.Write returns the byte count.
		n, err := bw.Write(encBuf.Bytes())
		if err != nil {
			j.logger.Error("write failed", "err", err)
			j.recordWriteError(err)
			return
		}
		written += int64(n)
	}

	// maybeRotateBySize rotates the current file when it has grown past
	// MaxFileBytes. It returns false when the post-rotate re-open fails —
	// the caller must then return from run() so the writer goroutine exits
	// cleanly instead of looping with bw==nil. This mirrors the age-rotate
	// path's `return` on re-open failure (see the rotateTimer case) rather
	// than falling through and leaving a nil bufio.Writer behind.
	maybeRotateBySize := func() (ok bool) {
		if j.opts.MaxFileBytes > 0 && written >= j.opts.MaxFileBytes {
			rotate()
			if err := open(); err != nil {
				j.logger.Error("re-open after size rotate failed; writer exiting", "err", err)
				// The journal can no longer persist frames — the queue
				// will drain into a nil file. Count this as a write-path
				// failure and arm the OnWriteError hook so the operator
				// sees the outage, not just a silent goroutine exit.
				j.recordWriteError(err)
				return false
			}
		}
		return true
	}

	if err := open(); err != nil {
		j.logger.Error("initial open failed; entering drain mode", "err", err)
		<-j.closed
		return
	}

	flushTimer := time.NewTicker(j.opts.FlushEvery)
	defer flushTimer.Stop()
	rotateTimer := time.NewTicker(rotateCheckInterval)
	defer rotateTimer.Stop()
	// The 60s ticker, post-ADR-0011, is a low-noise cumulative
	// heartbeat — the primary "operator should look" signal is the
	// rate-aware alarm hook fired from alarmTimer below.
	dropReport := time.NewTicker(dropReportInterval)
	defer dropReport.Stop()
	alarmTimer := time.NewTicker(alarmCheckInterval)
	defer alarmTimer.Stop()

	for {
		select {
		case <-j.closed:
			// Drain remaining queue, then fsync + close. Rotate as needed:
			// on shutdown bursts the residual queue can otherwise produce a
			// final file that exceeds MaxFileBytes by an unbounded margin.
			for {
				select {
				case e := <-j.in:
					writeOne(e)
					if !maybeRotateBySize() {
						return
					}
				default:
					flushAndSync()
					if f != nil {
						_ = f.Close()
					}
					return
				}
			}

		case e := <-j.in:
			writeOne(e)
			if !maybeRotateBySize() {
				return
			}

		case <-flushTimer.C:
			flushAndSync()

		case <-rotateTimer.C:
			if j.opts.MaxFileAge > 0 && f != nil && time.Since(opened) >= j.opts.MaxFileAge {
				rotate()
				if err := open(); err != nil {
					j.logger.Error("re-open after age rotate failed", "err", err)
					return
				}
			}

		case <-alarmTimer.C:
			j.checkDropAlarm()

		case <-dropReport.C:
			// Demoted to Info: this is the cumulative heartbeat now,
			// not the primary alert. ADR-0011: the rate-aware alarm
			// is what an operator should react to.
			if d := j.dropped.Load(); d > 0 {
				j.logger.Info("journal: cumulative drops",
					"src", j.opts.Source,
					"total_drops", d,
				)
			}
		}
	}
}

// checkDropAlarm samples the per-instance drop counter once per
// alarmCheckInterval, computes the per-second rate against the previous
// sample, and edge-fires j.onDropAlarm when the rate has been at or
// above j.alarmThreshold continuously for at least j.alarmSustain.
//
// Edge-trigger semantics (ADR-0011 §4): the hook fires exactly once per
// crossing. While the rate stays above threshold the hook does not
// re-fire. The rate must drop back below threshold (alarm cleared) for
// the next crossing to re-arm.
//
// "since" passed to the hook is (now - first-sustained-tick), so the
// operator can see how long the breach has been building.
func (j *Journal) checkDropAlarm() {
	now := time.Now()
	cur := j.dropped.Load()
	prev := j.droppedAtLastTick.Swap(cur)
	// Convert the per-tick delta into a per-second rate. With the
	// 1-second default tick, the delta IS the rate; the divide keeps
	// the math right under a test-shortened alarmCheckInterval.
	tickSec := alarmCheckInterval.Seconds()
	if tickSec <= 0 {
		tickSec = 1
	}
	deltaPerSec := uint64(float64(cur-prev) / tickSec)

	if deltaPerSec >= j.alarmThreshold {
		// Mark the start of a sustained-breach window the first time
		// we see a breach after a clear period.
		if j.alarmFirstSeenNanos.Load() == 0 {
			j.alarmFirstSeenNanos.Store(now.UnixNano())
		}
		firstSeen := time.Unix(0, j.alarmFirstSeenNanos.Load())
		since := now.Sub(firstSeen)
		if since >= j.alarmSustain && !j.alarmActive.Load() {
			// Edge-trigger: arm and fire exactly once for this crossing.
			j.alarmActive.Store(true)
			j.safeInvokeAlarm(j.opts.Source, cur, since)
		}
		return
	}

	// Rate has fallen back below threshold: clear the window and
	// re-arm the edge-trigger so a future sustained breach fires again.
	j.alarmFirstSeenNanos.Store(0)
	j.alarmActive.Store(false)
}

// safeInvokeAlarm runs the OnDropAlarm hook under a deferred recover
// so a panicking callback cannot kill the writer goroutine.
//
// ADR-0011 §Consequences specifically lists this as a guarded failure
// mode: the writer is the single owner of the on-disk file descriptor,
// and the OnDropAlarm seam is the prescribed integration point for
// Phase 6 Prometheus metrics. A buggy operator-supplied hook (nil
// deref, divide-by-zero, panic inside slog handler, etc.) must not be
// able to silently turn off all journaling — that would re-introduce
// the original silent-drop defect through a different layer.
//
// On panic: log the panic + stack at error level, bump
// alarmHookPanics, then return so the run() select loop continues.
// The next alarm tick will retry; if the hook keeps panicking the
// counter keeps ticking and an operator can observe via
// AlarmHookPanicTotal() (or, in Phase 6, the Prometheus gauge).
func (j *Journal) safeInvokeAlarm(src string, drops uint64, since time.Duration) {
	if j.onDropAlarm == nil {
		// Belt-and-braces: Open() always installs a non-nil hook
		// (custom or default-slog-warn fallback). Guarding here so a
		// future refactor that resets j.onDropAlarm cannot reintroduce
		// a nil-deref crash in the writer goroutine.
		return
	}
	defer func() {
		if r := recover(); r != nil {
			j.alarmHookPanics.Add(1)
			j.logger.Error("journal: OnDropAlarm panicked — writer survives, hook skipped this tick",
				"src", src,
				"drops", drops,
				"since", since,
				"recover", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	j.onDropAlarm(src, drops, since)
}

// recordWriteError bumps the write-error counter and, if an OnWriteError
// hook was supplied, invokes it under a deferred recover so a panicking
// hook cannot kill the writer goroutine — the same guarantee
// safeInvokeAlarm gives the drop path. Panics are folded into the
// existing alarmHookPanics counter so AlarmHookPanicTotal() remains the
// single "an operator hook misbehaved" signal. The counter always
// advances even when the hook is nil, so WriteErrorsTotal() is a
// complete tally regardless of whether a sink is wired.
func (j *Journal) recordWriteError(err error) {
	n := j.writeErrors.Add(1)
	if j.onWriteError == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			j.alarmHookPanics.Add(1)
			j.logger.Error("journal: OnWriteError panicked — writer survives, hook skipped this call",
				"src", j.opts.Source,
				"write_errors", n,
				"recover", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	j.onWriteError(j.opts.Source, n, err)
}

// newPath builds dir/source/YYYY-MM-DD/source-HHMMSS.NNNNNNNNN.jsonl,
// creating directories as needed.
func newPath(dir, source string) (string, error) {
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	folder := filepath.Join(dir, day)
	if err := os.MkdirAll(folder, 0o750); err != nil {
		return "", err
	}
	stamp := now.Format("150405.000000000")
	return filepath.Join(folder, fmt.Sprintf("%s-%s.jsonl", source, stamp)), nil
}

// gzipFile compresses src into src+".gz". Returns nil on success.
func gzipFile(src string) error {
	in, err := os.Open(src) //nolint:gosec // src is a journal file path we wrote ourselves
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(src+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // see above
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return err
	}
	// The .gz must be durable before rotate() unlinks the plain .jsonl
	// (the only fsynced copy). gz.Close() flushes the gzip footer into
	// out's kernel buffers but does not push them to stable storage; a
	// power loss after os.Remove but before write-back would lose the
	// rotated segment entirely. fsync here, then capture Close's error
	// (a deferred blind Close would swallow an ENOSPC surfacing at
	// close) so rotate() only removes the plain file when the compressed
	// copy is truly on disk.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
