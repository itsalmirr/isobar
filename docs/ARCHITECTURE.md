# Architecture

> How the code is laid out today, the invariants each package guarantees,
> and how the pieces compose. Read this before making changes that cross
> package boundaries.

## Guiding constraints

- **Single binary per concern.** Six binaries today, none of them a
  god-binary. Each command does one thing and exits.
- **`internal/` only.** No `pkg/` until there is a second binary that needs
  to import outside this module. The compiler enforces the boundary.
- **Stdlib first.** Every third-party dep is an audit burden. Current deps
  (and justification):
  - `coder/websocket` — maintained, context-aware, no hidden goroutines.
  - `golang.org/x/sync` — `errgroup` for orchestrator lifecycle.
  - `gopkg.in/yaml.v3` — YAML config only; JSON uses stdlib.
  - `testify` — test-only; kept to `require` for fail-fast assertions.
- **Hot-path discipline.** The recorder never decodes. Bytes from the
  socket are stamped + journaled, and decoding happens in the analyzer
  where latency doesn't matter and schema changes are cheap to fix.
- **Time-source discipline (ADR-0012).** Every wall-time read in
  `internal/*` flows through `clock.Clock` (preferred for new code) or
  a `now func() time.Time` field on a struct (existing pattern in
  `internal/kalshi`, `internal/openmeteo`). Direct `time.Now`,
  `time.Since`, `time.Until` calls outside the allowlist
  (`internal/clock`, `cmd/*/main.go`, `*_test.go`,
  `//nolint:clockcheck`) are rejected by the `clockcheck` custom
  analyzer in `tools/clockcheck/`, wired into `make lint`. See
  `internal/clock/clock.go` for the `Clock` / `Wall` / `FakeClock`
  seam and ADR-0012 for the full doctrine.

## Directory layout (current)

