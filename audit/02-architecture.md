# audit/02-architecture.md — Phase 2 architecture reconsider

**Project**: `isobar` (Kalshi weather-bracket trading bot)
**HEAD at draft**: `2b9248d` (post-Bucket-A, post-ADR-0013)
**Status**: **PROPOSAL ONLY.** Nothing in this document is implemented.
**Charter**: restructure and consolidate only — no new capability.
**Cadence**: per-decision sign-off gates implementation. No `git mv`, no public-API change, no new test until the relevant decision is approved.

This document is the output of Phase 2's **first stage**: a proposal-only consolidation of all structural decisions the audit identified. Each decision is presented in the operator-specified format (current state → proposed change → blast radius → alternatives considered → rollback plan → reserved ADR slot). Sign-off happens **per decision**, not per document.

Drafted by four parallel Opus 4.7 research agents (one per major theme), then synthesized. Each agent operated in an isolated worktree and was constrained to read-only on `*.go` files; only `audit/02-section-N-*.md` draft files were written. Synthesis here resolves ADR-slot collisions (all four agents independently proposed 0014) into a sequential assignment.

---

## Phase 2 charter constraints (reaffirmed)

**Allowed**:
- Move code between packages
- Merge duplicate tables / parsers / clients
- Introduce interfaces that already have ≥2 concrete implementations in current code
- Propose error-handling discipline as written policy
- Decide schema-migration policy collectively
- Decide test-coverage strategy for the 5 zero-test binaries
- Delete dead code
- Write ADRs 0014+ for every structural decision

**Not allowed without explicit unblock**:
- New external integrations
- New persistence layers
- New configuration surface beyond what consolidation forces
- New CLI commands or binaries
- Speculative interfaces for capabilities with no current consumer
- Anything touching `cmd/bot` or the money path

---

## Reconciled ADR slot table

Seven structural decisions; seven reserved ADR slots. Ordered by foundational-dependency (decisions cited by others land first). Standalone strategy / engineering-doctrine ADRs from §4.4's routing live in slots 0021+.

| Slot | Decision | Owner section | Why this order |
|---|---|---|---|
| **ADR-0014** | Error-handling discipline (taxonomy / counters / escalation / annotations / migration tiers) | §2.1 | Foundational. Every other decision cites the taxonomy. |
| **ADR-0015** | Schema-migration policy across journal / MOS / Bayesian / boot-frame | §3.1 | Data-shape doctrine; §1.1's boot-frame bump composes against it. |
| **ADR-0016** | Consolidate station registry into `internal/market` | §1.1 | First code-moving decision; cites 0014 + 0015. |
| **ADR-0017** | Defer `internal/edge` directional split until ADR-0010 Bayesian wiring; pin trigger | §1.2 | No code move in Phase 2; pins the future shape so the integrator doesn't relitigate under deadline. |
| **ADR-0018** | Three-role architecture (record / calibrate / trade) | §4.1 | Documents the seam; no code move. Trade role deferred. |
| **ADR-0019** | CLI binary test discipline (smoke + integration tiers) | §4.2 | Verdict matrix for the 5 zero-test binaries. |
| **ADR-0020** | `internal/`-only Go package layout | §4.3 | Standalone engineering-doctrine; commits the *de facto* rule. |
| 0021+ | Strategy/engineering-doctrine ADRs from §4.4 routing | (standalone) | Sonic-vs-stdlib, JSONL format, poll cadence, fee model, recorder-no-decode |

---

## Per-decision sign-off block

Each decision below ends with a sign-off marker. The operator's per-decision response uses one of:

- **APPROVED AS WRITTEN** — implementation may proceed.
- **APPROVED WITH MODIFICATIONS: …** — implementation may proceed against the modified spec.
- **HOLD: …** — decision returns to draft; reason recorded.

Implementation happens block-by-block against signed-off decisions. Each block lands as its own PR with its ADR.

---

# §1 — Package consolidation

Two decisions held over from Phase 1 Bucket B:

- **§1.1** — Three drifting station tables in `internal/edge/`. Bucket A1+A2 added canonical lat/lon to `internal/market` but didn't move the duplicates. Close the gap.
- **§1.2** — Prior-review §S1: `internal/edge` "is the right consolidation today but won't survive ADR-0010's T-3h Bayesian integration." Evaluate the proposed cut against current state, name a trigger, fix migration mechanics.

§1.1 is a low-blast-radius PR. §1.2 is "write the ADR, don't move code yet."

---

## Decision §1.1: Unify the three drifting station-mapping tables into `internal/market`

### Current state

Four station tables exist; three duplicate the registry:

1. **[internal/market/stations.go:59-84](../internal/market/stations.go:59)** — canonical `Station{Code, Location, City, Lat, Lon, UTCOffsetHours, HighSeries, LowSeries, CLIPil}`. Looked up via `StationByCode/Location/Series`. Consumed today by `internal/config/config.go:163` (boot-time coord validation), `cmd/tape/main.go:163` (boot frame), `cmd/calibrate-fit/main.go:80`, `cmd/edge-scan/main.go:105`.

2. **[internal/edge/poll_index.go:99-111](../internal/edge/poll_index.go:99)** — `stationFromKind` maps Open-Meteo `Kind` envelopes to station codes (`"rest:NY" → "KNYC"`, `…CHI" → "KMDW"`). Sole caller `loadPolls` at [poll_index.go:56](../internal/edge/poll_index.go:56). The CHI→KMDW row is what `chi-blast-radius.md` poisoned ~21% of the corpus over.

3. **[internal/edge/ticker.go:67-70](../internal/edge/ticker.go:67)** + **[:176-188](../internal/edge/ticker.go:176)** — `validSeries` (set of eight prefixes the parser accepts) and `Spec.Station()` (series → station code). The series→code map shadows `Station.HighSeries`/`LowSeries`.

4. **[internal/edge/scan.go:584-594](../internal/edge/scan.go:584)** — `stationLocation(station) (*time.Location, error)`, the only `time.LoadLocation` site on the scan hot path. Called from [scan.go:237](../internal/edge/scan.go:237) per paired trade.

**Motivating findings**:
- [audit/01-line-audit.md:181-183 + :961](01-line-audit.md), held into Bucket B at line 1009: "Three station-mapping tables drift independently … Consolidate via `internal/market`."
- [audit/01-line-audit.md P1 scan.go:559-569](01-line-audit.md): `stationLocation` re-reads zoneinfo per call — 38k file opens on a typical scan, nondeterministic-fail risk on stripped container images.
- Prior-review §F2.1: same issue; recommends extending `market.Station` with `*time.Location`.
- A1+A2 already made `internal/market` the canonical lat/lon source and added `validateStationCoords` enforcer at [config.go:162-192](../internal/config/config.go:162). The infrastructure for "one registry, boot-enforced" is in place.
- `cmd/edge-scan` is the *only* external importer of `internal/edge` ([cmd/edge-scan/main.go:27](../cmd/edge-scan/main.go:27)). No external API breaks regardless of rearrangement.

### Proposed change

`internal/market.Station` becomes the sole authority for every station-keyed lookup — code, location, series prefixes, lat/lon, timezone, Open-Meteo poll kind.

1. **Extend `Station`** with two fields:
   - `TZ *time.Location` — populated at registry init via `time.LoadLocation` (init panics on failure; missing zoneinfo on the recorder host is unrecoverable). Replaces `UTCOffsetHours`, whose DST caveat at [stations.go:62-83](../internal/market/stations.go:62) makes it unsafe outside Mar-Nov.
   - `PollKind string` — Open-Meteo envelope tag (`"rest:NY"` etc.). Inverse of `stationFromKind`; same layer as `CLIPil`.
2. **Add reverse-lookup helpers** in `internal/market/stations.go` (linear scan, matching the existing `StationBy*` pattern):
   - `StationByPollKind(kind string) (Station, bool)` — replaces `edge.stationFromKind`.
   - `IsKnownSeries(prefix string) bool` — replaces the `validSeries` membership check. The set is derived from `{HighSeries, LowSeries}` over `allStations`, not hardcoded.
3. **Delete the four edge-resident tables**:
   - `stationFromKind` ([poll_index.go:99-111](../internal/edge/poll_index.go:99)) → call site uses `market.StationByPollKind(env.Kind)`.
   - `Spec.Station()` body ([ticker.go:176-188](../internal/edge/ticker.go:176)) → one-liner `if s, ok := market.StationBySeries(spec.Series); ok { return s.Code }; return ""`. Method stays; only its body changes.
   - `validSeries` ([ticker.go:67-70](../internal/edge/ticker.go:67)) → membership check at [ticker.go:103](../internal/edge/ticker.go:103) becomes `market.IsKnownSeries(series)`. Map disappears.
   - `stationLocation` ([scan.go:584-594](../internal/edge/scan.go:584)) → call site reads `s, ok := market.StationByCode(station); …; tz := s.TZ`. Function disappears.
4. **Boot-time contract**: registry init panics if `time.LoadLocation` fails. The recorder fails fast on a broken container instead of silently skipping every trade.

### Blast radius

- **Files changed (production)**:
  - `internal/market/stations.go` — +50 LOC (two new fields, two helpers, init for TZ).
  - `internal/edge/poll_index.go` — −13 LOC.
  - `internal/edge/ticker.go` — −8 LOC.
  - `internal/edge/scan.go` — −11 LOC.
  - Net **+50/−32 production LOC**, 4 files touched in `internal/`. Zero `cmd/` changes (`Spec.Station()` keeps its signature).
- **Tests**: `internal/market/stations_test.go` extended for new fields and panic-on-bad-zoneinfo (~+30 LOC). Optional cleanup of direct `time.LoadLocation` calls in 5 test fixtures (`internal/edge/openmeteo_test.go` :91, :115, :161, :190; `internal/edge/scan_test.go:643`) — not required for correctness. ~4-6 test files, mostly mechanical.
- **Public APIs broken**:
  - `internal/edge` exports none of the deleted symbols (all package-private). `Spec.Station()` keeps its signature.
  - **One operator-visible change**: `market.Station.UTCOffsetHours` is removed. The single consumer is `cmd/tape/main.go:163`, which embeds the whole `Station` struct in the boot-frame journal envelope. The envelope's `Station` shape changes (offset field → IANA TZ name). This is a **boot-frame schema change** for one embedded field, and under ADR-0015 (schema-migration policy, §3.1) should travel with a `v` bump on the boot frame. The recorder rewrites the boot frame on every start, so no on-disk artifact migration is needed.
