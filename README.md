# isobar

Probabilistic-forecaster vs prediction-market research project: calibrated
weather-bracket pricing on Kalshi, with rigorous OOS verification and a
disciplined evidence-gated path to deployment. The codebase ships an
end-to-end pipeline (recorder → calibration → Bayesian update →
selective-entry edge-scan) and a series of decision-gate reports built
around it.

The name `isobar` refers to lines of constant atmospheric pressure on
weather maps — the project's edge would come from pricing weather better
than the market does. After four iterations of evidence:

- **v1 (retracted)** — naive ensemble fraction loses badly
- **v2** — MOS calibration is real but loses to market mid (−10 pp Brier)
- **v3** — MOS + T−3h Bayesian update closes 46% of the gap, still loses
  ($-4,964 over 6 days at notional)
- **v4** — Selective-entry filter (`--entry-filter h4`: exclude KXHIGHNY,
  require low-temp brackets OR extreme-OTM price band) flips the same
  6-day backtest to **$+794 with daily Sharpe 0.68** (5-day LOO CV).
  **First positive-EV finding**, not yet validated on truly OOS data.

## Pipeline

```
   Open-Meteo ensemble  +  Kalshi WS trades + book
            │                       │
            └──────────┬────────────┘
                       ▼
              cmd/tape (recorder)        single-file owner per source,
                       │                 raw frames stamped + journaled
                       ▼
            data/{kalshi,openmeteo}/<date>/*.jsonl[.gz]
                       │
                       ▼
              cmd/edge-scan             walks tape, pairs trades to polls,
                       │                 emits 4 probability columns
                       ▼
          edges-*.csv + summary-*.md     MOS + climate + naive + market
                       │
                       ▼
              realized-PnL backtest     "no imaginary bets" — skip trades
                                         outside NWS CLI truth window
```

Bayesian update (ADR-0010) is now **shipped and integrated**.
`cmd/calibrate-bayesian` fits a per-(station, variable, h_remain)
residual kernel from 21 months of NWS CLI truth + IEM ASOS hourly
obs (184 bins, all bins ≥ 5 paired observations). The scanner reads
the kernel via `--bayesian-kernel` and applies a precision-weighted
posterior update at trade time, using the running extremum from the
station's UTC-stamped hourly feed. Held-out evaluation is built into
the design: the fit window ends 2026-01-31 and the backtest starts
2026-04-29 (89-day gap → no leakage).

## Results — Phase 0.5, four iterations

### Dollar-quantified, held-out backtest (v3 / v4)

134,689 paired Kalshi trades on a 6-day held-out window
(2026-04-29 → 2026-05-04), realized PnL after Kalshi taker fees,
with NWS CLI truth grounding under the *no imaginary bets* rule:

| Strategy | Trades entered | Win rate | Realized PnL | Per-trade |
|---|---:|---:|---:|---:|
| Naive | 106,840 | 35.6% | $-9,198.02 | -8.61¢ |
| + MOS calibration | 105,417 | 34.2% | $-6,113.05 | -5.80¢ |
| + T−3h Bayesian update | 101,600 | 37.2% | $-4,963.86 | -4.89¢ |
| **+ H4 selective entry** | **23,832** | **34.7%** | **$+793.74** | **+3.33¢** |

The progression is real and ordered by evidence:

- MOS over naive: **+$3,085** (calibration matters — v2 Brier delta confirms)
- Bayesian over MOS: **+$1,149** (intraday observation matters — kernel ObsWeight rises 0.09 → 0.72 as h_remain shrinks 23 → 1, exactly as ADR-0010 predicted)
- H4 over no-filter: **+$5,758** (excluding KXHIGHNY + restricting to low-temp brackets / extreme-OTM bands)

Source: [`reports/phase0.5/2026-05-19-v4-h4-filter.md`](./reports/phase0.5/2026-05-19-v4-h4-filter.md)
(supersedes [v3](./reports/phase0.5/2026-05-19-v3-bayesian-verdict.md),
which supersedes [v2](./reports/phase0.5/2026-05-05-floor-estimate-v2.md)).

