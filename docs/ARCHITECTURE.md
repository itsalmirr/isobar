# Architecture

> How the code is laid out today, the invariants each package guarantees,
> and how the pieces compose. Read this before making changes that cross
> package boundaries.

## Guiding constraints

- **Single binary per concern.** `cmd/tape` records; `cmd/analyze` (Phase
  0.5) replays; `cmd/bot` (Phase 1) trades; `cmd/peek` is an ops-only
  Kalshi REST probe. No god-binary with subcommands for everything.
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

## Directory layout

```
cmd/
  tape/                Phase 0 recorder (main.go today)
  analyze/             Phase 0.5 sanity report — MVP today (counts,
                       gaps, monotonicity). Orderbook replay + gate
                       numbers arrive at W10 once calibration lands.
  peek/                Ops probe — reads Kalshi /series and /markets
                       to confirm series-name correctness when the
                       catalog changes (W6 hurricane series discovery).
  calibrate/           Phase 0.5 NGR fit + reliability plots (W2)
  bot/                 Phase 1+ live bot (W11–12)

internal/
  clock/               (Wall, Mono) stamp pair
  config/              YAML + env overlay + validation
  env/                 Stdlib-only .env loader
  journal/             Append-only rotating JSONL writer
  kalshi/              REST client + signer + authenticated WS subscriber
  obs/                 slog JSON bootstrap
  openmeteo/           GFS + ECMWF ensemble REST poller
  retry/               Context-aware exponential backoff with jitter
  calibration/         NGR, conformal, EVT, T-3h Bayesian (W2–5)
  nhc/                 NHC ATCF a-deck/b-deck advisory parser (W6)
  hurricanemodel/      HAFS/HWRF + NDBC SST (W7–8)
  microstructure/      Reservation price, half-spread, OFI, CUSUM (W9)
  risk/                L0–L4 pre/post-trade controls (W9)
  signal/              Signal interface + weather + hurricane (W5, W8)
  busevents/           In-process pub/sub for dashboard (W11)
  dashboard/           SSE + htmx server (W11)
  controlstore/        modernc.org/sqlite WAL for control plane (W11)
  state/               Warm-start + reconciliation (W12)
  exec/                Paper + Kalshi exec adapters (W11–12)

config/
  config.example.yaml  Canonical example; copy to config.yaml

web/
  templates/           html/template fragments (W11)
  static/              htmx, lightweight-charts vendored (W11)

docs/
  research/            8 research reports + SYNTHESIS.md
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
- Both fields are sampled from the *same* `time.Now()` call; they refer to
  the same physical event.
- Wall is the **only** cross-process comparable field. Use monotonic only
  for in-process interval measurement.

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
  `t` (wall), `m` (mono), `src`, `k` (kind), `p` (raw payload).

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

- REST, 30-second HTTP timeout, 32 MiB response cap per fetch.
- Validates `poll_interval >= 1m` at construction.
- First fetch fires *immediately* in `Run` so Phase 0 does not wait for
  the first tick to get data.

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

## Future (Phase 1+) shape

Phase 1 adds these packages without altering the Phase 0 boundaries:

```
internal/
  signal/              Signal interface + implementations
    weather/           Calibrated-ensemble-vs-bracket strategy
    hurricane/         NHC advisory + HAFS/HWRF + NDBC SST strategy
  calibration/         NGR, conformal, EVT, T-3h Bayesian update
  nhc/                 NHC ATCF a-deck/b-deck advisory parser
  hurricanemodel/      HAFS/HWRF fetch + NDBC SST feed
  microstructure/      Reservation price, half-spread, OFI, CUSUM
  risk/                Pre-trade caps + kill-switch + Kelly-with-ρ
  exec/                Exec interface + PaperExec + KalshiExec
  state/               SQLite WAL warm-start + reconciliation
  busevents/           In-process pub/sub
  dashboard/           SSE + htmx server
  controlstore/        modernc.org/sqlite WAL control plane
```

The `cmd/bot` entrypoint wires:
`feeds → signal.Tick → risk.Gate → exec.Submit`.

`cmd/tape` stays exactly as today — recording is a Phase 0 tool, not a
runtime dependency of the bot.

## Why it's laid out this way

- **Separation between recording and trading** keeps the trader idempotent.
  If the bot crashes, the tape keeps running; data is never lost.
- **Raw-frame journaling** future-proofs us against Kalshi API bumps. We
  never have to backfill, re-decode, or re-record if the schema changes.
- **Single owner goroutine per file** eliminates an entire class of
  file-locking bugs. The design of `journal.Journal.run` is intentional.
- **Strict `internal/` boundary** means we can refactor liberally without
  breaking external consumers — there are none.
