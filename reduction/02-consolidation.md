# reduction/02-consolidation.md — Phase 2 consolidation + complexity

**HEAD**: `144c08f` + uncommitted working tree (stacked on the Phase 1/1b sweep — see `01-deadcode.md`)
**Date**: 2026-07-03
**Scope**: Phase 2 per `00-baseline.md` protocol — judgment beyond deletion: duplication
consolidation, decomposition of the top-complexity functions, resolution of the five
Phase-1b OPERATOR-CALLs. No abstraction without ≥2 live callers.
**Methodology**: 3 parallel read-only survey agents (calibration / edge+CLI / data-plane)
produced ranked findings; 2 write agents executed the calibration and infra tracks while
the main session did `internal/edge` + `cmd/edge-scan`; gate = targeted build/vet/race
tests per group, `make all` green on the integrated tree, `make deadcode` diff == Phase-1
kept table.

## Headline

| Metric | Post-Phase-1b | Post-Phase-2 | Δ |
|---|---:|---:|---:|
| Production LOC (excl. tests) | 11,198 | **11,194** | −4 net (−145 dedup/deletion, +141 decomposition/validation) |
| Test LOC | 10,279 | **10,529** | +250 (2 new test files; CLI reporting now tested at all) |
| Worst gocyclo (was `edge.Scan` 47) | 47 | **35** (`kalshi.doRaw` / `journal.run`, untouched by design) | scan path 47→17 |
| `cmd/edge-scan` complexity | writeSummary 24 / main 20 | sections ≤ 9 / main 15 | |
| `tradeMsg.priceDollars` | 22, zero direct tests | ≤ 9, 17-case contract test | |
| `make all` | green | green | hold ✓ |
| `make deadcode` | == kept table | == kept table (26 symbols) | hold ✓ |
| Direct deps | 5 + 2 tool | 5 + 2 tool | hold |

Flat net LOC is the expected Phase-2 shape: the deletions (−54 unread ABI/registry
fields, −65 calibration boilerplate, −13 truth loader, −13 CLI dedup) paid for the
decomposition overhead (named helpers + doc comments + explicit validation errors),
and the complexity/duplication wins are the point of this phase, not the line count.

## Track A — `internal/edge` + `cmd/edge-scan` (main session)

### Decomposition

- `edge.Scan`'s 235-line frame closure → `scanner` struct with `processFrame` (decode/
  filter, cyc 6) + `processTrade` (pipeline, cyc 17). `Scan` itself is now bootstrap
  only (cyc 14, was 47). State that was closure-captured is now explicit fields.
- `naiveEnsembleStats` extracted (the member loop feeding both naive p_fair and the
  MOS ensemble mean).
- `Spec.Variable()` added; the three inline `"tmax"/"tmin"` branches collapse onto it.
- `tradeMsg.priceDollars` (cyc 22, previously covered only incidentally by e2e tests)
  → `priceFromCentsFields` + `parseDollarFraction`; new `poll_index_test.go` pins all
  three wire shapes (v2 int cents, recorded-WS dollar strings, legacy `price_dollars`),
  the fallback order, and the (0,1)-exclusive validity band. 17 cases.
- `sync.Map` model-set-mismatch log throttle → plain `map[string]struct{}` on the
  scanner (the frame walk is strictly sequential; audit/01b had flagged the same).

### Entry-filter registry

`entryFilterAdmits` (switch) + `isKnownEntryFilter` (parallel switch that had to be
kept in sync by hand) → single `entryFilters` map. Admit logic, the known-name check,
and the boot-warning text all read the one registry; a new filter is one map entry.

### `cmd/edge-scan` (was: zero tests, duplicated reporting)

- stdout report and markdown summary had a copy-pasted counts block that had already
  drifted; both now call one `writeCounts` (prefix-parameterized), and a test asserts
  line-for-line parity between the two renderings.
- **Bug fixed** (audit/01-line-audit.md:846): "Top brackets" section hardcoded `≥ 5¢`
  and ignored `-edge-threshold-cents`. `edge.Result` now echoes `EdgeThresholdCents`
  (defaulted value, alongside the existing FeeMode/EntryFilterName echoes) and both
  the section filter and its heading use it.
