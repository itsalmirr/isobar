# 0011 — Journal backpressure: drop with counter, sampled debug log, and alarm hook

**Status:** Accepted
**Date:** 2026-05-13
**Authors:** isobar team
**Triggered by:** P1 triage of `internal/journal/journal.go` silent-drop defect

## Context

`internal/journal/journal.go` is the on-disk landing zone for every byte
the recorder receives from upstream — Kalshi WS frames and Open-Meteo
REST poll bodies. Every downstream artifact (calibration kernels, MOS
coefficients, Brier-skill reports, backtest PnL) is built from
replaying these journals. The journal is the project's ground-truth
corpus.

The current contract (see package doc comment) says:

> Producers call Write. Write never blocks; if the internal queue is
> full the frame is dropped and a counter is incremented. The recorder
> must never apply backpressure to a market data feed.

The non-blocking choice is correct. The visibility of the drop is not.
The only signal a drop has occurred is a 60-second periodic warn log,
fired only if the cumulative counter is non-zero:

```go
case <-dropReport.C:
    if d := j.dropped.Load(); d > 0 {
        j.logger.Warn("frames dropped (queue full)", "count", d)
    }
```

No per-event log, no rate, no structured alarm, no operator hook. In
practice:

1. **Silent for up to 60s.** A transient fsync stall that fills the
   16384-frame queue and clears yields one warn line with a cumulative
   count and no time-localization.
2. **No rate signal.** "127 frames dropped" without a window cannot
   distinguish a 200ms burst from sustained pressure. The operational
   response differs.
3. **No programmatic alarm.** Phase-6 observability (Prometheus +
   alertmanager) is not yet wired; today's sink is stderr/journald.
   When Phase 6 lands we want a clean seam to plug a counter in.
4. **Corpus is silently lossy.** Every dropped frame is one observation
   the calibration backtest will not see. ADR-0007 set the NWS CLI
   bulletin as the y-target; the x-side is built from this journal. A
   30-second drop during a Kalshi book update at T−3h is
   indistinguishable from "no event happened" on replay. The product
   lead's framing is correct: this is a data-integrity defect.

### Who calls Write and how often

Two producers:

- **Kalshi WS subscriber** (`internal/kalshi/ws.go` → `Subscriber.runOnce`).
  Calls `conn.Read` in a tight loop and forwards each raw frame body to
  `OnFrame`, which in `cmd/tape` is `makeWSFrameHandler` → `jrn.Write`.
  Exactly **one goroutine** per source pushing into the channel. Frame
  rate during active hours sits below 100/s; pathological bursts (book
  snapshots after reconnect, bracket settlement) hit ~1000/s for
  sub-second windows.
- **Open-Meteo poller** (`internal/openmeteo`). Calls Write once per
  `PollInterval` (5 minutes in prod). Not a real drop-pressure source.

Both producers are single-goroutine. **Blocking the producer is not
free.** `coder/websocket` uses one read buffer per connection and
pumps frames out of the TCP socket as fast as the application reads.
A 50ms stall in `Write` propagates 50ms of receive-window slack back
to the kernel buffer; bursty Kalshi book updates can saturate the
kernel buffer within ~10-30ms during a settlement sweep. Once the
kernel buffer fills, upstream WS frames are TCP-buffered then
eventually dropped at the OS layer — and *that* loss is truly silent:
no application-level event for it. Mid-pipeline backpressure pushes
the queueing problem one layer down to a place with strictly less
visibility than the in-process drop counter.

### Journal lifecycle

The writer goroutine (`Journal.run`) is the single owner of the file
descriptor. It selects on `j.closed` (drains the queue, fsyncs, exits),
`j.in` (encode + bufio write + maybe rotate by size), `flushTimer.C`
every 250ms (bufio flush + fsync), `rotateTimer.C` every 15s (age
rotation check), and `dropReport.C` every 60s (the stale warn log).

The bufio buffer is 64 KiB. Under normal load (~20-50 frames/s,
payloads typically <2 KiB), the bottleneck is fsync latency, not
encoding. The 16384-slot queue gives ~5-10s of headroom over a
sustained 50/s rate — large compared to the prod Vultr fsync tail
(median ~3ms, p99 ~30ms, worst observed ~250ms during a concurrent
gzip-on-rotate). The queue is correctly sized for steady state;
drops indicate a real anomaly.

## Decision

**We adopt Option B: keep `Write(Frame)` non-blocking, and add
structured visibility around the drop event — a per-source monotonic
counter, a sampled per-event debug log, and an operator-installable
alarm hook that fires when the drop rate crosses a threshold.**

