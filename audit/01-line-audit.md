# audit/01-line-audit.md — Phase 1: Line-by-line audit

**Project**: `isobar` (Kalshi weather-bracket trading bot)
**HEAD**: `dee0df4` (post-P1-triage main)
**Methodology**: 6 parallel Opus 4.7 audit agents, one per package group. Every file read end-to-end. Every claim cites `file:line`. Severity-sorted within each package per the user's cadence rule.
**Scope (per the user's non-negotiables)**: every `go func`, every `context.Context`, every error path, every `time.` usage, every external boundary, every magic number, every hardcoded path/URL, every disabled test, every panic, every silently-swallowed error, every comment-vs-code drift, every TODO/FIXME/XXX, every untrusted-input boundary, every concurrency invariant.

---

## Severity rollup

| Package group | P1 | P2 | P3 | nit |
|---|---:|---:|---:|---:|
| `internal/edge/` | 5 | 13 | 21 | 5 |
| `internal/kalshi/` | 10 | 13 | 8 | 6 |
| `internal/calibration/` | 5 | 18 | 14 | 4 |
| `internal/journal/` + `internal/replay/` | 5 | 13 | 8 | 5 |
| utility packages | 8 | 18 | 14 | 6 |
| `cmd/*` | 2 | 17 | 14 | 8 |
| **Total** | **35** | **92** | **79** | **34** |

**Top headline (P1, "must fix before next prod deploy")**:

1. `cmd/tape` never installs the ADR-0011 alarm hook → every prod deploy logs "drop-alarm sink is default — wire a real alarm" then runs blind ([journal § P1 #1](#journaljournalgo))
2. `Journal.OnDropAlarm` is panic-unsafe → a hook panic kills the writer goroutine and silently stops all journaling ([journal § P1 #3](#journaljournalgo))
3. Orderbook snapshot-error leaves an empty book; subsequent deltas corrupt it silently ([replay § P1 #1](#replayorderbookgo))
4. `internal/clock` is not injectable — all "tests should be deterministic" claims downstream are aspirational ([utility § P1 clock.go](#internalclock))
5. Kalshi REST 429-retry on POST is a money-path duplicate-order hazard (latent — only fires when `cmd/bot` lands) ([kalshi § P1 #3](#clientgo-514-loc))
6. Kalshi WS has no read deadline / no ping-pong → recorder can hang silently on a stale connection ([kalshi § P1 ws.go #4](#wsgo-244-loc))
7. Kalshi HTTP client uses `http.DefaultTransport` (no per-stage timeouts, no TLS MinVersion) ([kalshi § P1 #1](#clientgo-514-loc))
8. CHI/KMDW: still partially in-flight (now fixed in tracked config, but ADR/OPERATIONS reference paths inconsistent — see [§14](00-map.md#14-issues-surfaced-during-mapping))
9. MOS `FittedAt: time.Now().UTC()` injects wall-clock into deterministic math ([calibration § P1 #2](#mosmosgo))
10. Climate Feb 29 obs silently aliased to March 1 (`cumDays` collision) ([calibration § P1 #3](#climateclimatego))

(Full list in each section below.)

---

## `internal/edge/`

Package is a pure offline analysis library — `Scan` is single-threaded apart from a `sync.Map` used only for once-per-key log throttling. No `goroutine`, no `time.Now`, no `time.Sleep`, no `time.After`, no network call, and **no `context.Context` parameter anywhere** in the package. That last point is itself worth noting: a multi-hour backtest cannot be cancelled cleanly.

### scan.go

#### P1 — must fix before next prod deploy

- [scan.go:559-569](../internal/edge/scan.go:559) — `stationLocation` calls `time.LoadLocation` on every trade. `time.LoadLocation` reads `/usr/share/zoneinfo/<name>` from disk per call (no internal cache). For a multi-million-frame scan that is millions of stat+open syscalls and can fail nondeterministically if zoneinfo is missing inside a stripped container. **Fix**: cache the four locations in a package-level `var` initialised once (or panic-on-init).
- [scan.go:151](../internal/edge/scan.go:151) — `EndDate.Before(StartDate)` permits `EndDate == StartDate`, which silently produces an empty scan (no error, no rows). An off-by-one CLI flag emits a clean "0 frames" report that looks like success.
- [scan.go:308-314](../internal/edge/scan.go:308) — empty-`FitModelSet` branch builds `mosSkipReason = "model_set_mismatch:<obs>vs"` with a trailing `vs` and an empty fit side. Counter keys here will not collide with well-formed `model_set_mismatch:<obs>vs<fit>` keys at [scan.go:323-325](../internal/edge/scan.go:323), so aggregates double-count. **Fix**: reject empty `FitModelSet` at Scan entry or use a canonical reason `"model_set_mismatch:fit_empty"`.
- [scan.go:243-249](../internal/edge/scan.go:243) — residual `return fmt.Errorf("daily extremes: %w", err)` aborts the *entire scan* on a single bad poll. Should downgrade to skip-and-count, matching every other malformed-input handler in this function.

#### P2

- [scan.go:155-181](../internal/edge/scan.go:155) — `loadPolls` + `collectSiblings` + main pass = 3× disk I/O on Kalshi files. Fold sibling collection into the main pass.
- [scan.go:225](../internal/edge/scan.go:225) — `time.Unix(0, msg.TsMs*int64(time.Millisecond))` can overflow on malicious input. Use `time.UnixMilli(msg.TsMs)`.
- [scan.go:375, :413-414](../internal/edge/scan.go:375) — `int(priceDollars*100 + 0.5)` biases negative values. Use `math.Round`.
- [scan.go:312](../internal/edge/scan.go:312) — paired with P1 model_set_mismatch above.
- [scan.go:455-457](../internal/edge/scan.go:455) — partial `res.Rows` discarded on `replay.ForEachFrame` non-EOF error. Return partial state with the error.
- [scan.go:195-196](../internal/edge/scan.go:195) — `log.Printf` mixes stdlib log with slog elsewhere. Use `slog.Warn`.
- [scan.go:455, :551](../internal/edge/scan.go:455), [poll_index.go:73](../internal/edge/poll_index.go:73) — `errors.Is(err, io.EOF)` check looks dead. Verify or drop.

#### P3

- [scan.go:186](../internal/edge/scan.go:186) — `sync.Map` overkill for single-threaded loop; use plain map.
- [scan.go:282](../internal/edge/scan.go:282) — `pFairNaive` div-by-zero defense missing (currently unreachable).
- [scan.go:399, :401](../internal/edge/scan.go:399) — Truth.Get false skips row entirely; consider `TruthRequired` knob.
- [scan.go:411-414](../internal/edge/scan.go:411) — `noPriceDollars` local is unused after computation.
- [scan.go:496-500](../internal/edge/scan.go:496) — `memberModelTag` unknown-prefix fallthrough has no counter.

#### nit

- [scan.go:8](../internal/edge/scan.go:8) — `"log"` stdlib import mixes with slog convention.
- [scan.go:194-196](../internal/edge/scan.go:194) — comment terminology drift.
- [scan.go:558](../internal/edge/scan.go:558) — station list documented in switch; derive from `market.AllStationCodes()`.

### scan_test.go

#### P2

- [scan_test.go:286-289](../internal/edge/scan_test.go:286) — 4 bare `_ =` swallows on `os.MkdirAll`, `os.WriteFile`, `LoadTruth`. Use `t.Fatal`.

#### P3

- [scan_test.go:41, 49, 137, 143, 194, 202, 275, 280, 340, 347, 441, 448, 536, 543](../internal/edge/scan_test.go:41) — every `_, _ := json.Marshal` silently drops the error.
- [scan_test.go:654-656](../internal/edge/scan_test.go:654) — `var _ = gzip.BestSpeed` is a compile-time pin claiming gzip round-trip "in a follow-up". Implement or delete.
- [scan_test.go:104-106](../internal/edge/scan_test.go:104) — magic literal `3` for "control+2 perturbations"; refer to named const.
- [scan_test.go:111-113](../internal/edge/scan_test.go:111) — stale-comment-vs-fixture risk.

### openmeteo.go

#### P1

- [openmeteo.go:90-93](../internal/edge/openmeteo.go:90) — `ParsePoll` returns an error when one member has `len(vals) != len(times)` ([openmeteo.go:94-97](../internal/edge/openmeteo.go:94)) — that bubbles up through `loadPolls` and silently skips the entire poll ([poll_index.go:62](../internal/edge/poll_index.go:62)). A single sparse member silently disappears the whole station's poll.

#### P2

- [openmeteo.go:174-181](../internal/edge/openmeteo.go:174) — `isAllDigits` reinvents `strconv.Atoi`.
- [openmeteo.go:240-241](../internal/edge/openmeteo.go:240) — `civilDate` timezone contract not enforced in signature.

#### P3

- [openmeteo.go:113-172](../internal/edge/openmeteo.go:113) — `matchTemperatureMember` is nested `strings.HasPrefix` checks; regex would be 5 lines.
- [openmeteo.go:300-305](../internal/edge/openmeteo.go:300) — `errFmt` reinvents `errors.New`.
- [openmeteo.go:195](../internal/edge/openmeteo.go:195) — `MinHourlySamplesPerMember = 12` justified inline but no ADR cross-ref.

### openmeteo_test.go

#### P3

- [openmeteo_test.go:93](../internal/edge/openmeteo_test.go:93) — `t.Skipf("America/New_York unavailable…")` hides real defects on stripped CI images.
- [openmeteo_test.go:114, 161, 189](../internal/edge/openmeteo_test.go:114) — bare `_` swallows on fixture setup.

### fee.go

#### P3

- [fee.go:10](../internal/edge/fee.go:10) — `KalshiFeeBPS = 0.07` misnamed (it's a fraction, not basis points). Rename `KalshiFeeFraction`.
- [fee.go:46-48](../internal/edge/fee.go:46) — `RoundTripFeeCents` aliases `FeePerContractCents` and is unused outside the package.

### poll_index.go

#### P1

- [poll_index.go:201-209](../internal/edge/poll_index.go:201) — `fmt.Sscanf(s, "%f", &v)` accepts Inf/NaN/scientific notation. Use `strconv.ParseFloat` with bounds.

#### P2

- [poll_index.go:117-145](../internal/edge/poll_index.go:117) — `dailyFiles` aborts on individual day-dir errors; partial backfills crash.
- [poll_index.go:151-162](../internal/edge/poll_index.go:151) — `tradeMsg` carries three price fields with no struct-level comment.

#### P3

- [poll_index.go:62](../internal/edge/poll_index.go:62) — `ParsePoll` failure silently swallowed in `loadPolls`. No counter.
- [poll_index.go:96-111](../internal/edge/poll_index.go:96) — Station mapping table #3 (also in scan.go:559 and ticker.go:176). Consolidate.
- [poll_index.go:107-108](../internal/edge/poll_index.go:107) — `"rest:CHI" → "KMDW"` lock with a prod-config regression test.

### resolution.go

#### P1

- [resolution.go:28-29](../internal/edge/resolution.go:28) — `FIXME — verify against Kalshi market detail page` has been in the file since at least May 10. Confirm-and-delete or ticket.
- [resolution.go:57-58](../internal/edge/resolution.go:57) — `BracketUnknown` branch is dead code (ParseTicker rejects unknown prefixes).

#### P3

- [resolution.go:70-100](../internal/edge/resolution.go:70) — `ResolveWithSiblings` dedupe is defensive but redundant given `collectSiblings`.

### ticker.go

#### P2

- [ticker.go:144-146](../internal/edge/ticker.go:144) — `dd > 31` accepts Feb 30 / Apr 31 (normalised silently).
- [ticker.go:127-148](../internal/edge/ticker.go:127) — 2-digit year always 2000+; ADR-worthy comment.
- [ticker.go:67-70](../internal/edge/ticker.go:67) — `validSeries` is a parallel station table.

#### P3

- [ticker.go:165](../internal/edge/ticker.go:165) — `strconv.ParseFloat` accepts Inf/NaN for bracket key.
- [ticker.go:39-47](../internal/edge/ticker.go:39) — `BracketUnknown` exported but only zero-value sentinel.

### truth.go

#### P1

- [truth.go:139-162](../internal/edge/truth.go:139) — `loadTruthFile` swallows per-row parse errors silently with `continue`. A malformed row vanishes the day's bet from the backtest with no signal.
- [truth.go:141-143](../internal/edge/truth.go:141) — `lineNo` increment is dead (never used in error reporting).
- [truth.go:140-143](../internal/edge/truth.go:140) — `if err != nil { break }` collapses real I/O errors and natural `io.EOF` into one path with no distinction.

#### P2

- [truth.go:88-108](../internal/edge/truth.go:88) — `stationFromTruthFilename` accepts `K[A-Z]{3}.csv` permissively. Tighten to `market.AllStationCodes()`.
- [truth.go:111](../internal/edge/truth.go:111) — `nolint:gosec` justified but combined with permissive matcher creates a small file-inclusion surface.

#### P3

- [truth.go:64-86](../internal/edge/truth.go:64) — `LoadTruth` returns partial state on per-file failure. Pick a policy (fail-fast or best-effort).

### Cross-cutting (edge package)

- **No context propagation anywhere.** Adding `ctx context.Context` to `Scan` would be a substantive but bounded change.
- **No wall-clock dependencies in production paths** (great — package is naturally deterministic).
- **Three station-mapping tables drift independently** at [poll_index.go:99-111](../internal/edge/poll_index.go:99), [scan.go:559-569](../internal/edge/scan.go:559), [ticker.go:176-188](../internal/edge/ticker.go:176). Consolidate via `internal/market`.
- **Unused exports** safe to unexport: `RoundTripFeeCents`, `KalshiFeeBPS`, `Poll.MemberCount`, `Poll.MemberIDs`, `Poll.DailyExtremesF` (non-WithSkip variant), `MemberDaily`, `ErrInsufficientCoverage`, `BracketUnknown`, `Resolution.Width`.
- **Two persistent FIXMEs** at [resolution.go:28](../internal/edge/resolution.go:28), [resolution.go:65](../internal/edge/resolution.go:65) gate bracket-resolution correctness.
- **Silent skip-and-count is the dominant error policy.** Add rate-limited slog.Warn on spikes.

---

## `internal/kalshi/`

The only **CRITICAL** runtime path (touches real money / live feed if/when bot goes live). No panics, no `t.Skip`, no TODOs/FIXMEs. Mutex discipline is correct (never held across network calls). Money path is currently dead in production but is one PR away.

### auth.go (75 LOC)

#### P1

- [auth.go:24-40](../internal/kalshi/auth.go:24) — **PEM bytes survive past parse.** `NewSigner` receives `pemKey []byte` and uses it once at line 25; caller's slice is not zeroed. Add `for i := range pemKey { pemKey[i] = 0 }` at end of `NewSigner`.
- [auth.go:42-52](../internal/kalshi/auth.go:42) — `parseRSAPrivateKey` returns the PKCS-1 error when PKCS-8 fails — operator sees `"asn1: structure error"` instead of "could not parse as PKCS8 or PKCS1". Wrap with `errors.Join(pkcs8Err, pkcs1Err)`.

#### P2

- [auth.go:59-74](../internal/kalshi/auth.go:59) — No clock injection at the AuthHeaders boundary; `s.now` is package-private. Use `internal/clock`.
- [auth.go:60](../internal/kalshi/auth.go:60) — `time.Now().UnixMilli()` from wall clock; NTP slew / VM-suspend can move it backward. Use monotonic from `internal/clock`.

#### P3

- [auth.go:63](../internal/kalshi/auth.go:63) — Signed-message concatenation without separators (timestamp + method + path); fragile but correct per Kalshi spec. Add comment pointing at the spec.

### client.go (514 LOC)

#### P1

- [client.go:44-46](../internal/kalshi/client.go:44) — `http.Client{Timeout: 10*time.Second}` with **no `Transport` configured**. Uses `http.DefaultTransport` — connection pool shared with any future tooling. Set explicit `Transport: &http.Transport{}` with `DialContext` timeout, `TLSHandshakeTimeout`, `ResponseHeaderTimeout`, `IdleConnTimeout`, `MaxIdleConnsPerHost`, `ForceAttemptHTTP2`.
- [client.go:44-46](../internal/kalshi/client.go:44) — No explicit `TLSClientConfig.MinVersion`. Pin `tls.VersionTLS12` minimum.
- [client.go:436-499](../internal/kalshi/client.go:436) — **Retry loop re-uses the same `*http.Request` for non-idempotent POSTs.** 429-on-POST + retry = duplicate order. Either don't retry POST on 429, or require `ClientOrderID` for Kalshi-side idempotency. Latent (no current POST callers) but lights up the moment trading engine lands.
- [client.go:460](../internal/kalshi/client.go:460) — `//nolint:gosec` mis-applied for `GetRaw` because `GetRaw` accepts arbitrary path strings. SSRF-shaped surface for any future caller passing attacker input.
- [client.go:485](../internal/kalshi/client.go:485) — Backoff `1 << uint(attempt+1) * time.Second` has no jitter; concurrent callers retry in lockstep. Add `retry.Jitter`.
- [client.go:471-491](../internal/kalshi/client.go:471) — Only 429 retried; 5xx returns immediately. Retry 502/503/504 too.
- [client.go:494-496](../internal/kalshi/client.go:494) — HTTP 401/403 indistinguishable from soft 4xx. Map to typed `ErrAuth`, return without retry.

#### P2

- [client.go:114-172](../internal/kalshi/client.go:114) — `DiscoverOpenTickers` has no max-pages guard. Add `const maxPages = 200`.
- [client.go:142-144](../internal/kalshi/client.go:142) — Mid-pagination error discards partial slice. Return partial + error.
- [client.go:222-240](../internal/kalshi/client.go:222) — `fpLevelsToInt` silently drops parse-failed levels.
- [client.go:465](../internal/kalshi/client.go:465) — 10 MiB body cap is unnamed const.
- [client.go:332-356](../internal/kalshi/client.go:332) — `getRaw` returns `string(bodyBytes)`; double copy with downstream `[]byte(s)`.
- [client.go:381-398](../internal/kalshi/client.go:381) — `json.Marshal` of `body interface{}` has no max-size check.

#### P3

- [client.go:51-57](../internal/kalshi/client.go:51) — `ResetRateLimit` is dead code (no non-test callers).
- [client.go:244-280](../internal/kalshi/client.go:244) — **Every order/portfolio method is dead in production.** `CreateOrder`, `CancelOrder`, `GetOrder`, `GetOrders`, `GetFills`, `GetPositions`, `GetBalance` (and Raw variants). Move to a sub-package `kalshi/trade` or mark `// EXPERIMENTAL`.
- [client.go:332-380](../internal/kalshi/client.go:332) — Duplicated `getRaw`/`get` logic.
- [client.go:413-426](../internal/kalshi/client.go:413) — `sign` silently no-ops on `signer == nil`. Log warn once at construction.
- [client.go:428-431](../internal/kalshi/client.go:428) — `maxRetries = 5` is global, not per-method.
- [client.go:439-446](../internal/kalshi/client.go:439) — Request body cloning logic is fragile; add sanity check + comment.

#### nit

- [client.go:13](../internal/kalshi/client.go:13) — Import grouping.
- [client.go:64](../internal/kalshi/client.go:64) — Stale comment from a previous bug fix.
- [client.go:447](../internal/kalshi/client.go:447) — "may have drifted" → "must" (the backoff sleep guarantees drift).

### ws.go (244 LOC)

#### P1

- [ws.go:143-150](../internal/kalshi/ws.go:143) — Bare `go func()` inside `runOnce` not owned by an errgroup. Safe today via `defer cancel()` but unasserted by tests. Convert to `errgroup.WithContext`.
- [ws.go:165](../internal/kalshi/ws.go:165) — `defer conn.Close(websocket.StatusInternalError, ...)` runs on every return path including clean ctx-cancel. Detect cause and use `StatusNormalClosure` accordingly.
- [ws.go:167](../internal/kalshi/ws.go:167) — `SetReadLimit(8 << 20)` is 8 MiB. Single frame can OOM allocator. Tighten to 1 MiB.
- [ws.go:179-184](../internal/kalshi/ws.go:179) — **No read deadline / no ping-pong loop.** Recorder hangs silently on idle/dead WS connection. Add ping-pong every 30s or wrap Read with ctx-deadline.
- [ws.go:178-184](../internal/kalshi/ws.go:178) — `OnFrame` is documented as receiving untrusted bytes; downstream callers must use bounded-depth JSON decoder.

#### P2

- [ws.go:67-72](../internal/kalshi/ws.go:67) — Reconnect defaults (500ms/30s) are unnamed magic.
- [ws.go:130-136](../internal/kalshi/ws.go:130) — `reloadCh` drain in `runOnce` can lose a reload signal if it fires during retry backoff.
- [ws.go:162-164](../internal/kalshi/ws.go:162) — `resp.Body.Close()` error silently ignored; add comment.
- [ws.go:199-218](../internal/kalshi/ws.go:199) — `subscribe` writes JSON but doesn't wait for `ack`/`subscription_ok`. Bad channel/ticker = silent wait forever.
- [ws.go:217](../internal/kalshi/ws.go:217) — `conn.Write` has no deadline.

#### P3

- [ws.go:22-32](../internal/kalshi/ws.go:22) — `SubscriberOptions.OnFrame` ownership semantics in comment, not type. Split `OnFrameSync` / `OnFrameAsync`.
- [ws.go:55-60](../internal/kalshi/ws.go:55) — Mixed-protection model (RWMutex + unprotected chan) is correct but confusing.
- [ws.go:188-197](../internal/kalshi/ws.go:188) — `subscribeMessage` hardcodes `ID: 1`.

#### nit

- [ws.go:24](../internal/kalshi/ws.go:24) — Stale ticker_v2 mention.
- [ws.go:230](../internal/kalshi/ws.go:230) — Defensive `strings.TrimSpace` uncommented.

### types.go (330 LOC)

#### P2

- [types.go:92-122](../internal/kalshi/types.go:92) — `parseFloat`/`parseInt`/`parseFP`/`parseFPCents` silently swallow strconv errors. `"NaN"` becomes 0 silently.
- [types.go:108-114](../internal/kalshi/types.go:108) — `int(f + 0.5)` rounds toward zero for negatives. Use `math.Round`.
- [types.go:233](../internal/kalshi/types.go:233) — `Fill.CreatedAt time.Time` inconsistent with `Market`/`Order` (string).

#### P3

- [types.go:39-66](../internal/kalshi/types.go:39) — `Market` has 27 fields; ~9 dead.
- [types.go:170-191](../internal/kalshi/types.go:170) — `Order` mixes legacy int + FP string fields. Discriminator pitfall.
- [types.go:171](../internal/kalshi/types.go:171) — `PlacedCount int \`json:"place_count"\`` — Kalshi typo.

#### nit

- [types.go:14-15](../internal/kalshi/types.go:14) — `BaseURL` exported but unused.
- [types.go:317](../internal/kalshi/types.go:317) — `ReducedBy int` undocumented unit.

### auth_test.go (93 LOC)

#### P3

- [auth_test.go:23-29](../internal/kalshi/auth_test.go:23) — `TestNewSignerRejectsNonRSAPKCS8` is misnamed; actually tests parse-failure not type-assertion. Add a real Ed25519 PKCS8 test.

#### nit

- [auth_test.go:31-56](../internal/kalshi/auth_test.go:31) — Package-internal test (`package kalshi`) for clock injection; document the choice.

### client_test.go (544 LOC)

#### P2

- [client_test.go:518-544](../internal/kalshi/client_test.go:518) — `TestDoRaw429RetriesThenSucceeds` only exercises idempotent GET. Add a POST counterpart asserting `CreateOrder` does **not** retry on 429.
- [client_test.go:256-281](../internal/kalshi/client_test.go:256) — `TestThrottleDoesNotSerializeCallers` uses wall-clock thresholds (`< 3500ms`). CI flake risk.

#### P3

- [client_test.go:540-543](../internal/kalshi/client_test.go:540) — Test takes ~2s due to hardcoded backoff base; allow test-only override.

### ws_test.go (322 LOC)

#### P2

- [ws_test.go:45-47](../internal/kalshi/ws_test.go:45) — `InsecureSkipVerify: true` uncommented (justified for httptest.NewServer).
- [ws_test.go:225-232](../internal/kalshi/ws_test.go:225) — `TestSubscriberDefaultOnFrameIsSafe` only checks no-panic; strengthen.

#### P3

- [ws_test.go:310-322](../internal/kalshi/ws_test.go:310) — `TestSubscriberConcurrentOnFrameIsLocal` race-detector name is misleading.

### Cross-cutting (kalshi package)

- **Money path: not exercised today.** Every order/portfolio method has zero non-test callers. The retry-on-POST hazard is the single biggest correctness risk when `cmd/bot` lands.
- **Critical-path data: live WS.** No keepalive (P1 ws.go:179) and stdlib-default HTTP transport (P1 client.go:44) are the two real production hazards.

---

## `internal/calibration/`

Math-heavy code where numerical bugs become alpha bugs. **No `t.Skip`, no commented-out tests, no `panic()`.** Schema versions checked but no migration path documented.

### bayesian/bayesian.go

#### P2

- [bayesian.go:113](../internal/calibration/bayesian/bayesian.go:113) — `MinKernelN = 5` far below `mos.MinHoldoutForSigma = 30`. With N=5, sample variance has ~50% relative error. Raise to 30 or add a separate trust-threshold.
- [bayesian.go:34](../internal/calibration/bayesian/bayesian.go:34) — `MinObsVariance = 0.25` (0.5°F stdev) asserted as floor with no measurement traceability. Derive empirically; consider per-station.

#### P3

- [bayesian.go:40](../internal/calibration/bayesian/bayesian.go:40) — `TerminalSigma = 0.5` magic; same issue as MinObsVariance.
- [bayesian.go:25](../internal/calibration/bayesian/bayesian.go:25) — Schema version naming matches mos. OK.
- [bayesian.go:104](../internal/calibration/bayesian/bayesian.go:104) — `map[string]map[int]Residual` is fragile across JSON encoders that produce numeric h-keys. Add regression test or accept-and-convert.

### bayesian/update.go

#### P1

- [update.go:21](../internal/calibration/bayesian/update.go:21) — `prior.Sigma <= 0` returns Posterior with negative Sigma intact. Downstream `math.Sqrt`, `ProbBelow` produce garbage silently. Floor to `TerminalSigma` or return NaN.

#### P2

- [update.go:30](../internal/calibration/bayesian/update.go:30) — `HoursRemaining == 0` step discontinuity in σ (0.7 → 0.5). Untested boundary.
- [update.go:60](../internal/calibration/bayesian/update.go:60) — `HoursRemaining < 0` not rejected; clock skew → silent prior-only.
- [update.go:46](../internal/calibration/bayesian/update.go:46) — Map miss → prior-only silently. No logging/counter; degraded production silently.
- [update.go:121-127](../internal/calibration/bayesian/update.go:121) — `LoadKernel` does not enforce VarDelta upper bound or EDelta sign convention.

#### P3

- [update.go:104](../internal/calibration/bayesian/update.go:104) — `os.ReadFile(path)` no `filepath.Clean` (acceptable per threat model).
- [update.go:74](../internal/calibration/bayesian/update.go:74) — `math.Sqrt(varPost)` safe (precPost > 0 always).

### bayesian/bayesian_test.go

#### P2

- [bayesian_test.go:165-189](../internal/calibration/bayesian/bayesian_test.go:165) — `TestUpdate_PrecisionWeightedMean_TMIN` doesn't lock down sign convention; passes if E[Δ] is misapplied.
- [bayesian_test.go:225-256](../internal/calibration/bayesian/bayesian_test.go:225) — `TestUpdate_WeightsSumProperly` doesn't include obs-only / prior-only branches.
- [bayesian_test.go:441-446](../internal/calibration/bayesian/bayesian_test.go:441) — `TestLoadKernel_MissingFile` doesn't assert `errors.Is(err, os.ErrNotExist)`.

### climate/climate.go

#### P1

- [climate.go:185-189, :290-295](../internal/calibration/climate/climate.go:185) — **Feb 29 obs silently aliased to March 1** (day 60 collision in `cumDays`). Acknowledged in comments; impact is small but the silent-collision pattern is dangerous.

#### P2

- [climate.go:227-229](../internal/calibration/climate/climate.go:227) — `RangeProb` returns NaN for both `lo == hi` (legitimate 0-width) and `lo > hi` (programmer error). Distinguish.
- [climate.go:107-110](../internal/calibration/climate/climate.go:107) — Malformed `valid_date` silently dropped without count.
- [climate.go:111-121](../internal/calibration/climate/climate.go:111) — Malformed `tmax_f` silently dropped without count.
- [climate.go:29](../internal/calibration/climate/climate.go:29) — `DefaultShrinkage = 0.2` consumed only via the constant; no test on the constant value.
- [climate.go:131-159](../internal/calibration/climate/climate.go:131) — `compile()` is O(N × 366); docstring claims constant-time which is only true for query.
- [climate.go:208, :237, :202-203](../internal/calibration/climate/climate.go:208) — **`BracketProb` (above=65 → ≥65) and `RangeProb` (hi=65 → strict <65) have inconsistent boundary inclusivity.** A value =65 is counted in `BracketProb(above)` and `BracketProb(below)` both. Pick a convention.

#### P3

- [climate.go:75-123](../internal/calibration/climate/climate.go:75) — 5× manual `_ = f.Close()` instead of `defer`.
- [climate.go:323](../internal/calibration/climate/climate.go:323) — `cumDays` is mutable `var`; should be array.
- [climate.go:279-287](../internal/calibration/climate/climate.go:279) — `maxIndex` -1 on empty; document contract.

#### nit

- [climate.go:160](../internal/calibration/climate/climate.go:160) — Const declared after first use.

### climate/climate_test.go

#### P2

- [climate_test.go:50-52](../internal/calibration/climate/climate_test.go:50) — Test bound [0.4, 0.65] hides boundary-inclusivity regression.
- [climate_test.go:96](../internal/calibration/climate/climate_test.go:96) — Conflates `lo>hi` and `lo==hi` NaN cases.
- [climate_test.go:198-206](../internal/calibration/climate/climate_test.go:198) — `formatInt` reinvents `strconv.Itoa`.

### climate/climate_bench_test.go

#### nit

- [climate_bench_test.go:13](../internal/calibration/climate/climate_bench_test.go:13) — Stale docstring.

### cliparse/cliparse.go

#### P1

- [cliparse.go:85, :180](../internal/calibration/cliparse/cliparse.go:85) — `zip.OpenReader` + unbounded `io.ReadAll(rc)` = **zip-bomb susceptibility** for third-party IEM archives. Add `LimitedReader` cap (~1 MB/entry); cap entry count.

#### P2

- [cliparse.go:118-124](../internal/calibration/cliparse/cliparse.go:118) — In-place filter `in := records[:0]` shares backing array; fragile.
- [cliparse.go:127-129](../internal/calibration/cliparse/cliparse.go:127) — `sort.Slice` lex compare on ISO dates (correct but worth a comment).
- [cliparse.go:155, :331](../internal/calibration/cliparse/cliparse.go:155) — `eachDate` re-parses Start/End 3× per Run.
- [cliparse.go:345-352](../internal/calibration/cliparse/cliparse.go:345) — `eachDate` silently swallows parse errors.
- [cliparse.go:218-220](../internal/calibration/cliparse/cliparse.go:218) — `parseBulletin` breaks on first MAX/MIN; YESTERDAY+TODAY both-blocks behavior is undocumented.
- [cliparse.go:188](../internal/calibration/cliparse/cliparse.go:188) — `parseIssuedAt` assumes UTC filename suffix; uncommented.
- [cliparse.go:241-244](../internal/calibration/cliparse/cliparse.go:241) — Year range 1900-2100 untested at boundary.
- [cliparse.go:54-57](../internal/calibration/cliparse/cliparse.go:54) — Regex anchored at start, not end (corner cases handled but undocumented).
- [cliparse.go:184](../internal/calibration/cliparse/cliparse.go:184) — `io.ReadAll(rc)` per-entry has no size limit.

#### P3

- [cliparse.go:140](../internal/calibration/cliparse/cliparse.go:140) — `defer f.Close()` after `os.Create` doesn't check error.
- [cliparse.go:131-141](../internal/calibration/cliparse/cliparse.go:131) — `out` lifetime mixed with conditional `f`.
- [cliparse.go:283-289](../internal/calibration/cliparse/cliparse.go:283) — Dedup lex tie-break is weakly motivated.

#### nit

- [cliparse.go:69-70](../internal/calibration/cliparse/cliparse.go:69) — Logger boilerplate; consider helper.

### cliparse/cliparse_test.go

#### P2

- [cliparse_test.go:259-300](../internal/calibration/cliparse/cliparse_test.go:259) — Test asserts log contains "skipping" but not that gap-report shows the missing date.
- [cliparse_test.go:140-150](../internal/calibration/cliparse/cliparse_test.go:140) — Path-traversal-style names untested.
- [cliparse_test.go:201-235](../internal/calibration/cliparse/cliparse_test.go:201) — No gap-report golden file.

#### Testdata gaps

- No fixture with both YESTERDAY *and* TODAY temperature blocks.
- No fixture with `RECORD`-tied numeric value.
- No fixture with year > 2030 (year-2100 boundary).

### mos/mos.go

#### P1

- [mos.go:127](../internal/calibration/mos/mos.go:127) — `holdoutDays=0` does not mean "no holdout"; pairs on maxDate fall into holdout silently. Test invariant violation.
- [mos.go:150](../internal/calibration/mos/mos.go:150) — **`FittedAt: time.Now().UTC()` injects wall-clock into deterministic math.** Reproducibility broken: identical inputs produce different artifacts.

#### P2

- [mos.go:104](../internal/calibration/mos/mos.go:104) — `MinTrainPerBin = 10` permits unstable σ from 8 d.o.f. fits.
- [mos.go:211](../internal/calibration/mos/mos.go:211) — `denom <= 0` guard doesn't catch near-zero (use relative tolerance).
- [mos.go:111](../internal/calibration/mos/mos.go:111) — `Fit` does not validate `Pair.Variable` (typos silently create new bins).
- [mos.go:114](../internal/calibration/mos/mos.go:114) — Empty-pairs error message lacks station/season/variable context.
- [mos.go:268-273](../internal/calibration/mos/mos.go:268) — `ProbBelow` falls back to indicator for sigma≤0; should return NaN.
- [mos.go:316-339](../internal/calibration/mos/mos.go:316) — `LoadCoefficients` uses stdlib `log.Printf` (slog elsewhere).
- [mos.go:330](../internal/calibration/mos/mos.go:330) — `len(c.Bins) == 0` hard error on load; soft warn would be friendlier for programmatic extension.
- [mos.go:325-328](../internal/calibration/mos/mos.go:325) — Schema-version error includes path twice.
- [mos.go:333](../internal/calibration/mos/mos.go:333) — Legacy default `[ModelGFS]` is a math correctness risk if old files were fit on different model.

#### P3

- [mos.go:36-42](../internal/calibration/mos/mos.go:36) — Comment "station-local civil date, midnight UTC" ambiguous.
- [mos.go:286-299](../internal/calibration/mos/mos.go:286) — NH-only season hardcoding (documented).
- [mos.go:344-356](../internal/calibration/mos/mos.go:344) — `CanonicalModelSet` nil input returns empty slice.
- [mos.go:223](../internal/calibration/mos/mos.go:223) — `sigma := math.Sqrt(ss / float64(n-1))` Bessel correction OK at n=10 but worth a comment.

### mos/mos_test.go

#### P2

- [mos_test.go:418](../internal/calibration/mos/mos_test.go:418) — Test uses `time.Now()` for `FittedAt` (perpetuating wall-clock pattern).
- [mos_test.go:36-53](../internal/calibration/mos/mos_test.go:36) — Tail-tolerance too loose (1e-3).
- [mos_test.go:128-152](../internal/calibration/mos/mos_test.go:128) — `TestFitProducesMultipleBins` uses uncorrelated data; doesn't validate σ.
- [mos_test.go:172-175](../internal/calibration/mos/mos_test.go:172) — No "some bins drop, some keep" test.
- [mos_test.go:270-331](../internal/calibration/mos/mos_test.go:270) — `nHoldout == 30` boundary not tested.
- [mos_test.go:337-377](../internal/calibration/mos/mos_test.go:337) — Test promise broken by holdoutDays=0 inclusivity bug.

### Cross-cutting (calibration)

- **Magic numbers without tests of their value**: `MinKernelN=5`, `MinObsVariance=0.25`, `TerminalSigma=0.5`, `MinTrainPerBin=10`, `MinHoldoutForSigma=30`, `DefaultShrinkage=0.2`, `SeasonWindowDays=30`. Add lockdown tests.
- **JSON unmarshal validation is partial**: neither package uses `DisallowUnknownFields`. A kernel with typo'd field name loads silently.
- **Schema versions are checked but migration policy is undocumented.**

---

## `internal/journal/` + `internal/replay/`

The journal package is in better shape than the surrounding code — ADR-0011 was implemented carefully. Replay has a few quiet correctness gaps.

### journal/journal.go

#### P1

- [journal.go:354-362](../internal/journal/journal.go:354), [cmd/tape/main.go:354-362](../cmd/tape/main.go:354) — **`cmd/tape` never wires the ADR-0011 alarm hook.** Every prod deploy logs the default-sink warn line and runs blind to drops. The ADR's OPERATIONS.md §1 detection check is guaranteed to fail.
- [journal.go:484](../internal/journal/journal.go:484) — Drop-rate computation: at default `AlarmThreshold=1` + `AlarmSustain=60s`, **a single drop per second sustained 60s trips the alarm.** Far more sensitive than the ADR text implies.
- [journal.go:497-499](../internal/journal/journal.go:497) — **`OnDropAlarm` invoked synchronously in writer goroutine with no panic-recover.** A panic kills the goroutine → `Write` keeps enqueuing → 16k frames later, drops everything silently with no log. **Single most damaging failure mode in this package.** Wrap hook with `defer recover()`.
- [journal.go:336-342](../internal/journal/journal.go:336) — `flushAndSync` errors logged once then ignored; subsequent writes against the bufio buffer may produce torn records.

#### P2

- [journal.go:332-343](../internal/journal/journal.go:332) — `flushAndSync` doesn't return error; rotate/Close treat un-Synced files as durable.
- [journal.go:351-352](../internal/journal/journal.go:351) — `_ = f.Close()` in rotate before gzip; truncated `.gz` possible.
- [journal.go:357-364](../internal/journal/journal.go:357) — `gzipFile` + `os.Remove` not atomic; killed process leaves both `.jsonl` and `.jsonl.gz`.
- [journal.go:368-371](../internal/journal/journal.go:368) — `encBuf` reset missing after failed encode.
- [journal.go:390-394](../internal/journal/journal.go:390) — Drain-mode never reads from `j.in`; channel becomes eternally full + leaks memory.
- [journal.go:481-485](../internal/journal/journal.go:481) — Drop-rate uses configured tickSec, not actual elapsed (GC stall skews).
- [journal.go:474-499](../internal/journal/journal.go:474) — `checkDropAlarm` uses atomic for fields only written by the writer goroutine (premature defense).
- [journal.go:415-426](../internal/journal/journal.go:415) — Shutdown drain may call `writeOne` against nil `bw` after rotate-fail.
- [journal.go:233-237](../internal/journal/journal.go:233) — Per-frame payload allocation; consider `sync.Pool` (Phase 6).

#### P3

- [journal.go:510-512](../internal/journal/journal.go:510) — `newPath` docstring wrong (source dir is added by caller).
- [journal.go:155](../internal/journal/journal.go:155) — Error message doesn't distinguish which required field is missing.
- [journal.go:118](../internal/journal/journal.go:118) — Comment implies per-source but Journal is per-Dir.
- [journal.go:128-131](../internal/journal/journal.go:128) — Doc references ADR-0011 §5 (no such section).
- [journal.go:295-298](../internal/journal/journal.go:295) — `alarmCheckInterval` package var read once for ticker, fresh inside checkDropAlarm — asymmetry.

### journal/journal_test.go

#### P2

- [journal_test.go:88-100](../internal/journal/journal_test.go:88) — `TestJournalDropsOnFullQueue` bound is too weak.
- [journal_test.go:507-556](../internal/journal/journal_test.go:507) — Wall-clock-bound; flake risk under CI load.
- [journal_test.go:631-657](../internal/journal/journal_test.go:631) — No test for shutdown crossing rotation boundary.
- [journal_test.go:665-699](../internal/journal/journal_test.go:665) — Write-during-Close race not actually probed.
- [journal_test.go:441-485](../internal/journal/journal_test.go:441) — Doesn't assert drop counter is monotonic.
- [journal_test.go:306-325](../internal/journal/journal_test.go:306) — `time.Sleep(80ms)` for tick wait; use `Eventually`.

#### P3

- [journal_test.go:381-398](../internal/journal/journal_test.go:381) — `syncBuf` duplicates a stdlib pattern.
- [journal_test.go:25-42](../internal/journal/journal_test.go:25) — `newTestJournal` silently ignores alarm config; document.

### replay/reader.go

#### P1

- [reader.go:134-136](../internal/replay/reader.go:134) — **`sonic.Unmarshal` failures silently skipped with no counter and no log.** Multi-day replay could silently drop 10% of frames from a single corrupt rotation.
- [reader.go:142-144](../internal/replay/reader.go:142) — `bufio.ErrTooLong` halts entire walk; single corrupt line aborts multi-day backtest.

#### P2

- [reader.go:64-78](../internal/replay/reader.go:64) — `.jsonl` file that's actually gzipped (mid-rotation rename crash) reads as raw with no warning.
- [reader.go:118-124](../internal/replay/reader.go:118) — gzip Close error (CRC trailer) silently dropped.
- [reader.go:125-126](../internal/replay/reader.go:125) — 64 KiB / 8 MiB buffer constants unconnected to writer buffer.
- [reader.go:106-115](../internal/replay/reader.go:106) — `io.EOF` early-stop contract untested at last-frame-of-last-file boundary.

#### P3

- [reader.go:80-89](../internal/replay/reader.go:80) — `gzReader.Close` error preserve via `errors.Join`.
- [reader.go:43-60](../internal/replay/reader.go:43) — `ListJournals` non-recursive; brittle for date-subdir callers.

### replay/orderbook.go

#### P1

- [orderbook.go:122-131](../internal/replay/orderbook.go:122) — **`ApplyFrameToBooks` on snapshot error leaves an EMPTY book in `books[ticker]`.** Subsequent deltas apply against empty state → phantom levels diverging from reality silently. **Real state-machine bug.**
- [orderbook.go:60-71](../internal/replay/orderbook.go:60) — `ApplySnapshot` silently tolerates malformed levels with `continue`; 5 of 50 levels missing without operator signal.
- [orderbook.go:101-105](../internal/replay/orderbook.go:101) — `newQty <= 0` removes level "defensively"; a buggy huge negative delta wipes price level silently.

#### P2

- [orderbook.go:116-144](../internal/replay/orderbook.go:116) — `malformed` bool conflates real malformed-snapshot with delta-before-snapshot. Return typed result.
- [orderbook.go:13-25](../internal/replay/orderbook.go:13) — `Yes`/`No` maps exported (anyone can mutate). Seal access.
- [orderbook.go:167-174](../internal/replay/orderbook.go:167) — `MidYesCents` integer division loses 0.5¢.
- [orderbook.go:178-185](../internal/replay/orderbook.go:178) — `SpreadYesCents` returns 100 sentinel for both "wide spread" and "one side empty"; provide `IsOneSided()`.

#### nit

- [orderbook.go:213-216](../internal/replay/orderbook.go:213) — `priceDollarsToCents` permits exact 0 or 1.0.
- [orderbook.go:219-227](../internal/replay/orderbook.go:219) — `maxKey` O(n); fine at Kalshi book sizes.

### replay/kalshi.go

#### P2

- [kalshi.go:34-37](../internal/replay/kalshi.go:34) — `MarketTicker` returns `""` on parse error indistinguishably from missing field. Return `(string, error)`.
- [kalshi.go:46-53](../internal/replay/kalshi.go:46) — `SeriesPrefix` empty-input vs leading-hyphen both return `""`.

#### nit

- [kalshi.go:6](../internal/replay/kalshi.go:6) — Sonic / stdlib JSON mixing across the three parse layers.

### Tests (replay)

#### P2

- [orderbook_test.go:147-187](../internal/replay/orderbook_test.go:147) — No test for "snapshot fails after newly-allocating empty book" — the P1 state-machine bug.
- [orderbook_test.go:189-207](../internal/replay/orderbook_test.go:189) — No test for duplicate-snapshot mid-tape.
- [reader_test.go:181-200](../internal/replay/reader_test.go:181) — No test for `ErrTooLong` rejection at boundary.
- [reader_test.go:84-91](../internal/replay/reader_test.go:84) — Corrupt-CRC-trailer (truncated mid-file) untested.

#### P3

- [reader_test.go:131-148](../internal/replay/reader_test.go:131) — Silent-skip test pins the *defect* in place.
- [orderbook_test.go:84-89](../internal/replay/orderbook_test.go:84) — Empty side untested.

#### nit

- [orderbook_bench_test.go:51-69](../internal/replay/orderbook_bench_test.go:51) — Bench skips envelope/sonic-unmarshal cost; undersizes real workload.

### Cross-cutting (journal + replay)

1. **`OnDropAlarm` panic-unsafe** — P1, converts a Phase 6 wiring bug into total silent tape loss.
2. **`cmd/tape` never installs an alarm hook** — ADR-0011 detection check broken on every prod deploy.
3. **Orderbook snapshot-error leaves empty book** — state-machine bug invisible to current tests.
4. **Replay silently skips malformed/oversized lines** — hides tape corruption.
5. **Several IO error paths in journal** ignore errors after a single log line.

---

## utility packages: `internal/{config, env, obs, clock, retry, market, openmeteo}`

### internal/config

#### P1

- [config.go:227-269](../internal/config/config.go:227) — `Validate()` is non-strict for poison fields when source is disabled. A typo in `kalshi.ws_url` while `kalshi.enabled=false` accepts; flipping enabled to true later silently surfaces drift in prod.
- [config.go:231-236](../internal/config/config.go:231) — No upper-bound check on `Journal.QueueSize` or `MaxFileBytes`. Typo'd `queue_size: 16384000` allocates 16M channel silently.
- [config.go:38-42](../internal/config/config.go:38) — No `Reconnect.MaxBackoff >= InitialBackoff` validation.

#### P2

- [config.go:150](../internal/config/config.go:150) — `stationCoordTolerance = 0.001` is package-level constant; non-configurable.
- [config.go:256-265](../internal/config/config.go:256) — No validation that `OpenMeteo.Variables` is non-empty when enabled.
- [config.go:211-213](../internal/config/config.go:211) — `ISOBAR_DATA_DIR` accepted without path sanitization.
- [config.go:91-113](../internal/config/config.go:91) — Magic constants without named defaults: `256 << 20`, `1*time.Hour`, `250*time.Millisecond`, `16384`, `4*time.Hour`, `30*time.Minute`, `3`.
- [config.go:55-56](../internal/config/config.go:55) — Comment "0 disables" but actual default is 4h; never set to 0.

#### P3

- [config.go:216-223](../internal/config/config.go:216) — `firstNonEmpty` confuses unset with empty.
- [config.go:122](../internal/config/config.go:122) — `//nolint:gosec` comment is stale.

#### nit

- [config.go:180-189](../internal/config/config.go:180) — `validateStationCoords` returns first-mismatch only; use `errors.Join`.
- [config.go:109](../internal/config/config.go:109), [openmeteo/client.go:116](../internal/openmeteo/client.go:116) — `gfs_seamless` default duplicated.

### internal/env

#### P1

- [env.go:115-118](../internal/env/env.go:115) — `parse()` silently drops malformed lines (no `=`).
- [env.go:107-108](../internal/env/env.go:107) — No UTF-8 BOM stripping (Windows-edited `.env` silently breaks first key).
- [env.go:107](../internal/env/env.go:107) — CRLF handling depends on TrimSpace; embedded `\r` not handled.

#### P2

- [env.go:28-30](../internal/env/env.go:28) — `os.Setenv` error returned un-prefixed (no key context).
- No test for duplicate keys in `.env`.

#### P3

- [env.go:66, :94](../internal/env/env.go:66) — Symlink follow semantics not validated against special files.
- [env.go:44-53](../internal/env/env.go:44) — `LoadNearest` doesn't log the chosen path.

#### nit

- [env.go:108, :134](../internal/env/env.go:108) — Loop variable shadowing.
- [env.go:106](../internal/env/env.go:106) — No size hint on map.

#### Tests

- [env_test.go](../internal/env/env_test.go) — No BOM, no CRLF, no malformed-line test.

### internal/obs

#### P1

- [log.go:16-32](../internal/obs/log.go:16) — **No redaction layer.** Sensitive fields (`pem`, `private_key`, `api_key`, `token`) not auto-redacted via `ReplaceAttr`. Convention-only protection.

#### P2

- [log.go:19](../internal/obs/log.go:19) — `Init` uses `os.Stderr` unconditionally; tests hijack via pipe.
- [log.go:19](../internal/obs/log.go:19) — JSON format stable; no source/line attached (fine for shipper).
- No way to inject `slog.Handler` for tests.

#### P3

- [log.go:42-44](../internal/obs/log.go:42) — `parseLevel` silently falls back to Info on typo.

#### nit

- `Init` returns logger AND calls `slog.SetDefault` (two side effects).

### internal/clock

#### P1

- [clock.go:18](../internal/clock/clock.go:18) — **`processStart = time.Now()` not injectable.** No interface, no swap-in. **All downstream "tests should be deterministic" claims are aspirational.** Add `var now = time.Now` (swap-in-test pattern).

#### P2

- [clock.go:18](../internal/clock/clock.go:18) — `processStart` shared package state; `t.Parallel()` flake potential.
- [clock.go:26](../internal/clock/clock.go:26) — `Stamp.Mono` uses `time.Sub` which mixes monotonic and wall axes (safe but document).

#### P3

- [clock.go:14-15](../internal/clock/clock.go:14) — JSON tags `t`/`m` differ from slog's `ts` convention.

#### nit

- [clock.go:30](../internal/clock/clock.go:30) — Docstring says "nanoseconds" but returns `time.Duration`.

#### Tests

- No clock injectability test (because it doesn't exist).
- [clock_test.go:71](../internal/clock/clock_test.go:71) — `time.Sleep(time.Millisecond)` flake risk.

### internal/retry

#### P1

- ctx-aware everywhere. `Do` checks `ctx.Err()` before first call, after each `fn`, and in the sleep select. **Clean.**
- `Do` returns `nil` on ctx cancellation (documented). Callers must check ctx.Err() themselves. openmeteo does; kalshi/ws.go callers need separate verification.

#### P2

- [retry.go:20, :37](../internal/retry/retry.go:20) — `math/rand/v2` global (per-CPU, auto-seeded). Good.
- [retry.go:43-45](../internal/retry/retry.go:43) — `Next` returns `500ms` for `prev <= 0` hardcoded.
- [retry.go:29](../internal/retry/retry.go:29) — `MaxAttempts == 0` means unlimited; operator-dangerous.
- [retry.go:66](../internal/retry/retry.go:66) — `MaxBackoff` default hardcoded.

#### P3

- [retry.go:87](../internal/retry/retry.go:87) — Logger field set differs from openmeteo's per-attempt log; potential operator confusion.

#### nit

- [retry.go:92](../internal/retry/retry.go:92) — `time.After` GC-safe on Go 1.23+; we're on 1.26.

### internal/market

#### P1 — DEAD CODE

- **`market.ParseTicker` and `market.Ticker` are unused outside this package.** Grep confirms only `internal/market/{ticker_test.go,bench_test.go}` call them. Live parser is `internal/edge/ticker.go:95` with different semantics. **Delete `market/ticker.go` and `market/ticker_test.go`** — saves ~165 LOC, removes the two-parser ambiguity called out in Phase 0.

#### P2

- [stations.go:113-144](../internal/market/stations.go:113) — Linear scans documented as faster than map for N=4. Confirmed by [bench_test.go:19-32](../internal/market/bench_test.go:19).
- [ticker.go:98-101](../internal/market/ticker.go:98) — `ParseTicker` does not validate temp range. Moot if deleted.

#### P3

- [stations.go:99](../internal/market/stations.go:99) — `AllStationsCanonical` is alias for `AllStations`; remove once Phase 1 stabilizes.
- [stations.go:62, 68, 74, 80](../internal/market/stations.go:62) — `UTCOffsetHours` hardcoded to DST; would be wrong post-Nov 2 if recording window extends.

### internal/openmeteo

#### P1

- Retry envelope well-bounded (1s, 30s, 4 attempts, ≈7s worst-case dwell). Good — addresses Phase 0 issue #3.
- ctx threaded correctly throughout. Verified at multiple sites.
- No goroutine leak risk in this package.
- `http.Client` timeout default 30s. Bounded.

#### P2

- No total wall-clock budget enforcement on a poll cycle.
- [client.go:156-173](../internal/openmeteo/client.go:156) — `fetchAll` iterates serially; one slow location delays others.
- [client.go:368](../internal/openmeteo/client.go:368) — `32 << 20` body limit unnamed magic.
- [client.go:289, :262-267](../internal/openmeteo/client.go:289) — `attempt` zero-indexed vs `attemptsUsed` one-indexed log inconsistency.
- [client.go:307-313](../internal/openmeteo/client.go:307) — `nextBackoffFor` quadratic across envelope (trivial at MaxAttempts=4).
- No metric for retry-recovered fetches.

#### P3

- [client.go:320-340](../internal/openmeteo/client.go:320) — 408 (Request Timeout) treated as non-retryable.
- [client.go:348-349](../internal/openmeteo/client.go:348) — Lat/lon 4-dp rounding silent.
- [client.go:360](../internal/openmeteo/client.go:360) — No User-Agent header.

#### nit

- [client.go:1-25](../internal/openmeteo/client.go:1) — Package comment is excellent.

#### Tests

- [client_test.go:148](../internal/openmeteo/client_test.go:148) — Wall-clock-bound (2s wait); flake risk.
- No `FetchFailuresTotal() == 0` test on fresh poller.
- [client_retry_test.go:217-274](../internal/openmeteo/client_retry_test.go:217) — **`TestPoller_BackoffEnvelope` is the audit's most important test for the retry-with-jitter envelope.** Tight bounds detect both "Jitter always returns 0" and "Next doesn't double" regressions.
- No retry-exhaustion log assertion.

---

## `cmd/`

### cmd/tape/main.go (PROD-RUNNING)

#### P1

- [cmd/tape/main.go:184, :321](../cmd/tape/main.go:184) — **`jrn.Close()` errors silently swallowed at deferred close.** Last few seconds of journalled data not guaranteed durable on disk and we'd never know. Propagate close errors into errgroup return.
- [cmd/tape/main.go:115](../cmd/tape/main.go:115) — `startKalshi` and `startOpenMeteo` share the same errgroup; an Open-Meteo fatal kills the high-value Kalshi WS stream. Decouple or document.

#### P2

- [cmd/tape/main.go:211-215](../cmd/tape/main.go:211) — `runRediscovery` (good — Phase 0 fixed it into errgroup) but spawned from inside `startKalshi`'s body. Subtle; comment.
- [cmd/tape/main.go:144-167](../cmd/tape/main.go:144) — `writeBootFrame` swallows `jrn.Write` (boot frame may be silently dropped if queue full).
- [cmd/tape/main.go:144](../cmd/tape/main.go:144) — `writeBootFrame` returns void; json.Marshal failure → no boot frame.
- [cmd/tape/main.go:41-43](../cmd/tape/main.go:41) — Build-info defaults `"dev"/"none"/"unknown"` stamped into permanent journal frames. Refuse to run if `commit == "none"` in prod, or warn loudly.
- [cmd/tape/main.go:102](../cmd/tape/main.go:102) — `computeConfigSHA` re-reads config file; race window between Load and SHA.

#### P3

- [cmd/tape/main.go:309-314](../cmd/tape/main.go:309) — `sample` returns sub-slice without copy.
- [cmd/tape/main.go:396-401](../cmd/tape/main.go:396) — `redactKeyID` uses U+2026 `…`; log greps with ASCII `...` won't match.
- [cmd/tape/main.go:127](../cmd/tape/main.go:127) — `bootSchemaVersion` convention unenforced by tests.
- [cmd/tape/main.go:48](../cmd/tape/main.go:48) — `--version` doesn't `os.Exit(0)` explicitly.
- [cmd/tape/main.go:225](../cmd/tape/main.go:225) — Panic-recover test only exercises pre-loop path.

#### nit

- [cmd/tape/main.go:46, :80](../cmd/tape/main.go:46) — Stale doc + dead "empty path" branch.

### cmd/tape/main_test.go

#### P2

- [main_test.go:175-197](../cmd/tape/main_test.go:175) — 5s timeout for sub-1ms operation; tighten to 100ms.
- [main_test.go:234-247](../cmd/tape/main_test.go:234) — Helpers with nil signer + `example.invalid` URLs are fragile.

#### nit

- [main_test.go:138](../cmd/tape/main_test.go:138) — `filepath.Walk` deprecated; use `WalkDir`.

### cmd/edge-scan/main.go

#### P2

- [edge-scan/main.go:39-40](../cmd/edge-scan/main.go:39), [:322](../cmd/edge-scan/main.go:322) — `-edge-threshold-cents` ignored in `writeSummary` (hardcoded ≥5).
- [edge-scan/main.go:156-219, :223](../cmd/edge-scan/main.go:156) — `writeCSV` not atomic; killed process leaves partial.
- [edge-scan/main.go:73-75](../cmd/edge-scan/main.go:73) — `MkdirAll` no path-traversal sanitization (acceptable).
- [edge-scan/main.go:31](../cmd/edge-scan/main.go:31) — **No signal handling.** 798MB tape runs take minutes; SIGINT leaves corrupted CSV.
- [edge-scan/main.go:80](../cmd/edge-scan/main.go:80) — `end.AddDate(0,0,1)` comment + code mismatched.

#### P3

- [edge-scan/main.go:216-219, :343-347](../cmd/edge-scan/main.go:216) — Close error joins.
- [edge-scan/main.go:316-340](../cmd/edge-scan/main.go:316) — Inline struct readability.

#### nit

- [edge-scan/main.go:188](../cmd/edge-scan/main.go:188) — `'f', -1, 64` for BracketKey produces variable precision.
- [edge-scan/main.go:392-397](../cmd/edge-scan/main.go:392) — `formatFloatOrBlank` handles NaN but not Inf.

### cmd/calibrate-fit/main.go

#### P2

- [calibrate-fit/main.go:113, :155](../cmd/calibrate-fit/main.go:113) — `defer f.Close()` swallows close errors.
- [calibrate-fit/main.go:39](../cmd/calibrate-fit/main.go:39) — `holdoutDays = 90` default undocumented.
- [calibrate-fit/main.go:77-95](../cmd/calibrate-fit/main.go:77) — `loadAllPairs` over-abstracts `*slog.Logger` to anonymous interface.
- [calibrate-fit/main.go:80](../cmd/calibrate-fit/main.go:80) — `ctx.Err()` checked at top of station loop but CSV readers don't accept ctx.
- [calibrate-fit/main.go:137-144, :184-191](../cmd/calibrate-fit/main.go:137) — Silently-skipped rows on parse errors; no surfacing.

#### P3

- [calibrate-fit/main.go:281-283](../cmd/calibrate-fit/main.go:281) — Missing `f.Sync()` between Encode and Rename.
- [calibrate-fit/main.go:303-309](../cmd/calibrate-fit/main.go:303) — Reinvents `strings.Repeat`.
- [calibrate-fit/main.go:232-251](../cmd/calibrate-fit/main.go:232) — `requireColumns` is O(headers+names).

### cmd/calibrate-parse-cli/main.go

#### P2

- [calibrate-parse-cli/main.go:23-53](../cmd/calibrate-parse-cli/main.go:23) — No CLI-side `start <= end` check.
- [calibrate-parse-cli/main.go:32-35](../cmd/calibrate-parse-cli/main.go:32) — Required-flag check is condensed; mirror calibrate-bayesian's per-flag pattern.

#### P3

- [calibrate-parse-cli/main.go:56-59](../cmd/calibrate-parse-cli/main.go:56) — `data/calibration/obs` hardcoded; share const with calibrate-fit.

### cmd/calibrate-bayesian/main.go (SCAFFOLD)

#### P2

- [calibrate-bayesian/main.go:73-80](../cmd/calibrate-bayesian/main.go:73) — Prints "not yet implemented" to stderr but exits 0; Makefile chains silently pass.
- [calibrate-bayesian/main.go:23-29](../cmd/calibrate-bayesian/main.go:23) — No signal handling; add now so eventual implementation PR is pure algorithm.

#### Recommendation

**Keep as scaffold; do not move to experiments/.** (1) ADR-0010 tracks it; (2) flags pre-defined for downstream wrappers; (3) deleting orphans the bayesian package's intended consumer. **Action**: tighten exit code to `os.Exit(2)` so chains catch the no-op.

### cmd/peek/main.go (DEAD)

#### P1

- [peek/main.go:35](../cmd/peek/main.go:35) — **Hardcoded Kalshi prod URL.** Bypasses `cfg.Kalshi.APIBase` indirection.
- [peek/main.go:41-52](../cmd/peek/main.go:41) — 26 hardcoded series candidates; the discovery output has already been encoded in config.

#### P2

- [peek/main.go:20-22](../cmd/peek/main.go:20) — `.env` load failure is warn-then-proceed (cmd/tape now fails fast).
- [peek/main.go:37](../cmd/peek/main.go:37) — Magic 120s timeout.
- [peek/main.go:92-100](../cmd/peek/main.go:92) — Third copy of `loadPEM`.
- [peek/main.go:62-64](../cmd/peek/main.go:62) — Error output to stdout, not stderr.

#### Recommendation

**DELETE.** Reasoning:
1. Series-discovery question is answered (encoded in config.yaml)
2. Uses hardcoded prod URL (regression vs other binaries)
3. Duplicates `loadPEM` in a drift-prone way
4. No test coverage; not in `make build`
5. Maintenance liability with zero current value

If product wants to keep it: move to `experiments/peek/`.

### cmd/analyze/main.go

#### P2

- [analyze/main.go:23](../cmd/analyze/main.go:23) — `time.Now().UTC()` evaluated at flag-registration; --help shows stale date.
- [analyze/main.go:77-117](../cmd/analyze/main.go:77) — `_ = replay.ForEachFrame(...)` discards error.
- [analyze/main.go:36-44](../cmd/analyze/main.go:36) — Date typo (no dashes) → silent empty output.
- [analyze/main.go:28](../cmd/analyze/main.go:28) — `--source` not validated against allowed set.
- [analyze/main.go:179-187](../cmd/analyze/main.go:179) — Magic numbers `0`/`100` for one-sided market detection.

#### P3

- [analyze/main.go:159](../cmd/analyze/main.go:159) — `sort.Slice` → `slices.SortStableFunc` on Go 1.26.
- [analyze/main.go:75](../cmd/analyze/main.go:75) — Redundant `bookErrors = 0` init.
- [analyze/main.go:283-292](../cmd/analyze/main.go:283) — `percentile` re-sorts per call.
- [analyze/main.go:264-324](../cmd/analyze/main.go:264) — `hist` type promote-to-internal comment is accurate.

#### nit

- [analyze/main.go:130, :135](../cmd/analyze/main.go:130) — Magic `20` vs `10` for top-N.

### Cross-cutting (cmd/*)

1. **Inconsistent error-print conventions** — `logger.Error` vs `fmt.Fprintln(os.Stderr)`. Pick a house style.
2. **Exit-code convention partially observed** — `2` for usage / `1` for runtime not enforced everywhere. cmd/analyze never exits non-zero.
3. **No binary `--help` documents output paths.**
4. **Hardcoded paths**: `data/calibration/obs` in two places, `./data` in two more. None honor `ISOBAR_DATA_DIR`.
5. **Three different ways of loading the Kalshi PEM** — cmd/tape, cmd/peek, internal/config. Consolidate.
6. **`//nolint:gosec` applied liberally** — document policy.
7. **5 production binaries have zero test files** — `edge-scan`, `calibrate-fit`, `calibrate-parse-cli`, `analyze`, `peek`. Only `tape` has tests. For calibrate-fit specifically, this is a real gap (calibration artifact integrity).
8. **Three binaries install signal handlers** (tape, calibrate-fit, calibrate-parse-cli). Edge-scan doesn't despite long-running disk reads.

---

## Cross-package patterns

- **Station / canonical-coord tables drift across 3+ packages** ([internal/edge/scan.go:559](../internal/edge/scan.go:559), [internal/edge/poll_index.go:99](../internal/edge/poll_index.go:99), [internal/edge/ticker.go:67](../internal/edge/ticker.go:67), [internal/market/stations.go:51](../internal/market/stations.go:51)). Consolidate.
- **No injectable clock** anywhere. `internal/clock` exists but isn't injectable. All time-dependent tests are wall-clock-bound. Phase 6 should fix.
- **Silent skip-and-count is the dominant error policy** across edge, replay, journal, cliparse. Adds up to a lot of invisible degradation.
- **Mixed log backends**: stdlib `log.Printf` in [internal/edge/scan.go:195](../internal/edge/scan.go:195) and [internal/calibration/mos/mos.go:316-339](../internal/calibration/mos/mos.go:316); slog everywhere else.
- **Schema versions are checked but no migration policy exists** in any of the 3 schema-versioned files (mos, bayesian, isobar-tape.boot).
- **No `panic()` in production code anywhere** — the only `log.Fatal` is in cmd/* main.go entry points (acceptable).
- **No `t.Skip` in any package** — green light.
- **No persistent TODO/FIXME/XXX in production code** except two persistent FIXMEs in [internal/edge/resolution.go:28-65](../internal/edge/resolution.go:28).

---

## End of Phase 1

Per the cadence rule: **stopping here.** Phase 2 (architecture reconsider) waits for confirmation.

Phase 2 will look at: domain/transport/storage seams, interface contracts at consumer boundaries, package-by-tech vs package-by-domain layout, value-object invariants, where a clearance-grade reviewer would point and say "footgun", concurrency/retry/timeout/backpressure consistency, reusability across services.
