# Changelog

All notable changes to `isobar` are recorded here. Format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/);
versioning is phase-gated (`vPHASE.X.Y`) per `docs/ROADMAP.md`.

Every PR that changes behavior **must** update this file in the same
commit. Non-behavior changes (pure docs, typo fixes, CI tweaks) may
skip it but are encouraged to note the fact in the PR description.

Section order within a release: `Added` · `Changed` · `Fixed` ·
`Removed` · `Deprecated` · `Security` · `Docs` · `Research`.

## [Unreleased]

### Changed
- `.golangci.yml`: errcheck exclusions widened to cover `(*os.File).Close`
  and `(*github.com/coder/websocket.Conn).Close` — the bare
  `(io.Closer).Close` pattern wasn't catching concrete-type defer closes.
- Recorder journal files now open at `0o600`; directories at `0o750`.
  Recorded data is owner-only; no shared read on the Vultr box is
  expected (`chmod` if that changes).
- `Makefile`: added `build-all`, `run-peek`, `run-analyze` targets for
  parity with `run-tape`.
- `cmd/peek`: error sites now use `fmt.Errorf("…: %w", err)` to match
  the `errorlint` convention.

### Docs
- `README.md`, `docs/ARCHITECTURE.md`, `docs/GLOSSARY.md`: added
  `cmd/peek` and refreshed `cmd/analyze` descriptions now that both
  exist. Fixed a stale `ticker_v2` reference in the README kalshi
  component blurb.
- `docs/decisions/0006-peek-probe-kalshi-naming.md` — ADR capturing
  the `KXHIGH*` / `KXLOWT*` naming asymmetry, the `category` label
  fact (`"Climate and Weather"`, not `"Weather"`), and the decision to
  keep `cmd/peek` first-class for W6 hurricane series discovery.

### Refactor
- Extracted `cmd/analyze`'s journal-reading and Kalshi-frame decoding
  into `internal/replay`. `cmd/analyze` now imports `replay.Open`,
  `replay.ListJournals`, `replay.ForEachFrame`, `replay.Envelope`,
  `replay.KalshiFrame`, and `replay.SeriesPrefix`. Same end-user
  output, 92.4% line coverage on the new package (drops slightly as
  orderbook reconstruction logic lands below).

### Added — Analysis
- `replay.Book` — reconstructable Kalshi market orderbook. Handles
  `orderbook_snapshot` (wipe + replace) and `orderbook_delta`
  (add/remove level with sign handling). Derives `BestYesBid`,
  `BestYesAsk` (from `100 - BestNoBid`), `MidYesCents`, and
  `SpreadYesCents`. Prices normalize to integer cents for exact math.
- `cmd/analyze` now prints an end-of-window book table for the top-25
  most-active tickers, with yesBid / yesAsk / mid / spread / implied P(yes).
  Tonight's data already flags wide-spread tail brackets
  (`KXLOWTNYC-26APR22-B45.5` at 42¢ spread, `KXLOWTMIA-26APR22-B69.5`
  at 31¢) — the mispricing pattern the weather thesis predicted.

### Tests
- `internal/kalshi`: added `TestGetRaw` + `TestGetRawPropagatesAPIError`.
  Coverage rose from 71.0% to 75.2% (floor is 85%; still under, but
  the newly-added public method is now covered).
- **Coverage push across 5 packages**, 4 of 5 now at or above floor:
  | package    | before | after | floor |
  |------------|-------:|------:|------:|
  | `config`   |  91.8% | 100%  |   95% |
  | `env`      |  88.1% | 96.6% |   95% |
  | `obs`      |  76.9% | 100%  |   90% |
  | `kalshi`   |  75.2% | 90.3% |   85% |
  | `journal`  |  71.9% | 85.9% |   90% |
  `journal` remains 4.1% below floor — the uncovered paths are
  fault-injection territory (file-write errors mid-run, encode
  failures, flush/fsync failures) that would require dependency
  injection of the filesystem layer to test cleanly. Tracked as a
  standalone follow-up.
- `internal/journal`: lifted two hardcoded ticker intervals
  (`rotateCheckInterval`, `dropReportInterval`) from 15s/60s constants
  to package-level vars so age-based-rotation and dropped-frames
  warning paths are testable in <100 ms rather than needing real wall
  time. Production behavior unchanged.

### Added
- `cmd/analyze` — Phase-0.5 sanity analyzer. Reads the per-source JSONL
  journals for a given date and prints a markdown report: frame counts,
  type histograms, per-series and per-market activity, wall-clock
  regression count, inter-frame gap distribution (p50/p95/p99/max,
  reset across file boundaries), Open-Meteo per-location poll counts
  and gaps. MVP scope; orderbook reconstruction and implied-vs-modeled
  edge are follow-ups once NGR/EMOS calibration lands.