- **Migration cost**: ~110 net LOC including tests. All mechanical.
- **Reviewer cost**: **3-4 person-hours**. Reviewer time goes to (a) confirming no caller of the four deleted symbols got missed and (b) reviewing the boot-frame schema bump alongside ADR-0015.

### Alternatives considered

1. **Status quo: leave the three tables drifting.** Rejected. Each new station (per the Bucket-B-implicit hurricane/secondary-cities expansion path at [stations.go:58](../internal/market/stations.go:58)) costs four parallel edits, and the audit history records one drift event already (CHI/KORD vs KMDW, [chi-blast-radius.md](chi-blast-radius.md)). The cost of the next drift exceeds the cost of consolidation.

2. **Minimum-fix per prior-review §F2.1: cache `*time.Location` in package-level `var` inside `internal/edge` only.** Rejected. Closes the perf wart but not the drift problem (the other three tables remain). Prior-review §F2.1 itself recommends against this in favor of the registry extension this proposal adopts.

3. **Move `Spec`/`ParseTicker`/`validSeries` into `internal/market` wholesale.** Rejected. The parser carries Kalshi ticker-format knowledge that doesn't belong in a station registry — it would inflate `market` and re-create a different drift surface. Parsers consult the registry; they do not contain it. (This decision is consistent with §1.2's direction-cut placement of parsers.)

### Rollback plan

Single revert of one PR (four production files + tests + the boot-frame schema bump). No on-disk migration needed: tape envelope `Kind` strings (`"rest:NY"` etc.) are unchanged; only the consumer-side lookup moves. The boot frame is rewritten on every recorder start.

**Recovery cost**: <1 hour. If the registry-init panic fires in production (zoneinfo missing in deployed container), revert restores the per-trade `time.LoadLocation` path, which already counted-and-skipped on the missing-zoneinfo case.

The only multi-commit unwind is if the boot-frame schema bump landed alongside ADR-0015 and 0015's other consumers depend on the bump. Acceptable; revert order is inverse of land order.

### Reserved ADR slot

**ADR-0016 — Consolidate station registry into `internal/market`**

Decision body: `internal/market.Station` is the single source of truth for every station-keyed lookup. No package outside `internal/market` may carry a parallel mapping. Adding a station means editing one file, running `make test`, and re-running `make build`; boot-time validation at `internal/config/config.go:162` catches operator config drift.

**Closes Bucket B P1s**:
- "3 station-mapping tables drifting (edge/scan, edge/poll_index, edge/ticker)" — [audit/01-line-audit.md:1009](01-line-audit.md).
- Prior-review §F2.1 — closed via the `TZ` field rather than per-package caching.

**Sign-off §1.1**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

## Decision §1.2: Defer the `internal/edge` directional split until ADR-0010 Bayesian wiring; pin the trigger now

### Current state

Prior-review §S1 ([01b-prior-review.md:207-213](01b-prior-review.md)):

> Once ADR-0010's Bayesian integration lands, `edge` will own: parser → resolution → naive p → MOS → climate → Bayesian → fee → backtest, all in 600+ lines. That's the point where you re-split.

Today, post-Bucket-A:

- **`internal/edge` is 13 files, 1773 production LOC + 1490 test LOC**. Top files: `scan.go` 594, `openmeteo.go` 316, `poll_index.go` 221, `ticker.go` 194, `resolution.go` 175, `truth.go` 163, `fee.go` 66.
- **No Bayesian code in `edge` today.** `internal/calibration/bayesian/` is a complete subpackage (252 prod LOC + 464 test LOC). `cmd/calibrate-bayesian` is scaffold-only ([01-line-audit.md:889-898](01-line-audit.md)). ADR-0010 is the integration plan; integration has not landed.
- **`edge` already imports two calibration subpackages** — `climate` and `mos` at [scan.go:16-17](../internal/edge/scan.go:16). Bayesian will be a third + ~40 LOC of dispatch in the scan loop. After integration, `scan.go` projects to ~640 LOC; the package projects to ~1820 production LOC.
- **External boundary**: `cmd/edge-scan/main.go:27` is the *only* importer. Used symbols: `Scan, ScanOptions, EdgeRow, Result, LoadTruth, TruthIndex`.
- **Live Bucket-B item**: prior-review §S3 — "`Scan` *implicitly* embeds 80% of what should be the signal interface … When Phase 1 wires `Signal.Tick`, half its body is already written inside `Scan`." Held at [01-line-audit.md:1015](01-line-audit.md).

#### Is the §S1 cut still right?

Yes. The boundaries §S1 proposes are already file boundaries in `edge` today: `ticker.go`, `openmeteo.go`, `resolution.go` are pure parsers/resolvers; `poll_index.go` is an index; `scan.go` is the join + math + backtest. The split is one `git mv` per file. The package boundaries are correct at the file level — only the package name needs to change.

But the **timing** has shifted slightly. Splitting today gains no signal-vs-execution clarity (consumer is offline-only). Splitting at ADR-0010 wiring time gains: (a) the per-row dispatch is then a 4-way switch (naive/MOS/climate/Bayesian) that benefits from its own subpackage with its own test surface, and (b) `internal/probability/*` lands as a stable surface for the Phase 1+ `Signal.Tick` extraction (prior-review §S3) without that extraction touching `cmd/bot`.

### Proposed change

**Do not split in Phase 2.** Write an ADR now that pins the trigger condition and the per-file migration plan, so the ADR-0010 integrator does not relitigate package layout under deadline pressure.

#### Trigger (binary — split when any one is true)

- **`scan.go` exceeds 800 production LOC** (current 594; projects to ~640 with ADR-0010 wiring; threshold is one error-policy expansion away after that).
- **A second importer of `internal/edge` lands** (currently only `cmd/edge-scan`). The candidate second importer is Phase 1+ `internal/signal`; that crossing is exactly where `decode/` and `probability/` need standalone import paths.
- **A fourth probability variant enters the scan loop** (today: naive, MOS, climate. Bayesian is the planned third. A fourth — e.g. conformal interval per prior-review §3 calibration-eval — pushes the per-trade dispatch past readable threshold in one function).

None fire today.

#### Migration mechanics (when triggered)

