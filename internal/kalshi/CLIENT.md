# Kalshi client — transport, retry, and liveness reference

Numbers, not adjectives. This document captures every timeout, retry
threshold, and rate-limit knob the Kalshi REST + WS client uses, with
the rationale for each value. If you change a number, update this file
in the same commit.

## Overview

`internal/kalshi` is a typed Go client for the Kalshi Trade API v2. It
exposes:

  - `Client` — REST client with built-in rate limiting, retry, and
    explicit per-stage transport timeouts. Signs every request with
    RSA-PSS using `Signer`.
  - `Subscriber` — WebSocket subscriber with auto-reconnect, ticker
    swap (`Reload`), and a frame-liveness watchdog with TCP-level ping.
  - `Signer` — RSA-PSS signature generator (SHA-256, salt = hash length).

All time-based behavior is parameterized by `now func() time.Time` so
tests can drive the clock deterministically. Default is `time.Now`.

## Transport configuration

The client uses an explicit `*http.Transport` rather than
`http.DefaultTransport` so every stage of the request lifecycle has a
deadline.

| Knob                                  | Value | Rationale |
|---------------------------------------|-------|-----------|
| `DialContext.Timeout`                 | 5s    | TCP handshake to a healthy peer is <100ms; 5s catches network blackholes without exceeding the outer client timeout. |
| `DialContext.KeepAlive`               | 30s   | Linux default — no special tuning. Long enough to amortize the syscall over reused conns. |
| `TLSHandshakeTimeout`                 | 5s    | TLS 1.2/1.3 handshakes complete in <200ms on a warm path; 5s catches a stuck handshake. |
| `ResponseHeaderTimeout`               | 5s    | Kalshi's typical TTFB is <100ms. 5s catches a server that accepts the connection but hangs before the response line. |
| `IdleConnTimeout`                     | 90s   | Tradeoff between LB hairpin (servers may close idle conns near 2 min) and reconnect cost. |
| `MaxIdleConnsPerHost`                 | 4     | Concurrency budget × 2; recorder + future bot share the same client. |
| `ForceAttemptHTTP2`                   | true  | Kalshi supports HTTP/2; reduces TCP/TLS overhead via multiplexing. |
| `TLSClientConfig.MinVersion`          | TLS 1.2 | TLS 1.0/1.1 are deprecated by every major CDN. |
| `http.Client.Timeout` (outer guard)   | 10s   | Per-call ceiling. Should be ≥ sum of all stage timeouts. |
| `perRequestTimeout` (ctx wrap)        | 10s   | Same value, applied via `withCallDeadline` so a caller without a deadline still gets bounded latency. Callers with a tighter deadline keep theirs. |

Every public method threads `ctx` explicitly into `doRaw`/`do`. There
are no naked `httpClient.Do` calls; there is no fallback to
`context.Background()` anywhere in the request path.

## Retry policy

| Status / condition       | Retried?              | Backoff |
|--------------------------|-----------------------|---------|
| 200–299                  | Success path          | n/a     |
| 401, 403                 | No — typed `ErrAuth`  | n/a     |
| Other 4xx                | No — fast bail        | n/a     |
| 429 (any method)         | Yes (existing behavior) | `2^(attempt+1)s` floored by `Retry-After`, capped at 60s |
| 502, 503, 504 (GET only) | Yes                   | `2^(attempt+1)s` (no `Retry-After` parsing — 5xx is server health, not rate-limit) |
| Network error (GET only) | Yes                   | `2^(attempt+1)s` |

  - **`maxRetries = 5`**. With base 2^(attempt+1) seconds and a `Retry-After` cap of 60s, total worst-case wall time is bounded near `5 × 60s + minDelay × 5 ≈ 5min`.
  - **POST / DELETE on 5xx and network errors**: not retried. The duplicate-side-effect hazard (double order, double cancel) outweighs the resilience benefit.
  - **POST retries on 429**: preserved from prior behavior. The broader POST-on-429 hazard is gated for Phase 1 (when `cmd/bot` materializes); revisit then.

### `Retry-After` parsing

`parseRetryAfter` implements RFC 9110:

  - Delta-seconds first: `strconv.Atoi("120")` → `120s`.
  - HTTP-date fallback: `http.ParseTime("Wed, 21 Oct 2026 ...")` → `t.Sub(now)`.
  - Negative or unparseable → `(0, false)`; the caller falls back to its bit-shift backoff.

The parsed value is the **floor**, not the override: actual wait is
`max(parsed, currentBackoff)`. Capped at `retryAfterCap = 60s` so a
3600-second header can't park us for an hour.

### `ErrAuth`

`var ErrAuth = errors.New("kalshi: authentication failed")` — wrapped
with status + body on 401 / 403 responses. Callers should
`errors.Is(err, ErrAuth)` and surface this for human attention rather
than spinning. Likely causes: revoked key, signature drift beyond
server clock skew tolerance, missing key file.

## Rate limiting (self-throttle)