Specifically:

1. **Signature is preserved.** `Write(f Frame)` stays exactly as it is.
   No caller in `cmd/tape` or anywhere else has to change.

2. **Drop counter is per-instance and per-source.** The existing
   `dropped atomic.Uint64` stays. `opts.Source` becomes a label on
   every log line and alarm payload. Phase 6 maps this to
   `journal_frames_dropped_total{src="kalshi"|"openmeteo"}`.

3. **Per-event sampled debug log.** On each drop, emit a debug-level
   structured log every `SampleEvery` drops (default: 1024). The log
   carries `{src, count_since_open, queue_size, kind}`. At debug level
   it is off by default in prod; at info level the operator can crank
   it on for 5 minutes to localize a burst without flooding stderr.

4. **Alarm hook with rate detection.** Add a callback field to the
   `Journal`:

   ```go
   OnDropAlarm func(src string, drops uint64, since time.Duration)
   ```

   The writer goroutine runs a 1-second tick that computes
   `delta = dropped.Load() − last_seen`. If `delta ≥ AlarmThreshold`
   (default: 1 drop/second) **and** the condition has persisted for at
   least `AlarmSustainFor` (default: 60s), the hook is invoked exactly
   once per crossing edge with `(src, total_drops, since)`. The hook is
   debounced — the next invocation requires the condition to clear and
   re-cross.

   **Default hook:** a structured `slog.Warn` with fields
   `{src, drops, since, threshold, sustained_for}`. If the operator
   installs nothing, behavior is strictly better than today's
   60-second cumulative log: the alarm fires only on sustained
   pressure, carries a rate, and is timestamped to the crossing.

   **Phase 6 hook:** the operator wires the hook to increment a
   Prometheus counter and route to alertmanager. The Journal does not
   import any Prometheus package; the hook is the seam.

5. **`Dropped()` accessor stays.** The existing `Dropped() uint64`
   method is preserved for tests and the `health-check.sh` script in
   `docs/OPERATIONS.md` §3.

6. **Buffer size unchanged.** 16384 frames at the prod config is the
   right size given the measured fsync tail and load. Larger buffers
   are not a fix for visibility, and we explicitly reject "just raise
   it" as the response to drops (see Alternatives).

### Why not Option A (block-with-deadline + typed error)

The argument for Option A is locality: the caller knows *this* frame
was dropped and can decide. But all `Write` callers in this repo are
single-goroutine producers reading from a network socket with no
application-layer flow control. The options on `ErrQueueFull` are:

1. **Retry in a loop.** Blocking, just relocated. Same TCP buffer
   pressure problem.
2. **Drop at the caller.** What we do today, with no gained visibility.
3. **Escalate (kill the WS, force reconnect).** Causes gigabytes of
   state replay on the snapshot channel; loses more frames than the
   drop event itself.

None is operationally better. The only "real" use of `ErrQueueFull`
would be routing to a secondary disk-backed overflow queue — a second
journal defending against the first journal being slow, which is an
architectural smell.

The WS reader cannot do anything useful with the error: no fallback
storage, no way to slow the producer. Option A buys a richer signal
nothing in the codebase is shaped to act on. Option B puts the signal
where the *operator* can act: "sustained drop rate exceeded threshold"
is the correct trigger for "go look at disk, fsync, CPU, or downstream
consumer" — none of which the WS reader can address from its select
loop.

## Alternatives considered

- **Option A — Block-with-deadline + typed error.** Strictly worse for
  our producer shape (single-goroutine network socket, no app-layer
  flow control). Punts visibility to a caller that cannot use it. See
  "Why not Option A" above for the full case.

- **Raise QueueSize alone (32K, 64K).** Rejected. Moves the threshold
  without resolving the silent-drop problem. A 64K queue that drops
  one frame still drops one frame; we remain blind. Worse memory
  amplification on overflow bursts, longer shutdown drain. May raise
  later as a separate decision if the new alarm signal shows
  sustained near-saturation — but only with evidence.

- **Bounded ring buffer with overwrite-newest.** Rejected. Loses the
  most *recent* frame, which is operationally worst — recent frames
  carry the trade-time info closest to calibration. Overwrite-oldest
  silently rewrites tape shape and discards historical context a
  backtest needs. "Drop incoming when full" is the correct policy;
  only visibility was missing.

- **Synchronous fsync (drop the 250ms batching).** Shortens
  worst-case queue residency but multiplies syscall pressure ~10x; a
  known foot-gun on consumer-grade NVMe. Queueing is the right
  elasticity mechanism.

