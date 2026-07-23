# reduction/01-deadcode.md — Phase 1 dead-code sweep

**HEAD**: `397004f` + uncommitted working tree (same-day review/polish pass — see BUILDLOG 2026-07-03)
**Date**: 2026-07-03
**Scope**: Phase 1 per `00-baseline.md` protocol — deletion only, no abstraction
**Methodology**: `golang.org/x/tools/cmd/deadcode@v0.44.0` over all 8 mains (61 flagged
symbols; raw output archived in session log), seeded with review findings F24/F26 and
baseline §4 candidates; every candidate whole-repo-grepped (incl. `_test.go`, docs,
scripts) and classified DELETE / KEEP before any edit; 4 parallel verify-then-delete
agents; gate = `make all` (lint+clockcheck+vet+race+build) green after each group.

## Headline

| Metric | Pre-sweep | Post-sweep | Δ |
|---|---:|---:|---:|
| Production LOC (excl. tests) | 11,739 | **11,206** | **−533** |
| Test LOC | 10,771 | 10,279 | −492 |
| `make all` | green | green | hold ✓ |
| `peek` stripped binary | 6.51 MB | 6.49 MB | −16 KB (dep-graph-bound; Phase-5 lever) |

Biggest single item: the `internal/kalshi` order-management/portfolio REST surface
(copied verbatim from arbhunter, never wired here) — **−888 LOC across 4 files**.
Side effect worth naming: ADR-0014's hard rail ("no code path that can place a real
order") is now **structural** — `CreateOrder`/`CancelOrder` no longer exist in the
module. Recovery path if/when Phase C unblocks cmd/bot: git history (ADR-0002
precedent: delete, don't freeze, what has no current caller).

## Deletions (file:line per removal, tool attribution per group)


### kalshi — flagged by deadcode

| Symbol | File:line | Note |
|---|---|---|
| `Client.GetMarket` | `internal/kalshi/client.go:152` | dead in prod; no non-test caller. Test TestGetMarket deleted; retry/backoff tests retargeted to GetMarketsPage. |
| `Client.GetMarketRaw` | `internal/kalshi/client.go:161` | dead; test TestGetMarketRaw deleted. |
| `Client.GetMarkets` | `internal/kalshi/client.go:166` | dead; test TestGetMarkets deleted. |
| `Client.GetEvents` | `internal/kalshi/client.go:244` | dead; test TestGetEvents deleted. |
| `Client.GetOrderbookRaw` | `internal/kalshi/client.go:253` | dead; test TestGetOrderbookRaw deleted. |
| `Client.GetOrdersRaw` | `internal/kalshi/client.go:258` | dead; test TestGetOrdersRaw deleted. |
| `Client.GetFillsRaw` | `internal/kalshi/client.go:263` | dead; test TestGetFillsRaw deleted. |
| `Client.GetOrderbook` | `internal/kalshi/client.go:269` | dead; test TestGetOrderbook deleted. |
| `fpLevelsToInt` | `internal/kalshi/client.go:291` | only called by GetOrderbook; test TestFPLevelsToIntDropsInvalid deleted. |
| `Client.CreateOrder` | `internal/kalshi/client.go:314` | dead; tests TestCreateOrder + CreateOrder case in TestAllEndpointsPropagateErrors deleted. |
| `Client.CancelOrder` | `internal/kalshi/client.go:326` | dead; tests TestCancelOrder, TestDeleteReturnsParsedBody deleted. |
| `Client.GetOrder` | `internal/kalshi/client.go:335` | dead; test TestGetOrder deleted. |
| `Client.GetOrders` | `internal/kalshi/client.go:344` | dead; test TestGetOrders deleted. |
| `Client.GetFills` | `internal/kalshi/client.go:355` | dead; test TestGetFills deleted. |
| `Client.GetPositions` | `internal/kalshi/client.go:364` | dead; test TestGetPositions deleted. |
| `Client.GetPositionsRaw` | `internal/kalshi/client.go:373` | dead; test TestGetPositionsRaw deleted. |
| `Client.GetBalance` | `internal/kalshi/client.go:379` | dead; test TestGetBalanceAndRaw deleted. |
| `Client.GetBalanceRaw` | `internal/kalshi/client.go:388` | dead; test TestGetBalanceAndRaw deleted. |
| `Client.GetRaw` | `internal/kalshi/client.go:395` | dead; tests TestGetRaw, TestGetRawPropagatesAPIError deleted. ADR-0006 updated. |
| `Client.getRaw` | `internal/kalshi/client.go:419` | orphaned: all callers (GetMarketRaw/GetOrderbookRaw/GetOrdersRaw/GetFillsRaw/GetPositionsRaw/GetBalanceRaw/GetRaw) deleted. |
| `Client.post` | `internal/kalshi/client.go:474` | orphaned: only caller CreateOrder deleted. |
| `Client.delete` | `internal/kalshi/client.go:496` | orphaned: only caller CancelOrder deleted. |
| `SideYes / SideNo` | `internal/kalshi/types.go:18` | const block; only used by deleted CreateOrderRequest/order tests. |
| `ActionBuy / ActionSell` | `internal/kalshi/types.go:24` | const block; only used by deleted order tests. |
| `TIFGoodTilCanceled / TIFFillOrKill / TIFImmediateOrCancel` | `internal/kalshi/types.go:30` | TIF const block; unreferenced after CreateOrderRequest deletion. |
| `parseFP` | `internal/kalshi/types.go:108` | only used by deleted Order/Fill FP methods; test cases removed from TestParseHelpersHandleEmpty, TestParseFPRoundsHalfUp deleted. |
| `parseFPCents` | `internal/kalshi/types.go:116` | only used by deleted Order/Fill methods; TestParseFPCentsRoundsHalfUp deleted. |
| `OrderbookLevel` | `internal/kalshi/types.go:126` | dead after GetOrderbook/fpLevelsToInt deletion. |
| `OrderbookLevelFP` | `internal/kalshi/types.go:129` | dead after fpLevelsToInt deletion. |
| `Orderbook` | `internal/kalshi/types.go:132` | dead after GetOrderbook deletion. |
| `OrderbookFP` | `internal/kalshi/types.go:138` | dead after GetOrderbook deletion. |
| `CreateOrderRequest` | `internal/kalshi/types.go:144` | dead after CreateOrder deletion. |
| `Order + YesPriceCents/TotalFillCount/RemainingQty` | `internal/kalshi/types.go:162` | dead after order methods deleted; tests TestOrderYesPriceCentsPrefersFP/TestOrderTotalFillCount/TestOrderRemainingQty deleted. |
| `Fill + YesPriceCents/Qty` | `internal/kalshi/types.go:222` | dead after GetFills deletion; test TestFillAccessors deleted. |
| `Position` | `internal/kalshi/types.go:255` | dead after GetPositions deletion. |
| `Event` | `internal/kalshi/types.go:268` | dead after GetEvents deletion; not used by any survivor. |
| `Balance` | `internal/kalshi/types.go:325` | dead after GetBalance deletion. |
| `getMarketResponse` | `internal/kalshi/types.go:277` | response wrapper; only used by deleted GetMarket. |
| `getOrderbookResponse` | `internal/kalshi/types.go:281` | response wrapper for deleted GetOrderbook. |
| `createOrderResponse` | `internal/kalshi/types.go:286` | response wrapper for deleted CreateOrder. |
| `getOrderResponse` | `internal/kalshi/types.go:290` | response wrapper for deleted GetOrder. |
| `getOrdersResponse` | `internal/kalshi/types.go:294` | response wrapper for deleted GetOrders. |
| `getFillsResponse` | `internal/kalshi/types.go:299` | response wrapper for deleted GetFills. |
| `getPositionsResponse` | `internal/kalshi/types.go:309` | response wrapper for deleted GetPositions. |
| `cancelOrderResponse` | `internal/kalshi/types.go:314` | response wrapper for deleted CancelOrder. |
| `getEventsResponse` | `internal/kalshi/types.go:319` | response wrapper for deleted GetEvents. |
| `import bytes / import math` | `internal/kalshi/client.go:4` | unused after post (bytes) and fpLevelsToInt (math) deletion. |

### market — flagged by deadcode + review-F26

| Symbol | File:line | Note |
|---|---|---|
| `StationByLocation` | `internal/market/stations.go:145` | Dead in production (deadcode.txt flags unreachable at stations.go:145). Only callers were tests. Function deleted. |
| `StationBySeries` | `internal/market/stations.go:158` | Dead in production (deadcode.txt flags unreachable at stations.go:158). edge.Spec.Station() uses a hardcoded switch, not this fn; audit/02-architecture.md on… |
| `TestStationByLocation` | `internal/market/stations_test.go:31` | Test whose only subject was StationByLocation. Deleted. |
| `TestStationBySeries` | `internal/market/stations_test.go:41` | Test whose only subject was StationBySeries. Deleted. |
| `BenchmarkStationBySeries` | `internal/market/bench_test.go:9` | Bench existed solely to benchmark the deleted StationBySeries. Entire file bench_test.go deleted. |
| `BenchmarkStationByLocation` | `internal/market/bench_test.go:17` | Bench existed solely to benchmark the deleted StationByLocation. Entire file bench_test.go deleted. |

### edge — flagged by deadcode + review-F24

| Symbol | File:line | Note |
|---|---|---|
| `KalshiFeeBPS` | `internal/edge/fee.go:20` | Legacy alias const for KalshiTakerFeeBPS. Zero non-test callers; only named in live docs (updated) and historical audit/ADR records. Deleted the const + its … |
| `FeePerContractCents` | `internal/edge/fee.go:82` | No-mode wrapper over FeePerContractCentsMode(_, FeeTaker). Zero non-test callers (scan.go uses the Mode variant). Deleted func + 'Preserved for backward comp… |
| `RoundTripFeeCents` | `internal/edge/fee.go:108` | No-mode wrapper; only caller was the (now-deleted) FeePerContractCents-based body. RoundTripFeeCentsMode survives and is the live path. deadcode.txt flagged … |
| `EdgeAfterFeeCents` | `internal/edge/fee.go:126` | No-mode wrapper over EdgeAfterFeeCentsMode(_,_,FeeTaker). Zero non-test callers. deadcode.txt flagged it unreachable. |
| `Resolution.Width` | `internal/edge/resolution.go:29` | Method (r Resolution) Width() float64. Zero callers anywhere incl tests; only self-named in a doc comment (updated) and audit/. deadcode.txt flagged it unrea… |
| `TestFeePerContractCents_TakerParity` | `internal/edge/fee_test.go:111` | Test whose sole purpose was asserting the deleted no-mode wrapper FeePerContractCents equals FeePerContractCentsMode(_, FeeTaker). Vacuous once the wrapper i… |

## Per-file LOC deltas

| File | Before | After | Δ |
|---|---:|---:|---:|
| `internal/kalshi/client.go` | 725 | 477 | -248 |
| `internal/kalshi/types.go` | 329 | 97 | -232 |
| `internal/kalshi/client_test.go` | 805 | 468 | -337 |
| `internal/kalshi/types_test.go` | 110 | 39 | -71 |
| `internal/market/stations.go` | 194 | 170 | -24 |
| `internal/market/stations_test.go` | 159 | 111 | -48 |
| `internal/market/bench_test.go` | 22 | 0 | -22 |
| `docs/ARCHITECTURE.md` | 484 | 483 | -1 |
| `internal/edge/fee.go` | 140 | 114 | -26 |
| `internal/edge/fee_test.go` | 257 | 243 | -14 |
| `internal/edge/resolution.go` | 175 | 172 | -3 |
| `internal/edge/scan_test.go` | 708 | 692 | -16 |
| `docs/ARCHITECTURE.md` | 617 | 616 | -1 |

## Kept — the standing explanation for future `make deadcode` runs

`deadcode` sees reachability from `main()` only; everything below is flagged by the
tool but is a deliberate retention. **The recurring signal is the diff against this
list, not zero output.**

| Symbol | Why kept |
|---|---|
| `Client.GetMarketsPage` | Live: cmd/peek/main.go:57 and DiscoverOpenTickers both call it. |
| `Client.DiscoverOpenTickers` | Live: cmd/tape/main.go:201,228,283 (discovery + rediscovery + RefreshTickers). |
| `Client.ResetRateLimit (client.go:108)` | Deadcode-flagged and no current prod caller, but NOT in the authorized deletion list; it is a rate-limiting knob documented in CLIENT.md/ARCHITECTURE.md that the assignment told me to keep tests fo… |
| `Client.SetNowForTest (client.go:96)` | Deadcode-flagged and currently unused even by tests, but NOT in the authorized deletion list and is the ADR-0012 time-source seam for the client. Kept as a deliberate documented seam; outside autho… |
| `JournalDropsTotal (cmd/tape/alarms.go:52)` | Test seam / asserted: cmd/tape/main_test.go:305,338,340 call and assert on it (the A1 drop-hook wiring test). Not orphaned. Left untouched. |
| `Market + parseFloat/parseInt-backed accessors` | Live via GetMarketsPage; cmd/peek reads m.Status/m.Ticker and the assignment explicitly requires keeping Market and its parse-backed methods. TestMarketAccessors retained. |
| `getMarketsResponse` | Decoded by surviving GetMarketsPage. |
| `doRaw / do / get / sign / throttle / recordSuccess / parseRetryAfter / withCallDeadline and their consts` | Retry/backoff/rate-limit/signing core still reached by GetMarketsPage; retry+backoff+auth+throttle tests retargeted onto GetMarketsPage and kept. |
| `StationByCode` | NOT a candidate; market.StationTZ and multiple tests use it — surviving live code. |
| `TestStationFieldsAreInternallyConsistent` | Registry-consistency guard covers surviving StationByCode/AllStations. Kept but edited: removed the StationBySeries/StationByLocation round-trip legs, retained the StationByCode round-trip. |
| `TestKMDWNotKORD` | ADR-0007 guard on surviving registry data (KMDW not KORD). Kept but rewired from StationByLocation("CHI") to StationByCode("KMDW"). |
| `internal/env parse() multi-line quoted-value branch (env.go:129-143)` | Baseline §4's 'shape we don't use' claim is empirically FALSE. The live /Users/wh0o0pz/dev/isobar/.env uses it: KALSHI_API_KEY=" opens a quote, ~30-line RSA PEM block follows, bare " line closes it… |
| `internal/env TestLoadQuotedAndMultiline + TestParseUnterminatedQuoteBreaksCleanly (env_test.go:34,131)` | Dedicated tests for the multi-line branch that is KEPT (live .env depends on it); they lock the exact contract the operator's PEM file relies on. |
| `Book.BestNoBid (internal/replay/orderbook.go:425)` | deadcode flags it unreachable-from-mains, but it is a live test seam. orderbook_test.go:464-465 asserts the parity invariant BestYesAsk()==100-BestNoBid() when the No side is populated. Verified Be… |
| `Book.ApplySnapshot / Book.ApplyDelta (internal/replay/orderbook.go:88,144)` | Exported decode-from-bytes primitives; ~19 test/bench call sites; kept per review F09 (per assignment). |
| `RejectedDeltasTotal / SnapshotErrorsTotal (internal/replay/orderbook.go:225,230)` | ADR-0011-style observability accessors reserved for the Phase-6 Prometheus bridge, test-asserted (per assignment). |
| `ResetCountersForTest (internal/replay/orderbook.go:235)` | Test seam (per assignment). |
| `ResolveUnknownBranchTotal (internal/edge/resolution.go:54, now :49)` | Documented ADR-0011-style observability counter accessor. Its doc names cmd/analyze / metrics exporters as intended consumers, and three tests assert it directly (resolution_test.go TestResolve_Unk… |
| `KalshiTakerFeeBPS (internal/edge/fee.go:19)` | Live — FeeMode.BPS() returns it and TestFeeMode_BPSAndString asserts it. Not a candidate; only the KalshiFeeBPS alias was removed. |
| `TestFeePerContractCents / TestFeePerContractCents_ClampsOutOfRange / TestEdgeAfterFeeCents (fee_test.go)` | Kept but re-pointed from the deleted no-mode wrappers to the surviving *Mode variants with FeeTaker, so taker-schedule + clamp coverage of the live code does not drop. |
| `scan_test.go EdgeCents assertion (was :615)` | Covers surviving scan.go EdgeCents computation, not just the deleted helper. Re-pointed EdgeAfterFeeCents -> EdgeAfterFeeCentsMode(_,_,FeeTaker) instead of deleting. |
| `RoundTripFeeCentsMode / FeePerContractCentsMode / EdgeAfterFeeCentsMode` | Live fee path used by scan.go; unchanged except doc text absorbed the deleted wrappers' rationale. None depended on a deleted symbol. |

Also kept, flagged across other packages (test seams / documented intent, untouched
this phase): `clock.Fake*`/`InitForTest`/`SinceStart` (ADR-0012 test seams),
`journal.NoopAlarmHook`/`Dropped`/`WriteErrorsTotal`/`AlarmHookPanicTotal`/`Wait`/
`SetAlarmCheckIntervalForTest` (ADR-0011 accessors + test seams; several asserted in
tests), `openmeteo.FetchFailuresTotal`/`SetNowForTest` (same pattern),
`mos.ProbAbove` + `climate.Lookup.BracketProb` (**ADR-0014 frozen weather capability —
freeze, don't delete**), `edge.Resolution` accessors asserted in tests.

## Baseline hypotheses tested — two refuted

1. **`internal/env` multi-line branch (baseline §4: "shape we don't use, drop ~80
   LOC") — REFUTED.** The live `.env` stores the inline RSA PEM as a multi-line
   quoted value; deleting the branch would have broken credential loading in prod.
   Branch + its tests kept. Baseline §4 was written before the PEM moved into `.env`.
2. **`replay.Book.BestNoBid` — REFUTED as dead.** It anchors the
   `BestYesAsk()==100−BestNoBid()` parity invariant in the property tests.

## Toolchain change

`make deadcode` added (advisory, not in `all`) — runs the same whole-program analysis;
review its output against the Kept table above.

## End of Phase 1

Next phase per protocol (when authorized): Phase 2+ — candidates that require
judgment beyond deletion: `cmd/edge-scan` `writeSummary` (cyc 24) decomposition,
sonic-vs-stdlib bench + ADR (the 7-module dep-graph lever), missing hot-path benches
(§2 inventory).

---

# Phase 1b — residual sweep (same day)

**Scope**: what `deadcode` cannot see — unused consts/vars/types, unread struct
fields, dead config keys, unused flags, orphaned test helpers, dead local
computation. 4 area-partitioned verify-then-delete agents + 1 docs/plumbing
consistency agent. Empty outcomes were explicitly permitted and occurred in 3
of 5 areas — the tree is genuinely lean post-Phase-1.

## Deletions (−8 LOC)

| Symbol | File | Note |
|---|---|---|
| `kalshi.BaseURL` const | `internal/kalshi/types.go` | Zero references anywhere; dead duplicate of config's default `api_base` string |
| `DailyTruth.Date` field | `internal/edge/truth.go` | Written, never read — consumers use only `TMaxF`/`TMinF`; the civil date lives in the index key |
| `maxB` dead local | `internal/edge/resolution.go` (`ResolveWithSiblings`) | Computed in the sibling loop, never read — only `minB` is consumed; invisible to `unused` because declared in a multi-assign |

## Verified-clean (no action)

- `internal/universe`: zero external importers, but every exported symbol is
  internally reachable — deliberately-staged ADR-0014 capability awaiting cmd
  wiring, not dead code.
- corelibs (journal/config/env/retry/clock/obs/market/openmeteo), calibration
  (incl. the new obscsv/jsonatomic), docs/scripts/Makefile/example-config: no
  residual dead code; all leads resolved to live readers or documented seams.
- Fresh `make deadcode` output == the Kept table exactly (26 symbols, zero
  cascading orphans).

## OPERATOR-CALL — escalated, not deleted

1. **`kalshi.Market` 13 unread decode fields** (Title, Subtitle, Liquidity…,
   TickSize, etc., ~13 lines): decode-struct mirrors the live GET /markets
   ABI. Delete if the mirror isn't meant to be a frozen contract.
2. **`market.Station.{UTCOffsetHours, City, HighSeries, LowSeries, CLIPil}`**:
   zero Go readers post-StationTZ; registry data with documentation value.
3. **`config.OpenMeteoLocation.Series`** YAML key: decoded, zero readers —
   either wire it or drop it from struct + example.
4. **`replay.KalshiFrame.ID`**: decoded, unread, NOT in ADR-0015's reserved
   Seq/Sid list — delete or add to the reservation.
5. **`bayesian.Posterior.PriorWeight`**: set on every update branch, read by
   nothing in prod (frozen package; diagnostic value only).

## End of Phase 1b

Function-level dead code: zero outside the documented Kept table. Residual
non-function dead code: zero outside the OPERATOR-CALL list above.
