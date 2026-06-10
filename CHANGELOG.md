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

Nothing yet — the next behavior-changing commit adds its entry here.

---

## [0.5.0] — 2026-06-10 — Phase 0.5 closed: directional gates failed; structure-first pivot (ADR-0014)

*Cut as a backfill on 2026-06-10 after ~7 weeks of changelog drift
(everything since `6a905e1`, ~123 commits). Entries are curated
highlights, not a commit mirror — the per-commit record is
`git log 6a905e1..`. The "every behavior-changing PR updates this
file" rule is back in force from here.*

### Added
- `internal/edge` — tape→trade→poll edge scanner with realized-PnL
  backtest mode ("no imaginary bets"), Kalshi parabolic taker + maker
  fee schedules, bracket resolution incl. T-bracket siblings,
  `--entry-filter h4` selective entry.
- MOS calibration pipeline — NWS CLI bulletin parser → daily TMAX/TMIN
  truth CSVs, IEM ASOS hourly obs fetcher, per-(station, season)
  coefficients with `FitModelSet` guard against model-set mismatch.
- T−3h Bayesian update (ADR-0010) — `cmd/calibrate-bayesian` fits a
  per-(station, variable, h_remain) residual kernel; scanner applies a
  precision-weighted posterior using the running extremum.
- `internal/universe` — catalog discovery via public `/series` +
  `/events`, structural classification (mutex sets / ladders /
  complements), per-series fee params from the API, **fail-closed**
  NY-eligibility gate.
- `internal/clock` + `tools/clockcheck` analyzer (ADR-0012) — direct
  `time.Now`/`Since`/`Until` outside the allowlist fails `make lint`.
- `internal/journal` hardening (ADR-0011) — drop counter, panic-safe
  `OnDropAlarm`, `alarms/<date>.jsonl`, envelope schema version `"v"`.
- `cmd/tape` — boot frame with config SHA + station snapshot; refuses
  to boot when `.env` is group/world readable.
- Analysis scripts — LOO CV, subset mining, bankroll-aware `paper_sim`
  + `paper_sweep`.
- `cmd/analyze` — Phase-0.5 sanity analyzer. Reads the per-source JSONL
  journals for a given date and prints a markdown report: frame counts,
  type histograms, per-series and per-market activity, wall-clock
  regression count, inter-frame gap distribution, Open-Meteo per-location
  poll counts and gaps; plus an end-of-window book table for the top-25
  most-active tickers (yesBid / yesAsk / mid / spread / implied P(yes)).
- `cmd/peek` — one-off Kalshi REST probe. Enumerates candidate series
  names and prints market counts per status. Built to diagnose the
  `KXLOW*` naming mismatch.
- `kalshi.Client.GetRaw(ctx, path, params)` — public wrapper supporting
  arbitrary signed GET for diagnostics.
- `replay.Book` — reconstructable Kalshi market orderbook
  (`orderbook_snapshot` wipe + replace, `orderbook_delta` with sign
  handling); derives `BestYesBid`, `BestYesAsk`, `MidYesCents`,
  `SpreadYesCents`; integer-cent math throughout.
- `scripts/tape-health.sh` — smoke-test checker for the first hour of
  tape uptime; exit `0` = run 48h, `1` = fix first. `scripts/README.md`
  with conventions.

### Changed
- Performance: sonic JSON on the read path + pre-bucketed climatology
  (2.5× end-to-end), per-UTC-date parallel scan (a further 3.27×).
- Consolidated fee/scanner/ensemble into `internal/edge`; repo-wide
  `Ts` → `TS` field rename.
- Chicago station pinned to canonical KMDW coordinates (was ORD).
- `KALSHI_PRIVATE_KEY` is now the primary bare-name env var for the
  inline PEM; the former `KALSHI_API_KEY` is kept as a back-compat
  alias (`KALSHI_PRIVATE_KEY` wins when both are set). `.env.example`,
  error messages, and the tape's credential-source log label updated.
- `.golangci.yml`: errcheck exclusions widened to cover
  `(*os.File).Close` and `(*github.com/coder/websocket.Conn).Close`.
- `Makefile`: added `build-all`, `run-peek`, `run-analyze` targets.
- Extracted `cmd/analyze`'s journal-reading and Kalshi-frame decoding
  into `internal/replay` (92.4% line coverage); `cmd/analyze` output
  unchanged.
- `docs/OPERATIONS.md` §2.1 "Smoke-test your first run" — how to
  use `tape-health.sh`, what passing/failing runs look like, the
  48h decision criterion.

### Tests
- `internal/kalshi`: added `TestGetRaw` + `TestGetRawPropagatesAPIError`.
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