**H4 is a candidate strategy, not yet proven.** Leave-one-day-out CV on
5 days gives daily Sharpe 0.68 with 3 of 5 days positive — promising but
not statistically distinguishable from chance at n=5 (95% CI on Sharpe ≈
[−1.2, +2.6]). The repo deliberately gates further deployment on
≥10 forward trading days of truly out-of-sample data.

### v2 verdict (Brier units; preserved for continuity)

n=79 tradeable [35,65)¢ rows; bootstrap 95% CIs on Brier deltas:

| Calibrator | Brier vs naive (Δ) | vs market mid (Δ) | Verdict |
|---|---:|---:|---|
| MOS | +0.060 [+0.008, +0.113] | −0.100 [−0.188, −0.013] | calibration is real; loses to market |
| Climate-shrunk-naive | +0.070 [+0.047, +0.094] | −0.090 [−0.173, −0.009] | ties MOS; same loss to market |
| MOS vs climate-shrunk | −0.011 [−0.056, +0.035] | — | statistically **tied** |

Source: [`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](./reports/phase0.5/2026-05-05-floor-estimate-v2.md).
The market is genuinely better-informed than our static daily forecast,
mostly because it absorbs intraday observation that our prior doesn't
see. The T−3h Bayesian update (v3) addressed this directly; the H4 filter
(v4) addressed *which markets* the calibration is best on.

## What I built and why

Five engineering bets driving the current state of the code:

1. **MOS calibration with the right σ.** Per-bin OLS on
   `(station, season, variable)` with holdout-σ when the holdout has
   ≥30 pairs and a labelled `training-fallback` otherwise. Coefficients
   are stamped with `FitModelSet`; the scanner refuses to apply
   GFS-only-trained MOS to a GFS+ECMWF poll. Schema-versioned
   (`isobar.mos/v1`) so future loaders can refuse to silently
   re-interpret. ([`internal/calibration/mos`](./internal/calibration/mos/),
   [docs/CALIBRATION.md](./docs/CALIBRATION.md))
2. **Climate baseline as a critique.** A 5-line shrinkage estimator
   `p = 0.2·climate + 0.8·naive` ships as a separate probability column
   so the calibration story can be tested against the simplest possible
   alternative. It ties MOS in the tradeable midrange — the load-bearing
   evidence behind ADR-0009. Implementation uses a per-day-of-year
   pre-sorted slice for O(log N) binary-search lookup over a ±30-day
   seasonal window. ([`internal/calibration/climate`](./internal/calibration/climate/))
3. **Multi-model ensemble decoder (GFS + ECMWF).** Open-Meteo's
   `models=` parameter lets one poll carry two ensembles; the parser in
   `internal/edge.ParsePoll` tags each member with its source
   (`gfs:NN` / `ecmwf:NN`) and the scanner derives the observed model
   set to gate MOS application. Parse + guard are shipped; the
   recorder stays GFS-only by default until a multi-model fit is
   available. ([`internal/edge/openmeteo.go`](./internal/edge/openmeteo.go))
4. **Realized-PnL backtest with truth-file coverage.** `edge-scan
   --truth <dir>` loads NWS CLI ground-truth CSVs and scores every
   paired trade. Trades with no truth row are skipped under the
   *no imaginary bets* rule — the aggregate `no_truth_available`
   counter is the operator's first audit hook. `scripts/run-backtest.sh`
   is the end-to-end wrapper. ([`docs/BACKTEST.md`](./docs/BACKTEST.md))
5. **Decision-gate methodology.** Every iteration ships a written
   report under `reports/` with stratified Brier scores and bootstrap
   95% CIs, *before* any capital commitment. ADR-0009 fired on the
   first such report; the project is currently a forecasting-research
   artifact with a documented failed-trading-gate, not a deployed bot.
   This pattern is the most important thing in the repo.
6. **T−3h Bayesian update — math + fit + integration.**
   `internal/calibration/bayesian` ships the precision-weighted Gaussian
   update math, a schema-versioned kernel loader, and a station-keyed
   hourly-obs index that computes R(t) and h_remain at scan time.
   `cmd/calibrate-bayesian` is the fitter end-to-end (Welford online
   stats, civil-day reconstruction in station-local TZ with proper DST
   handling, 22 unit tests). `cmd/edge-scan --bayesian-kernel ...
   --hourly-obs ...` performs the posterior update inside the entry
   decision and writes `p_fair_bayes`, `bayes_obs_weight`, and
   `bayes_h_remain` as auditable columns. Held-out evaluation is built
   into the design (89-day gap between fit-end and backtest-start →
   zero leakage). ([ADR-0010](./docs/decisions/0010-t-minus-3h-bayesian-update.md),
   [v3 report](./reports/phase0.5/2026-05-19-v3-bayesian-verdict.md))

7. **H4 selective-entry filter — first positive-EV finding.**
   `--entry-filter h4` on `edge-scan` restricts entries to the trades
   where the calibration has measurable edge: not KXHIGHNY, AND either
   a low-temp bracket OR a deep-OTM price band. Discovered via
   subset-mining (`scripts/mine_subsets.py`), validated via
   leave-one-day-out CV (`scripts/loo_cv.py`), each condition has a
   physical hypothesis (NYC weather variability + GFS bias, easier
   TMIN prediction physics, favorites-longshot bias in extreme bands).
   First +EV result on real Kalshi tape with NWS CLI truth.
   ([v4 report](./reports/phase0.5/2026-05-19-v4-h4-filter.md))

## Status — Phase 0.5 v4: positive-EV candidate, forward-validation gated

The latest decision-gate report is
[`reports/phase0.5/2026-05-19-v4-h4-filter.md`](./reports/phase0.5/2026-05-19-v4-h4-filter.md).
It supersedes v3 (which superseded v2, which superseded the retracted
v1). The chain of evidence:

| Iter | Strategy | 6-day PnL | Verdict |
|---|---|---:|---|
| v1 | Naive | $-9,198 | retracted |
| v2 | MOS calibration | $-6,113 | gate not met (Brier units) |
| v3 | MOS + Bayesian | $-4,964 | gate not met (dollars) |
| **v4** | **MOS + Bayesian + H4 filter** | **$+794** | **+EV candidate** |

The v4 strategy `--entry-filter h4` shows positive expected value on
the only held-out window we have (5 days, daily Sharpe 0.68, 3/5 positive
days). The verdict is **not** yet "deploy money":

- 5 days of leave-one-day-out CV is a hypothesis test, not a proof.
- The H4 conditions were partly discovered by inspecting the same
  window's profitable subsets (data-mining risk).
- The recorder is running fresh tape since 2026-05-19 to produce
  truly out-of-sample data. Re-evaluation requires ≥10 forward days
  of tape + NWS CLI truth (24h truth latency).

**v5 gate criteria** (proposed): ≥10 forward days, n ≥ 25,000 H4
entries, aggregate PnL > 0, daily Sharpe ≥ 0.5, positive_days ≥ 60%,
max-single-day drawdown < positive-day PnL. If v5 passes, Phase 1
paper bot unblocks. If v5 fails, ADR-0014 documents the H4 finding
as a 6-day artifact and the project reverts to forecasting-research
framing.

**No capital has been committed.** `KalshiExec` (live order
placement) does not exist in the repo by design — see
[`audit/live-allow-trace.md`](./audit/live-allow-trace.md).

## What's shipped

Seven binaries plus calibration / verification / scanner packages:

```
cmd/tape                    Recorder (Kalshi WS + Open-Meteo REST → JSONL).
                            Live on Vultr HF Chicago with rolling
                            ticker re-discovery (4h cycle).
cmd/peek                    Kalshi REST probe for series/market discovery.
cmd/analyze                 Phase-0.5 sanity report over recorded journals.
cmd/calibrate-parse-cli     NWS CLI bulletin zip → daily TMAX/TMIN CSV.
cmd/calibrate-fit           OLS-fit MOS coefficients per (station,
                            season, variable) bin; outputs JSON.
cmd/calibrate-bayesian      T−3h Bayesian-kernel fitter (ADR-0010,
                            v3-shipped). Fits per-(station,variable,
                            h_remain) residual moments via Welford
                            online stats over 21 months of NWS CLI
                            truth + IEM ASOS hourly. Emits a
                            schema-versioned Kernel JSON.
cmd/edge-scan               Joins recorded book state to ensemble polls,
                            applies all probability models (naive, MOS,
                            climate-shrunk, Bayesian-updated). Supports
                            --bayesian-kernel + --hourly-obs for the
                            posterior update path, --entry-filter h4
                            for the v4 selective-entry strategy, and
                            --truth for realized-PnL backtest mode.

internal/calibration/cliparse   NWS CLI bulletin parser (90.1% coverage).
internal/calibration/mos        OLS bias correction + ProbBelow/Above,
                                holdout-σ honesty, FitModelSet guard.
internal/calibration/climate    Historical-frequency lookup with seasonal window
                                + p_shrunk = w·climate + (1-w)·naive.
internal/calibration/bayesian   T−3h precision-weighted Gaussian posterior
                                update + station-keyed HourlyIndex with
                                R(t) lookup and DST-aware civil-day
                                reconstruction. Schema-versioned kernel
                                loader (isobar.bayesian/v1). Integrated
                                into internal/edge.Scan via ScanOptions.
                                {BayesianKernel,HourlyObs}.
internal/edge                   Offline scan engine: ensemble decoder, fee model,
                                ticker parser, bracket resolution (with
                                sibling-aware T-direction), truth index, Scan.
internal/market                 Station/series/TZ registry. No fee model
                                here anymore — it moved to internal/edge.
internal/openmeteo              Live Open-Meteo REST poller. Multi-model via
                                Options.Model = "gfs_seamless,ecmwf_ifs025".
internal/replay                 JSONL journal reader + orderbook reconstruction.
internal/journal                Crash-safe rotating JSONL writer.
internal/kalshi                 RSA-signed REST + authenticated WS subscriber
                                with rolling re-discovery.
```

15 internal Go packages, 0 lint issues, all binaries build via `make build-all`.

**What landed in this iteration (Lane A + Lane C).**

- **Multi-model ensemble decoder (GFS + ECMWF).** The poll parser in
  `internal/edge.ParsePoll` recognises Open-Meteo's multi-model field
  names and tags each member with its source model
  (`gfs:NN` / `ecmwf:NN`). The scanner's `pollModelSet` derives the
  observed model set and compares it against `Coefficients.FitModelSet`,
  refusing to apply GFS-only-trained MOS to a GFS+ECMWF poll. Parse +
  guard are shipped; the recorder is still GFS-only by default — flip
  by setting `openmeteo.model` in `config.yaml`.
- **Realized-PnL backtest mode.** `cmd/edge-scan --truth <dir>` loads
  `KSTATION.csv` truth files and scores every paired trade against
  realized TMAX/TMIN. Trades without truth coverage are skipped (the
  *no imaginary bets* rule). `scripts/run-backtest.sh` is the end-to-end
  wrapper (fetch CLI bulletins → parse → run scan with `--truth`). See
  `docs/BACKTEST.md` for the column reference.
- **MOS calibration with the right σ.** `internal/calibration/mos`
  now sources σ from holdout residuals when the holdout has
  ≥`MinHoldoutForSigma` (30) pairs, falling back to training-σ
  *with* a `SigmaSource` field labelling the fallback. Coefficients
  are stamped with `FitModelSet` so the scanner can refuse mismatched
  model sets at scan-time (`SkippedReason = "model_set_mismatch:..."`).
- **Climate-shrunk-naive baseline.** Five-line shrinkage estimator
  `p = 0.2·climate + 0.8·naive` shipped as a separate probability
  column (`--climate <obs-dir>` on `edge-scan`) to test whether MOS
  earns its complexity. The v2 floor report says it doesn't — see
  ADR-0009.
- **T−3h Bayesian update — shipped end-to-end.** Math layer in
  `internal/calibration/bayesian` (`Update`, `LoadKernel`,
  `LoadHourlyIndex`, schema `isobar.bayesian/v1`); fitter body in
  `cmd/calibrate-bayesian` (Welford online stats, civil-day reconstruction
  with proper DST, 22 unit tests covering tmax/tmin trajectories,
  cold-start, DST fall-back); scanner integration via `ScanOptions.
  BayesianKernel` + `ScanOptions.HourlyObs` with 6 new `bayes_*`
  CSV columns. Held-out evaluation built into the design: kernel
  fit window ends 2026-01-31, backtest window starts 2026-04-29
  (89-day gap, zero leakage). See ADR-0010 + v3 report.
- **Selective-entry filter (H4) — first positive-EV finding.**
  `--entry-filter h4` on `edge-scan` (predicate in
  `internal/edge.entryFilterAdmits`). Excludes KXHIGHNY, requires
  KXLOWT* or extreme-OTM price band. v4 report has the subset-mining
  + LOO CV evidence.

**Hot-path principle.** The recorder does not decode WS frames. Bytes
from the socket are wrapped in a thin envelope (timestamps + source tag)
and written to disk. All decoding happens offline in `cmd/analyze` and
`cmd/edge-scan`.

## Reports (current → archived)

Four iterations of the Phase 0.5 evaluation, each superseding the prior:

- **Current — v4**: [`2026-05-19-v4-h4-filter.md`](./reports/phase0.5/2026-05-19-v4-h4-filter.md)
  — H4 selective-entry filter shows +$794 on the 6-day backtest, daily
  Sharpe 0.68. **First positive-EV result; forward-validation gated.**
- *Superseded — v3*: [`2026-05-19-v3-bayesian-verdict.md`](./reports/phase0.5/2026-05-19-v3-bayesian-verdict.md)
  — MOS + Bayesian closes 46% of the loss gap, still −$4,964 over 6 days
- *Superseded — v2*: [`2026-05-05-floor-estimate-v2.md`](./reports/phase0.5/2026-05-05-floor-estimate-v2.md)
  — MOS Brier-skill measurement; gate not met
- *Superseded — interim*: [`2026-05-02-pipeline-validation.md`](./reports/phase0.5/2026-05-02-pipeline-validation.md)
  — interim status after v1 retraction
- *Retracted — v1*: [`2026-05-02-floor-estimate_RETRACTED.md`](./reports/phase0.5/2026-05-02-floor-estimate_RETRACTED.md)
  — original first-pass report with overstated headline; preserved with
  retraction header for audit trail

See [`reports/INDEX.md`](./reports/INDEX.md) for the chain.

## Architectural decisions

13 Architecture Decision Records (ADRs) under [`docs/decisions/`](./docs/decisions/),
in dependency order:

| ADR | Decision |
|---|---|
| 0001 | Pivot strategy after 8-agent research wave (kill naive thesis) |
| 0002 | Kill `btc-hourly`, delete `internal/coinbase` |
| 0003 | Dashboard = stdlib Go + htmx + SSE (deferred) |
| 0004 | Two-signal scope: weather-calibrated + hurricane-landfall |
| 0005 | Extract `internal/retry`; promote `DiscoverOpenTickers` |
| 0006 | `cmd/peek` as ongoing Kalshi catalog probe |
| 0007 | NWS CLI bulletins as calibration y-target |
| 0008 | Deterministic GFS for first-pass MOS (canary now fired — see ADR-0009) |
| 0009 | Trigger ensemble-spread NGR / ECMWF blend after MOS canary |
| 0010 | T−3h Bayesian update of MOS prior via intraday observations (proposed) |
| 0011 | Journal backpressure: drop with counter, sampled debug log, alarm hook |
| 0012 | Time-source discipline: Clock interface, now-func fields, custom lint analyzer |
| 0013 | Retire PR#9 paper-trader/scoring lineage from `main`; defer until cmd/bot unblock |

Phase 2 structural decisions are proposed in
[`audit/02-architecture.md`](./audit/02-architecture.md) (reserved ADR
slots 0014-0020, pending per-decision sign-off).

## Quickstart

```bash
# 1. Go 1.26+ and (optionally) golangci-lint
# 2. Get a Kalshi API key + RSA private key (PEM)
mkdir -p secrets && chmod 700 secrets
cp /path/to/kalshi.pem ./secrets/kalshi.pem
chmod 600 ./secrets/kalshi.pem

# 3. Configure
cp config/config.example.yaml config/config.yaml
cp .env.example .env
# edit .env with KALSHI_API_KEY_ID

# 4. Build everything
make build-all

# 5. Run the recorder (live capture)
set -a && source .env && set +a
./bin/tape -config config/config.yaml

# 6. Calibration pipeline (offline, on accumulated tape)
./bin/calibrate-parse-cli --zip data/calibration/cli/KNYC.zip \
                         --start 2024-04-29 --end 2026-05-04
./bin/calibrate-fit --out data/calibration/mos-coefficients.json --holdout-days 90

# 7. Bayesian kernel fit (T−3h residual moments per station/variable/h_remain)
./bin/calibrate-bayesian \
    --obs data/calibration/obs \
    --truth data/calibration/obs \
    --out data/calibration/bayesian-kernel.json \
    --fit-start 2024-04-29 --fit-end 2026-01-31

# 8. Full backtest with all four calibration layers + H4 selective entry
./bin/edge-scan --data ./data --start 2026-04-29 --end 2026-05-04 \
                --mos data/calibration/mos-coefficients.json \
                --climate data/calibration/obs \
                --truth data/calibration/obs \
                --bayesian-kernel data/calibration/bayesian-kernel.json \
                --hourly-obs data/calibration/obs \
                --entry-filter h4
# → reports/.../summary-*.md ; current best PnL: +$793.74 on 6 days
```

Recorded data lands under `./data/<source>/<YYYY-MM-DD>/<source>-<hour>.jsonl[.gz]`.

## Project conventions

- Go 1.26+, `internal/` only (no `pkg/` until cross-binary need)
- stdlib-first: `log/slog`, `errgroup`, `context`. Outside deps: `coder/websocket`, `yaml.v3`, `testify` (test-only)
- `make all` runs lint + vet + race-tested unit tests + build
- `make lint` runs both `golangci-lint` and the custom `clockcheck`
  analyzer (`tools/clockcheck/`) per ADR-0012 — direct `time.Now` /
  `Since` / `Until` outside the allowlist fails the build
- `make install-hooks` enables the pre-commit tripwire (refuses commits with gitignored-but-tracked files)
- Secrets live in `./secrets/` (gitignored) or env vars with `ISOBAR_` prefix
- Recorded data lives in `./data/` (gitignored); journal-drop alarms
  (ADR-0011) land in `./data/alarms/<YYYY-MM-DD>.jsonl`

## Documentation map

- [Strategy](./docs/STRATEGY.md) — edge thesis and execution doctrine (note: needs v3 update post-Phase 0.5 v2)
- [Architecture](./docs/ARCHITECTURE.md) — package layout and invariants
- [Calibration](./docs/CALIBRATION.md) — MOS math, climate baseline, shrinkage, σ-source honesty
- [Backtest](./docs/BACKTEST.md) — realized-PnL backtest pipeline + CSV column reference
- [Roadmap](./docs/ROADMAP.md) — phase-by-phase with explicit gates
- [Operations](./docs/OPERATIONS.md) — runbook and troubleshooting
- [Risk](./docs/RISK.md) — the L0–L4 control hierarchy
- [Testing](./docs/TESTING.md) — rules, coverage floors, CI expectations
- [Glossary](./docs/GLOSSARY.md) — terms, defined once
- [Research](./docs/research/) — 8 agent reports + [SYNTHESIS.md](./docs/research/SYNTHESIS.md)
- [Decisions](./docs/decisions/) — 10 ADRs documenting major design choices
- [Reports](./reports/) — Phase 0.5 evaluation reports (see INDEX)

Running log: [CHANGELOG.md](./CHANGELOG.md).
PR + commit conventions: [CONTRIBUTING.md](./CONTRIBUTING.md).
