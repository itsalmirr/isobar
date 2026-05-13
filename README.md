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

Six binaries plus three calibration / verification packages:

```
cmd/tape                    Recorder (Kalshi WS + Open-Meteo REST → JSONL).
                            Live on Vultr HF Chicago with rolling
                            ticker re-discovery (4h cycle).
cmd/peek                    Kalshi REST probe for series/market discovery.
cmd/analyze                 Phase-0.5 sanity report over recorded journals.
cmd/calibrate-parse-cli     NWS CLI bulletin zip → daily TMAX/TMIN CSV.
cmd/calibrate-fit           OLS-fit MOS coefficients per (station,
                            season, variable) bin; outputs JSON.
cmd/edge-scan               Joins recorded book state to ensemble polls,
                            applies all probability models (naive, MOS,
                            climate-shrunk, market-mid), emits per-bracket
                            edge with full provenance columns.

internal/calibration/cliparse   NWS CLI bulletin parser (90.1% coverage).
internal/calibration/mos        OLS bias correction + ProbBelow/Above.
internal/calibration/climate    Historical-frequency lookup with seasonal window
                                + p_shrunk = w·climate + (1-w)·naive.
internal/edge                   Offline scan engine, fee model, recorded poll decoder.
internal/market                 Ticker parsing and station/location/TZ registry.
internal/openmeteo              Open-Meteo REST poller for live recording.
internal/replay                 JSONL journal reader + orderbook reconstruction.
internal/journal                Crash-safe rotating JSONL writer.
internal/kalshi                 RSA-signed REST + authenticated WS subscriber
                                with rolling re-discovery.
```

13 Go packages, 0 lint issues, all binaries build via `make build-all`.

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

8 Architecture Decision Records (ADRs) under [`docs/decisions/`](./docs/decisions/),
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
./bin/edge-scan --data ./data --out data/calibration/edge-scan \
                --mos data/calibration/mos-coefficients.json \
                --climate data/calibration/obs
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
- [Roadmap](./docs/ROADMAP.md) — phase-by-phase with explicit gates
- [Operations](./docs/OPERATIONS.md) — runbook and troubleshooting
- [Risk](./docs/RISK.md) — the L0–L4 control hierarchy
- [Testing](./docs/TESTING.md) — rules, coverage floors, CI expectations
- [Glossary](./docs/GLOSSARY.md) — terms, defined once
- [Research](./docs/research/) — 8 agent reports + [SYNTHESIS.md](./docs/research/SYNTHESIS.md)
- [Decisions](./docs/decisions/) — 9 ADRs documenting major design choices
- [Reports](./reports/) — Phase 0.5 evaluation reports (see INDEX)

Running log: [CHANGELOG.md](./CHANGELOG.md).
PR + commit conventions: [CONTRIBUTING.md](./CONTRIBUTING.md).