| Current file | LOC | Target package |
|---|---:|---|
| `internal/edge/openmeteo.go` | 316 | `internal/decode/openmeteo` |
| `internal/edge/ticker.go` | 194 | `internal/decode/kalshi` |
| `internal/edge/resolution.go` | 175 | `internal/decode/kalshi` (consumes `Spec` directly) |
| `internal/edge/poll_index.go` | 221 | `internal/scan` (it is the join's lookup half) |
| `internal/edge/truth.go` | 163 | `internal/scan` (only backtest reads truth) |
| `internal/edge/fee.go` | 66 | `internal/probability` (open: could also be own `internal/fee`) |
| `internal/edge/scan.go` | 594 | decomposed (see below) |

`scan.go` decomposes into:
- Orchestration (`Scan`, `Result`, `EdgeRow`, the per-row write logic) → `internal/scan`.
- Naive `p_fair` calc ([scan.go:269-282](../internal/edge/scan.go:269)) → `internal/probability/naive` as a pure function `NaivePFair(extremes, resolution, isHigh) (p, nIn, nMembers, sumT)`.
- MOS dispatch ([scan.go:299-344](../internal/edge/scan.go:299)) → `internal/probability/mos`.
- Climate shrinkage ([scan.go:354-365](../internal/edge/scan.go:354)) → `internal/probability/climate`.
- Bayesian dispatch (lands with ADR-0010) → `internal/probability/bayesian`.
- Backtest math ([scan.go:395-443](../internal/edge/scan.go:395)) stays in `internal/scan`; it is the orchestrator's concern.

Imports flow downward: `internal/scan` → `internal/decode/*` + `internal/probability/*`. `internal/probability/*` → `internal/decode/*` + existing `internal/calibration/*`. No reverse imports.

`cmd/edge-scan` rewrites ~5 import lines and renames `edge.Scan/ScanOptions/EdgeRow/Result` → `scan.Run/Options/Row/Result`. Mechanical.

#### What lives on the seams

Three boundary decisions the ADR pins:

- **`MemberDaily`** (currently [openmeteo.go:295-302](../internal/edge/openmeteo.go:295)) lives in `internal/decode/openmeteo`; consumed by `internal/probability/*`.
- **`Spec`, `Resolution`** (currently [ticker.go](../internal/edge/ticker.go), [resolution.go](../internal/edge/resolution.go)) live in `internal/decode/kalshi`; same import direction.
- **`EdgeRow`** lives in `internal/scan`. Probability variants return scalars, not rows. This is the seam that — post-split — lets `WeatherSignal` (prior-review §S3) extract cleanly: a future `Signal.Tick` calls `probability/*` directly and emits a different output type, never depending on `scan.Row`.
- **`fee.go`**: open question between `internal/probability` and a standalone `internal/fee`. Defer to integrator; both work.

### Blast radius

**This decision proposes no code change in Phase 2.** Cost is the ADR text plus a fixed migration plan that makes the future PR mechanical.

- **Files changed in Phase 2**: zero `.go` files. One new ADR file.
- **Files changed at trigger time (out of Phase 2 scope, captured here for completeness)**:
  - `internal/edge/` deleted (11 production files moved).
  - 7 new packages: `internal/decode/{openmeteo,kalshi}` (3 files); `internal/probability/{naive,mos,climate,bayesian}` (4 files); `internal/scan` (3 files).
  - `cmd/edge-scan/main.go` — 5-10 line edits (import paths + symbol renames).
  - Tests follow production files (1490 test LOC redistribute across 7 packages).
  - **Estimated 25-30 files in the future split PR.** Mechanical.
- **Public APIs broken at split time**: every used `edge.*` symbol gets a new import path. Only `cmd/edge-scan` imports `edge`; cost is bounded by definition.
- **Reviewer cost at trigger time**: **6-10 person-hours**, mostly re-confirming test ownership and import flow. Cost of *not* writing the ADR now: the integrator either does the split inline with ADR-0010 wiring (high blast radius, harder review) or defers it, leaving the 1820-LOC `edge` package that §S1 already flagged.

### Alternatives considered

1. **Split now, in Phase 2, pre-Bayesian.** Rejected. The seams are correct at the file level today, but no consumer is paying clarity cost yet. Splitting now means doing the rename twice — once structurally, once when Bayesian + the live signal extraction land and the API surface emerges. The Phase 2 charter forbids speculative interfaces; same hazard.

2. **Never split.** Rejected. ARCHITECTURE.md's defense ("one data flow") relies on no-multi-dispatch. After Bayesian, `Scan`'s per-row body is a 4-way probability dispatch — the rationale fails. Trigger conditions in this ADR encode that threshold.

3. **Split by layer instead of direction** — e.g. all parsers in `internal/parse`, all math in `internal/math`. Rejected. Direction-cut (decode/probability/scan) keeps related types together (`Spec` with `Resolution`; `Poll` with `MemberDaily`). Layer-cut puts `Spec` next to `Poll`, despite zero shared logic. Decision §1.1 above already shows the project preferring direction-cut.

### Rollback plan

**Today**: zero. The proposal does not modify code. If rejected, the only artifact is an ADR text file with `Status: Rejected`.

**At trigger time** (future PR): single revert of the split PR. The split is one bulk `git mv` per package; revert is symmetric. `cmd/edge-scan` and `internal/edge` tests live in the same PR by construction and revert together.

**Recovery cost** at trigger time: <1 day, dominated by re-running tests and verifying `cmd/edge-scan` rebuilds. The §1.1 boot-frame schema bump is unrelated and survives.

### Reserved ADR slot

**ADR-0017 — Defer `internal/edge` directional split until ADR-0010 Bayesian wiring**

Decision body: `internal/edge` stays as one package through Phase 2. The directional split into `internal/decode/{openmeteo,kalshi}` + `internal/probability/{naive,mos,climate,bayesian}` + `internal/scan` happens **with** the ADR-0010 wiring PR, triggered by any of the three conditions above. The ADR fixes the per-file migration plan so the integrator does not relitigate package layout.

**Closes Bucket B P1s**:
- "`internal/edge` split (decode / probability / scan) before Bayesian integration" — [audit/01-line-audit.md:1014](01-line-audit.md).
- "Extract `WeatherSignal` from `Scan` for future Signal interface seam" — [audit/01-line-audit.md:1016](01-line-audit.md) — ADR documents why this extraction is gated on the same trigger.
- Prior-review §S1.

**Sign-off §1.2**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

# §2 — Error-handling discipline

## Decision §2.1: Error-handling discipline policy

### Current state

Phase 1 line audit and the prior-review both surfaced the same pattern across four packages: **silent skip-and-count** is the dominant error policy. A malformed input is counted (in a string-keyed map or, more recently, an `atomic.Uint64`) and execution proceeds. There is no rate-aware operator signal, no typed error at the boundary, and no written discipline governing escalation. Bucket A landed point fixes (`replay.ErrBookUninitialized`, `kalshi.ErrAuth`, `journal.ErrAlarmHookRequired`) but no cross-package policy.

Phase 1 headline: *"Silent skip-and-count is the dominant error policy across edge, replay, journal, cliparse. Adds up to a lot of invisible degradation."* ([`01-line-audit.md:963`](01-line-audit.md)). Bucket B routing entry ([`01-line-audit.md:1012`](01-line-audit.md)) reserves Phase 2 for *"typed-error / escalation / counter taxonomy as a policy ADR."* This proposal is the response.

**Per-package survey (file:line, current signal):**

`internal/edge/` — 11 silent-skip sites in two files. Notable:
- [scan.go:203-204, :213-214, :217-220, :227-230, :232-235, :238-241, :308-314, :346-350, :395-400](../internal/edge/scan.go:203) — `res.SkippedReason` string-keyed map++ on each of 9 skip reasons.
- [scan.go:244-247](../internal/edge/scan.go:244) — `ErrInsufficientCoverage` typed-error caught, map++.
- [scan.go:550-575](../internal/edge/scan.go:550) — `collectSiblings` dropped entirely; double-counted by main loop.
- [poll_index.go:55-72](../internal/edge/poll_index.go:55) — `ParsePoll` failure in `loadPolls`: **no counter, no log** (P3).
- [truth.go:139-162](../internal/edge/truth.go:139) — malformed CSV row: `continue` — **no counter, no log** (P1).

`internal/replay/` — three sites; one already typed (Bucket A3), two open:
- [reader.go:148-151](../internal/replay/reader.go:148) — malformed JSONL line: `continue` — **no counter, no log** (P1 still open).
- [orderbook.go:88-116](../internal/replay/orderbook.go:88) — malformed levels in snapshot: `continue` per level — **no counter, no log** (P1 still open).
- [orderbook.go:299-352](../internal/replay/orderbook.go:299) — snapshot apply / delta-before-snapshot: typed (`ErrSnapshotApplyFailed`, `ErrBookUninitialized`) + counters + `slog` with `logOnceOrSuppress`. **A3 baseline — the exemplar this proposal generalizes.**

`internal/journal/` — four sites; ADR-0011 / Bucket A1 instrumented the queue-full path; flush/sync errors still log-and-drop:
- [journal.go:258-275](../internal/journal/journal.go:258) — queue-full drop: `dropped atomic.Uint64` + sampled `slog.Debug` + edge-triggered `OnDropAlarm` hook (ADR-0011).
- [journal.go:383-410](../internal/journal/journal.go:383) — `bw.Flush` / `f.Sync` / encode / gzip failures: `slog.Error/Warn` once, no counter — subsequent writes can produce torn records (P1).

`internal/calibration/cliparse/` — four sites:
- [cliparse.go:104-114](../internal/calibration/cliparse/cliparse.go:104) — zip entry / bulletin parse: `logger.Warn`, `skipped++`, no typed error.
- [cliparse.go:14, :120-124](../internal/calibration/cliparse/cliparse.go:14) — out-of-window valid_date: dropped silently (documented in pkg comment).
- [cliparse.go:347-352](../internal/calibration/cliparse/cliparse.go:347) — `eachDate` parse error: swallowed `_` (P2).

**Cost of the status quo:**

1. **Operator surface is post-hoc and noisy.** `SkippedReason` is a per-run map dumped at end; cannot distinguish transient from sustained corruption.
2. **No invariant on what "silent" means.** Some sites have an `atomic.Uint64`, some a string-keyed map, some neither (`reader.go:148`, `truth.go:139`, `cliparse.go:347`).
3. **No invariant on what "skip" means.** `scan.go:243-249` hard-errors on `daily extremes` failure, aborting the entire scan, while every neighbor in the same loop returns `nil` and counts (Phase 1 P1, [`01-line-audit.md:50`](01-line-audit.md)).
4. **Bucket A A3 created the first exemplar** (typed errors + counter + log-suppress) but did not name it as policy — ad-hoc reuse will drift.
5. **Phase 6 Prometheus wiring has no canonical labels** to map to; counter names are currently inconsistent.

### Proposed change — six sub-policies

#### §2.1.1 — Typed error taxonomy

**Five categories.** Each existing typed error folds into one. New typed errors declare their category in the doc comment.

| Category | Semantic | Caller obligation |
|---|---|---|
| `ErrCorruptInput` | Input bytes don't decode (malformed JSONL, bad CSV row, unmarshalable snapshot). Producer-side defect. | Skip + count (default). Do **not** return through a loop boundary. |
| `ErrPartialCoverage` | Record decodes but doesn't meet downstream quality (sparse ensemble member, `MM` for TMAX). Producer is innocent — the world is partially observed. | Skip + count, same handling as corrupt; separated for diagnostics. |
| `ErrConfigDrift` | Runtime-detected misconfig the validator missed (empty `FitModelSet`; poll model set mismatched against coeffs; unknown station code). | Logged warn + counter — **not silent**. Non-zero counter is a deploy-pipeline alert. |
| `ErrUpstreamUnavailable` | Network/disk/OS resource unreachable but system is otherwise healthy (Kalshi 429, Open-Meteo 5xx, `journal.Open` `ENOSPC`). | Retry policy applies (`internal/retry`); on exhaustion escalate to caller, never silent. |
| `ErrInvariantViolation` | In-process caller violated a package precondition (`ApplyDelta` on uninitialized `Book`; `Predict` with `sigma ≤ 0`). | **Return error** + count. Bug surface; the test is "would we ever ship code producing this in steady state". |

**Folding existing typed errors:**

| Error | Category | Notes |
|---|---|---|
| `journal.ErrAlarmHookRequired` ([j:185](../internal/journal/journal.go:185)) | `ErrConfigDrift` | startup config validation |
| `replay.ErrBookUninitialized` ([o:163](../internal/replay/orderbook.go:163)) | `ErrInvariantViolation` | A3 state-machine guarantee |
| `replay.ErrSnapshotApplyFailed` ([o:169](../internal/replay/orderbook.go:169)) | `ErrCorruptInput` | wraps `sonic.Unmarshal` |
| `kalshi.ErrAuth` ([c:27](../internal/kalshi/client.go:27)) | `ErrUpstreamUnavailable` (non-retryable subcat) | 401/403; doc must say non-retryable |
| `edge.ErrInsufficientCoverage` ([o:310](../internal/edge/openmeteo.go:310)) | `ErrPartialCoverage` | already used via `errors.Is` |
| `edge.ErrNotWeatherTicker` ([t:63](../internal/edge/ticker.go:63)) | `ErrCorruptInput` | "input out of domain" — bucketed with corrupt |

**Note on `mos.ErrSchemaMismatch`.** It does NOT exist today. [`mos.go:339-341`](../internal/calibration/mos/mos.go:339) emits an inline `fmt.Errorf` with no sentinel. Phase 2 Tier 1 must **introduce** `mos.ErrSchemaMismatch` and `bayesian.ErrSchemaMismatch`, owned by ADR-0015 (schema-migration, §3.1).

#### §2.1.2 — Counter taxonomy

Per prior-review §S5 and ADR-0011 §5: *the counter is the visible signal that the silent-skip is happening.* No counter → no signal.

**Naming convention** (single rule):

```
<package>_<event>_total          (no labels)
<package>_<event>_total{reason}  (one event, multiple causes)
```

**Storage shape (audit era, pre-Phase 6):**

- Package-level `var <name> atomic.Uint64`.
- Public accessor `<Event>Total() uint64` — the seam Phase 6 reads at scrape time.
- `ResetCountersForTest()` for subtest isolation ([replay/orderbook.go:209](../internal/replay/orderbook.go:209)).

**Why atomic.Uint64 not a map**: maps need a mutex; hot path is one atomic add. Keeps the hot path lock-free and Phase 6 mapping mechanical.

**Why not `expvar`**: HTTP-handler integration with global registry — unwanted coupling for packages that need to be importable from non-HTTP test binaries.

#### §2.1.3 — Escalation rules

Default disposition per category:

| Category | Disposition | Operator visibility |
|---|---|---|
| `ErrCorruptInput` | Skip + counter | Counter only — **no log per event**. Phase 6 rate-aware watcher generates the alert. |
| `ErrPartialCoverage` | Skip + counter | Same as `ErrCorruptInput`. |
| `ErrConfigDrift` | Continue + counter + **rate-limited slog.Warn** | `logOnceOrSuppress` pattern from [replay/orderbook.go:234-254](../internal/replay/orderbook.go:234). One warn per (reason, key) per 10 min default. |
| `ErrUpstreamUnavailable` | Retry → caller | `Warn` once per retry-cycle exhaustion; `<pkg>_upstream_failures_total{reason}` per exhaustion (not per attempt). |
| `ErrInvariantViolation` | **Return error** + counter + slog.Error | Bug surface; stack-localized error is the right shape. |

**Promotion ladder** (Step 0 → Step 4, most severe):

```
Step 0: Skip + counter increment                  (default for corrupt/partial)
Step 1: Logged warn + counter (rate-limited)      (default for config-drift)
Step 2: Logged error + counter + alarm hook fire  (operator notification)
Step 3: Aborted operation (return error)          (default for invariant violation)
Step 4: Process exit (last resort)                (flush-failure cluster only)
```

**Step 4 is reserved for the journal.** Phase 1 P1 entry [`journal.go:336-342`](../internal/journal/journal.go:336): after N consecutive flush-or-sync failures in a window, the disk is effectively gone and the bufio buffer is silently producing torn records. Proposed rule: **5 consecutive failures within 30s → `os.Exit(2)`** after a final synchronous log.

#### §2.1.4 — Per-package exception process

Some sites legitimately deviate from §2.1.3 defaults (e.g. `non_weather_ticker` is *expected noise* in the edge scan loop — rate-warn would dominate stderr). Declare with an `//errpolicy:` comment immediately before the skip:

```go
//errpolicy: ErrCorruptInput; skip+counter, no rate-warn; ADR-0014.
//  Expected noise — Kalshi tape carries non-weather tickers on
//  every frame; logging-on-rate would dominate stderr.
res.SkippedReason["non_weather_ticker"]++
return nil //nolint:nilerr
```

Greppable, localized, diff-visible. A one-paragraph deviation lands in a normal PR; a class deviation (>3 sites, or public-API impact) requires ADR amendment. ADR-0014 will list every audit-era exception by file:line so initial migration doesn't need per-site PRs.

#### §2.1.5 — Migration tiers

**Tier 1 — must migrate in Phase 2** (10 sites named below; the Phase 1 P1 findings that name silent-skip as the *defect*):

| # | Site | Phase 1 ref | Category | Counter to add |
|---|---|---|---|---|
| T1.1 | [reader.go:148-151](../internal/replay/reader.go:148) | replay §P1 #3 | `ErrCorruptInput` | `replay_lines_dropped_total{reason}` |
| T1.2 | [reader.go:118-124](../internal/replay/reader.go:118) | replay §P2 | `ErrCorruptInput` | `replay_gzip_close_errors_total` |
| T1.3 | [orderbook.go:88-116](../internal/replay/orderbook.go:88) | replay §P1 #2 | `ErrCorruptInput` | `replay_levels_dropped_total{reason}` |
| T1.4 | [orderbook.go:101-105](../internal/replay/orderbook.go:101) | replay §P1 #3 | `ErrInvariantViolation` | `replay_negative_qty_events_total` |
| T1.5 | [truth.go:139-162](../internal/edge/truth.go:139) | edge §P1 truth #1 | `ErrCorruptInput` | `edge_truth_rows_dropped_total{reason}` |
| T1.6 | [poll_index.go:55-72](../internal/edge/poll_index.go:55) | edge §P3 poll_index #1 | `ErrCorruptInput` | `edge_polls_dropped_total{reason}` |
| T1.7 | [scan.go:243-249](../internal/edge/scan.go:243) | edge §P1 scan #4 | `ErrPartialCoverage` | downgrade hard-error to skip+counter |
| T1.8 | [scan.go:308-314](../internal/edge/scan.go:308) | edge §P1 scan #3 | `ErrConfigDrift` | use canonical key (closes trailing-`vs` bug) |
| T1.9 | [journal.go:336-342](../internal/journal/journal.go:336) | journal §P1 #4 | `ErrUpstreamUnavailable` → Step 4 at threshold | `journal_flush_errors_total`, `journal_sync_errors_total` |
| T1.10 | [cliparse.go:104-114](../internal/calibration/cliparse/cliparse.go:104) | cliparse §P2 | `ErrCorruptInput` | `cliparse_bulletins_dropped_total{reason}` |

**Tier 2 — migrate as touched**: non-P1 sites; do not ship in Phase 2. Rule: when this file is touched in Phase 3+, migrate the surrounding skip sites. ~8-12 sites in `scan.go` long-tail, `kalshi/client.go`, `kalshi/types.go`, `cliparse.go:347`, `obs/log.go:42`, `env/env.go:115`, `bayesian/update.go:46`.

**Tier 3 — legacy-tolerated**: documented expected behavior. Add `//errpolicy:` annotation; no other change. Sites: `scan.go:217-220` (non_weather_ticker noise), `cliparse.go:14` (out-of-window valid_date contract), the five "expected noise" reasons inside the edge scan loop if maintainer decides rates are "always non-zero on healthy runs".

#### §2.1.6 — Test patterns

Four-part discipline (reference: [replay/orderbook_test.go](../internal/replay/orderbook_test.go:184) — A3 changeset):

**(a) Counter sink.** `Reset…ForTest()` + delta-assert against accessor.

**(b) Typed-error path.** `require.ErrorIs(t, err, ErrSnapshotApplyFailed)` only. No `Contains(err.Error(), "snapshot")` string matches.

**(c) Slog handler capture.** `slog.NewJSONHandler(buf, ...)`, parse JSON records, assert `level`, `reason`, fields. Pattern: [`journal_test.go:208-220`](../internal/journal/journal_test.go:208). A small `internal/testlog` helper owns the boilerplate.

**(d) Alarm-hook discipline test.** Install hook that records `(src, count, since)` on a channel, assert edge-trigger semantics. Pattern: ADR-0011 §Test plan items 3-4.

**Discipline-on-the-policy test.** ADR-0014 adoption PR adds a `tools/errpolicy` analyzer (or `make lint` step) that scans for `//errpolicy:` annotations and asserts every §2.1.4 / Tier-3 site has one.

### Blast radius

- **Tier 1 (Phase 2)**: **10 sites across 4 packages**.
- **Tier 2 (migrate-as-touched)**: 8-12 sites, 5 packages, long-tail.
- **New types** (~11 exported sentinels): 5 category sentinels in `internal/errs` or `internal/errpolicy`; ~6 site sentinels (`replay.ErrMalformedLine`, `replay.ErrMalformedLevel`, `edge.ErrMalformedTruthRow`, `edge.ErrMalformedPoll`, `cliparse.ErrMalformedBulletin`, plus the two `ErrSchemaMismatch` owned by ADR-0015).
- **Counter accessors added**: 10 across 4 packages.
- **Tests**: 10 new counter-bump tests + 1 analyzer/lint test. Existing `SkippedReason`-map tests are not rewritten (counters are additive), *except* [`reader_test.go:131-148`](../internal/replay/reader_test.go:131), which is rewritten to assert the *counter*, not the *silence*.
- **Doc surface added**: ADR-0014 (this proposal → ADR, ~5-8 pages); **`docs/ERROR_HANDLING.md`** reference doc (~2-3 pages); one paragraph in `docs/OPERATIONS.md` §3.

### Alternatives considered

**Alt A — Status quo, add Prometheus in Phase 6.** Rejected: Phase 6 is plumbing, not structural. Without shared discipline, the metric namespace fragments and no single dashboard covers "corruption-rate across packages".

**Alt B — Convert every silent skip to an error return.** Rejected: breaks the `replay.ForEachFrame` callback contract (lines 117-121). A multi-million-frame scan that halts on the first corrupt line is unusable.

**Alt C — Sentinels + `errors.Is` with no counters.** Rejected: the silent-skip site is inside a `return nil` branch — there is no caller to `errors.Is` against. Counter at site is the only signal that survives.

**Alt D — Pure logger-based discipline, no counters.** Rejected: this is the exact failure mode ADR-0011 / Bucket A1 fixed. `cmd/tape` was running blind to journal drops because the only signal was a log line.

**Alt E — Centralize all skip-and-count in a shared helper.** Rejected (deferred): introduces a new abstraction every contributor must learn. Convention (§2.1.1) is cheaper than import-and-call. Phase 6 candidate.

### Rollback plan

- **Tier 1**: each of the 10 sites is one PR. Revert restores silent-skip byte-for-byte (counters are additive; `return nil` semantics unchanged). Category sentinel becomes an unused export (harmless).
- **Tier 3**: removing the `//errpolicy:` annotation reverts to pre-policy. No behavior change.
- **ADR**: 0014 can be superseded; typed errors stay importable through one deprecation release; counters stay (additive).

**Reversibility cost is low** everywhere except T1.9 (journal Step 4 ladder): `os.Exit(2)` on flush-cluster is a behavior change. Rollback is to demote Step 4 → Step 2 (one-line change matching pre-ADR behavior).

### Reserved ADR slot

**ADR-0014 — Error-handling discipline (typed-error / counter / escalation taxonomy)**

Foundational. Every other §-decision references typed-error / counter / escalation language defined here. Specifically:
- ADR-0015 (schema-migration) instantiates `mos.ErrSchemaMismatch` / `bayesian.ErrSchemaMismatch` under §2.1.1 taxonomy.
- ADR-0016 (station consolidation) emits `market.ErrUnknownStation` under §2.1.1.
- ADR-0017 (edge-split deferral) defines a `probability/*` per-package error-shape constraint that follows §2.1.1.

**Closes Bucket B P1s**:
- "Silent skip-and-count error policy across edge/replay/journal/cliparse" — [audit/01-line-audit.md:1012](01-line-audit.md).

**Open questions for sign-off**:
1. Shared sentinel package name — `internal/errpolicy` vs `internal/errs`. Lock at ADR-0014 acceptance.
2. `SkippedReason` map deprecation. Default: **keep both**; counters are observability, map is reporting. Map gets a doc-comment crosswalk to its mirror counter.
3. Step 4 ladder thresholds (T1.9): 5 consecutive failures in 30s → `os.Exit(2)`. Lock at per-site PR.
4. Phase 2 vs Phase 6 ownership of the lint check. Phase 2 preferred — discipline is at risk of drift before any code lands.

**Sign-off §2.1**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

# §3 — Schema-migration policy

## Decision §3.1: Per-artifact migration policy across the four schema-versioned on-disk artifacts

### Current state — inventory

| Artifact | Constant | Const at | Loader / reader | Validation today | Legacy behaviour |
|---|---|---|---|---|---|
| **Journal envelope** | `isobar.journal/v1` | `journal.go:77` | `reader.go:144-158` | None on read; writer stamps `v1` | Reader rewrites missing `v` to `v0-legacy` (`reader.go:53`); test `TestEnvelope_LegacyReadAsV0` |
| **MOS coefficients** | `isobar.mos/v1` | `mos.go:82` | `LoadCoefficients` (`mos.go:330-353`) | Hard reject on mismatch; reject empty `bins` | `FitModelSet` defaults to `["gfs"]` + warn log on missing; test `TestLoadCoefficients_LegacyFileBackCompat` |
| **Bayesian kernel** | `isobar.bayesian/v1` | `bayesian.go:25` | `LoadKernel` (`update.go:103-132`) | Hard reject on mismatch; reject null entries map; reject `N < MinKernelN`; reject `VarDelta < 0` | None: no legacy on disk |
| **Boot frame** | `isobar-tape.boot/v1` | `cmd/tape/main.go:140` | _none yet_; writer only (`cmd/tape/main.go:157-180`) | Writer-only; replay reader sees frame but does not interpret | None: new schema, single producer |

**Quantified legacy surface (measured 2026-05-13):**

| Source tree | Size | File count | Date span | Schema content |
|---|---|---|---|---|
| `data/kalshi/` | ~707 MB | 372 `.jsonl` + `.jsonl.gz` | 2026-04-18 → 2026-05-12 | 100% `v0-legacy` |
| `data/openmeteo/` | ~9 MB | 194 `.jsonl` + `.jsonl.gz` | 2026-04-18 → 2026-05-12 | 100% `v0-legacy` |
| **Total** | **~716 MB** | **566 files** | **~26 days** | **100% `v0-legacy`** |

The 100% legacy share is _today_'s state. Once `cmd/tape` resumes recording post-cutover the mix accretes; over a 3-6-month replay window the legacy share dominates and slowly decays. The policy must remain stable across the long co-existence window — load-bearing constraint.

`data/calibration/` — no `mos-coefficients.json` or `bayesian-kernel.json` on disk at audit time; both are produced on demand. Migration cost for these is _operator-time-bounded_, not corpus-bounded — a re-fit is minutes.

Cost of inconsistency (per `01b-prior-review.md` §S4 + `01-line-audit.md:497`):

1. **Silent miscoding** if a future loader (Phase 1+ `cmd/replay-server`, etc.) is added without consulting `reader.go:144-158`; it would decode legacy frames with `Version == ""` and trip nil-checks the existing reader has been masking.
2. **Operator surprise** during a `v1 → v2` evolution: four loaders, four debates, each conducted under whatever the local pressure is.

### Proposed change — primary policy (per artifact)

The four artifacts have **categorically different** migration economics, so a single policy is wrong. The proposal adopts a **hybrid** keyed on _regenerability cost_ and _historical-data accumulation rate_:

> **Refuse-and-reformat** for artifacts that regenerate cheaply (MOS, Bayesian, boot frame). **Accept-legacy-on-read** for the journal envelope, because the recorder is forward-only and the corpus is the project's most expensive artifact.

| Artifact | Primary policy | Rationale |
|---|---|---|
| Journal envelope | **Accept-legacy-on-read** (current behaviour, formalised) | 716 MB of corpus, growing ~30 MB/day; recorder writes forward-only so the "ratchet" is automatic — every new frame is `v1`, every old frame stays `v0-legacy`. No on-disk rewrite. |
| MOS coefficients | **Refuse-and-reformat** (current behaviour, formalised) | Single JSON; re-fit by `cmd/calibrate-fit` takes minutes. |
| Bayesian kernel | **Refuse-and-reformat** (current behaviour, formalised) | Same as MOS: regenerable; no on-disk legacy exists yet. |
| Boot frame | **Refuse-and-reformat-with-bypass** (new) | Embedded in another schema-versioned artifact (journal). Readers that interpret payload MUST reject unknown schema; readers that don't MUST skip regardless (hard-fail would render whole tape unreadable). |

### Version-bump rules

One rule, applied to every artifact: **could a v1 reader produce a meaningfully wrong answer if handed a v2 file?** If yes, bump. If no, don't.

| Change kind | Example | Bump? |
|---|---|---|
| **Field addition (optional, defaulted)** | `FitModelSet` added to MOS | **No.** Mark `omitempty`, loader provides default + warn log. |
| **Field addition (required, no default)** | Required `model_version` in MOS | **Yes.** |
| **Field removal** | Dropping `holdout_window_start` | **Yes.** |
| **Field rename** | `sigma` → `residual_stdev` | **Yes.** |
| **Field semantic change** (same name, different meaning) | `Sigma` reinterpreted in-sample → OOS-holdout | **MUST bump.** Most dangerous case. |
| **Format change** (JSON → MessagePack) | Phase 6 compression | **Major bump** + new top-level name (e.g. `isobar.mos/v2-msgpack`); requires magic-byte sniff. |
| **Field-default change** | `MinHoldoutForSigma` 30 → 50 | **No bump.** Numerical-policy change, not structural. |

Schema-version strings: `isobar.<artifact>/v<N>` with `N` monotone integer. No SEMVER.

### Per-artifact policy — full spec

#### Journal envelope — `isobar.journal/v1`

- **Policy**: Accept-legacy-on-read. Reader rewrites missing `v` to `isobar.journal/v0-legacy`.
- **Write**: Writer stamps the constant; `journal.go:77` is single source of truth.
- **Migration on bump**: reader accepts `v0-legacy`, `v1`, `v2` simultaneously, discriminating on `v`.
- **Cost ceiling**: when legacy set grows past ~3 versions, write fresh ADR retiring `v0-legacy` via one-shot script.

#### MOS coefficients — `isobar.mos/v1`

- **Policy**: Refuse-and-reformat. `LoadCoefficients` hard-rejects mismatch.
- **Optional fields**: `FitModelSet` is the canonical pattern (`omitempty`, default + warn).
- **Migration on bump**: operator re-fits via `cmd/calibrate-fit`. Deterministic given input pairs (ADR-0012); ~1-2 min/refit.

#### Bayesian kernel — `isobar.bayesian/v1`

- **Policy**: Refuse-and-reformat. `LoadKernel` hard-rejects mismatch.
- **Migration on bump**: operator re-fits via `cmd/calibrate-bayesian`.

#### Boot frame — `isobar-tape.boot/v1`

- **Primary policy**: Refuse-and-reformat-with-bypass.
- **Discriminator**: `src == "isobar-tape"` AND `k == "boot"`.
- **Write side**: `writeBootFrame` (`cmd/tape/main.go:157-180`) stamps the constant.
- **Bump rule**: per §3.1.3, restricted to the boot _payload_ (`cmd/tape/main.go:144-151`). Envelope follows journal-envelope policy.
- **Test discipline (gap)**: Line audit `01-line-audit.md:823` flagged "convention unenforced by tests". Phase 2 closes with one round-trip test in `cmd/tape/main_test.go`.

### Migration tool surface

Phase 2 forbids new top-level CLIs. Migration lives inside existing binaries or under `scripts/`:

- **Journal envelope**: No tool for v0-legacy → v1 (reader handles it). For future v1 → v2: (1) **in-loader migration** (preferred); (2) `scripts/migrate-tape.sh` wrapping a `cmd/tape -migrate <in> <out>` sub-mode if (1) is unworkable. **Not allowed**: new `cmd/migrate-tape` binary.
- **MOS coefficients**: Re-run `cmd/calibrate-fit`; old file discarded.
- **Bayesian kernel**: Re-run `cmd/calibrate-bayesian`.
- **Boot frame**: Next `cmd/tape` boot writes the current schema; old boot frames are ignored.

### Test discipline

**Cross-artifact tripwire** (NEW): `TestSchemaVersionConstants` asserts every versioned constant matches the regex `^isobar(-tape)?\.[a-z]+/v[0-9]+(-legacy)?$`. Fails before any loader test if a contributor empties or rewrites a constant.

**Per-artifact tests** (NEW additions in **bold**):

| Artifact | Rule | Test |
|---|---|---|
| Journal | Reader labels missing `v` as `v0-legacy` | `TestEnvelope_LegacyReadAsV0` — keep |
| Journal | Reader does NOT re-label valid `v1` | `TestEnvelope_CurrentVersionReadsThrough` — keep |
| Journal | Writer stamps current constant | **NEW** `TestJournalWriter_StampsSchemaVersion` |
| MOS | Loader rejects mismatched `schema_version` | **NEW** `TestLoadCoefficients_RejectsMismatchedSchemaVersion` |
| MOS | Loader handles missing optional `fit_model_set` | `TestLoadCoefficients_LegacyFileBackCompat` — keep |
| MOS | `Fit` emits current constant | `TestFitEmitsSchemaVersion` — keep |
| Bayesian | Loader rejects mismatched | `TestLoadKernel_SchemaMismatch` — keep |
| Bayesian | Loader rejects null entries map | `TestLoadKernel_RejectsNullEntriesMap` — keep |
| Boot frame | Writer stamps current constant | **NEW** `TestWriteBootFrame_StampsSchemaVersion` (closes line-audit gap `01-line-audit.md:823`) |
| Boot frame | Readers bypass unknown boot schemas | **NEW** `TestReplay_BypassesUnknownBootSchema` |

### Future versions

The policy itself must be versionable:

1. **Each artifact bump = a new ADR**, not an edit to ADR-0015.
2. **Cumulative cost is bounded.** With accept-legacy for journal, all historical schemas remain readable forever via `internal/replay`. With refuse-and-reformat elsewhere, only current version is loadable; cost forward is one ADR per bump.
3. **When the journal accumulates 3+ versions**, retire `v0-legacy` via a one-shot script under `scripts/`. Not needed today (n=2).

### Blast radius

- **Code changes**: ~5 LOC total. Boot frame needs `bootSchemaVersion` exported (or getter). No behavioural change for journal/MOS/Bayesian.
- **New tests**: 5 tests, ~115 LOC. Journal writer-stamp (~20), MOS explicit-mismatch (~25), boot-frame writer-stamp + bypass (~50), cross-artifact constants (~20).
- **Operator burden change**: *reduces* operator burden. Today: four loaders, four implicit behaviours, no doc. Post-proposal: one policy doc, four reference cards.
- **Migration cost for existing on-disk artifacts**: zero. Formalises existing v0-legacy reader; 716 MB of tape stays readable.
- **Doc surface added**: ADR-0015 + this section; optional `docs/SCHEMAS.md` aggregator (~80 LOC).

### Alternatives considered

**Alt A — uniform policy across all four artifacts.** Rejected.
- _Uniform refuse-and-reformat_ forces a one-shot `scripts/migrate-tape.sh` to re-encode 716 MB of corpus. Script becomes load-bearing — a bug eats the corpus.
- _Uniform accept-legacy_ forces every loader to carry default-fill code for every removed/renamed field forever. MOS/Bayesian have no legacy on disk; paying perpetual back-compat for an empty legacy set is strictly worse.

**Alt B — sidecar `.migrations` index per artifact.** Rejected. Heavyweight: extra file is a separate correctness concern.

**Alt C — in-place migrate (loader rewrites old files on load).** Rejected.
- Silent on-disk mutation by a reader violates the recorder's forward-only contract.
- For MOS/Bayesian, on-load rewrite breaks determinism property ADR-0012 pins.
- Operators can no longer trust mtime.

### Rollback plan

Almost entirely doc + small test surface; rollback is cheap.
- Journal: accept-legacy code already in `reader.go`. Backing out means deleting new tests. No data risk.
- MOS: backing out means removing new explicit mismatch test. No data risk.
- Bayesian: no code change to roll back.
- Boot frame: backing out exported `bootSchemaVersion` is mechanical.
- Policy reversal: relax via `0015-amends-0015` follow-up ADR. Total reversal ceiling: zero data, zero operator workflow, ~115 LOC of tests.

### Reserved ADR slot

**ADR-0015 — Schema-migration policy across the four versioned on-disk artifacts**

- ADR-0014 reserves error-taxonomy slot 0014; ADR-0015 reserves schema-migration. Both are doctrine for downstream §-decisions.
- Schema-migration is the most _structurally load-bearing_ of the seven ADR-gap candidates in `01b-prior-review.md` §3.

**Closes Bucket B P1s**:
- "Schema-migration policy" — [audit/01-line-audit.md:1011](01-line-audit.md).
- ADR-gap items: "Journal on-disk schema versioning" + "JSONL-NDJSON corpus format" + (potentially) "Sonic vs stdlib JSON for replay".

**Open questions for sign-off**:
1. **Boot frame export discipline.** Export `bootSchemaVersion` (cleaner for cross-package test) vs keep internal + test inside `cmd/tape/main_test.go`. Operator call.
2. **`v0-legacy` suffix exception** to the §3.1.3 naming rule. Confirm convention before cross-artifact test lands.
3. **`v0-legacy` retirement timeline** unspecified. Out of scope here; fresh ADR if/when needed.

**Sign-off §3.1**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

# §4 — Roles, tests, internal-only, Bucket B mapping

Four closing decisions sharing a structural theme — *where roles live*, *how we test them*, *what we expose externally*, and *what's left after Bucket A*. §§4.1 and 4.2 are coupled (test strategy depends on the role definition deciding which binaries survive); §4.3 stands alone; §4.4 is a routing table landing every Bucket B P1 onto a §1.1–§4.3 owner.

---

## Decision §4.1: Record / calibrate / trade role boundaries

### Current state

Two explicit roles today:

- **record** — `cmd/tape/main.go` (465 LOC + 240 LOC `alarms.go` + 395 LOC `main_test.go`). Owns the live WS subscription (`internal/kalshi/ws.go`), the Open-Meteo poller, and the journal writer. Writes `data/{kalshi,openmeteo}/YYYY-MM-DD/*.jsonl[.gz]`.
- **scan/analyze** — `cmd/edge-scan` (397 LOC), `cmd/analyze` (343 LOC), `cmd/peek` (105 LOC, dead). The first three call `replay.ForEachFrame` / `replay.ListJournals`; none call `journal.Open`.

ADR-0010 introduces a third invariant — *fit a kernel from tape + truth* — sprawling across three binaries with no shared layout:

- `cmd/calibrate-parse-cli/main.go` (59 LOC): NWS CLI bulletin zip → `data/calibration/obs/K{STA}.csv`.
- `cmd/calibrate-fit/main.go` (310 LOC): pairs CLI obs × hand-fetched GFS forecasts → `data/calibration/mos-coefficients.json`.
- `cmd/calibrate-bayesian/main.go` (80 LOC, scaffold): planned NWS CLI × Open-Meteo historical → `data/calibration/bayesian-kernel.json`. Stub today.

Prior-review §S2 (`01b-prior-review.md:215-223`): "The Phase 1+ shape should be three top-level roles, not two: **record** (`cmd/tape`), **calibrate** (the family of `cmd/calibrate-*` + a planned `cmd/calibrate-eval`), **trade** (offline scan today, paper/live exec in Phase 1+)."

**Trade** is deferred to a future `cmd/bot` (per ADR-0013, paper-trader lineage retired). Documenting its seam is forward-looking only.

### Proposed change

**Pin "calibrate" as a third top-level role and document its invariants and seams. Do not move code in this PR.**

1. **Role location**: keep the calibrate role distributed across `cmd/calibrate-*` binaries. No top-level `internal/calibrate` shared package today — the math already lives under `internal/calibration/{cliparse, mos, climate, bayesian}` and the binaries are thin orchestrators. The §1.2 split of `internal/edge` would place `probability/{mos,climate,bayesian}` under a probability namespace, and `internal/calibration/*` is structurally the *fitter* side of that domain. Defer any rename of `internal/calibration → internal/probability/fit` until after §1.2 (ADR-0017) lands — coupled refactors, doing them separately doubles churn.

2. **Calibrate-role invariants** (to be codified in the reserved ADR):
   - **Reads**: recorded tape (replay package), NWS CLI ground-truth CSVs, GFS forecast CSVs, Open-Meteo historical hourly (future, for Bayesian).
   - **Writes**: schema-versioned coefficient/kernel JSON under `data/calibration/*.json`.
   - **Forbidden**: writes orderbook, writes live tape, opens WS, signs Kalshi requests. Test enforcement is structural: no `cmd/calibrate-*` binary imports `internal/kalshi`, `internal/journal`, or the live Open-Meteo poller.

3. **Calibrate → Record seam**: unidirectional, file-system. Calibrate reads tape via `internal/replay`; never writes tape. The seam is the on-disk JSONL format (covered by ADR-0015 schema-migration).

4. **Calibrate → Trade seam**: file-system, coefficient JSON. Trade (future `cmd/bot`) calls `mos.LoadCoefficients` / `bayesian.LoadKernel`; never writes coefficients. The typed boundary on the trade side is the future `WeatherSignal.Probability(MarketState) (PFair, EdgeRow, error)` (prior-review §S3) — extraction owned by §1.2 (ADR-0017), not §4.1.

5. **`cmd/calibrate-eval` scope** (forthcoming, separate ADR):
   - **Consumes**: recorded tape, `edge.LoadTruth`, MOS JSON, Bayesian kernel JSON.
   - **Produces**: eval report — calibration plot (P_predicted vs P_realized), Brier score, log-loss, per-bin sample-size + CIs, drift signal vs prior fit. Markdown/CSV under `data/calibration/eval/YYYY-MM-DD/`.
   - **Cadence**: weekly (decoupled from monthly MOS refit; rationale in the eval ADR, not here).
   - **Gate**: per RISK.md §P0, eval pass-criteria are the gate before any `cmd/bot` real-money order.

### Blast radius

- **Files**: 0 production Go files. One new ADR + one paragraph in `docs/ARCHITECTURE.md` §"Future shape".
- **APIs**: 0 changes. Invariants are static-analysis rules (no-import checks), not Go interface changes.
- **Tests**: optional `go list -deps ./cmd/calibrate-...` lint that fails if `internal/kalshi/ws.go` or `internal/journal` appear in the closure. Cheap.
- **Docs**: ADR + ARCHITECTURE cross-link. Reserved slot for the eval ADR stays open.

### Alternatives considered

1. **Promote `cmd/calibrate-*` into a top-level `internal/calibrate` shared package now.** Rejected: binaries are 59–310 LOC; shared math already lives under `internal/calibration/*`. The aggregator earns its keep only after `cmd/calibrate-eval` + `cmd/calibrate-bayesian` (post-scaffold) land at 600+ LOC each with overlapping pipelines.
2. **Treat "calibrate" as a sub-mode of scan (flag on `cmd/edge-scan`).** Rejected: edge-scan is read-only against tape; calibrate writes coefficient artifacts with their own refit cadence. Mixing them muddies the unidirectional file-system seam.
3. **Defer pinning until `cmd/calibrate-bayesian` ships.** Rejected: if `cmd/bot` lands first (independent timeline), the no-write-coefficients invariant has no enforcement story.

### Rollback plan

Trivial. Revert one ADR + one ARCHITECTURE paragraph. No production code touched. Lint check (if added) is one removable line in `make lint`.

### Reserved ADR slot

**ADR-0018 — Three-role architecture (record / calibrate / trade)**

Cross-links: ADR-0010, ADR-0013, ADR-0017 (edge split), prior-review §S2.

**Sign-off §4.1**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

## Decision §4.2: Test-coverage strategy for the 5 zero-test binaries

### Current state

`01-line-audit.md` cmd cross-cutting #7: "5 production binaries have zero test files — `edge-scan`, `calibrate-fit`, `calibrate-parse-cli`, `analyze`, `peek`. Only `tape` has tests."

Per-binary LOC:

| Binary | LOC | Imports `replay` | Imports `kalshi` (live) | Status |
|---|---:|:-:|:-:|---|
| `cmd/tape` | 465 + 240 alarms.go | no | yes (WS + signer) | prod-running |
| `cmd/edge-scan` | 397 | yes (via `edge.Scan`) | no | prod artifact |
| `cmd/analyze` | 343 | yes | no | human-facing sanity |
| `cmd/calibrate-fit` | 310 | no | no | prod artifact |
| `cmd/peek` | 105 | no | yes (REST signer) | DEAD (audit cmd § P1) |
| `cmd/calibrate-bayesian` | 80 | no | no | scaffold (out of this §) |
| `cmd/calibrate-parse-cli` | 59 | no | no | prod artifact |

Test cost is dominated by **fixture setup**, not test code: edge-scan needs a recorded-tape fixture + a `edge.Truth` CSV; calibrate-fit needs paired GFS-forecast/CLI-obs CSVs; calibrate-parse-cli can reuse the zip fixtures already in `internal/calibration/cliparse/cliparse_test.go`.

### Proposed change — per-binary verdict matrix

| Binary | Survive? | Test strategy | Test cost | Owner section |
|---|---|---|---:|---|
| `cmd/tape` | **YES** | Existing test stays; add §2.1 typed-error coverage when error-policy ADR lands. | already paid | §2.1 |
| `cmd/edge-scan` | **YES** | (a) Smoke: `TestMain_RunsAgainstFixtureTape` invokes `main` with `-data testdata/tape`, asserts non-panic + non-empty CSV header. (b) Integration: `writeCSV` / `writeSummary` against a hand-rolled `edge.Result`. Scan math correctness already lives in `internal/edge/scan_test.go`. | ~150 LOC + ~5 MB fixture tape (gzipped) | §1.2 |
| `cmd/analyze` | **YES (minimally)** | Smoke only — invoke `main` against a 100-frame fixture day, assert stdout contains `## kalshi`, `## openmeteo` headings. Do NOT assert report numbers. | ~50 LOC (reuses §1.2 fixture) | §1.2 |
| `cmd/calibrate-fit` | **YES** | (a) Integration: `loadAllPairs` + `mos.Fit` + `writeJSON` end-to-end against 2 stations × 200 days of synthetic CSV pairs. Assert output validates via `mos.LoadCoefficients`, `FittedAt` non-zero (clock-injected via `internal/clock.Fake` per ADR-0012), bin count matches, sigma bounded ~[0.5°F, 10°F]. (b) Unit tests of `requireColumns` / `maxIndex` / `pairByDate`. | ~200 LOC + tiny CSV fixtures | §4.1 |
| `cmd/calibrate-parse-cli` | **YES** | Smoke only — exercises `cliparse.Run` via the binary; asserts `-start <= -end` validation + `inferOutPath` derivation. Reuses cliparse zip fixtures. | ~80 LOC | §4.1 |
| `cmd/peek` | **HARDEN, keep on ADR-0006 canary clock** (see below) | Smoke: compile + `-help`. Route through `cfg.Kalshi.APIBase` (drop hardcoded prod URL). Consolidate `loadPEM` with `cmd/tape` and `internal/config`. | ~50 LOC + ~30 LOC refactor | §4.1 |
| `cmd/calibrate-bayesian` | **KEEP AS SCAFFOLD** | Tighten `os.Exit(2)` only. Real tests land with the implementation PR (post-Phase-2). | n/a | §4.1 |

### `cmd/peek` reconciliation (DELETE vs ADR-0006 "ongoing")

ADR-0006 (dated 2026-04-22) accepted `cmd/peek` as an ongoing ops probe and names the canary: "if six months go by and we never run `peek` for a real question, it's clutter and should go." Six-month expiry: ~2026-10-22.

`01-line-audit.md` cmd § "DELETE recommendation" flags hardcoded prod URL + duplicate `loadPEM` + no tests.

**Recommended: keep + harden now.** Honoring ADR-0006 means peek survives; the audit's substantive complaints (hardcoded URL, duplicated PEM-load) are real and trivial to fix and intersect with the cmd § cross-cutting "three PEM loaders" finding. The ADR-0006 canary continues to tick; if October 2026 arrives without a peek-driven question, delete then via a supersession ADR.

### Cost estimate

| Item | Cost |
|---|---:|
| edge-scan smoke + writer-integration | ~150 LOC + ~5 MB fixture tape |
| analyze smoke | ~50 LOC (reuses fixture) |
| calibrate-fit integration + helper units | ~200 LOC + <10 KB CSV |
| calibrate-parse-cli smoke | ~80 LOC (reuses zip fixtures) |
| peek harden + smoke | ~80 LOC |
| **Total new test LOC** | **~560 LOC** |
| **Engineering effort** | **3–5 person-days** (fixture curation dominates) |

### Blast radius

- **Files**: 5 new `*_test.go`; 1–2 new `testdata/` subdirs; possibly one new helper (`config.LoadPEM`) to deduplicate.
- **APIs**: no public API changes; `LoadPEM` consolidation may expose a new exported helper.
- **Tests**: ~5 new test files; CI walk over the 5 MB fixture <1s.
- **Docs**: `docs/TESTING.md` adds §"CLI smoke tests".

### Alternatives considered

1. **No smoke tests; rely on `internal/*` package tests + manual operator verification.** Rejected: the current state. `cmd/calibrate-fit`'s atomic-rename+sync path and `cmd/edge-scan`'s killed-process-leaves-partial-CSV (audit P2) are not covered by any `internal/*` test. Smoke catches binary-level bugs that package tests structurally cannot.
2. **Full E2E per binary including a fake Kalshi.** Rejected: gold-plates a research project; re-evaluate when `cmd/bot` lands.
3. **Delete all `cmd/calibrate-*` and inline into `cmd/edge-scan`.** Rejected: contradicts §4.1's role pin; couples calibration cadence to scan cadence; violates ADR-0008's deterministic-only first-pass.

### Rollback plan

`git revert`; fixture-tape addition is the only persistent cost (~5 MB git history), reversible via `filter-branch` if a sensitive-data leak later surfaces in the fixture.

### Reserved ADR slot

**ADR-0019 — CLI binary test discipline (smoke + integration tiers)**

Cross-links: `docs/TESTING.md`, prior-review §3.

**Sign-off §4.2**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

## Decision §4.3: `internal/`-only rule — commit or discard

### Current state

The constraint is documented as *guiding* in `docs/ARCHITECTURE.md` §"Guiding constraints" but has no ADR commitment. The codebase honors it:

- `find /Users/wh0o0pz/dev/isobar -type d -name pkg | grep -v node_modules | grep -v .git` returns **no results**. No `pkg/` directory exists.
- `grep -rn '^package ' internal/` enumerates 15 packages, all rooted at `github.com/wh0o0pz/isobar/internal/*`: `bayesian, climate, cliparse, clock, config, edge, env, journal, kalshi, market, mos, obs, openmeteo, replay, retry`.
- `grep -rn '^package ' tools/` shows `clockcheck` (analyzer logic) + its own `cmd/clockcheck` driver + testdata. No production-shaped package outside `internal/`.

The convention is *de facto* enforced today. §4.3's question is whether to pin it with an ADR.

### Proposed change

**Commit the rule explicitly, with documented trigger conditions for revisiting.**

1. **Rule (ADR text)**: "All reusable Go packages live under `internal/`. No `pkg/` directory exists. Exceptions:
   - `cmd/<binary>/` — `package main` entry points.
   - `tools/<analyzer>/` — static-analysis tooling that operates on the codebase, including its own `cmd/` driver. Not part of the production binary closure (verified via `go list -deps ./cmd/...` excluding `tools/*`)."

2. **Rationale**: `internal/` is a Go-toolchain-enforced API barrier. Inside the isobar module every `cmd/*` and `tools/*` can import any `internal/*` — practical scope is unchanged. The rule's payload is *what we promise external consumers*: nothing. A future external consumer (e.g. hypothetical `cmd/replay-server` for a dashboard) would be blocked from importing — by design.

3. **Trigger conditions for revisiting** (in the ADR's "Reversal" section):
   - A second Go module ships alongside isobar (e.g. a separate `dashboard-server` repo wanting `internal/replay/orderbook.go`). Then either (a) the helper moves to `pkg/` with deliberate API-stability commitment, or (b) the dashboard repo is pulled into this module under `cmd/dashboard-server/`.
   - An open-source release of `internal/clock` or `tools/clockcheck` as a standalone utility. Same trigger.

The rule is not "never export". It is "never export *casually*, accidentally, or as a side-effect of restructuring." Every promotion is a deliberate ADR.

### Blast radius

- **Files**: 0 production Go. One new ADR.
- **APIs**: 0 changes. De-facto state already matches.
- **Tests**: optional `make lint` check that fails if a `pkg/` directory appears (`test ! -d pkg`). Trivial.
- **Docs**: ADR + one-line citation in `docs/ARCHITECTURE.md` §"Guiding constraints".

### Alternatives considered

1. **Discard the rule; allow `pkg/` proactively.** Rejected: invites accidental promotion. Every `pkg/`-rooted package is an unwritten API-stability promise.
2. **Hybrid: commit `internal/`-only for current shape, allow `pkg/` for future "library mode".** Rejected as premature — the ADR's "trigger conditions for revisiting" already handles this. Once a real external consumer materializes, the rule revisit is forced by the ADR's own reversal section.
3. **Defer the ADR until a real revisit-trigger fires.** Rejected: prior-review §3 specifically calls out the unwritten nature as the gap. Writing it down costs half a day; not writing it down has asymmetric cost (the "we always did it this way" amnesia at maintainer turnover).

### Rollback plan

Trivial — one ADR file + one line in ARCHITECTURE.md + optional one line in `make lint`. `git revert`.

### Reserved ADR slot

**ADR-0020 — `internal/`-only Go package layout**

Cross-link: prior-review §3, line "internal/ only — no pkg/".

**Sign-off §4.3**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

## Decision §4.4: Map the Bucket B P1s to their structural decisions

### Current state

`audit/01-line-audit.md` Bucket A appendix (lines 1005-1018) lists **Held for Phase 2 (Bucket B)** as 10 table rows; the appendix's "Ground state count" states 18 P1s held. The discrepancy resolves: row 10 is compound — the parenthetical says "7 ADR-gap candidates" but lists 8 specific items (journal schema versioning, recorder-no-decode invariant, edge consolidation, sonic choice, JSONL format, poll cadence, internal-only rule, fee model). Enumerable items: 9 standalone rows + 8 sub-items = **17 atomic items** vs the appendix's headline 18. The 1-item discrepancy is most likely the "7 vs 8" parenthetical drift; flagged in close-out.

### Proposed change — the routing table

For each Bucket B P1: which §1.1–§4.3 decision it gates on, whether the fix lands *in* that decision's PR or *after*, and effort once unblocked.

| # | Bucket B P1 | Source | Gates on | Fix in PR? | Effort once unblocked |
|---|---|---|---|---|---:|
| 1 | 3 station-mapping tables drifting | edge cross-cutting | **§1.1 / ADR-0016** | **IN** — the consolidation *is* the fix | ~1 day |
| 2 | Kalshi REST 429-retry on POST duplicate-order hazard | kalshi §P1 #3 | **`cmd/bot` ADR** (OUT of Phase 2) | AFTER (`cmd/bot` PR lands the fix) | ~1 day |
| 3 | Schema-migration policy | calibration cross-cutting + §S4 | **§3.1 / ADR-0015** | IN — defines the policy ADR | ~2 days |
| 4 | Silent skip-and-count error policy | edge §P3, replay §P1, cliparse §P2 | **§2.1 / ADR-0014** | IN — defines the taxonomy ADR | ~2 days |
| 5 | 5 binaries with zero tests | cmd §cross-cutting #7 | **§4.2 / ADR-0019** | IN (verdict matrix); test code lands AFTER per matrix owner-section | ~3–5 days total |
| 6 | `internal/edge` split | §S1 | **§1.2 / ADR-0017** | IN — the deferral ADR *is* the fix; actual split is post-trigger | ~3–5 days at trigger |
| 7 | Record/calibrate/trade three-role split | §S2 | **§4.1 / ADR-0018** | IN — no code moved by §4.1 itself | 0 days (cost bundled into §4.2 + §1.2) |
| 8 | Extract `WeatherSignal` from `Scan` | §S3 | **§1.2 + §4.1** | IN — extraction during §1.2 split | ~2–3 days |
| 9 | Time-source-discipline doctrine (remainder beyond ADR-0012) | §S5 | **§3.1 / ADR-0015 or standalone** | IN — extends ADR-0012 | ~0.5 day |
| 10 | ADR-gap: Journal on-disk schema versioning | §3 row 1 + §S4 | **§3.1 / ADR-0015** | IN — same ADR as item 3 | (covered) |
| 11 | ADR-gap: Recorder-never-decodes invariant | §3 row 2 | **§2.1 / ADR-0014 or standalone** | IN (separate ADR slot 0021) | ~0.5 day |
| 12 | ADR-gap: `internal/edge` consolidation | §3 row 3 + §S1 | **§1.2 / ADR-0017** | IN — ADR-0017 cites *and supersedes* | ~0.5 day |
| 13 | ADR-gap: `bytedance/sonic` choice for replay JSON | §3 row 5 | **§3.1 / ADR-0015 or standalone** | IN (separate ADR slot 0022) | ~1 day |
| 14 | ADR-gap: JSONL-NDJSON corpus format | §3 row 6 | **§3.1 / ADR-0015** | IN — same ADR family as item 3 | ~0.5 day |
| 15 | ADR-gap: Open-Meteo 30-min poll cadence | §3 row 7 | **none of §1.1–§4.3** — strategy-doctrine ADR | OUTSIDE Phase 2; standalone slot 0023 | ~0.5 day |
| 16 | ADR-gap: `internal/`-only rule | §3 row 9 | **§4.3 / ADR-0020** | IN — the ADR *is* §4.3's commitment | (covered) |
| 17 | ADR-gap: Kalshi fee model | §3 row 8 | **none of §1.1–§4.3** — strategy-doctrine | OUTSIDE Phase 2; standalone slot 0024 | ~0.5 day |

**Total Bucket B effort**: ~15–22 person-days. Breakdown: ~8–12 days inside the §1.1/§1.2/§2.1/§3.1/§4.1/§4.2/§4.3 PRs (where the decision *is* the fix); ~3–5 days of test build-out (item 5); ~1 day for the `cmd/bot`-gated POST-retry fix (item 2, deferred); ~3 days for the small standalone engineering/strategy-doctrine ADRs (items 11, 13, 15, 17).

### Notable observations

1. **Five of 17 items are pure ADRs**, not code changes. Phase 2's ADR write-up burden is **7 reserved (0014-0020)** + **~3-4 standalone** (items 11, 13, 15, 17 = 0021-0024). Roughly half of Phase 2's deliverable is *written decisions*, not code.

2. **Six items collapse into others**: item 7 (three-role split) is §4.1 itself with zero standalone code cost; items 10 + 14 collapse into item 3's schema-migration ADR; item 12 collapses into item 6's edge-split ADR; item 16 collapses into §4.3. 17 P1s map to **~8 actual PRs** once collapses are honored.

3. **Two items are explicitly out of Phase 2**: item 2 (Kalshi 429-on-POST) is deferred behind `cmd/bot`; items 15 + 17 are strategy-doctrine, can ship at any time without gating Phase 2.

4. **One item is sub-divided**: item 8 (`WeatherSignal` extraction) maps against §1.2 *and* §4.1 — the extraction supports both the package split (§1.2 owns the move) and the future trade-role seam (§4.1 documents the *shape*).

### Blast radius

§4.4 itself has **zero blast radius** — it's a routing table, not a code change. Cumulative blast radius lives under each owner section.

### Alternatives considered

1. **Map each P1 to a separate owner PR with no collapsing.** Rejected: produces 17 PRs from 17 items, but several P1s are literally the same change as the structural decision (item 1 *is* §1.1). Artificial coordination overhead.
2. **Defer the mapping entirely; do Bucket B in a single mega-PR after Phase 2's decisions land.** Rejected: per-item effort becomes invisible to operator review.
3. **Treat the 8 ADR-gap candidates as Bucket C (post-Phase-2).** Rejected: prior-review §3 explicitly placed them in Phase 2's scope.

### Rollback plan

Routing tables don't roll back; their downstream PRs do, individually. If §4.4 mis-routes an item, the fix is to update this section + the owner section's blast radius.

### Reserved ADR slot

**None.** §4.4 is a routing table; the ADRs it routes to are reserved under their owner sections.

**Sign-off §4.4**: [ ] APPROVED AS WRITTEN  |  [ ] APPROVED WITH MODIFICATIONS  |  [ ] HOLD

---

# Cross-cutting observations

## Phase 2's deliverable shape

- **7 reserved ADRs** (0014-0020) + **~4 standalone** (0021-0024) = **~11 ADRs total**.
- **~8 actual PRs** (after Bucket B item collapses).
- **~15-22 person-days** estimated for all Bucket B work.
- **~560 LOC of new tests** + **~5 MB fixture tape** for §4.2 CLI test build-out.
- **~280 LOC of new production code** (mostly §1.1 + §2.1 Tier 1 migrations) + **~400 LOC deleted** (the duplicate station tables + Tier 3 `//errpolicy:` cleanup).
- Net: roughly half of Phase 2's deliverable is *written decisions*, not code. Aligns with the charter.

## Inter-decision dependencies

```
ADR-0014 (error taxonomy) ─┬─→ ADR-0015 (schema migration; uses Err-categories for SchemaMismatch)
                           ├─→ ADR-0016 (station consolidation; uses ErrConfigDrift for ErrUnknownStation)
                           ├─→ ADR-0017 (edge split deferral; defines per-pkg error shape for probability/*)
                           └─→ ADR-0019 (CLI test; smoke tests assert typed-error paths)

ADR-0015 (schema migration) ─┬─→ ADR-0016 (station consolidation; boot-frame schema bump composes against 0015)
                             └─→ ADR-0018 (role boundaries; uses schema as seam between calibrate→trade)

ADR-0016 (station consolidation) ─→ (independent of 0017-0020 once 0014 + 0015 land)

ADR-0017 (edge split deferral) ─→ defers actual split until ADR-0010 wiring; no Phase 2 PR

ADR-0018 (role boundaries) ─→ ADR-0019 (CLI test) — test discipline per role

ADR-0019 (CLI test) ─→ (independent doctrine)

ADR-0020 (internal/-only) ─→ (independent doctrine)
```

**Recommended landing order**: 0014 → 0015 → 0016 → 0018 → 0019 → 0020 → 0017 (the deferral ADR can land last; it commits to a future split, not a current one). 0021-0024 (standalone strategy/engineering doctrine) land any time after 0014.

## Open coordination questions (operator decision)

1. **Sentinel-package name** (`internal/errs` vs `internal/errpolicy`) — locked at ADR-0014 acceptance.
2. **Boot-frame export discipline** (`bootSchemaVersion` exported vs internal) — locked at ADR-0015 acceptance.
3. **§2.1 lint analyzer ownership** (Phase 2 vs Phase 6) — recommended Phase 2 to prevent discipline drift.
4. **`cmd/peek` reconciliation** (delete vs keep+harden vs let ADR-0006 canary tick) — recommended keep+harden until 2026-10-22 canary fires.
5. **Bucket B P1 headcount** (appendix says 18, faithful enumeration yields 17) — appendix correction in next audit pass.

---

# Per-decision sign-off summary

Operator response on each decision below. Implementation may proceed only on signed-off decisions, in dependency order.

| § | ADR | Decision | Sign-off |
|---|---|---|---|
| §1.1 | ADR-0016 | Unify station tables into `internal/market` | [ ] |
| §1.2 | ADR-0017 | Defer `internal/edge` split until ADR-0010 wiring | [ ] |
| §2.1 | ADR-0014 | Error-handling discipline taxonomy | [ ] |
| §3.1 | ADR-0015 | Schema-migration policy across 4 artifacts | [ ] |
| §4.1 | ADR-0018 | Three-role architecture (record/calibrate/trade) | [ ] |
| §4.2 | ADR-0019 | CLI binary test discipline | [ ] |
| §4.3 | ADR-0020 | `internal/`-only Go package layout | [ ] |
| §4.4 | (routing) | Bucket B P1 mapping to structural decisions | [ ] |

**End of audit/02-architecture.md.** Per cadence rule, **stopping here** for per-decision sign-off. No code is moved, no ADR-0014+ is committed, no test is added until per-decision approval lands in the operator's response. Synthesis-author note: ~14K words; reading time ~45-60 min; suggest reviewing in §-order and recording sign-offs as you go.