```
cmd/
  tape/                       Phase 0 recorder. Live on Vultr with rolling
                              ticker re-discovery (4h cycle).
  analyze/                    Sanity report over recorded journals (frame
                              counts, gaps, monotonicity, type histogram).
  peek/                       Kalshi REST probe — confirms series-name
                              correctness when the catalog changes.
  calibrate-parse-cli/        NWS CLI bulletin zip → daily TMAX/TMIN CSV.
  calibrate-fit/              OLS-fit MOS coefficients per (station,
                              season, variable) bin → JSON output.
  calibrate-bayesian/         Fits the T−3h residual kernel for
                              internal/calibration/bayesian (ADR-0010).
                              Flags wired; fit body deferred to a
                              follow-up commit per the foundation/
                              integration split.
  edge-scan/                  Joins recorded book state to ensemble polls,
                              applies all probability models (naive, MOS,
                              climate-shrunk, market), emits per-bracket
                              edge CSV with full provenance + optional
                              realized-PnL backtest when --truth is set.

internal/
  clock/                      (Wall, Mono) stamp pair.
  config/                     YAML + env overlay + validation. Field
                              `kalshi.rediscover_interval` controls the
                              recorder's rolling re-discovery cadence.
  edge/                       Offline scan engine: recorded poll decoder
                              (ParsePoll, DailyExtremesFWithSkip), Kalshi
                              fee model, ticker parser, bracket
                              resolution with sibling-aware T-direction
                              inference, truth-file index, and the Scan
                              entrypoint for cmd/edge-scan. The ensemble
                              decoder and fee model used to live in
                              internal/openmeteo and internal/market;
                              both were consolidated here so the scanner
                              owns the offline-only surface end-to-end.
  env/                        Stdlib-only .env loader.
  journal/                    Append-only rotating JSONL writer with
                              size + age rotation, batched fsync, gzip
                              on rotate.
  kalshi/                     RSA-signed REST client + signer +
                              authenticated WS subscriber with
                              SetTickers/Reload for graceful re-subscribe.
  market/                     Station/location/timezone/series registry
                              (single source of truth) + a separate
                              market-ticker parser. No fee model here
                              — fees moved into internal/edge.
  obs/                        slog JSON bootstrap.
  openmeteo/                  Live Open-Meteo REST poller only. The
                              ensemble decoder (ParsePoll, DailyExtremes*,
                              MemberDaily) moved into internal/edge so
                              this package stays a thin recording
                              dependency.
  replay/                     JSONL journal reader (plain + gz) +
                              orderbook reconstruction. Shared input
                              layer for cmd/analyze, cmd/edge-scan.
  retry/                      Context-aware exponential backoff with
                              jitter. Used by ws subscriber + any other
                              reconnect-style consumer.
  calibration/
    cliparse/                 NWS CLI bulletin parser; emits canonical
                              daily TMAX/TMIN CSV per station.
    mos/                      OLS bias correction per (station, season,
                              variable) + ProbBelow/ProbAbove via Φ.
                              FitModelSet stamping + holdout-σ honesty.
    climate/                  Historical bracket-frequency lookup with
                              ±30-day seasonal window + ShrinkP helper
                              (`p = w·climate + (1-w)·naive`).
    bayesian/                 T−3h precision-weighted Gaussian posterior
                              update (foundation only; ADR-0010). Not
                              yet wired into edge-scan.

config/
  config.example.yaml         Canonical example; copy to config.yaml.

scripts/
  calibration-fetch-cli.sh    Pull NWS CLI bulletin zips (IEM AFOS).
  calibration-fetch-obs.sh    Pull hourly ASOS observations (IEM).
  calibration-fetch-forecast.sh   Pull deterministic GFS historical
                              forecasts (Open-Meteo historical-forecast-api).
  tape-health.sh              Operational smoke check.
  loop-prompt.txt             Health-check playbook.

reports/
  phase0.5/                   Phase 0.5 evaluation artifacts.
  INDEX.md                    Chain-of-reports index.

docs/
  research/                   8 research reports + SYNTHESIS.md.
  decisions/                  13 ADRs (0001-0013). ADR-0010 proposes the
                              T−3h Bayesian update; ADR-0011/0012/0013
                              are the Bucket A hardening decisions
                              (journal backpressure, time-source
                              discipline, PR#9 retirement).

tools/
  clockcheck/                 Custom x/tools/go/analysis analyzer that
                              flags direct time.Now/time.Since/time.Until
                              calls outside the ADR-0012 allowlist.
                              Wired into `make lint` via
                              `tools/clockcheck/cmd/clockcheck`.
```

*Historical note:* `internal/coinbase` (public Coinbase WS subscriber)
was removed in W1. BTC-hourly lead-lag is dead alpha at
REST-from-cloud latency after Coinbase's Kalshi integration (Jan 2026).

## Runtime shape (Phase 0)

```
  ┌────────────┐              ┌────────────────┐
  │ kalshi.WS  │              │ openmeteo.REST │
  └─────┬──────┘              └────────┬───────┘
        │ OnFrame(raw)                 │ OnFetch(loc, raw)
        ▼                              ▼
   ┌──────────────────────────────────────────────┐
   │           journal.Journal (one per source)   │
   │   in <- envelope (copied) → writer goroutine │
   │       → bufio → *os.File → fsync + rotate    │
   └──────────────────────────────────────────────┘
                       │
                       ▼
     data/<source>/<YYYY-MM-DD>/<source>-<ts>.jsonl[.gz]
```

Orchestration uses `errgroup.WithContext`: if any goroutine returns a
non-nil error, the context cancels and peers wind down gracefully.
`signal.NotifyContext` ties that context to SIGINT/SIGTERM, so `Ctrl-C`
produces a clean drain.

## Package invariants

### `internal/clock`

- `Stamp.Wall` is `UnixNano` (UTC).
- `Stamp.Mono` is nanoseconds since process start.
- Both fields are sampled from the *same* `Clock.Now()` observation; they
  refer to the same physical event.
- Wall is the **only** cross-process comparable field. Use monotonic only
  for in-process interval measurement.
