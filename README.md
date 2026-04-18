# isobar

Automated Kalshi trading bot, weather-bracket focused. Phase 0: dual-source
recorder (`cmd/tape`) for empirical edge measurement before strategy commitment.

The name `isobar` refers to lines of constant atmospheric pressure on weather
maps — the bot's edge comes from pricing weather better than the market does.

## Status — Phase 0: data collection

The bot records synchronized market data from three sources for ≥48h, then a
separate analyzer (Phase 0.5, next) produces a written **decision-gate
report**: do tradeable edges exist on weather brackets, on BTC contracts,
both, or neither?

Decision gate (must pass before Phase 1 build): `>10 brackets/day with >8%
edge after fees`.

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
  coinbase        Public WS (BTC spot ticker + matches). No auth.
  openmeteo       NOAA GFS ensemble REST poller (free tier, no auth).
```

**Hot-path principle.** The recorder does not decode WS frames. Bytes from the
socket are wrapped in a thin envelope (timestamps + source tag) and written to
disk. All decoding happens in the analyzer. This minimizes the producer hot
path and future-proofs against schema changes.

**Reused from `arbhunter`.** The Kalshi REST client (`auth.go`, `client.go`,
`types.go`, `client_test.go`) is copied verbatim — battle-tested code, ~840
LOC, zero churn. Everything else is new (the strategy was BTC-only there).

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

## Roadmap

- **Phase 0** *(now)*: record 48h, run `cmd/analyze` (next), produce gate report
- **Phase 0.5**: `cmd/analyze` — compute edge counts, lead-lag distributions, fee-adjusted PnL on simulated quotes
- **Phase 1**: live bot — strategy modules behind a common `Signal` interface, maker-only execution, warm-start via SQLite WAL
- **Phase 2**: paper-trade with realistic fill sim (queue position, adverse selection)
- **Phase 3**: live $50 → $200 after gate clears

Full detail — exit criteria per phase, risk controls, test strategy — is
in [`docs/`](./docs/README.md):

- [Roadmap](./docs/ROADMAP.md) — phase-by-phase with explicit gates
- [Strategy](./docs/STRATEGY.md) — edge thesis and execution doctrine
- [Architecture](./docs/ARCHITECTURE.md) — package layout and invariants
- [Testing](./docs/TESTING.md) — rules, coverage floors, CI expectations
- [Operations](./docs/OPERATIONS.md) — runbook and troubleshooting
- [Risk](./docs/RISK.md) — the L0–L4 control hierarchy
- [Glossary](./docs/GLOSSARY.md) — terms, defined once
