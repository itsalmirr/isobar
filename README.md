# isobar

Probabilistic-forecaster vs prediction-market research project: calibrated
weather-bracket pricing on Kalshi, with rigorous OOS verification and a
documented failure-to-gate. The codebase ships an end-to-end pipeline
(recorder → calibration → edge-scan) and a series of decision-gate
reports built around it.

The name `isobar` refers to lines of constant atmospheric pressure on
weather maps — the project's edge would come from pricing weather better
than the market does. The current evidence (see Phase 0.5 v2 below) says
we don't.

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

Next-alpha foundation (not yet integrated):
`internal/calibration/bayesian` ships the T−3h precision-weighted
posterior update (ADR-0010). `cmd/calibrate-bayesian` defines the
kernel-fitting CLI; the fit body lands once the held-out evaluation
harness (ADR-0011, proposed) clears its gate.

## Results — Phase 0.5 v2

n=79 tradeable [35,65)¢ rows; bootstrap 95% CIs on Brier deltas
(positive Δ = lower Brier = better):

| Calibrator | Brier vs naive (Δ) | vs market mid (Δ) | Verdict |
|---|---:|---:|---|
| MOS | +0.060 [+0.008, +0.113] | −0.100 [−0.188, −0.013] | calibration is real; loses to market |
| Climate-shrunk-naive | +0.070 [+0.047, +0.094] | −0.090 [−0.173, −0.009] | ties MOS; same loss to market |
| MOS vs climate-shrunk | −0.011 [−0.056, +0.035] | — | statistically **tied** |
| **Canary verdict** | — | — | **[ADR-0009 fired](./docs/decisions/0009-mos-canary-fired-escalate-calibration.md)** |

Source numbers and methodology:
[`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](./reports/phase0.5/2026-05-05-floor-estimate-v2.md).
The honest one-paragraph reading: any calibration of the ensemble beats
the naive estimator significantly, but MOS doesn't earn its complexity
over a 5-line shrinkage estimator, and both calibrators lose to Kalshi
market mid in the tradeable zone. The market is genuinely better-informed
than our static daily forecast, mostly because it absorbs intraday
observation that our prior doesn't see.

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
6. **(Next-alpha foundation) T−3h Bayesian update.**
   `internal/calibration/bayesian` ships the precision-weighted Gaussian
   update math + a schema-versioned kernel loader; `cmd/calibrate-bayesian`
   scaffolds the fit CLI. Integration into Scan is gated on ADR-0011's
   held-out evaluation harness — math first, integration once it
   demonstrably clears the gate. ([ADR-0010](./docs/decisions/0010-t-minus-3h-bayesian-update.md))

## Status — Phase 0.5 first verdict: gate NOT met by current MOS calibration

The latest decision-gate report
([`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](./reports/phase0.5/2026-05-05-floor-estimate-v2.md))
is the current source of truth on whether the project produces tradeable
edge.

Headline (n=299 verifiable bracket outcomes, bootstrap 95% CIs in the
tradeable [35,65)¢ midrange):

- **MOS calibration helps over naive ensemble fractions** (+5.96 pp Brier,
  CI excludes zero) — calibration is real
- **MOS does NOT earn its complexity over a 5-line climate-shrunk-naive
  baseline** (statistically tied) — `0.2·climate + 0.8·naive` does the
  same job for free
- **Both calibrators LOSE to Kalshi market mid in the tradeable zone**
  (-9 to -10 pp Brier, CI excludes zero) — market mid is genuinely better
  informed than our model, most likely because it incorporates intraday
  observed-temperature trajectory the daily forecast does not

The Phase 0.5 gate criterion ("calibrated Brier-skill > market-implied")
is **not met**. The first iteration of the calibration thesis fails. The
report enumerates next steps (T−3h Bayesian update, ECMWF IFS blend,
ensemble NGR via NOAA GEFS backfill — see ADR-0009) before any further
gate evaluation.

The project is currently positioned as a **forecasting-research artifact
with a documented failed-trading-gate**, not as a deployed bot. No
capital has been committed.

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
cmd/calibrate-bayesian      T−3h Bayesian-kernel fitter (ADR-0010
                            foundation; flags wired, fit body pending).
cmd/edge-scan               Joins recorded book state to ensemble polls,
                            applies all probability models (naive, MOS,
                            climate-shrunk, market-mid), emits per-bracket
                            edge with full provenance columns. Also runs
                            the realized-PnL backtest when --truth is set.

internal/calibration/cliparse   NWS CLI bulletin parser (90.1% coverage).
internal/calibration/mos        OLS bias correction + ProbBelow/Above,
                                holdout-σ honesty, FitModelSet guard.
internal/calibration/climate    Historical-frequency lookup with seasonal window
                                + p_shrunk = w·climate + (1-w)·naive.
internal/calibration/bayesian   T−3h precision-weighted Gaussian posterior
                                update (foundation only; not yet wired into Scan).
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
- **T−3h Bayesian update — foundation.** `internal/calibration/bayesian`
  ships the math (`Update`, `LoadKernel`) and schema (`isobar.bayesian/v1`).
  `cmd/calibrate-bayesian` defines the fit-pipeline CLI; the fit body
  is deferred to a follow-up commit. Integration into Scan is gated on
  the held-out evaluation harness proposed in ADR-0011. See ADR-0010
  for the math and design rationale.

**Hot-path principle.** The recorder does not decode WS frames. Bytes
from the socket are wrapped in a thin envelope (timestamps + source tag)
and written to disk. All decoding happens offline in `cmd/analyze` and
`cmd/edge-scan`.

## Reports (current → archived)

The Phase 0.5 evaluation has gone through three reports as methodology
matured. The current one supersedes both predecessors:

- **Current**: [`2026-05-05-floor-estimate-v2.md`](./reports/phase0.5/2026-05-05-floor-estimate-v2.md)
  — Phase 0.5 v2 with stratified Brier, climate baseline, bootstrap CIs.
  **Gate: not met.**
- *Superseded*: [`2026-05-02-pipeline-validation.md`](./reports/phase0.5/2026-05-02-pipeline-validation.md)
  — interim status doc after the original was retracted
- *Retracted*: [`2026-05-02-floor-estimate_RETRACTED.md`](./reports/phase0.5/2026-05-02-floor-estimate_RETRACTED.md)
  — original first-pass report with overstated headline; preserved with
  retraction header for audit trail

See [`reports/INDEX.md`](./reports/INDEX.md) for the chain.

## Architectural decisions

10 Architecture Decision Records (ADRs) under [`docs/decisions/`](./docs/decisions/),
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
./bin/edge-scan --data ./data --start 2026-04-29 --end 2026-05-06 \
                --mos data/calibration/mos-coefficients.json \
                --climate data/calibration/obs

# 7. Realized-PnL backtest (after CLI bulletins have been parsed
#    into data/calibration/obs/KSTATION.csv)
./scripts/run-backtest.sh --tape ./data --start 2026-04-29 --end 2026-05-06
```

Recorded data lands under `./data/<source>/<YYYY-MM-DD>/<source>-<hour>.jsonl[.gz]`.

## Project conventions

- Go 1.26+, `internal/` only (no `pkg/` until cross-binary need)
- stdlib-first: `log/slog`, `errgroup`, `context`. Outside deps: `coder/websocket`, `yaml.v3`, `testify` (test-only)
- `make all` runs lint + vet + race-tested unit tests + build
- Secrets live in `./secrets/` (gitignored) or env vars with `ISOBAR_` prefix
- Recorded data lives in `./data/` (gitignored)

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