- Per ADR-0012, the package now exports a `Clock` interface with two
  implementations: `Wall()` returns the production singleton backed by
  `time.Now`; `Fake(initial)` returns a `*FakeClock` for deterministic
  tests (`Advance`, `Set`, concurrent-safe). `InitForTest(c)` reanchors
  the package-level `Stamp.Mono` origin for tests that exercise `Now()`
  directly. The clockcheck analyzer (`tools/clockcheck/`) enforces this
  discipline at lint time — see the guiding constraint above.

### `internal/journal`

- `Write` **never** blocks. If the queue is full, the frame is dropped and
  the counter incremented. The recorder must never apply backpressure to a
  market-data feed; we would rather lose frames than stall a producer.
- Payload bytes are **copied on enqueue** so callers can reuse their read
  buffer (critical for `coder/websocket`, which reuses its read buffer
  between `Read` calls).
- On `Close`: drain queue → flush → fsync → close file. Idempotent; safe
  from multiple goroutines.
- Rotation is size-based (`MaxFileBytes`) or age-based (`MaxFileAge`),
  whichever fires first. Rotated files are optionally gzipped.
- On-disk shape: newline-delimited JSON, one envelope per line, with keys
  `t` (wall), `m` (mono), `src`, `k` (kind), `p` (raw payload), and
  `v` (schema version, `isobar.journal/v1`; readers rewrite missing
  `v` to `v0-legacy` for back-compat).
- Per ADR-0011, the queue-full drop path is **operator-visible**.
  `Options.OnDropAlarm` is *required* — `Open` returns
  `ErrAlarmHookRequired` if nil — and is invoked at most once per
  crossing edge (drops sustained at ≥ `AlarmThreshold` per second for
  ≥ `AlarmSustain`). The hook is panic-safe: a panicking operator
  callback is logged + counted (`AlarmHookPanicTotal`) but never kills
  the writer goroutine. `cmd/tape` installs its own sink in
  `cmd/tape/alarms.go` that appends one JSONL line per crossing to
  `data/alarms/<YYYY-MM-DD>.jsonl` (lazily opened, civil-date-rotated,
  fsynced + closed in the errgroup-shutdown defer chain) alongside a
  structured `slog.Error` and a process-wide `JournalDropsTotal`
  counter — the tripwire that fails the build if `cmd/tape` ever
  drops the hook is `TestProdJournalHasAlarmHookWired`.

### `internal/config`

- `Load` reads YAML, overlays env vars (`ISOBAR_*` prefix and bare
  legacy names), and calls `Validate`. Any failure short-circuits startup.
- `Validate` refuses to accept:
  - empty `data_dir`.
  - zero `journal.queue_size`.
  - both `journal.max_file_bytes == 0` AND `max_file_age == 0`.
  - `kalshi.enabled` without an API key + PEM source.
  - `kalshi.enabled` with no series or no channels.
  - `openmeteo.enabled` with `poll_interval < 1m`.

### `internal/env`

- Never overwrites an already-set env var. Precedence is always: real
  env ≫ file.
- `LoadNearest` walks upward from CWD, so running a binary from a subdir
  still finds the project `.env`.
- Parses `export KEY=VALUE`, `KEY="quoted value"`, and multi-line quoted
  values. Comments (`#`) and blanks are skipped.

### `internal/kalshi`

- `Signer.AuthHeaders(method, path)` returns `(key, signature, timestamp)`
  where `signature = RSASSA-PSS(SHA256, timestamp + method + path)`. The
  path must include Kalshi's API version prefix (e.g. `/trade-api/v2/...`).
- `Client` applies a built-in min-delay rate limiter (1 req/s baseline)
  that reserves slots under the mutex and sleeps *outside* it — so
  concurrent callers queue past each other instead of serializing on
  the sleep. Adaptive slowdown on 429 is capped at `maxAdaptiveDelay`
  (2 s) so a flaky API can't compound into minute-long gaps. Call
  `ResetRateLimit` after discovery to drop the inflated delay before
  polling.
- `Client` retries on 429 up to `maxRetries=5` with bit-shift
  exponential backoff; non-2xx/non-429 are surfaced as
  `API error <status>: <body>`.