- `cmd/peek` — one-off Kalshi REST probe. Enumerates candidate series
  names and prints market counts per status. Built to diagnose the
  `KXLOW*` naming mismatch; will be reused for hurricane-landfall series
  discovery in W6.
- `kalshi.Client.GetRaw(ctx, path, params)` — public wrapper on the
  private `getRaw` helper. Supports arbitrary signed GET for diagnostics;
  production callers should still add typed wrappers.

### Fixed
- `config/config.example.yaml`: corrected Kalshi WS channel list — `ticker_v2`
  is rejected by the current API (`code 8 "Unknown channel name"`); removed.
  Remaining `orderbook_delta` + `trade` carry strictly more information.
- `config/config.example.yaml`: corrected Kalshi daily-low series names.
  `KXLOW{CITY}` returns zero markets; the live API uses `KXLOWT{CITY}` with
  an extra `T`, and NY is `KXLOWTNYC`. `DiscoverOpenTickers` count rises
  from 48 (highs only) to 72 (highs + lows) on a current run.
- `config/config.example.yaml`: removed stale `KXBTCD` (BTC-hourly was
  killed in 0.1.0 but the example still listed it).
- `kalshi.ws.Subscriber.runOnce`: HTTP upgrade response body is now
  closed on successful dial (previously leaked the 101 Switching
  Protocols response body — flagged by `bodyclose`).
- `internal/obs/log_test.go`: handler `Enabled` tests now pass
  `context.Background()` instead of `nil` (SA1012 staticcheck).
- `internal/retry/retry.go:72,79`: ctx-cancel-returns-nil is documented
  package behavior; silenced the `nilerr` linter with a per-line
  `//nolint` + rationale.

### Security
- Journal files open at `0o600` and journal dirs at `0o750` (was
  `0o644` / `0o755`).

---

## [0.2.0] — 2026-04-18 — Refactor: `internal/retry` + throttle fix

### Added
- `internal/retry` package (`Do` / `Jitter` / `Next`) — single canonical
  exponential-backoff-with-jitter primitive shared across every WS and
  REST consumer. 97% covered.
- `kalshi.Client.DiscoverOpenTickers(ctx, series, logger)` — promoted
  out of `cmd/tape`; paginates, deduplicates, guards against the
  cursor-with-empty-page infinite-loop case.
- `make coverage` target producing `coverage.html`.
- `TestThrottleDoesNotSerializeCallers` race-regression guard.
- `TestDiscoverOpenTickersPaginatesAndDedupes` +
  `TestDiscoverOpenTickersHandlesEmptyPageWithCursor`.

### Changed
- `kalshi.Subscriber.Run` now delegates reconnect/backoff to
  `retry.Do` instead of reimplementing the loop.
- `kalshi.Client.throttle` reserves its rate-limit slot under the mutex
  and sleeps outside it. Previously the lock was held across
  `time.Sleep`, serializing every concurrent caller.
- Adaptive `minDelay` growth on HTTP 429 capped at `maxAdaptiveDelay =
  2s` to prevent compound slowdown.
- 429 retry backoff uses bit-shift `1<<n` instead of
  `math.Pow(2, float64(n))`.

### Fixed
- Concurrency bug: `kalshi.Client.throttle` held `rateMu` across the
  sleep — N concurrent callers would wait N × `minDelay` total instead
  of queuing past each other.

### Removed
- Private `jitter()` function in `kalshi/ws.go` and its test.
- Inline `discoverTickers` in `cmd/tape/main.go`.

### Docs
- `docs/ARCHITECTURE.md` updated: new `internal/retry` section; kalshi
  section rewritten for the new throttle semantics +
  `DiscoverOpenTickers`.
- `docs/TESTING.md` updated: coverage target row for `internal/retry`;
  `make coverage` entry.

---

## [0.1.0] — 2026-04-18 — W1: research pivot + Coinbase kill

### Added
- `docs/research/SYNTHESIS.md` consolidating the 8 agent reports into
  a single decision document.
- `docs/research/06-microstructure-math.md` — Wave-2 agent output
  (reservation price with bounded σ² = P(1-P)/√t, optimal half-spread,
  M/M/∞ fill probability, OFI gate, no-trade zone, CUSUM, Kelly with
  ρ = −1/(K−1)).