- **Encoding worker pool.** JSON encoding is not the bottleneck
  (profiled <2% CPU at peak). A pool would shuffle frame ordering on
  disk, breaking the NDJSON-is-a-tape invariant replays depend on.

- **Disk-backed overflow queue.** Reinvents the journal one layer up.
  If the primary cannot write fast enough, a secondary on the same
  disk does not help. Defer indefinitely.

## Consequences

### What changes in `internal/journal/journal.go`

New fields on `Journal`: `droppedAtLast atomic.Uint64` (for rate
computation), `alarmActive atomic.Bool` (edge-triggered debounce),
`alarmFirstSeen atomic.Int64` (unix-nanos of first sustained breach),
plus `onDropAlarm`, `alarmThreshold`, `alarmSustain`, `sampleEvery`.

New fields on `Options`: `OnDropAlarm func(src string, drops uint64,
since time.Duration)`, `AlarmThreshold uint64` (drops/sec; 0 → default
1), `AlarmSustain time.Duration` (0 → default 60s), `SampleEvery
uint64` (0 → default 1024).

`Write` gains a sampling branch on the drop path:

```go
default:
    n := j.dropped.Add(1)
    if j.sampleEvery > 0 && n % j.sampleEvery == 0 {
        j.logger.Debug("frame dropped (queue full)",
            "n", n, "kind", f.Kind, "queue_cap", cap(j.in))
    }
```

`run` gains a 1-second alarm tick separate from the existing 60-second
`dropReport.C`; `checkDropAlarm` computes the 1s delta, manages the
`alarmActive`/`alarmFirstSeen` debounce, and calls `onDropAlarm` on
the crossing edge. Default sink is a structured `slog.Warn`. The 60s
`dropReport` is retained as a low-noise cumulative heartbeat.

`Close` semantics are unchanged. The shutdown drain loop (lines
264-279 today) already walks `j.in` until empty before fsync+close;
the alarm machinery is purely in-band and does not interact with
shutdown.

### What changes for callers

Nothing in steady state. `Write(Frame)` keeps its signature and
non-blocking semantics. `cmd/tape/main.go` gains one optional
option-pass when constructing the Journal — initially the default
(slog warn) hook, replaced by a Prometheus-emitting hook when Phase
6 lands. One-line change to `openJournal`.

### What stays gated on Phase 6

The Prometheus counter (`journal_frames_dropped_total{src}`) is not
exported by this change. `OnDropAlarm` is the seam where Phase 6
plugs in. Phase 6 will stand up the Prometheus registry in
`internal/obs`, define `journal_frames_dropped_total` as a
`CounterVec` labelled by `src`, and pass an `OnDropAlarm` to each
Journal that increments the counter and routes a higher-cardinality
event to alertmanager. ADR-0011 lands the seam; Phase 6 fills the
socket.

### Other consequences

Good:

- Silent-drop defect closed. Operators get a rate-aware,
  edge-triggered alarm with timestamp and source label. The 60s warn
  becomes a cumulative heartbeat, not the primary alert.
- Producer stays non-blocking. TCP-layer frame loss avoided; the 16K
  buffer + 250ms flush headroom continues to absorb normal bursts.
- Hook seam is the right shape for Phase 6: no Prometheus imports in
  `internal/journal`, no metric naming committed in this package, no
  test-time dependency on the observability stack.
- `OPERATIONS.md` §3 (healthy-steady-state signals) and §7
  (troubleshooting entry) get a real signal to point at.

Bad:

- Operator must wire `OnDropAlarm` to a real sink for full value;
  default is `slog.Warn` until Phase 6 — better than today but still
  log-grep-driven. Treated as a startup requirement (see below).
- Three new Option fields on a deliberately minimal struct; defaults
  preserve `journal.Options{}` ergonomics. One more 1s timer on the
  writer goroutine, negligible CPU.

Neutral: no on-disk format change (JSONL envelope schema untouched;
tapes replay identically); no producer call-site changes (the two
`jrn.Write` calls in `cmd/tape/main.go` are unmodified).

## Detection, mitigation, owner

**Residual risk:** if the operator does not install an `OnDropAlarm`
hook, the alarm path falls back to the default `slog.Warn` sink. The
alarm carries a rate, a sustained-for window, and an edge-triggered
debounce — strictly better than today — but it is still log-driven
and requires the operator to watch journald or to have set up a
journald-to-alert bridge.

**Detection:** the recorder logs `journal: drop-alarm sink installed
(<name>)` at startup if `OnDropAlarm` is non-nil; otherwise it logs
`journal: drop-alarm sink is default slog.Warn — wire a real alarm
for prod` at `Warn` level. The latter surfaces in the 3-line startup
grep an operator does on a new deploy.