- `Client.DiscoverOpenTickers(ctx, series, logger)` paginates
  `GET /markets` for each series prefix, deduplicates, filters to
  open markets matching the prefix. One call, reusable from any
  caller — not reimplemented in `cmd/tape`.
- `Subscriber` is the WS counterpart: authenticated dial, subscribe
  to configured channels + market tickers, forward raw frames via
  `OnFrame`. Reconnect/backoff is delegated to `internal/retry`.

### `internal/retry`

- `Do(ctx, cfg, logger, fn)` is the single canonical reconnect/backoff
  loop. Every WS-ish component and REST poller that needs to retry goes
  through it. Zero values in `Config` degrade to sensible defaults
  (500 ms initial, 30 s cap, unlimited attempts).
- `Jitter(d)` returns `d + U[0, d/4]`; `Next(prev, max)` returns the
  next doubled backoff capped at `max`. Callers that need a custom loop
  (e.g. the Kalshi 429 handler) compose with these helpers instead of
  reimplementing them.
- Clean context cancellation always returns `nil` from `Do`; callers
  don't have to tell "graceful shutdown" from "real failure".

### `internal/openmeteo`

The live REST poller used by `cmd/tape`. Post-refactor this package
holds only the recording-side concerns; the offline ensemble decoder
moved into `internal/edge` so the recorder pulls in less code than the
scanner.

- HTTP `GET ensemble-api.open-meteo.com/v1/...` with a 30-second
  timeout and a 32 MiB response cap per fetch.
- `NewPoller` validates `poll_interval >= 1m` at construction; Open-Meteo
  free-tier rate limits forbid anything tighter.
- `Run` fires the first fetch *immediately* (before the first ticker
  tick) so Phase 0 has data right after process start.
- `Model` is a free-form string passed through verbatim to the API's
  `models=` parameter (defaults to `"gfs_seamless"`). Multi-model
  callers can pass `"gfs_seamless,ecmwf_ifs025"` to get both ensembles
  in one response; the per-member field-name fan-out is handled at decode
  time by `internal/edge.matchTemperatureMember`.
- `Variables` is passed through as `hourly=`; the recorder requests
  `temperature_2m` and lets the decoder split that into per-member
  series.
- Locations supply `(lat, lon, name)`; the human-readable `Name` is
  stamped into the journal envelope `kind` (`"rest:NY"`,
  `"rest:MIA"`, `"rest:LAX"`, `"rest:CHI"`) so the offline scanner
  can map polls back to station codes without re-deriving from lat/lon.
- `OnFetch` is called with the freshly allocated response bytes; the
  recorder's journal copies on enqueue, so the callback can return
  before journaling completes.

### `internal/market`

Single source of truth for the four-station registry plus the older
market-ticker parser kept for `cmd/peek` and dashboards. Fees are *not*
here anymore — they moved to `internal/edge.FeePerContractCents` so
all scanner-side concerns live in one package.

- `Station{Code, Location, City, Lat, Lon, UTCOffsetHours, HighSeries,
  LowSeries, CLIPil}` is the canonical record. Lookup helpers:
  `AllStations`, `AllStationCodes`, `StationByCode`, `StationByLocation`,
  `StationBySeries`. Linear-scan O(N) is intentional with N=4 —
  benchmarks show it beats a hash map by ~3x on cache-line-resident
  inputs. Switch to a map when N exceeds ~16.