### Fixed
- **`kalshi/ws`: reconnect-time ticker refresh ends the
  2026-05-21 → 06-01 frame-starvation outage** — the subscriber held its
  boot-time ticker list forever; once every held market settled, each
  watchdog-forced reconnect resubscribed a dead list and starved.
  `RefreshTickers` re-resolves the open-market set on every connection
  attempt (non-fatal fallback to last-known list); `retry.ResetAfter`
  resets the backoff ladder after sustained health. Postmortem:
  `docs/OPERATIONS.md` §15.
- `kalshi/ws`: app-level liveness watchdog — ping-pong + read deadline +
  max-idle-between-frames forced reconnect.
- `kalshi` REST: ctx-aware throttle (was a non-cancellable `time.Sleep`),
  `Retry-After` parsing, 5xx retry, typed `ErrAuth` on 401/403,
  `minDelay` success-counter decay.
- `replay/orderbook`: explicit uninitialized state — deltas arriving
  before a snapshot are rejected instead of applied to an empty book.
- `calibration/climate`: leap-year-aware day-of-year (Feb 29 no longer
  aliases Mar 1).
- `openmeteo`: bounded exponential per-fetch retry with jitter.
- `cmd/tape`: `runRediscovery` moved into the errgroup with
  panic-recover (a panic there previously died silently).
- `internal/kalshi/ws.go` `SubscriberOptions.Channels` comment listed
  `ticker_v2` in its example; removed — rejected by the current API
  (ADR-0006).
- `config/config.example.yaml`: corrected WS channel list (`ticker_v2`
  removed), daily-low series names (`KXLOWT{CITY}`, not `KXLOW{CITY}`),
  and dropped stale `KXBTCD`.
- `kalshi.ws.Subscriber.runOnce`: HTTP upgrade response body closed on
  successful dial (leaked the 101 response body; flagged by `bodyclose`).
- `internal/obs/log_test.go`: `Enabled` tests pass `context.Background()`
  instead of `nil` (SA1012).
- `internal/retry`: ctx-cancel-returns-nil documented as package
  behavior; `nilerr` silenced per-line with rationale.
- `docs/ROADMAP.md` Phase-0 risks: stale "`ticker_v2` as liveness
  canary" mitigation replaced with actual behavior.
- `docs/GLOSSARY.md`: **Fair value** updated from the money-losing
  naive members-fraction definition to the calibrated-posterior
  definition; **Signal** examples updated to the two actual signals.

### Security
- TLS 1.2 minimum + explicit `http.Transport` with per-stage timeouts
  on the Kalshi client.
- `cmd/tape` refuses to boot when `.env` is group/world readable.
- Journal files open at `0o600` and journal dirs at `0o750` (was
  `0o644` / `0o755`).

### Docs
- ADR-0007 → ADR-0014: calibration ground truth via CLI bulletins,
  deterministic historical forecasts, MOS canary escalation, T−3h
  Bayesian update, journal backpressure, time-source discipline,
  PR-9 lineage retirement, and the structure-first pivot.
- `docs/BUILDLOG.md` — coordination journal between autonomous build
  runs and interactive sessions; the freshest state in the repo.
- `docs/OPERATIONS.md` §15 — WS frame-starvation postmortem + triage
  runbook; §2.1 smoke-test guide moved with it.
- `docs/TESTING.md` coverage table: added `internal/replay` (floor
  90%, at 92.4%); `journal` 85.9% vs 90% floor tracked.
- `README.md`, `docs/ARCHITECTURE.md`, `docs/GLOSSARY.md`: `cmd/peek` +
  `cmd/analyze` described; stale `ticker_v2` references removed.
- `docs/decisions/0006-peek-probe-kalshi-naming.md` — the
  `KXHIGH*`/`KXLOWT*` naming asymmetry and the `category` label fact.
- README / ROADMAP / STRATEGY post-pivot accuracy pass (2026-06-10):
  status tables now end at the forward-week-1 verdict and point at
  ADR-0014 + BUILDLOG.

### Research
- `docs/research/09-structure-first-pivot.md` — the accepted pivot
  proposal, incl. §0 epistemic grading of the opportunity classes.
- Phase 0.5 report chain: floor v2 (n=299, MOS loses to mid by 10pp
  Brier), v3 Bayesian verdict (−$4,963.86 full-universe), v4 H4 filter
  (+$793.74 backtest, flagged in-window-mined), forward week 1 verdict
  (−$30.24 realized, 22.3% win rate) + 24-position re-score
  (expected +$9.45 / realized −$0.05).

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
