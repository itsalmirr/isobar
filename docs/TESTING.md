# Testing

> How we test isobar. The rules below are non-negotiable for code that
> touches market data, signed requests, or risk controls. Anything the bot
> might rely on at 3 AM without a human watching must have tests that would
> wake us up if it broke.

## Test pyramid

```
          ┌───────────────┐
          │ live canary   │  Phase 3+: 1 market, tiny size, alert on
          │ (real money)  │  reconciliation mismatch.
          ├───────────────┤
          │ replay-based  │  Phase 0.5+: `cmd/analyze` reruns recorded
          │ end-to-end    │  journals through the full decoder + strategy.
          ├───────────────┤
          │ integration   │  Mock HTTP server / mock WS server, goroutine
          │ (mocks)       │  lifecycle, reconnect, context cancel.
          ├───────────────┤
          │ unit (table)  │  Pure functions, boundary cases, property
          │               │  tests. 80%+ of test count lives here.
          └───────────────┘
```

## Rules

1. **Every package has a `_test.go`.** If a package is too trivial to test,
   it's too trivial to exist — fold it into its caller.
2. **`go test -race` is the canonical runner.** We never commit code whose
   tests pass only without `-race`. Hidden races ship as flaky fills.
3. **No sleeps as goroutine synchronization.** A test that uses
   `time.Sleep` to "wait for the goroutine to do the thing" is a bug
   report waiting to happen. Use channels, `sync.WaitGroup`, or
   `Wait(ctx)` methods. *Real-time validation* sleeps (rotation timers,
   clock monotonicity) are legitimate when the asserted behavior is
   itself time-driven; keep them ≤100 ms and document why.
4. **Timeouts on every test that could hang.** CI runs
   `go test -race -count=1 -timeout=120s ./...`. Local equivalent should
   match.
5. **Fast by default.** Full race suite finishes in under ~30s on a
   modern laptop (current: ~25s, dominated by `internal/kalshi` mocked
   WS handshakes). When the suite passes 60s wall-time, gate the slow
   half behind `testing.Short()` and add a `make test-short` target.
   Today no test uses `testing.Short()` because none needs to.
6. **No test reaches the real network.** Any test that talks to
   `api.kalshi.com` or `ensemble-api.open-meteo.com` fails CI by design
   — mock the server with `httptest` / `websocket.Accept`.
7. **Secrets never leak.** RSA keys used in tests are freshly generated
   with `rsa.GenerateKey(rand.Reader, 2048)` in the test helper. No
   checked-in PEMs, not even "obviously throwaway" ones.
8. **Determinism.** If a test uses randomness (jitter, seeded data),
   either seed with a fixed value or use `testing/quick` with a
   documented count. Flakes are filed, bisected, and fixed — never
   retried.
9. **Benchmarks live alongside tests.** Hot paths (orderbook
   reconstruction, journal write, ensemble decode, calibration lookup)
   have `_bench_test.go` files. Run with
   `go test -run='^$' -bench=. -benchmem -benchtime=2s ./...`.

## Coverage targets

| package                | line coverage target | notes                              |
| ---------------------- | -------------------- | ---------------------------------- |
| `internal/clock`       | 100%                 | Pure; there's no excuse. Now includes `Clock` interface + `FakeClock` per ADR-0012. |
| `internal/env`         | 95%                  | Multi-line quote paths included.   |
| `internal/obs`         | 90%                  | Level parsing + handler shape.     |
| `internal/config`      | 95%                  | Every `Validate` branch.           |
| `internal/journal`     | 90%                  | Rotation, drop, roundtrip, shutdown, `OnDropAlarm` edge-trigger + panic safety + envelope `v` (ADR-0011). |
| `internal/kalshi`      | 85%                  | All REST verbs + signer + ws subscribe msg + DiscoverOpenTickers + throttle + SetTickers/Reload. |
| `internal/edge`        | 85%                  | Ticker parser + fee model + poll pairing + scan/backtest math. |
| `internal/market`      | 90%                  | Station registry round-trip (market-ticker parser was deleted as dead — A9). |
| `internal/openmeteo`   | 80%                  | URL build, error propagation, live poller behavior, 7-case retry-policy table (A6). |
| `internal/replay`      | 90%                  | Journal read + Kalshi frame decode + orderbook apply (incl. uninitialized-book state machine + typed errors + counters — A3). |
| `internal/retry`       | 95%                  | Jitter, Next, Do happy-path + ctx + max-attempts. |
| `internal/calibration/cliparse` | 85%         | NWS bulletin parser + dedupe + gap report. |
| `internal/calibration/mos`      | 90%         | OLS fit (clock-injected for determinism) + Predict + ProbBelow + degenerate sigma. |
| `internal/calibration/climate`  | 85%         | Frequency lookup + range probability + season window + ShrinkP + leap-year-aware day-of-year (A7) + missing-column failure. |
| `internal/calibration/bayesian` | 85%         | `Update`, `LoadKernel`, schema reject + `MinKernelN` floor + `MinObsVariance` floor (ADR-0010 foundation). |
| `tools/clockcheck`     | n/a (analyzer)       | `analysistest.Run` against testdata fixtures verifies ADR-0012 enforcement: bans direct `time.Now/Since/Until` outside the allowlist. |

