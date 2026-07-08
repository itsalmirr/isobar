# Realized-PnL Backtest

> How `edge-scan` simulates a hypothetical strategy against recorded
> Kalshi trades, what the output CSV columns mean, and the rule the
> backtest uses to refuse to score trades we can't actually resolve.

This doc is the engineer-readable companion to `internal/edge/scan.go`
and `scripts/run-backtest.sh`. Read it before changing the truth-file
contract or the entry-decision math.

## Overview

A *realized-PnL backtest* in isobar is:

> For each Kalshi trade event in the recorded tape, ask: "Would our
> hypothetical strategy have entered? If so, on which side? What did
> the contract actually settle to? What was the PnL?"

Implemented in `internal/edge/scan.go` when `ScanOptions.Truth != nil`.
Driven from the CLI by `cmd/edge-scan --truth <dir>` or the
end-to-end wrapper `scripts/run-backtest.sh`.

The point isn't profitability — Phase 0.5 v2 says the floor estimate
is *negative* in the tradeable midrange. The point is that the
backtest provides the same row-level transparency the rest of the
edge-scan output has: every entered trade is auditable, every skipped
trade is counted under a named reason, and the aggregate PnL is
reproducible end-to-end from the recorded data.

## The `--truth` flag

```
./bin/edge-scan --truth data/calibration/obs ...
```

The path is a directory containing `KSTATION.csv` files produced by
`cmd/calibrate-parse-cli` from NWS CLI bulletins (per ADR-0007).
`internal/edge.LoadTruth` reads every file matching the
4-character-station regex (`K[A-Z]{3}.csv`), skips any
`*-hourly.csv` sitting in the same dir, and builds an in-memory
`TruthIndex` keyed on `(station, civil_date)`.

CSV schema accepted by `LoadTruth`:

```csv
valid_date,tmax_f,tmin_f,issued_at,bulletin_id
2024-04-29,70,54,2024-04-30T06:41:00Z,CLINYC_202404300641.txt
```

Only the first three columns are required; trailing columns are
tolerated. Rows with malformed dates or unparseable integer
temperatures are silently dropped (best-effort load).

**The "no imaginary bets" rule.** Inside `Scan`, after a trade is
parsed and successfully paired with an Open-Meteo poll, the truth
lookup happens *before* PnL math:

```go
truth, ok := opts.Truth.Get(station, spec.ValidDate)
if !ok {
    res.SkippedReason["no_truth_available"]++
    return nil
}
```

If the truth row isn't loaded, the trade is skipped with reason
`"no_truth_available"` rather than scored against forecast-only
`p_fair`. The motivation is operator-honest accounting: we never
report PnL on a contract we don't actually know the outcome of.
The aggregate `Result.SkippedReason` counter surfaces how many trades
fell to this branch, so the operator can either extend the truth file
or accept the smaller scoreable subset.

## The `scripts/run-backtest.sh` wrapper

End-to-end pipeline. Reads `scripts/run-backtest.sh --help` for the
authoritative CLI; the contract:

```
scripts/run-backtest.sh \
    --tape <tape-dir>      # must contain kalshi/ and openmeteo/ subdirs
    --start YYYY-MM-DD     # first civil date to score, inclusive
    --end   YYYY-MM-DD     # last civil date to score, inclusive
    [--edge-threshold N]   # min post-fee edge (cents) to enter; default 5
    [--skip-fetch]         # reuse already-downloaded CLI bulletins
```

Three stages:

1. **Fetch CLI bulletins** via `scripts/calibration-fetch-cli.sh` from
   IEM AFOS archive. The wrapper extends the requested end-date by
   +1 day because CLI bulletins for civil date `D` are issued on the
   morning of `D+1`. Output: `<tape>/calibration/cli/`.
2. **Parse bulletins** via `cmd/calibrate-parse-cli` into the
   canonical `KSTATION.csv` (TMAX/TMIN per civil day). Output:
   `<tape>/calibration/obs/`.
3. **Run edge-scan with `--truth`** on the parsed obs dir. Output:
   `<tape>/calibration/edge-scan/edges-<start>_<end>.csv` plus a
   companion `summary-<start>_<end>.md`.

Exit codes: `0` = backtest produced output; `1` = a stage failed; `2`
= usage error.

The wrapper auto-builds `bin/calibrate-parse-cli` and `bin/edge-scan`
if they aren't already present. It does **not** auto-build
`calibrate-fit` because the backtest path itself is independent of
MOS — `--mos` is an additional flag the caller passes through (the
wrapper does not currently forward `--mos`; pass it on the underlying
`edge-scan` invocation if you want MOS-based entries instead of naive).