- `writeSummary` (cyc 24) → per-section writers (`writeRunConfig`, `writeBacktestSection`,
  `writeDailyPnL`, `writeSkippedReasons`, `writeEdgeHistogram`, `writeTopBrackets`), each ≤ 9.
- `main` (cyc 20 → 15): model-artifact loading (truth/MOS/climate/kernel/hourly-obs)
  extracted to `loadModelInputs`; the `-bayesian-kernel requires -hourly-obs` check
  moved up to flag validation (fail-fast, same exit 2).
- First CLI tests: `cmd/edge-scan/main_test.go` (counts parity, threshold echo,
  backtest totals incl. daily table, no-entries branch). Closes part of the
  audit/02 §cmd-test-discipline gap.

## Track B — calibration pipeline (write agent 1)

- **`obscsv.ReadFile(path, opts) ([]Row, error)`** added; the open→NewReader→Next-loop→
  close boilerplate deleted from all four daily-CSV callers (climate `Lookup.Load`,
  calibrate-bayesian `loadTruth`, calibrate-fit `readObsCSV`, edge `loadTruthFile` —
  the last retrofitted by the main session). Alias set / int-vs-float / nil-if-empty
  policies stay in the callers.
- **calibrate-fit forecast CSV** (`readForecastCSV` hand-rolled header/row loop) now
  reads through obscsv with a caller-owned alias set (`gfs_tmax_f`/`gfs_tmin_f` →
  canonical Tmax/Tmin). The only obscsv change: missing-column errors report the
  caller-facing spelling. This orphaned `RequireColumns` + `MaxIndex` — both deleted
  (−45 within obscsv).
- Duplicate `LoadHourlyCSV` round-trip test deleted from `fit_test.go` (its two extra
  value assertions folded into the surviving `bayesian/hourly_test.go` copy first).
- `bayesian.canonicalStations` var deleted — `LoadHourlyIndex` iterates
  `market.AllStationCodes()` (one registry, one edit point).
- climate `BracketProb`/`RangeProb` shared station/variable/season-window preamble →
  private `Lookup.seasonWindow`.
- Per-file: fit.go −15, calibrate-fit/main.go −28, climate.go −18, hourly.go −6,
  obscsv.go +2 (ReadFile added, dead helpers removed). Prod −65.
- Behavior notes: obscsv error texts unchanged (test-pinned substrings hold); the
  forecast reader inherits obscsv tolerances (case-insensitive headers, trimmed cells,
  trailing columns ignored); open-failure wrap prefixes lost one duplicated `open <path>`.

## Track C — data plane / infra (write agent 2)

- **`kalshi.LoadPEM(inline, path)`** in auth.go replaces copy-pasted inline-vs-file key
  loading in `cmd/tape` + `cmd/peek` (log/fatal policy stays in callers; 3 new tests).
  Log label format: `inline` / `file:<path>`.
- **`cmd/tape` `writeFrame` helper** — boot marker, WS handler, and OnFetch all build
  `journal.Frame{Stamp: clock.Now(), …}` through one function (ADR-0012 uniformity).
- **OpenMeteo defaults single-sourced in `internal/config`**: `NewPoller` no longer
  silently re-defaults `Model`/`ForecastDays` and no longer duplicates the 1m
  rate-limit floor (keeps only a mechanical `> 0` ticker guard); it errors explicitly.
  Identical behavior for any config that passed `Validate`; an explicit `model: ""`
  now fails fast instead of booting on a silent default.
- **Bug fixed (live recorder, found while touching the poller)**: a client-side
  `http.Client.Timeout` satisfies `errors.Is(err, context.DeadlineExceeded)` even
  while the parent ctx is live — the old checks classified it as shutdown, so one
  slow location aborted the whole fetch cycle unretried and uncounted. Both sites now
  gate on `ctx.Err() != nil`. Regression test `TestPoller_ClientTimeoutIsRetried`
  (slow NY + healthy LA: NY retried the full envelope, LA still fetched, counter bumped).