Coverage is a floor, not a ceiling, and not a substitute for
thinking — a 100%-covered function can still be wrong. But a package under
the floor means something untested is about to ship.

## Test categories & conventions

### Unit tests

- Table-driven whenever there are ≥3 similar cases. One `cases :=
  []struct{...}{}` slice, one `for _, tc := range cases`, `t.Run(tc.name, …)`.
- Each case's `name` is a full sentence of what's being asserted.
- Assert on behavior, not implementation. Don't bind tests to private
  fields if a public method answers the same question.

### Property tests

Used where the input space is large enough that table-driven cases miss
edges:

- `clock.Now()` monotonic non-regression under rapid calls.
- `journal` invariants (lines written ≤ lines enqueued; dropped + written
  = enqueued; after `Close`, all queued items are either on disk or in
  the `Dropped` count).
- `config.Load` round-trip (serialize, load, compare).
- Fee math (Phase 1+): per-price fee rounds to the same cents as Kalshi's
  documented formula.

Write them with `testing/quick` when the input is numeric; hand-rolled
loops with `rand/v2` when the input needs structure.

### HTTP / WS integration tests

Pattern:

```go
srv := httptest.NewServer(http.HandlerFunc(func(w, r) {
    // assert on r.Method, r.URL.Path, r.Header.Get("KALSHI-ACCESS-KEY")
    // write a fixture response
}))
t.Cleanup(srv.Close)

c := NewClient(srv.URL, signer)
```

- Every integration test creates *its own* server. No shared test server.
- Handlers that require auth headers assert their presence and
  non-emptiness. The exact signature is re-verified in the signer unit
  test.
- For WS, use `coder/websocket.Accept` inside the handler to complete the
  upgrade, then test the subscribe message round-trip.

### Replay tests (Phase 0.5+)

- Fixtures live at `internal/replay/testdata/<scenario>/`.
- A replay test reads a small journal snippet, feeds it through the
  analyzer, and asserts on the computed edges or orderbook state.
- Expected output is a golden file (`want.json`). Update via
  `-update` flag; reviewers eyeball the diff.

### Strategy tests (Phase 1+)

- Each `Signal` implementation has a deterministic test:
  `given MarketState X, the signal emits Quote Y`.
- Adversarial tests: malformed state (missing fields, NaN prices, empty
  orderbook) must **not** emit a quote — no orders off bad data.
- Pre-commit to every strategy: a *fuzz test* that drives the signal with
  `testing/fuzz` for 30s and asserts no panics, no orders outside
  price/size caps.

### Risk tests (Phase 1+)

- Property test: for any sequence of signal outputs, the resulting order
  stream never exceeds any configured cap.
- Kill-switch test: touching `./halt` while a test tick is running
  produces zero orders, even if the strategy wanted to place one.
- Clock-drift test: drift > 1s halts trading; drift ≤ 250ms does not.

## Make targets

| target             | purpose                                      |
| ------------------ | -------------------------------------------- |
| `make test`        | full race-tested run, canonical CI run       |
| `make test-short`  | `-short` for pre-commit speed                |
| `make bench`       | `go test -bench=.` on hot paths              |
| `make coverage`    | race-tested run with HTML coverage report    |
| `make lint`        | `clockcheck` (custom analyzer, ADR-0012) + golangci-lint |
| `make vet`         | `go vet ./...`                               |
| `make all`         | lint + vet + test + build                    |

## Lint discipline