## CSV column reference

`cmd/edge-scan/main.go` writes the columns in this order. The `bayes_*`
columns are populated only when `--bayesian-kernel` + `--hourly-obs`
are set; the backtest-mode columns (last five) only when `--truth` is
set. Otherwise they are blank / `false` / `0`.

| Column | Type | Meaning |
|---|---|---|
| `ts_ms` | int64 | trade timestamp (Kalshi WS, ms since epoch UTC) |
| `ticker` | string | full Kalshi market ticker (e.g. `KXHIGHNY-26APR29-B62.5`) |
| `series` | string | series prefix (e.g. `KXHIGHNY`) |
| `valid_date` | YYYY-MM-DD | resolution civil date (station-local) |
| `bracket_kind` | `B` / `T` | between-style (`B`) or threshold-style (`T`) bracket |
| `bracket_key` | float | numeric suffix on the ticker; midpoint for `B`, threshold for `T` |
| `side` | `yes` / `no` | the trade's taker side |
| `trade_price_cents` | int | executed YES-price in integer cents |
| `p_fair_naive` | float | fraction of ensemble members in bracket |
| `p_fair_mos` | float | MOS-calibrated p_fair; 0 when MOS not applied |
| `p_fair` | float | the p_fair used for edge math (Bayes if applied, else MOS, else naive) |
| `mos_applied` | bool | true iff MOS produced a valid prior for this row |
| `climate_p` | float | bare climate empirical frequency; blank when no obs |
| `p_fair_climate_shrunk` | float | `0.2·climate + 0.8·naive`; blank when climate_p is blank |
| `climate_applied` | bool | true iff climate-shrunk was computable |
| `edge_climate_shrunk_cents` | int | post-fee edge using climate-shrunk p_fair |
| `p_fair_bayes` | float | posterior p_fair after the T−3h update; 0 when not applied |
| `bayes_applied` | bool | true iff the posterior update fired for this row |
| `bayes_skip_reason` | string | why not: `no_prior`, `wrong_civil_day`, `cold_start`, `h_remain_zero`, `prior-only`, or blank |
| `bayes_running_temp_f` | float | R(t) at trade time; blank when not applied |
| `bayes_h_remain` | int | integer hours to civil-day close; blank when not applied |
| `bayes_obs_weight` | float | posterior ObsWeight (1 = obs-dominated); blank when not applied |
| `n_members` | int | ensemble member count contributing to p_fair_naive |
| `n_members_in_bkt` | int | subset whose daily extreme landed in bracket |
| `poll_age_s` | int | seconds between trade and the paired poll |
| `fee_cents` | int | Kalshi parabolic fee at the trade price (taker by default; maker with `--maker-fees`) |
| `edge_cents` | int | `\|p_fair − price\| · 100 − fee` (the entry threshold uses this) |
| `has_truth` | bool | true iff `(station, valid_date)` was in `TruthIndex` |
| `actual_temp_f` | int | TMAX (`KXHIGH*`) or TMIN (`KXLOWT*`) on `valid_date` |
| `yes_resolved` | bool | true iff the bracket would have paid YES |
| `our_side` | `yes` / `no` / `""` | hypothetical entry side; empty when no edge |
| `our_pnl_cents` | int | realized PnL after entry fee; 0 when `our_side=""` |

## Sample output

A small excerpt from a tradeable-edge row (run without
`--bayesian-kernel`, so the `bayes_*` columns are at their defaults):

```csv
ts_ms,ticker,series,valid_date,bracket_kind,bracket_key,side,trade_price_cents,p_fair_naive,p_fair_mos,p_fair,mos_applied,climate_p,p_fair_climate_shrunk,climate_applied,edge_climate_shrunk_cents,p_fair_bayes,bayes_applied,bayes_skip_reason,bayes_running_temp_f,bayes_h_remain,bayes_obs_weight,n_members,n_members_in_bkt,poll_age_s,fee_cents,edge_cents,has_truth,actual_temp_f,yes_resolved,our_side,our_pnl_cents
1777477112000,KXHIGHNY-26APR29-B62.5,KXHIGHNY,2026-04-29,B,62.5,yes,38,0.6452,0.6217,0.6217,true,0.5806,0.6323,true,23,0.0000,false,,,,,31,20,142,2,22,true,63,true,yes,60
```

