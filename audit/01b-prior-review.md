> **Provenance**: This document predates the dispatched Phase 1 audit (mtime 2026-05-13 03:10:54,
> HEAD pinned at 5687c5e). Author and dispatch context unattributed. Verified non-contradictory with
> audit/01-line-audit.md on 2026-05-13. Retained for net-new coverage. Treat findings as input
> requiring corroboration, not as authoritative output of this audit run.

# audit/01b-prior-review.md — Consolidated code, design, ADR audit (origin unknown)

**Project**: `isobar` · **Date**: 2026-05-13 · **HEAD**: `5687c5e` (per `audit/00-map.md`)
**Scope**: deep code review, system design, tech debt prioritization, ADR gap analysis.

This audit sits on top of and is non-duplicative with prior work:

- `audit/00-map.md` — territory map, 14 numbered findings (referenced as **00-map P*N***)
- `audit/chi-blast-radius.md` — CHI/ORD config bug forensic
- `audit/strategy-risk-reconciliation.md` — doc-vs-code reconciliation
- ADR-0011 — journal backpressure (covers 00-map P1 #4)

Everything below is **incremental** to those. I do not re-litigate the CHI bug, the bare `go runRediscovery`, the missing Open-Meteo retry, or the journal silent-drop — they are already entered, triaged, and (for 0011) decided.

---

## 1. New code-level findings

P1 = correctness/durability under realistic failure; P2 = perf/reliability; P3 = hygiene.

### P1 — durability/correctness gaps not in 00-map

#### F1.1 — `journal.flushAndSync` skips fsync on flush error, then keeps writing

File: `internal/journal/journal.go:191-202`

```go
flushAndSync := func() {
    if bw == nil || f == nil { return }
    if err := bw.Flush(); err != nil {
        j.logger.Error("flush failed", "err", err)
        return                  // ⚠️ early return, no fsync, no rotation, no halt
    }
    if err := f.Sync(); err != nil {
        j.logger.Error("fsync failed", "err", err)
    }
}
```

If `bw.Flush` returns an error (which propagates from the underlying `*os.File.Write` — disk full, EIO, network FS hiccup), the loop logs and continues. The next `writeOne` call appends to the same `bw`, `bw.Flush` is retried on the next tick, and the cycle persists. Two problems:

1. `written` counter (line 232-237) was *advanced* by the bytes that bufio swallowed but the OS file hasn't received — `maybeRotateBySize` operates on a wrong count. Theoretically possible to roll past `MaxFileBytes` undetected.
2. `RISK.md` §"Known unknowns" claims *"Journal writer panics on a sustained Write error; recorder process exits; systemd Restart=on-failure should restart with a fresh date directory."* **The code does not panic.** It logs at error level and keeps trying. Doc/code mismatch.

**Fix.** On a persistent (e.g. 3 consecutive) `bw.Flush` error, the writer should rotate to a new file (closing the current fd to surface the error to the OS, attempting `open()` to a fresh path) or escalate to a process-fatal `slog.Error` + `os.Exit(2)` if reopen also fails. Critically the `written` accounting must be reset on flush failure so size-based rotation reflects on-disk state.

#### F1.2 — `gzipFile` doesn't fsync before `os.Remove` of the plain file

File: `internal/journal/journal.go:204-223, 320-339`

```go
rotate := func() {
    ...
    if err := gzipFile(oldPath); err != nil { ... return }
    if err := os.Remove(oldPath); err != nil { ... }       // ← plain file deleted
}
```

`gzipFile` opens output, copies, calls `gz.Close()` (which flushes the gzip framer to the file), and returns. The file is **not fsync'd**. The OS page cache is allowed to defer the actual disk write. If a power loss happens between `gzipFile` returning and the kernel's next dirty-page writeback, the `.gz` is empty or truncated on next mount and the plain file was already unlinked. Net result: one rotation worth of recorded data is gone with no warning.

**Fix.** Inside `gzipFile`, after `gz.Close()` and before returning success, call `out.Sync()`. Cheap (~ms per rotation). The pattern is "fsync the new before unlinking the old", the same shape `cmd/calibrate-fit` uses for `mos-coefficients.json` (atomic rename + sync).

#### F1.3 — Kalshi `doRaw` only retries 429, not transient 5xx

File: `internal/kalshi/client.go:471-499`

```go
if resp.StatusCode == 429 && attempt < maxRetries {
    ...
    continue
}
if resp.StatusCode < 200 || resp.StatusCode >= 300 {
    return nil, fmt.Errorf("kalshi: API error %d: %s", resp.StatusCode, ...)
}
```

A 500/502/503/504 — common during exchange deploys or load-balancer warm-up — is surfaced to the caller with no retry. For the REST poller during `DiscoverOpenTickers`, one transient 502 inside the inner pagination loop kills the entire startup with `kalshi: discover tickers: series KXHIGHNY: kalshi: API error 502: ...`. The recorder then exits and waits for systemd to restart it.

**Fix.** Treat 502/503/504 (and network-layer connection-reset, which currently returns from `httpClient.Do` outside the retry loop) as retryable with the same bit-shift backoff but **without** the adaptive `minDelay` doubling (that's a rate-limit signal, not a server-health signal). For non-idempotent `POST /portfolio/orders`, you want a `ClientOrderID`-based dedup (already in the Phase 1+ design per STRATEGY.md) before retry is safe; for everything in Phase 0 (only GETs), retry is unambiguously correct.

#### F1.4 — Kalshi `doRaw` ignores `Retry-After` on 429

Same site. The adaptive backoff is bit-shift (`2^(attempt+1) * time.Second`); the server's `Retry-After: <seconds>` header is read by `httpClient.Do` but the application ignores it. If Kalshi tells you "wait 60s" on a rate-limit, you wait 2s, get another 429, walk into a longer suspension. Honoring `Retry-After` is the standard.

**Fix.** Parse `resp.Header.Get("Retry-After")` (RFC 7231 says it can be either delta-seconds or HTTP-date); fall back to bit-shift if absent or unparseable. Cap at, say, 60s so a misbehaving server can't park you.

#### F1.5 — Kalshi adaptive `minDelay` never decays back to baseline

File: `internal/kalshi/client.go:472-481, 51-57`

```go
c.rateMu.Lock()
if c.minDelay < maxAdaptiveDelay {
    c.minDelay *= 2
    ...
}
c.rateMu.Unlock()
```

`minDelay` doubles on 429, capped at 2 s. The *only* place it decays back to 1 s is `ResetRateLimit()`, called once in `cmd/tape` after `DiscoverOpenTickers` completes (line 107). After that one reset, any 429 in the subsequent rolling-rediscovery cycle (every 4 h) leaves the client throttled at 2 s for the remaining process lifetime — effectively halving throughput until restart. For a recorder that only does ~1 REST call per 4 h per series prefix this is invisible; for a Phase 1+ exec path that needs sub-second order placement on a fill, it would silently make the bot 2× slower than it should be.

**Fix.** After a successful 2xx response, decay `minDelay` toward baseline (e.g. divide by 2 every N consecutive successes, floor at 1 s). Or alternatively keep `lastSeen429` and let the doubling be ephemeral with a TTL.

#### F1.6 — WS subscriber has no app-level liveness check

File: `internal/kalshi/ws.go:178-184`

```go
for {
    _, data, err := conn.Read(runCtx)
    if err != nil { return err }
    s.opts.OnFrame(data)
}
```

`coder/websocket` handles transport-level pings/pongs transparently, so a half-open TCP doesn't immediately wedge. But there's no application-level "I expect a frame every N seconds" timeout. If Kalshi's WS gateway stops delivering frames but keeps the TCP socket and ping/pong loop alive (a known cloud LB failure mode under partial backend outage), the recorder happily sits in `conn.Read` forever, `retry.Do` never re-enters, and no frames land on disk. The 60 s journal drop-report doesn't fire because nothing is being dropped — nothing is arriving.

**Fix.** A watchdog goroutine alongside the existing reload watcher: track `lastFrameTime atomic.Int64` updated on every `OnFrame` invocation; if `time.Since(lastFrameTime) > MaxIdleBetweenFrames` (e.g. 60 s during market hours; configurable to handle quiet hours), `cancel()` the runCtx to force a reconnect. `Subscriber` already has the right ctx scaffolding — this is ~30 LOC.

This is also the precondition for the Phase 1+ exec-side WS-disconnect interlock named in RISK.md §"Kalshi WS disconnect storm".

#### F1.7 — `runRediscovery` is not in errgroup *and* its `DiscoverOpenTickers` call inherits the same un-cancellable rate limiter

00-map P1 #2 already flags the bare `go runRediscovery`. Adding to it: `rest.DiscoverOpenTickers` (line 151) calls `c.throttle()` which does `time.Sleep(gap)` with **no `ctx`-aware select**. If a 429 storm during rediscovery has driven `minDelay` to 2 s and there are 8 series × ~50 paginated calls per cycle, the sleep dominates wall time. Worse: `throttle` doesn't take a context, so `time.Sleep` is non-cancellable. Process-shutdown signal during rediscovery has to wait up to `8 * 50 * 2s = 13 minutes` for the rolling rediscovery to finish.

**Fix.** Refactor `throttle` to `throttle(ctx context.Context) error` and use `select { case <-ctx.Done(): ...; case <-time.After(gap): }`. Every caller already has ctx in scope.

### P2 — hot-path perf

#### F2.1 — `stationLocation` re-reads the tzdata file per paired trade

File: `internal/edge/scan.go:559-568`

```go
func stationLocation(station string) (*time.Location, error) {
    switch station {
    case "KNYC", "KMIA":
        return time.LoadLocation("America/New_York")
    ...
}
```

`time.LoadLocation` opens `/usr/share/zoneinfo/...` on first call, but it does **not** cache the result across calls — every invocation re-reads. At 38 k weather trades in the Apr 29 – May 6 corpus, that's 38 k file opens. Page cache hides most of the cost, but it's pure waste on the hot scan path.

**Fix.** Either:

- Cache in package-level `var nyTZ, miamiTZ, chicagoTZ, laxTZ *time.Location` initialized with `MustLoadLocation` in `init()`.
- Cleaner: extend `market.Station` with `*time.Location`, lazily filled at registry init, and have scan look up via `market.StationByCode(station).TZ`. That also fixes the `market` package's awkward `UTCOffsetHours` field (which is wrong half the year due to DST and currently has an explicit "re-validate before winter" caveat in its doc).

#### F2.2 — `pollModelSet` allocates per trade on the scan hot path

File: `internal/edge/scan.go:470-479, 311-326`

```go
func pollModelSet(extremes map[string]MemberDaily) []string {
    tags := make([]string, 0, 2)
    for id := range extremes {
        tags = append(tags, memberModelTag(id))
    }
    return mos.CanonicalModelSet(tags)
}
```

Called for every paired trade just to compare against `fitModelSet`. For 38 k trades × ~31 members per poll, that's 1.2 M `memberModelTag` calls (each scanning the member ID for `:`), 38 k slice allocations, 38 k sorts inside `CanonicalModelSet`. The same poll object is reused across many trades — the model set is invariant per `*Poll`.

**Fix.** Compute `Poll.ModelSet []string` once during `idx.latest` lookup (or lazily on first access) and cache on `*Poll`. The scan loop reads the precomputed value.

#### F2.3 — Open-Meteo poller — no `Accept-Encoding: gzip`

File: `internal/openmeteo/client.go:122-126`

```go
req.Header.Set("Accept", "application/json")
```

Ensemble responses are 31-member float arrays per hour × 72 hours × 4 stations ≈ ~800 KB raw JSON. Open-Meteo serves gzip if asked; without it, we eat 800 KB / poll × 4 stations × 48 polls/day = ~150 MB/day of unnecessary bandwidth on the Vultr egress, plus more time in `io.ReadAll` for the journal copy. Go's `http.Transport` adds `Accept-Encoding: gzip` and decompresses transparently only if you don't set the header yourself; setting any `Accept-*` header preserves the transport's behaviour. Worth verifying with a packet capture — likely already gzip'd but worth confirming.

#### F2.4 — `mu sync.Map` in `scan.go:186` for log throttling is a perf wart

`sync.Map` is the wrong tool here. The set of `(observed, fit)` model-set keys is tiny (≤ 4 in practice — GFS, ECMWF, GFS+ECMWF, unknown), bounded, and only written from a single goroutine (scan is single-threaded — `replay.ForEachFrame` is sequential). A plain `map[string]struct{}` would be simpler and faster.

### P3 — hygiene

- `internal/edge/openmeteo.go:303-305`: `type errFmt string` reinventing `errors.New` as a sentinel. `var ErrInsufficientCoverage = errors.New(...)` is the idiom; `errFmt` adds zero value over it. Refactor cost: 3 lines.
- `internal/edge/scan.go:8` imports `"log"`; the rest of the codebase uses `log/slog` per ARCHITECTURE.md §"Error-handling contract" ("log/slog is the only logger"). Lines 195 and 320 use `log.Printf` directly. **Doc/code violation**, caught nowhere by the linter.
- `internal/calibration/bayesian/update.go:104`: `//nolint:gosec // path is operator-supplied calibration file`. The annotation is fine; but consider validating the path is under `cfg.DataDir/calibration/` so a misconfigured `--kernel ../../etc/passwd` (Phase 1 CLI) hits a clear error early.
- `cmd/edge-scan/main.go:397` LOC for a CLI is a smell. The scan-options builder and CSV writer should probably be extracted; this is the largest cmd file.

---

## 2. System design observations (incremental to ARCHITECTURE.md)

### S1 — `internal/edge` is the right consolidation today but won't survive Phase 1

ARCHITECTURE.md correctly defends the size of `internal/edge` ("one data flow that crosses many concerns"). Agreed for now. But once ADR-0010's Bayesian integration lands, `edge` will own: parser → resolution → naive p → MOS → climate → Bayesian → fee → backtest, all in 600+ lines. That's the point where you re-split.

The clean cut is by *direction*: `internal/decode/{openmeteo,kalshi}` for parsers (currently `edge.ParsePoll`, `edge.ParseTicker`, `replay/orderbook.go`); `internal/probability/{naive,mos,climate,bayesian}` (currently `edge`-resident `pollModelSet` + the calibration subpackages); `internal/scan` for the join + EdgeRow assembly + backtest. Each cut produces a package that survives the Signal/Exec extraction in Phase 1 without re-shuffling.

This is not a "do it now" recommendation — the current cut is fine pre-integration. It's a "the next ADR after 0010 lands should propose this split" recommendation.

### S2 — Recorder vs trader separation needs a third role: kernel-eval

The current architecture has two roles: recorder (writes tape) and offline scanner (reads tape, emits CSVs). ADR-0010 introduces a third invariant — *fits a kernel from tape* — which neither role owns cleanly today. `cmd/calibrate-bayesian` is scaffold-only because the kernel-fit pipeline has new constraints:

- It reads the same tape as the scanner but joins against truth bulletins.
- It writes to `data/calibration/bayesian-kernel.json` with a refit cadence that's *not* the same as MOS coefficients.
- The output is consumed by both the scanner and (planned) the live execution path.

The Phase 1+ shape should be three top-level roles, not two: **record** (`cmd/tape`), **calibrate** (the family of `cmd/calibrate-*` + a planned `cmd/calibrate-eval` per RISK.md §P0), **trade** (offline scan today, paper/live exec in Phase 1+). The calibrate role needs its own package layout discipline because the kernel/coefficient artifacts proliferate: MOS coefficients, climate observations, Bayesian kernel, eval reports.

### S3 — The Signal/Exec/Risk seam is invisible in code

STRATEGY.md and ARCHITECTURE.md §"Future shape" sketch the interfaces:

```go
type Signal interface { Tick(MarketState) []Quote }
type RiskGate func(ctx, order, state) error
type Exec interface { Submit(order) (fill, error); Cancel(id) error }
```

None exist in code today. The risk: Phase 1 starts with `internal/exec`, `internal/risk`, `internal/signal` as greenfield, but the current `internal/edge.Scan` *implicitly* embeds 80% of what should be the signal interface (probability emission + edge calculation + size suggestion via `EdgeThresholdCents`). When Phase 1 wires `Signal.Tick`, half its body is already written inside `Scan`; you'll either copy-paste (bad) or refactor under deadline pressure (worse).

Recommend: when ADR-0012 (calibration-eval harness) lands, also extract a `WeatherSignal` struct with a pure `Probability(MarketState) (PFair, EdgeRow, error)` method out of `Scan`. The current `Scan` remains the offline driver that wraps it. This gives Phase 1 a target to wire `Signal.Tick` around without rewriting.

### S4 — No on-disk schema version on journal envelopes

`internal/journal/journal.go:48-55` defines the envelope as fixed `{t, m, src, k, p}`. Replay readers in `internal/replay/reader.go` decode against this shape. There's no `v` field, no schema version. Future evolution (e.g. adding a `seq` field for per-source frame ordering, or splitting `p` into `p_raw` and `p_decoded`) breaks every existing tape silently — the JSON decode will succeed with zero values for missing fields, no warning.

MOS (`isobar.mos/v1`) and Bayesian (`isobar.bayesian/v1`) get versioned. Journal — the foundation everything else replays from — does not. This is structural; should be an ADR (see §3).

### S5 — Time-source discipline is inconsistent across the codebase

- `cmd/tape` uses `clock.Now()` (paired wall+mono) per ARCHITECTURE.md §"clock".
- `internal/journal/journal.go:309` uses `time.Now().UTC()` directly for the date directory path.
- `internal/edge/scan.go:225` uses `time.Unix(0, msg.TsMs*int64(time.Millisecond)).UTC()` — fine for offline replay (wall clock matters, not monotonic).
- `internal/openmeteo/client.go` uses `time.NewTicker` (monotonic-anchored, correct).
- `internal/kalshi/client.go` uses `time.Now()` for the throttle's `lastCall` accounting — that's `time.Time{wall, mono}`, and `now.Sub(lastCall)` uses the monotonic component, so it's correct, but the choice isn't documented.

These are individually defensible but together they show the codebase has never written down its time-source rule. The `clock.Stamp` doc comment hints at one (wall for cross-process, monotonic for intervals) but it's not surfaced as a doctrine for new code. A `docs/CLOCK.md` or an ADR codifying "use `clock.Now()` in producers; raw `time.Now()` in offline tooling; `time.NewTicker` for cadence" would prevent drift.

---

## 3. ADR gap analysis — what's decided in code but not in `docs/decisions/`

These are decisions present in the codebase or RISK.md/STRATEGY.md without a corresponding ADR. Each is load-bearing enough that a future contributor (or future-you) should not be able to change it without a written debate.

| Gap | Where it's actually decided | Why it needs an ADR |
|---|---|---|
| **Journal on-disk schema versioning** (§S4) | `internal/journal/journal.go:48-55` envelope | Foundation everything replays from; needs a `"v"` field and a documented bump rule à la `isobar.mos/v1`. Worst-case breakage: every recorded tape becomes silently miscoded after a future envelope tweak. |
| **Recorder-never-decodes invariant** | ARCHITECTURE.md §"Hot-path discipline" + journal contract | Stated as a guiding constraint but not pinned as a reviewable decision. A future contributor could "just add a quick decode for filtering" and you lose the schema-evolution insurance. |
| **`internal/edge` package consolidation** | ARCHITECTURE.md §"internal/edge" prose | Defends size against the natural "this is too big, split it" instinct. Should be an ADR ("don't split edge while it's one data flow; revisit when Bayesian integrates"). Pairs with §S1 above. |
| **Calibration-eval harness** | RISK.md §P0; STRATEGY.md §"Decision gate"; ADR-0010 §"Gate criteria for ADR-0011"; this is the planned **ADR-0012** | Already P0 in RISK.md. The "this is the gate before any real-money order" assertion needs its own decision record, not just a forward reference in 3 other docs. |
| **`bytedance/sonic` choice for replay JSON** | `internal/replay/orderbook.go:10`, `reader.go`, `kalshi.go` (3 imports) | Sonic is a reflection-heavy CGO-free JSON parser with its own caveats (panic-on-bug history, ASM-injected hot path, larger attack surface than stdlib). It's listed in audit §2 as **CRITICAL** because every replay goes through it. The choice of sonic over stdlib is undocumented; ADR justifying perf-vs-audit-burden would either confirm or kill it before Phase 1 adds order-decoding paths through it. |
| **JSONL-NDJSON corpus format** | `internal/journal/journal.go` package doc | Clear win for the recorder (audit-friendly, schema-evolvable, gzippable, no DB dep) but never argued out in writing. When Phase 1 adds the exec-state store, the question "why not just put trades in SQLite directly?" will recur. Write the answer once. |
| **Open-Meteo 30-min poll cadence** | `config/config.yaml`; `internal/openmeteo/client.go` (`PollInterval < 1m` rejected) | Why 30 min and not 5, 15, or 60? GFS ensemble updates every 6 h, so anything < 30 min is bandwidth waste; > 60 min loses near-settlement responsiveness; 30 min is the chosen point. Not explicit anywhere. |
| **Time-source discipline** (§S5) | Scattered conventions | Codify or factor out into a clock-discipline ADR before Phase 1's exec path adds more time-sensitive code (clock drift gate, deterministic order IDs). |
| **`internal/` only — no `pkg/`** | ARCHITECTURE.md §"Guiding constraints" | A constraint, not a decision record. Future "second binary needs to import" pressure (e.g. a hypothetical `cmd/replay-server`) will challenge it. Pin it. |
| **`KalshiFeeBPS = 0.07` constant + parabolic model** | `internal/edge/fee.go` | The Kalshi fee schedule (taker 7% of P·(1−P)) is hardcoded with the Feb 2026 maker schedule "not yet modeled" caveat in ARCHITECTURE.md. Worth a "fee model assumptions" ADR so the Feb 2026 update doesn't go in as a stealth commit. |

There are 11 existing ADRs. Adding 5-7 from the list above brings the doc set in line with what the code actually commits to.

The gaps are clustered around **structural invariants** (schema, recorder/trader separation, time-source) and **third-party choices** (sonic). The ADR sequence has been strong on *strategy* decisions (calibration math, signal scope) and weak on *engineering-doctrine* decisions. That's the right priority — strategy decisions are reversal-expensive — but the doctrine gaps will bite during Phase 1's interface extraction.

---

## 4. Prioritized tech-debt backlog

Ordered by **risk × leverage**: high-risk-if-ignored × easy-to-fix first. Combines new findings here with open items from 00-map and the ADR gaps.

### Must-fix before Phase 1 exec ships

| # | Item | Source | Effort |
|---|---|---|---|
| 1 | CHI lat/lon → KMDW config swap | 00-map P1 #1; `chi-blast-radius.md` | 1 line + retroactive re-poll script |
| 2 | Calibration-eval harness ships (ADR-0012 + `cmd/calibrate-eval`) | RISK.md P0; STRATEGY.md §"Decision gate" | 3-5 days |
| 3 | Journal flush-failure handling (F1.1) — rotate-or-exit on persistent error | F1.1 | 1 day; needs tests with injected file errors |
| 4 | Journal gzip fsync before plain-file remove (F1.2) | F1.2 | 1 hour |
| 5 | Kalshi `Retry-After` honoring + 5xx retry (F1.3, F1.4) | F1.3, F1.4 | 1 day; testable with `httptest` |
| 6 | WS app-level liveness watchdog (F1.6) | F1.6 | ~30 LOC + table-driven test |
| 7 | Ctx-aware `throttle` (F1.7) | F1.7 | refactor; 1 hour |
| 8 | `runRediscovery` joins errgroup | 00-map P1 #2 | small; gated on F1.7 |
| 9 | Open-Meteo retry with bounded backoff | 00-map P1 #3; RISK.md | half-day |
| 10 | ADR-0011 implementation lands | ADR-0011 §Test plan | ~2 days per its test plan |
| 11 | `ISOBAR_LIVE_ALLOW` — implement or strip from docs | 00-map P3 #8 | decision-bound, then ~30 min either way |
| 12 | `.env` + PEM perms boot-time check | 00-map P1 #6; RISK.md | half-day |

### Should-fix soon (P1 exec depends on at least three of these)

| # | Item | Source | Effort |
|---|---|---|---|
| 13 | Cached station timezone (F2.1) — extend `market.Station` | F2.1 | 1 hour |
| 14 | Adaptive `minDelay` decay (F1.5) | F1.5 | 1 hour |
| 15 | `Poll.ModelSet` precompute (F2.2) | F2.2 | 1 hour |
| 16 | Journal envelope schema version field (§S4) | §S4 + new ADR | 1 day + back-compat test |
| 17 | Extract `WeatherSignal` from `Scan` (§S3) | §S3 | 2-3 days |
| 18 | `cmd/edge-scan/main.go` size refactor (P3) | F P3 #4 | 1 day |
| 19 | Three structural-doctrine ADRs (envelope, recorder-no-decode, edge consolidation) | §S4, §S5, §S1 | half-day each |
| 20 | Deploy artifacts to repo (`deploy/isobar-tape.service` + install.sh) | 00-map §10; RISK.md | 1 day |

### Nice-to-have (hygiene, not blocking anything)

| # | Item | Source | Effort |
|---|---|---|---|
| 21 | `log.Printf` → `slog` in `internal/edge/scan.go` | F P3 #2 | 15 min |
| 22 | `errFmt` → `errors.New` (`internal/edge/openmeteo.go`) | F P3 #1 | 5 min |
| 23 | `sync.Map` → `map[string]struct{}` in `scan.go:186` | F2.4 | 5 min |
| 24 | `Accept-Encoding: gzip` verification on Open-Meteo poller | F2.3 | half-day to verify + bench |
| 25 | ADRs for sonic, JSONL, poll cadence, fee model, internal/only, time discipline | §3 table | ~half-day each |
| 26 | OPERATIONS.md, STRATEGY.md, RISK.md doc refresh (already mostly done in 2026-05-13 batch) | 00-map P3 #10–13 | done in part |
| 27 | `bin/` cleanup; `data/kalshi/2026-05-12/` rotation | 00-map P2 #5, P2 #7 | small |

---

## 5. What I deliberately did **not** re-cover

- The CHI lat/lon bug — fully forensic'd in `chi-blast-radius.md`.
- The bare `go runRediscovery` — 00-map P1 #2; design fix is in §1 F1.7 (`throttle` cancellation) which makes the errgroup-rejoin trivial.
- Journal silent-drop — decided in ADR-0011, implementation pending.
- Open-Meteo missing retry — 00-map P1 #3; backlog item 9.
- `.env` perms / PEM at-rest — 00-map P1 #6; backlog item 12.
- STRATEGY.md / RISK.md doc-vs-code reconciliation — done in `audit/strategy-risk-reconciliation.md` and the 2026-05-13 doc rewrites.
- The Phase 0.5 v2 calibration verdict and the canary firing — that's the project's strategic state, not a code-audit finding.

---

## 6. Topline

The codebase is **engineering-mature for its phase**: 13.4 k LOC of Go with 11 ADRs, three prior audits, schema-versioned calibration artifacts, race-tested goroutine lifecycle, and a written-down phased gate before any real money moves. Most of what I found is **second-order** — refinements on top of an already-thoughtful structure. Nothing here changes the strategic verdict (Phase 0.5 gate not met, ADR-0009 → 0010 escalation in flight).

The two specific things that should not wait for Phase 1:

1. **Journal durability hygiene** (F1.1, F1.2) — your corpus is the project's most valuable artifact, and the writer's failure-mode story is currently "log and continue, hope systemd notices". For a research project with a documented failed gate this is fine; if you ever do flip to Phase 1, the journal's behavior under disk-full / EIO needs to be tested-and-deterministic, not "we'll see".
2. **WS liveness watchdog** (F1.6) — same root cause: silent failure in the recording layer poisons every downstream artifact. The fix is small and the test story is clean.

Everything else is on the backlog above in priority order.