- `docs/research/07-atmospheric-physics.md` — Wave-2 agent output
  (predictability ceilings, NGR/EMOS/AIFS blending, EnKF 3h pre-close,
  conformal intervals, hurricane SST+intensity, radar advection
  inversion).
- `docs/research/08-evt-statmech.md` — Wave-2 agent output
  (Peaks-Over-Threshold for tail brackets, copula joint pricing,
  Lyapunov chaos bound, power-law tail MM, rabbit-holes to skip).

### Changed
- `docs/STRATEGY.md` rewritten: two signals (`weather-calibrated` +
  `hurricane-landfall`); NGR/EMOS calibration mandatory; conformal
  intervals wrap predictors; EVT/POT for tail brackets; intraday
  Bayesian update at T−3h; Lyapunov 8-day hard gate; fee-aware edge
  floor `> 0.07·2P(1-P) + 1¢`.
- `docs/ROADMAP.md` rewritten: Phase-0.5 gate now requires calibrated
  Brier-skill beating market-implied on OOS 90-day held-out data;
  hurricane-landfall milestone timed for the 2026 Atlantic season
  (June 1).
- `docs/RISK.md` updated: fee-aware edge floor, Lyapunov horizon cap,
  Kelly with ρ = −1/(K−1) correction, bounded-payoff σ² correction.
- `docs/ARCHITECTURE.md`, `docs/TESTING.md`, `docs/OPERATIONS.md`,
  `docs/GLOSSARY.md`, `README.md` all updated to reflect 2-source
  tree and 2-signal strategy.
- `config/config.example.yaml`: `coinbase:` block removed.
- `internal/config/config.go`: `Coinbase` struct and its validation
  deleted; `Config` loses one field.

### Removed
- `internal/coinbase/ws.go` and `internal/coinbase/ws_test.go`.
- `startCoinbase` goroutine in `cmd/tape/main.go`.
- BTC-hourly / Coinbase-vs-KXBTCD strategy references throughout
  docs and tests.

### Research
- Wave 1 (4 agents): Kalshi bot landscape, weather-thesis critical
  audit, alternative non-BTC markets, dashboard architecture.
  See `docs/research/01`–`04`.
- Wave 2 (3 agents): microstructure math, atmospheric physics,
  EVT + stat-mech + chaos. See `docs/research/06`–`08`.
- Literature review (1 agent) in `docs/research/05`.

### Decision summary
After 8 parallel research agents the naive `p_fair = members/31`
thesis was judged structurally money-losing on Kalshi after fees
(raw ensemble fractions uncalibrated; Kalshi fees `ceil(0.07·N·P·(1-P))`
peak at 50¢ mid; Kalshi MMs already run calibrated multi-model stacks).
BTC-hourly was judged dead alpha at REST-from-cloud latency after
Coinbase's Kalshi integration in Jan 2026. Pivot: two calibrated
signals sharing a probabilistic-forecaster-vs-event-contract framework.
Full reasoning: `docs/research/SYNTHESIS.md`; individual ADRs:
`docs/decisions/`.

---

## [0.0.2] — 2026-04-18 — Initial doc + test coverage

### Added
- `docs/ROADMAP.md`, `docs/STRATEGY.md`, `docs/ARCHITECTURE.md`,
  `docs/TESTING.md`, `docs/OPERATIONS.md`, `docs/RISK.md`,
  `docs/GLOSSARY.md`, `docs/README.md` (index).
- Package tests across the board: `clock` (100%), `config`
  (92.6%), `env` (88.1%), `openmeteo` (87.7%), `coinbase` (79.5%),
  `obs` (76.9%), `journal` (71.9%), `kalshi` (67.6%). Total 75.1%.
- Race-tested WS mocks for Kalshi + Coinbase.
- Httptest-based mocks for Open-Meteo.

### Changed
- `README.md` links the new `docs/` suite.

---

## [0.0.1] — 2026-04-18 — Phase 0 scaffold

### Added
- `cmd/tape` recorder (Kalshi WS + Coinbase WS + Open-Meteo REST).
- `internal/journal` append-only rotating JSONL writer with batched
  fsync and non-blocking `Write`.
- `internal/kalshi` REST client + RSA-PSS signer + authenticated
  WS subscriber (ported from `arbhunter`).
- `internal/coinbase` public WS subscriber.
- `internal/openmeteo` GFS ensemble REST poller.
- `internal/clock` paired (wall, monotonic) timestamp.
- `internal/config` YAML + env overlay + validation.
- `internal/env` minimal `.env` loader.
- `internal/obs` JSON `slog` bootstrap.
- `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`.
