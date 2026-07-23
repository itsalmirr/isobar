# 0005 — Extract `internal/retry`; fix kalshi throttle; promote DiscoverOpenTickers

**Status:** Accepted
**Date:** 2026-04-18
**Authors:** isobar team

## Context

A deep code review after the W1 pivot (Explore agent transcript in
git commit history for `feada2d`) identified three concrete defects
in the current tree:

1. **Jitter + exponential-backoff loop reinvention.** `kalshi/ws.go`
   had a private `jitter()` and an inline reconnect-with-doubled-
   backoff loop. The 12-week plan adds at least four packages that
   will need the same pattern (`internal/nhc`, `internal/exec`,
   `internal/signal/*`, any future WS adapter). If we don't extract
   it now, we'll either reimplement four times or refactor later
   across four call sites.
2. **Concurrency bug in `kalshi.Client.throttle`.** The function
   held `rateMu` across `time.Sleep(minDelay - elapsed)`. N concurrent
   callers would therefore wait N × minDelay total because only the
   holder could make progress through the sleep. Not a correctness
   bug (rate limit is still enforced) but a throughput bug — hides
   parallelism we've paid for.
3. **`discoverTickers` in `cmd/tape`.** Pagination logic for a Kalshi-
   specific REST endpoint lived in the binary, not the client
   package. It would need to be duplicated for `cmd/analyze` and any
   future service that enumerates markets.

## Decision

Apply three surgical fixes in a single commit:

1. **Create `internal/retry`** with `Do(ctx, cfg, logger, fn)`, `Jitter(d)`,
   `Next(prev, max)`. Wire `kalshi.Subscriber.Run` through `retry.Do`.
   Delete the private `jitter`.
2. **Fix `kalshi.Client.throttle`** to reserve its slot under the
   mutex (advance `lastCall`) and sleep *outside* the lock. Cap
   adaptive `minDelay` growth at a new constant `maxAdaptiveDelay =
   2s` so a flaky API can't compound into minute-long gaps. Replace
   `math.Pow(2, float64(attempt+1))` with bit-shift `1<<n` in the
   429 retry. Guard against regression with a new test that fails
   if wall-time grows linearly with caller count.
3. **Promote `discoverTickers` to `kalshi.Client.DiscoverOpenTickers(
   ctx, series, logger)`**. Guard against the cursor-with-empty-page
   infinite-loop case (`len(markets) == 0` ⇒ break). Cover with two
   new tests — pagination+dedup, and the empty-page guard.

## Alternatives considered

- **Leave the retry loop inline now, extract later.** Rejected —
  premature optimization of a reuse pattern that is provably coming
  within the next 6 weeks. Extracting after four sites exist is
  4× the refactor cost.
- **Use `golang.org/x/time/rate` instead of our hand-rolled throttle.**
  Rejected — adds a dependency surface we're deliberately avoiding,
  and the adaptive behavior on 429 (`minDelay` doubles) is not
  expressible in `rate.Limiter` cleanly.
- **Fix the throttle with a channel-based token bucket.** Rejected —
  more code for the same semantics; reserving `lastCall` under the
  lock is the minimal diff.

## Consequences

Good:
- Four future packages get retry semantics "for free" (same backoff
  constants, same context handling, same log shape).
- REST client throughput improves under concurrent load.
- `DiscoverOpenTickers` is testable in isolation and reusable from
  analyzer + dashboard.
- 77.3% total coverage; `internal/retry` at 97%.

Bad:
- Public API surface grows slightly (`DiscoverOpenTickers`,
  `retry.Do/Jitter/Next`). These are `internal/` so churn is cheap.

Neutral:
- `internal/retry` becomes a canonical home for future retry-related
  helpers (e.g. circuit-breaker, if we ever need one).

## Reversal cost

Near zero. The extracted code paths are small; inlining them back
would be a find-and-replace.

Canary: none. These are pure refactors guarded by tests.

## References

- Git commit `feada2d` — implementation.
- `docs/ARCHITECTURE.md` §`internal/retry`.
- `docs/TESTING.md` — coverage target row.