| Knob                       | Value | Rationale |
|----------------------------|-------|-----------|
| `baseMinDelay`             | 1s    | ~1 req/s baseline to stay well within Kalshi's documented limit. |
| `maxAdaptiveDelay`         | 2s    | Ceiling so a flaky API doesn't compound into minute-long gaps. |
| `successDecayThreshold`    | 10    | After this many 2xx responses, halve `minDelay` toward `baseMinDelay`. Big enough that a brief healthy burst doesn't immediately undo a protective bump; small enough that recovery is brisk. |

### State transitions

  - **On 429**: `minDelay = min(minDelay × 2, maxAdaptiveDelay)`; reset `consecutive2x = 0`.
  - **On 2xx**: increment `consecutive2x`; if it reaches `successDecayThreshold`, set `minDelay = max(minDelay / 2, baseMinDelay)` and reset to 0.
  - **`ResetRateLimit()`**: force-resets `minDelay = baseMinDelay` and `consecutive2x = 0`. Used after one-time bursts (e.g. ticker discovery) so the bot's steady-state polling isn't carrying over inflated delays.

### Concurrency

`throttle(ctx)` reserves the next slot under the mutex by advancing
`lastCall = now + gap`, then releases the lock before waiting. N
concurrent callers thus queue at `minDelay` intervals rather than
serializing on the wait (which would compound to `N × minDelay` even
though the rate limit is per-process).

The wait honors `ctx`: a cancelled context returns immediately with the
ctx error.

## WS keepalive

The `Subscriber.runOnce` goroutine pool (errgroup) has three workers:
  1. Read pump — `conn.Read` → update `lastFrameTime` → `OnFrame`.
  2. Reload watcher — returns `errReload` when `Reload()` is called.
  3. Liveness watchdog — see below.

### Watchdog

| Knob                          | Default | Rationale |
|-------------------------------|---------|-----------|
| `PingInterval`                | 30s     | Half the dead-window — typical pattern for "ping at half the expected-quiescence interval". |
| `MaxIdleBetweenFrames`        | 60s     | Kalshi weather brackets see at least one orderbook frame per minute during market hours; 60s is the floor below which we treat the feed as dead, not just quiet. |

On each tick (`PingInterval`):
  - If `time.Since(lastFrameTime) > MaxIdleBetweenFrames`: log warn with `last_frame_age_ms` plus `tickers`/`tickers_sample` (Kalshi tickers embed bracket dates, so a subscription to settled markets is readable off the warn line), return error → reconnect via `retry.Do`.
  - Else: `conn.Ping(ctx)` with a `PingInterval`-bounded deadline. Ping failure also returns error → reconnect.

The 60s default was validated empirically against the 2026-05-20 tape
(394k frames): the maximum intra-connection inter-frame gap across the
whole day was 40s, with zero gaps above 45s. Quiet books do not trip
it; a silent subscription does.

The watchdog catches two failure modes the underlying coder/websocket
library doesn't:
  - Half-open TCP connections where the peer's TCP stack is gone but the local kernel hasn't noticed (no TCP RST, no FIN).
  - Live TCP, dead application — the upstream feed is silent due to a market data gateway hang.

### Ticker refresh on reconnect

A watchdog kill is only a useful recovery action if the next connect
can actually succeed. The 2026-05 outage (docs/OPERATIONS.md §15)
showed the failure mode where it can't: the in-memory ticker list had
gone fully stale, Kalshi answered every resubscribe with stub
snapshots (`market_id:""`, no book fields) and no deltas, and the
kill→reconnect loop spun at ~122s/cycle for days.

`SubscriberOptions.RefreshTickers` closes the loop: when set, it is
called at the start of **every** connection attempt (bounded by
`RefreshTimeout`, default 30s) and its result replaces the ticker
list before subscribing. Errors and empty results fall back to the
last-known list with a warn — a flaky discovery source degrades to
the old behavior, never worse. `cmd/tape` wires this to
`DiscoverOpenTickers`; the 4h rediscovery loop remains for adding
next-day markets to connections that stay healthy for hours.

### Reconnect backoff reset

`retry.Config.ResetAfter` (the subscriber passes
`2×MaxIdleBetweenFrames`): an attempt that ran at least that long
before failing resets the backoff ladder and attempt counter, so a
connection that streamed for hours reconnects at `InitialBackoff`
instead of whatever cap earlier flapping ratcheted up to. Before
this, backoff was monotonic over the process lifetime — by
2026-05-26 the recorder had logged 6,090 attempts, every one paying
the full 30s cap.

## Test access

The client and subscriber are designed to be tested without network or
real clocks:

  - **`now func() time.Time`**: every time-based decision (signer
    timestamp, throttle gap, watchdog age, `Retry-After` HTTP-date) flows
    through an injectable clock. Tests pass a fake `now` to drive the
    clock deterministically.
  - **`httptest.NewServer`**: `testClient(t, handler)` in
    `client_test.go` builds a `*Client` pointed at a stub server.
  - **`websocket.Accept`**: the `newKalshiWSServer` helper in
    `ws_test.go` accepts upgrades and replays a scripted frame
    sequence.
  - **Test-friendly accessors**: `baseMinDelay`, `minDelay`,
    `consecutive2x`, `lastCall` are package-private; tests in the same
    package read them directly under `rateMu`.

If you add a new time-based knob, plumb it through `now` rather than
calling `time.Now` directly. Future-Almir will thank you.