- **Hardened (tape shutdown ordering)**: `startKalshi` ran `runRediscovery` on the
  parent errgroup, so it could outlive the function's deferred `jrn.Close()` — benign
  today (rediscovery doesn't write frames) but a Write-after-Close race the moment it
  does. Both the rediscovery loop and `sub.Run` now live in a child errgroup;
  `startKalshi` returns `gk.Wait()`, keeping Close strictly after both exit.

### Phase-1b OPERATOR-CALL resolutions (this user request = the operator call)

| Item | Resolution |
|---|---|
| `kalshi.Market` unread decode fields | **16 deleted** (EventTicker, Title, Subtitle, LiquidityDollars, PreviousYesBid/Ask, PreviousPrice, NotionalValue, Result, ExpirationTime, ClosingTime, CanCloseEarly, ExpectedExpirationTime, MarketType, TickSize, ResponsePriceUnits). **10 kept**: Ticker + Status (live readers) + the 8 accessor-backed price/volume fields (TestMarketAccessors). |
| `market.Station.{UTCOffsetHours, City, HighSeries, LowSeries, CLIPil}` | **All 5 deleted** from struct + 4 registry literals. Zero production readers; the one test read was a round-trip of the deleted City. `scripts/calibration-fetch-cli.sh` derives PILs itself. |
| `config.OpenMeteoLocation.Series` | **Deleted** (struct + example yaml + 2 test fixtures). Zero readers incl. scripts/. |
| `replay.KalshiFrame.ID` | **Deleted.** ADR-0015 reserves Seq/Sid only; ID is Kalshi's command/response correlation id, not market-data sequencing. Decode-side deletion — old tapes unaffected. Seq/Sid got an explicit ADR-0015 reservation comment. |
| `bayesian.Posterior.PriorWeight` | **Kept** — ADR-0014-frozen package, diagnostic value; freeze-don't-delete. |

Consequence: the tape boot frame's embedded `stations` entries slimmed to
`{Code, Location, Lat, Lon, TZ}` → **`bootSchemaVersion` bumped to `isobar-tape.boot/v2`**
per the in-code "bump when fields are removed" convention (writer-only string today;
nothing matches on it programmatically). OPERATIONS.md payload example updated.

## Skipped with reason (standing answers for the next pass)

| Candidate | Why skipped |
|---|---|
| `jsonatomic` read helper (LoadKernel/LoadCoefficients skeleton) | Error texts diverge in every line and are test-pinned; helper (~15 LOC) > code removed (~12). Net-positive LOC. |
| universe ↔ kalshi HTTP client merge | ~75 LOC structural overlap but different throttle/retry/clock policies; net ~20-30 LOC for medium-high risk on the live-recorder client. Revisit when ADR-0014 wires a universe cmd. |
| `kalshi.doRaw` → `internal/retry` | cyc 35 but the 429 adaptive throttle is intertwined; 468 test-LOC client is money-path. Needs its own design pass. |
| sonic → stdlib swap | 7-module dep-graph lever, but the 3-5× claim needs the Phase-5 bench + ADR first (`00-baseline.md` §4). |
| Shared `internal/obs.Counter` for ADR-0011 accessors | Net LOC loss; named package-scope accessors are the documented pattern. |
| Fold `collectSiblings` into the main pass | Two-pass is load-bearing: ResolveWithSiblings needs the complete (series, valid_date) bucket before scoring any trade in it. |
| `cmd/analyze` hist helpers → internal/ | No second consumer yet. |
| Civil-day + running-extremum shared core (fit.go vs hourly.go) | Highest-value remaining item, but kernel-JSON/scan semantics must stay bit-identical — needs cross-package golden tests first. Includes the `openmeteo.go` `Add(24h)`-vs-`AddDate` DST discrepancy on 25h days. Queued for Phase 2b. |

## Gates

- `make all` (lint + clockcheck + vet + race + build): **green** on the integrated tree.
- `golangci-lint` targeted runs during the phase: 0 issues (one govet shadow caught and fixed).
- `make deadcode`: output == `01-deadcode.md` kept table exactly (26 symbols, no new orphans).
- Test suite grew +250 LOC / 2 new test files; every pre-existing test unmodified except
  where its subject moved (documented above).

## End of Phase 2