- Chicago resolves on `KMDW` (Midway), *not* `KORD` (O'Hare) — per
  ADR-0007 and commit `35d5622`. Adding a new station means appending
  to `allStations` *and* updating `scripts/calibration-fetch-*.sh` to
  include the matching code.
- Per-location `UTCOffsetHours` is hardcoded for US daylight time
  (April–October). Re-validate before any winter-month recording window.
- The market-ticker parser handles Kalshi's asymmetric naming
  (`KXHIGH{2-3-letter}` vs `KXLOWT{3-letter}`). Ticker shape is
  `<SERIES>-<YYMMMDD>-<{B|T}><KEY>`; the bracket-direction inference
  for T-tickers (high-side vs low-side) lives in `internal/edge`
  because it needs sibling Specs.

### `internal/edge`

Offline scan engine — the only consumer of recorded Open-Meteo polls
and the home of every probability column edge-scan emits. Post-refactor
this is a deliberately large package because the scan pipeline is one
data flow that crosses many concerns; splitting it lost more clarity
than it bought.

Surface area, in dependency order:

- **Ensemble decoder** (`ParsePoll`, `Poll.DailyExtremesF`,
  `Poll.DailyExtremesFWithSkip`, `MemberDaily`,
  `ErrInsufficientCoverage`, `MinHourlySamplesPerMember`). `ParsePoll`
  takes a single recorded JSON payload and returns a station-agnostic
  `Poll` with per-member hourly series. Members are decoded out of the
  field-name pattern: bare `temperature_2m` → `"00"`,
  `temperature_2m_memberNN` → `"NN"`, multi-model variants prefix the
  model name (`ncep_gefs_seamless:NN`, `ecmwf_ifs025_ensemble:NN`).
  Aggregator fields (`_max`, `_min`) are skipped on parse — daily
  extremes are derived per-member by `DailyExtremesFWithSkip`, which
  windows hourly values to the station-local civil day. Members with
  fewer than `MinHourlySamplesPerMember = 12` in-window samples are
  *individually* skipped and the count is returned alongside the
  surviving subset; the scanner sums these into `Result.MembersSkipped`
  so per-member sparseness doesn't silently lose entire trades.
  `ErrInsufficientCoverage` only fires when *all* members miss the
  floor.
- **Fee model** (`FeePerContractCents`, `RoundTripFeeCents`,
  `EdgeAfterFeeCents`, constant `KalshiFeeBPS = 0.07`). Kalshi's
  parabolic taker fee `ceil(7·P·(1−P))` cents per side, peaking at
  P=0.50 and zero at the boundaries. `RoundTripFeeCents` is currently
  equal to one entry fee because Phase 0's hold-to-expiry strategy pays
  no settlement fee; the function exists as a separate entry point so
  cancel-and-quote strategies can swap to a genuine round-trip without
  touching call sites. The Feb 2026 maker fee schedule is **not yet
  modeled** — open item in the v2 floor report.
- **Ticker parser** (`Spec`, `ParseTicker`, `BracketKind`,
  `ErrNotWeatherTicker`). Strict format `SERIES-YYMMMDD-{B|T}KEY`;
  non-weather tickers return `ErrNotWeatherTicker` so the scanner can
  `errors.Is` them away as expected noise rather than aborting. Date
  parser is YY → 2000+YY; uppercase month abbreviations.
- **Bracket resolution** (`Resolution`, `Spec.Resolve`,
  `ResolveWithSiblings`). `B<K.5>` resolves to a 2°F bracket
  `[K−1, K+1)`; `B<K>` integer to `[K, K+1)`; `T<K>` defaults to
  high-side `[K, +Inf)`. `ResolveWithSiblings(spec, siblings)` flips a
  T-bracket to low-side `(−Inf, K)` when its key is below the lowest
  sibling B midpoint, which fixes the silent miscoding that previously
  treated low-side T markets as high-side. The bracket-convention check
  (sum of bracket prices ≈ 100¢) is still tagged FIXME in
  `resolution.go`.
- **Truth index** (`DailyTruth`, `TruthIndex`, `LoadTruth`,
  `stationFromTruthFilename`). Loads `KXYZ.csv` files (output of
  `cmd/calibrate-parse-cli`) and exposes a `(station, civil_date)` →
  `(TMAX_F, TMIN_F)` map. Filenames containing `-hourly` are skipped
  so the ASOS feature data sitting next to truth CSVs doesn't get
  pulled in as ground truth.
- **Scan entrypoint** (`Scan`, `ScanOptions`, `EdgeRow`, `Result`).
  `Scan(opts)` walks `<data>/openmeteo/<date>/` into a station-keyed
  poll index, makes a first pass over `<data>/kalshi/<date>/` to
  collect sibling Specs for T-direction inference (see
  `collectSiblings` / `ResolveWithSiblings`), then a second pass
  pairing each weather trade with the most-recent poll for its station
  (subject to `MaxPollAge`, default 1h). For each paired trade the
  scanner emits four probability columns (see below) plus per-row
  diagnostics. Trades that fail any guard are tallied in
  `Result.SkippedReason` so the summary reports a closed accounting.

#### The four-column probability flow

For every paired trade `Scan` produces all four of:

| Column | Source | Notes |
|---|---|---|
| `PFairNaive` | fraction of ensemble members whose daily extreme falls in the bracket | Always populated. The naive lower-bound estimator. |
| `PFairMOS` | Φ-integral of the calibrated normal `(μ=a+b·f̄, σ)` over the bracket | 0 if MOS not loaded, bin missing, or model-set mismatch |
| `PFairClimate` | climate-shrunk-naive: `w·climate + (1−w)·naive` (w=0.2 by default) | NaN if no climate observations match the season window |
| `PFair` | the column used for edge math | MOS when `MOSApplied`, otherwise `PFairNaive` |

Market-mid is read directly from the recorded trade price (`TradePriceCents`)
and lives in the same row, making the four columns + price + truth a
complete record for downstream Brier/PnL stratification.

`SkippedReason` is a per-row tag explaining why MOS was *not* applied:

- `""` — MOS was applied (`MOSApplied == true`).
- `"no_coeffs"` — `ScanOptions.MOS == nil`.
- `"no_bin"` — coefficients loaded but no fitted bin for
  (station, season, variable).
- `"model_set_mismatch:<obs>vs<fit>"` — the observed poll's model set
  doesn't match `Coefficients.FitModelSet` (e.g. GFS+ECMWF poll against
  GFS-only coefficients). The aggregate `Result.SkippedReason` map
  counts each distinct mismatch key.