Reading it column-by-column:

- A `yes` taker trade for `KXHIGHNY-26APR29-B62.5` fills at 38¢. The
  bracket resolves YES if the NY-NYC high on Apr 29, 2026 is 62 or 63°F
  (the `B62.5` 2°F bracket covers `[61.5, 63.5)` → integer high ∈ {62, 63}).
- 20 of 31 ensemble members predict a high in that bracket
  (`p_fair_naive = 20/31 ≈ 0.645`).
- MOS, applied successfully (`mos_applied=true`), shrinks that to 0.622.
- Climate baseline says 58.1% of historical Apr-29-±30-day NYC highs
  landed in that bracket; shrunk-naive is 0.632.
- The poll was 142 seconds old (within the default `--max-poll-age 1h`).
- Fee at price 38¢ is 2¢: `ceil(0.07·0.38·0.62·100) = ceil(1.65) = 2`.
- `p_fair − price = 62.17 − 38 = 24.17¢ post-fee = 22¢` → clears the
  default 5¢ threshold.
- Truth: actual high was 63°F → `yes_resolved=true`.
- Strategy enters YES; PnL = 100 − 38 − 2 = 60¢.

Aggregate `TradesEntered`, `TradesWon`, `TotalPnLCents`, and a
per-`valid_date` PnL table are summarized in the companion
`summary-*.md`.

## Pitfalls

**T-bracket low-side resolution.** Earlier scanner versions resolved
every `T<K>` ticker as `[K, +Inf)` (high-side), which silently
miscoded the "low-side" T markets (e.g. `KXHIGHNY-T56` = *high < 56*).
**Fixed** by `internal/edge.ResolveWithSiblings`: a first pass collects
every Spec observed in the scan window, groups by
`(series, valid_date)`, and the bracket resolver checks whether the
T-bracket's key is at or below the lowest sibling B midpoint — if so,
it flips to `(−Inf, K)`. Commit
`a0d1c26` ("fix(edge): use ResolveWithSiblings for T-brackets") landed
this in Lane A; the bracket convention itself (previously a FIXME) has
since been locked in — see the evidence notes on `Spec.Resolve` in
`internal/edge/resolution.go` and its tests.

**Truth-file coverage windows.** NWS CLI bulletins are usually issued
the morning after the civil date they resolve (`D+1` AM). When asking
for a backtest with `--end D`, the wrapper fetches bulletins through
`D+1` to ensure the bracket for `D` is covered. Trades whose
`valid_date` falls *outside* the loaded truth window count as
`no_truth_available` and are skipped — not failed. If the
`SkippedReason` summary shows a large `no_truth_available` count,
either:

- The CLI fetch missed a date (often: a Kalshi market is live before
  the truth source has a bulletin; let the recorder run another day
  and re-fetch).
- The truth dir is missing a station's CSV entirely (e.g. you parsed
  KNYC but not KMIA bulletins).

The `summary-*.md` `Counts` section is the operator's first stop;
`paired trades` vs `trades entered` vs aggregate `no_truth_available`
should add up cleanly.

**Realized PnL is an upper bound for live trading.** The backtest
assumes:

- The fill price in the recorded tape was achievable (the trade was
  *observed* at that price, so this is solid for the YES taker side;
  cross-side fills are not the same as resting orders).
- The Kalshi parabolic taker fee `ceil(7·P·(1−P))` is the only cost.
  Maker fees (since Feb 2026) and contract-rebate complications
  aren't modeled — open item in the v2 floor report.
- Settlement is at par (100¢ YES if `yes_resolved=true`, else 0¢). No
  early-close, no Kalshi-side settlement adjustments.

In other words: the backtest is the optimistic case for the entry
rule given the prices we recorded. Live execution would have
hard-to-model slippage and queueing on top.

## References

- `internal/edge/scan.go` — `Scan`, `EdgeRow`, the backtest branch
- `internal/edge/truth.go` — `LoadTruth`, `TruthIndex.Get`,
  `stationFromTruthFilename`
- `internal/edge/fee.go` — `FeePerContractCentsMode`, `RoundTripFeeCentsMode`,
  `EdgeAfterFeeCentsMode`
- `cmd/edge-scan/main.go` — CSV column order and writer
- `scripts/run-backtest.sh` — end-to-end wrapper
- [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md) — why
  NWS CLI is the y-target source
- [`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](../reports/phase0.5/2026-05-05-floor-estimate-v2.md)
  — what the realized-PnL numbers actually look like in aggregate