**Mitigation:** `docs/OPERATIONS.md` §1 gains a checklist item:
*"Verify a non-default alarm hook is installed for production runs."*
The check is the absence of the `drop-alarm sink is default` warn
line in the first 5 seconds of logs. Until Phase 6 lands a real
Prometheus sink, the "non-default hook" can be a stderr-printing
hook that also writes to a dedicated alert file under `data/alerts/`.

**Owner:** isobar team. The Phase 6 work item that promotes the
hook to Prometheus is tracked in `docs/ROADMAP.md`.

## Test plan

Deterministic tests in `internal/journal/journal_test.go`, no
real-time sleeps. Uses the existing package-level interval-swap
pattern (`rotateCheckInterval`, `dropReportInterval`); a new
`alarmCheckInterval` is added in the same shape.

1. **Drop counter increments under saturation.** Open with
   `QueueSize: 4`, `FlushEvery: 1*time.Hour` (writer parked), call
   `Write` 100 times, assert `Dropped() == 96`. The 4 in-flight are
   blocked on the writer; counter increments are atomic and visible
   immediately.

2. **Sampled debug log fires at the configured cadence.** Install a
   capturing `slog.Handler`, set `SampleEvery: 10`, drop 100 frames,
   assert exactly 10 debug records with expected fields
   (`{n, kind, queue_cap}`).

3. **Alarm hook fires on threshold crossing.** Set
   `AlarmThreshold: 5`, `AlarmSustain: 50ms`, install a hook pushing
   onto a buffered channel, swap `alarmCheckInterval = 10ms`. Drop 50
   frames in a burst, wait for receive, assert
   `(src == "test", drops >= 50, since >= 50ms)`. Hook called
   **exactly once** per crossing, not per tick.

4. **Alarm hook is edge-triggered.** Continue (3): stop dropping for
   >`AlarmSustain`, drop another burst. Hook fires a second time on
   the re-cross. Does not fire while continuously active.

5. **In-flight frames pre-drop flushed before close.** Open with
   `QueueSize: 8`, write 5 frames, assert `Dropped() == 0`. `Close()`.
   Re-open the file, assert all 5 envelopes parseable and in order.
   Guards against the drop machinery short-circuiting the `Close`
   drain.

6. **Default hook is structured slog.Warn.** With no `OnDropAlarm`,
   capture handler, force the alarm condition, assert one warn
   record with `level == slog.LevelWarn` and expected fields.

7. **Race detector clean.** All tests run under `go test -race`.
   Counter, `alarmActive`, `alarmFirstSeen` are all `atomic.*`;
   tests exercise concurrent writers + the single owner goroutine.

Benchmarks (`journal_bench_test.go`):
`BenchmarkWrite_NotFull` baseline (assert ≤10% regression vs
pre-change), and `BenchmarkWrite_Full_Dropping` (drop path cost
including sampled log; sets a budget for the Phase 6 Prometheus
increment).

## Reversal cost

Low. The hook is additive; on-disk format is unchanged; the `Write`
signature is preserved. If the alarm machinery has a flaw — e.g.,
spurious edge-triggered fires under an unanticipated load pattern —
we revert by setting `OnDropAlarm: nil` and `SampleEvery: 0` in
prod config, returning to today's "cumulative 60s warn" behavior. No
data migration, no caller change. If we later decide Option A was
right (e.g., a disk-backed overflow queue ships and wants
`ErrQueueFull` routing), we can add `WriteContext(ctx, Frame) error`
as a parallel API; the ADR for that change supersedes this one.

## References

- `internal/journal/journal.go` — package under change
- `cmd/tape/main.go` — the two `jrn.Write` call sites
  (`makeWSFrameHandler` and the Open-Meteo poller callback)
- `internal/kalshi/ws.go` — `Subscriber.runOnce`, the single-goroutine
  producer whose blocking would cascade to TCP-layer loss
- `docs/OPERATIONS.md` §3 (healthy-steady-state signals) and §7
  (`frames dropped (queue full)` troubleshooting) — text needs an
  update in the implementation commit to reference the new alarm
- `docs/ROADMAP.md` — Phase 6 (observability) is where the
  Prometheus hook lands
- ADR-0007 — the calibration corpus this journal is the ground truth
  for; the reason "silent drop" is a data-integrity defect, not log
  hygiene
- ADR-0010 — the Bayesian update consumer that depends on
  hour-resolution intraday observation fidelity; dropped frames
  during a T−3h window directly degrade kernel quality