This per-row tag is why the scanner never silently disappears a trade:
every fallback path is *visible* in the CSV, and the summary tallies
match the input frame count.

#### Realized-PnL backtest mode

When `ScanOptions.Truth != nil`, every row gets `HasTruth`, `ActualTempF`,
`YesResolved`, `OurSide`, `OurPnLCents` populated. Trades whose
`(station, valid_date)` is missing from the truth index are skipped
with reason `"no_truth_available"` — this is the *no imaginary bets*
rule: we never score a trade whose outcome we can't look up. The
hypothetical strategy enters whichever side has the larger post-fee
edge if it clears `EdgeThresholdCents` (default 5¢). Aggregate counters
`Result.TradesEntered`, `TradesWon`, `TotalPnLCents` are surfaced in
the edge-scan summary. See `docs/BACKTEST.md` for the full pipeline +
column reference.

### `internal/calibration/cliparse`

- Parses NWS CLI bulletin text from IEM AFOS archive zips.
- Contract: `valid_date` from bulletin header (not `YESTERDAY`/`TODAY`
  label). Multiple issuances per civil date dedupe to latest;
  ties break on lexicographically latest filename.
- `MM` temperature value → empty CSV field with gap report on stderr.

### `internal/calibration/mos`

- `Fit(pairs, holdoutDays)` performs closed-form OLS per (station,
  season, variable) bin. Bins below `MinTrainPerBin = 10` are dropped.
- `Predict` applies `μ_calibrated = a + b·forecast`; `σ` comes from
  *holdout* residual stdev when the holdout has at least
  `MinHoldoutForSigma = 30` pairs, otherwise the in-sample training
  stdev is kept and labelled `SigmaSource = "training-fallback"`.
  Training-σ understates predictive uncertainty (the fit minimised the
  same residuals it then measured), so this distinction matters when
  reading downstream tail probabilities.
- `BinCoeffs.SigmaSource` is one of `"holdout"`,
  `"training-fallback"`, or `""` (legacy file → interpret as
  training-fallback for back-compat).
- `FitModelSet` is stamped on the `Coefficients` at fit time
  (`[]string{"gfs"}` for the current deterministic-GFS pipeline,
  per `cmd/calibrate-fit/main.go`). The scanner refuses to apply
  coefficients whose `FitModelSet` doesn't match the observed poll's
  model set, surfaced as `SkippedReason = "model_set_mismatch:..."`.
  `CanonicalModelSet` and `ModelSetEqual` are the helpers.
