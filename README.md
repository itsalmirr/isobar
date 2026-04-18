# isobar

Automated Kalshi trading bot — **calibrated weather brackets + hurricane
landfall**. Phase 0: dual-source recorder (`cmd/tape`) for empirical
edge measurement before strategy commitment.

The name `isobar` refers to lines of constant atmospheric pressure on
weather maps — the bot's edge comes from pricing weather better than the
market does.

## Status — Phase 0: data collection

The bot records synchronized market data from two sources (Kalshi WS +
Open-Meteo REST) for ≥48h. Phase 0.5 produces a written
**decision-gate report**: do tradeable edges survive on **calibrated**
probabilities after Kalshi's parabolic fee schedule?

**Research verdict (April 2026):** the naive `p_fair = members/31`
thesis would lose money without NGR/EMOS calibration, a fee-aware edge
floor, and a Lyapunov 8-day horizon gate. The current strategy is being
rebuilt around these findings. See `docs/research/SYNTHESIS.md`.

**BTC-hourly was killed.** Coinbase integrated Kalshi Jan 2026,
compressing lead-lag below our latency floor. `internal/coinbase` is
removed.

Decision gate (must pass before Phase 1 build):
`>10 events/day with >5% calibrated edge after fees` on at least one
signal, AND calibrated Brier-skill beats market-implied on OOS 90-day
held-out data.

## Architecture

```
cmd/tape          Orchestrator. One goroutine per source, errgroup-managed,
                  signal-cancellable. Each source writes to its own journal.

internal/
  clock           Paired (monotonic, wall) timestamps. Wall is the cross-source
                  sync axis; monotonic is for in-process latency diagnostics.
  config          YAML defaults + env overlay (ISOBAR_*) + validation.
  env             Minimal .env loader (no external deps).
  journal         Crash-safe rotating JSONL writer. Batched fsync for
                  throughput; periodic flush bounds data loss to N ms.
  obs             log/slog setup. JSON output to stderr, level via env.
  kalshi          RSA-signed auth, REST client, authenticated WS subscriber
                  (orderbook_delta / ticker_v2 / trade).
  openmeteo       NOAA GFS + ECMWF IFS ensemble REST poller (free tier).
```

**Hot-path principle.** The recorder does not decode WS frames. Bytes from the
socket are wrapped in a thin envelope (timestamps + source tag) and written to
disk. All decoding happens in the analyzer. This minimizes the producer hot
path and future-proofs against schema changes.

**Reused from `arbhunter`.** The Kalshi REST client (`auth.go`,
`client.go`, `types.go`, `client_test.go`) is copied verbatim —
battle-tested code, ~840 LOC, zero churn. The BTC lead-lag strategy
from arbhunter is *not* reused: Coinbase integrated Kalshi in Jan
2026 and that alpha is gone at REST-from-cloud latency.

## Quickstart

```bash
# 1. Go 1.26+ and (optionally) golangci-lint
# 2. Get a Kalshi API key + RSA private key (PEM), drop in ./secrets/
mkdir -p secrets
cp /path/to/kalshi.pem ./secrets/kalshi.pem

# 3. Configure
cp config/config.example.yaml config/config.yaml
cp .env.example .env
# edit .env with your KALSHI_API_KEY_ID

# 4. Build & run
make build
set -a && source .env && set +a
./bin/tape -config config/config.yaml
```

Recorded data lands under `./data/<source>/<YYYY-MM-DD>/<source>-<hour>.jsonl`.

## Project conventions

- Go 1.26+, `internal/` only (no `pkg/` until cross-binary need)
- stdlib-first: `log/slog`, `errgroup`, `context`. Outside deps: `coder/websocket`, `yaml.v3`, `testify` (test-only)
- `make all` runs lint + vet + race-tested unit tests + build
- Secrets live in `./secrets/` (gitignored) or env vars with `ISOBAR_` prefix
- Recorded data lives in `./data/` (gitignored)

## Roadmap (12-week rebuild, post-research pivot)

- **W1** *(now)*: prune Coinbase, save research, update strategy docs.
- **W2–5** `weather-calibrated` signal: NGR/EMOS, ECMWF blend, conformal
  intervals, EVT tail pricing, Lyapunov 8-day gate, T−3h Bayesian update.
- **W6–8** `hurricane-landfall` signal: NHC ATCF parser, HAFS/HWRF,
  NDBC-SST-nudged intensity, regime-switch HMM. Timed for June 1.
- **W9** microstructure + risk: Avellaneda-Stoikov with bounded-payoff
  σ² = P(1-P)/√t, OFI gate, no-trade zone, CUSUM, Kelly with
  ρ = −1/(K−1) correction.
- **W10** Phase 0.5 gate report (hard gate).
- **W11** operator dashboard (stdlib Go + htmx + SSE + Lightweight Charts).
- **W12** paper + live exec (`$50 → $200` canary first).

Full detail — exit criteria per phase, risk controls, test strategy — is
in [`docs/`](./docs/README.md):

- [Roadmap](./docs/ROADMAP.md) — phase-by-phase with explicit gates
- [Strategy](./docs/STRATEGY.md) — edge thesis and execution doctrine
- [Architecture](./docs/ARCHITECTURE.md) — package layout and invariants
- [Testing](./docs/TESTING.md) — rules, coverage floors, CI expectations
- [Operations](./docs/OPERATIONS.md) — runbook and troubleshooting
- [Risk](./docs/RISK.md) — the L0–L4 control hierarchy
- [Glossary](./docs/GLOSSARY.md) — terms, defined once