`make lint` runs `golangci-lint` plus `clockcheck`, a custom
`x/tools/go/analysis` analyzer at
[`tools/clockcheck/`](../tools/clockcheck/clockcheck.go). Per ADR-0012,
clockcheck flags direct `time.Now` / `time.Since` / `time.Until` calls
outside the allowlist: `internal/clock`, `cmd/*/main.go`, `*_test.go`,
`//nolint:clockcheck` lines, and `internal/journal` (scoped exemption
until that package's clock migration lands). Everywhere else,
wall-time reads flow through `clock.Clock` or a `now func() time.Time`
struct field — see `internal/kalshi/client.go` for the existing
`now`-func pattern, and `withCallDeadline` for the
`//nolint:clockcheck` escape hatch (`ctx.Deadline` is intrinsically
wall-bound).

Tests use `clock.Fake(initial)` for deterministic timing.
`internal/calibration/mos.TestFit_DeterministicAcrossCalls` is the A8
regression: with a fixed `FakeClock`, `Fit` is byte-identical across
calls — was not true under the pre-A8 `time.Now()` stamp.

Error-handling discipline test patterns (typed-error / counter /
slog-handler-capture / alarm-hook) are proposed in
[`audit/02-architecture.md`](../audit/02-architecture.md) §2.1.6
(pending per-decision sign-off as ADR-0014). The A3
`internal/replay/orderbook_test.go` changeset is the working exemplar.

## CI expectations

- `make all` must pass on every PR.
- Race detector is on — every test, every time.
- Coverage numbers are printed in CI logs (even if not enforced as a
  fail-gate yet; will be once Phase 1 lands).
- `go mod tidy` is a diff-fail gate — `go.mod` and `go.sum` must be
  canonical on every push.

## Local workflow

```bash
# Tight loop while editing a single package.
go test -race -count=1 ./internal/journal

# Before committing.
make all

# When chasing a flake.
go test -race -count=100 -run TestJournalRotatesOnSize ./internal/journal
```

## Tripwires & post-audit test inventory

Bucket A (post-Phase-1 hardening) shipped three live regression
tripwires plus several new test files. Pinned here because they're
load-bearing — losing one of these silently means losing an audit
finding:

- [`cmd/tape/main_test.go`](../cmd/tape/main_test.go) —
  `TestProdJournalHasAlarmHookWired` fails the build if `journal.Open`
  is ever called from `cmd/tape` without a non-nil `OnDropAlarm`
  (ADR-0011, A1). Also exercises the alarm-crossing path against a
  shortened `AlarmSustain` via the test-only knob.
- [`internal/journal/journal_test.go`](../internal/journal/journal_test.go)
  — drop-counter, panic-safe hook (A2), envelope `v` field
  (`isobar.journal/v1` + `v0-legacy` read path, S4), and alarm-hook
  edge-trigger semantics.
- [`internal/replay/orderbook_test.go`](../internal/replay/orderbook_test.go)
  — A3 state-machine tests: typed `ErrBookUninitialized` /
  `ErrSnapshotApplyFailed`, counters (`ResetCountersForTest`),
  `logOnceOrSuppress`-via-slog-handler-capture pattern.
- [`internal/calibration/bayesian/bayesian_test.go`](../internal/calibration/bayesian/bayesian_test.go)
  — math primitive + kernel-loader + schema-reject + floor enforcement
  (ADR-0010 foundation).
- [`internal/openmeteo/client_retry_test.go`](../internal/openmeteo/client_retry_test.go)
  — A6 retry-policy table: 7 cases covering 429/5xx/4xx/`Retry-After`
  parsing.
- [`tools/clockcheck/clockcheck_test.go`](../tools/clockcheck/clockcheck_test.go)
  — `analysistest.Run` against `testdata/` fixtures: positive
  (banned call) + allowlist (entry point / test file / `//nolint`) +
  package-exempt (internal/clock, internal/journal) cases. Shipped
  with ADR-0012.

The three regression tripwires (alarm-hook + clockcheck +
gitignore-vs-tracked) are summarized in
[audit/01-line-audit.md](../audit/01-line-audit.md) Bucket A appendix.

## Known non-goals

- **We do not test the real Kalshi/Open-Meteo APIs from CI.** They
  change, they rate-limit, and they'd block developers without secrets.
  Manual smoke tests happen in staging with a `-dry-run` flag.
- **We do not test `main` directly** beyond the alarm-hook tripwire
  above. `cmd/tape/main.go` is otherwise a thin wiring file — its
  behavior is covered by the integration tests of the packages it
  wires. The four other `cmd/*` binaries have zero tests today; a
  Phase 2 proposal (ADR-0019, in `audit/02-architecture.md` §4.2)
  decides which of them survive consolidation and what test tier each
  warrants. Pending per-decision sign-off — do not assume it's policy
  yet.

## When tests break

- **Flakes are bugs.** File an issue, reproduce with `-count=100`, bisect
  with `git bisect run`, fix. Do not add a retry loop.
- **Race detector false positives** don't exist — if `-race` flags
  something, it's a real race. Fix it, or explain in the PR why the
  flagged shared state is actually safe (rare but real for
  single-writer/single-reader patterns).
- **Timeout under CI but not locally.** CI runners are slower. Either the
  test has a sleep that needs replacing with synchronization, or the
  test's timeout budget is too tight for the work it's doing.