- `CoefficientsSchemaVersion = "isobar.mos/v1"` gates `LoadCoefficients`;
  a mismatched on-disk schema fails loudly rather than silently
  reading old files into new code.
- `ProbBelow(threshold, μ, σ)` = Φ((threshold − μ) / σ) via `math.Erf`;
  `ProbAbove = 1 − ProbBelow`.
- Math note: this is MOS in the Glahn-Lowry (1972) sense, not full NGR
  with `σ² = c + d·ensvar`. See ADR-0008 for the deterministic-GFS
  scope decision and ADR-0009 for the canary that fired.

### `internal/calibration/climate`

- Empirical historical-frequency baseline. `Load(obsDir, stations)`
  reads `KSTATION.csv` truth files per station; at the end of load,
  `compile()` builds a per-day-of-year sorted `[]float64` of observed
  values from days within `±SeasonWindowDays = 30` (circular calendar
  distance, so Dec/Jan wrap), enabling binary-search lookups.
- `BracketProb(station, variable, target, threshold, bracketAbove)`
  returns the empirical frequency of the bracket condition restricted
  to the ±30-day calendar window around `target`.
- `RangeProb(station, variable, target, lo, hi)` returns the frequency
  inside `[lo, hi)`, supporting open-ended ±Inf thresholds (matches the
  scanner's `Resolution` shape).
- `ShrinkP(naive, climate, w)` = `w·climate + (1−w)·naive`; NaN climate
  falls back to naive. `DefaultShrinkage = 0.2` is the Pass-3-recommended
  weight, treated as a hyperparameter to be tuned post-gate if the
  baseline beats MOS.
- Used as a critique baseline against MOS: if MOS doesn't beat the
  climate-shrunk naive in the tradeable midrange, MOS isn't earning
  its complexity. (Phase 0.5 v2 confirmed this critique — see
  `reports/phase0.5/2026-05-05-floor-estimate-v2.md`.)

### `internal/calibration/bayesian`

Foundation for the T−3h precision-weighted Gaussian posterior update
proposed in ADR-0010. **Not yet wired into the scan path** — integration
is gated on a held-out evaluation harness that ADR-0010 names as a
follow-up, not yet ratified as its own ADR (the original 0011 slot
was reassigned to journal backpressure during the audit).

- `Update(prior, obs, kernel) → Posterior` is the pure math primitive:
  `1/σ²_post = 1/σ²_prior + 1/σ²_obs`,
  `μ_post = σ²_post·(μ_prior/σ²_prior + μ_obs/σ²_obs)` where
  `μ_obs = R(t) + E[Δ]` for `R(t)` the running extremum through trade
  time and `E[Δ], Var[Δ]` loaded from the kernel JSON.
- `Posterior.Method` records which branch produced the result:
  `"bayes-gauss"` (standard path), `"obs-only"` (h_remain == 0),
  `"prior-only"` (kernel missing or prior invalid). The integration
  commit will canary the `prior-only` rate to detect kernel-coverage
  regressions.
- `LoadKernel(path)` validates `KernelSchemaVersion = "isobar.bayesian/v1"`,
  rejects bins below `MinKernelN = 5`, rejects negative `VarDelta`.
- `MinObsVariance = 0.25`°F² floors `σ²_obs` to defend against
  empirically degenerate kernel bins (without it, two coincidental
  samples could collapse the posterior).
- `TerminalSigma = 0.5`°F is the posterior σ at settlement
  (h_remain == 0), reflecting Open-Meteo / NWS CLI source mismatch
  rather than meaningful predictive uncertainty.
- The kernel fitter lives in `cmd/calibrate-bayesian` and is a scaffold
  today (flags wired, fit body pending). Integration into edge-scan
  follows once the held-out evaluation harness (ADR-0010 follow-up)
  shows the update clears its gate.

### `internal/obs`

- `Init(levelStr)` returns a JSON `slog.Logger` writing to stderr and
  also installs it as `slog.Default()` so stdlib-adjacent code is
  coherent.
- Time attribute key is renamed `ts` so log-shipper configs can
  normalize across languages.

## Error-handling contract

- Errors are wrapped with `%w` (`fmt.Errorf("ctx: %w", err)`) so
  `errors.Is`/`errors.As` keep working across package boundaries.
  Enforced by `errorlint` in the linter config.
- `panic` is reserved for programmer bugs. Runtime and I/O failures
  return errors.
- `log/slog` is the only logger. No `fmt.Println` in production code
  paths (test helpers excepted).
- `errgroup` cancels the peer context on first non-nil error — every
  long-lived goroutine must honor `ctx.Done()`.

## Concurrency invariants

- Every goroutine is either spawned inside an `errgroup.Group` *or* owned
  by a single struct with a documented `Close` method that drains it.
- No goroutine reads a shared variable without either a mutex, an atomic,
  or a channel hand-off. Race detector runs on every `make test`.
- Channels either have a documented sender/receiver contract in the
  package docstring or they're considered a bug waiting to happen.

## Future (post-Phase-0.5) shape — only after the gate clears

The Phase 0.5 v2 report (May 2026) found the current MOS calibration
does not clear the gate criterion in the tradeable midrange. The
sequencing of next packages depends on the next-iteration verdict
(see ADR-0009 for the trigger). Tentative future layout, conditional
on a passing gate:

```
internal/
  calibration/
    ngr/               Full NGR with σ² = c + d·ensvar (requires GEFS
                       ensemble historical backfill — ADR-0009)
    conformal/         Empirical-coverage prediction intervals
    bayesian/          T−3h posterior update from observed ASOS
                       trajectory
  signal/              Signal interface + implementations
    weather/           Calibrated-ensemble-vs-bracket strategy
    hurricane/         NHC advisory + HAFS/HWRF + NDBC SST strategy
  nhc/                 NHC ATCF a-deck/b-deck advisory parser
  hurricanemodel/      HAFS/HWRF fetch + NDBC SST feed
  microstructure/      Reservation price, half-spread, OFI, CUSUM
  risk/                Pre-trade caps + kill-switch + Kelly-with-ρ
  exec/                Exec interface + PaperExec + KalshiExec
  state/               SQLite WAL warm-start + reconciliation
```

A `cmd/bot` entrypoint would wire:
`feeds → signal.Tick → risk.Gate → exec.Submit`.

`cmd/tape` stays exactly as today — recording is a research tool, not
a runtime dependency of the bot.

If the gate continues to fail after the methodology improvements above,
the project pivots from "trading bot" to "forecasting research artifact"
per the v2 report's recommendation. None of the future packages would
be built in that scenario.

**ADR-0013 (PR#9 retirement).** An earlier branch carried a
paper-trader / fillability / score / settle / backfill-score lineage
toward this future shape; it was retired from `main` to keep the
hardening audit focused. Recovery paths (re-extract from git history,
or rebuild against the post-audit base) are spelled out in ADR-0013.
None of the retired code is present in current `main`.

**Phase 2 architecture decisions** are proposed in
[audit/02-architecture.md](../audit/02-architecture.md) (pending
per-decision sign-off; reserved ADR slots 0014-0020): error-handling
discipline, schema-migration policy, station-registry consolidation
into `internal/market`, deferral of the `internal/edge` directional
split until ADR-0010 Bayesian wiring, three-role record/calibrate/trade
architecture, CLI binary test discipline, and the `internal/`-only
package-layout commitment. None of these are accepted ADRs today.

## Why it's laid out this way

- **Separation between recording and trading** keeps the trader idempotent.
  If the bot crashes, the tape keeps running; data is never lost.
- **Raw-frame journaling** future-proofs us against Kalshi API bumps. We
  never have to backfill, re-decode, or re-record if the schema changes.
- **Single owner goroutine per file** eliminates an entire class of
  file-locking bugs. The design of `journal.Journal.run` is intentional.
- **Strict `internal/` boundary** means we can refactor liberally without
  breaking external consumers — there are none.
