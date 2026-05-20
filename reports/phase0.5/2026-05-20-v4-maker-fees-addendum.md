# v4 addendum — Maker-fees re-run, empirical correction

**Date**: 2026-05-20
**Builds on**: [v4 (H4 filter)](2026-05-19-v4-h4-filter.md)
**Status**: Diagnostic measurement only. Does not change the v5 gate.

## Headline

Switching the v4 backtest from **taker** to **maker** fees lifts realized
PnL by **+$44.24** over the same 6-day window, not the ~$284 a
naive `fee_savings = total_fees_eliminated` calculation would suggest.

| Variant | Trades | Win rate | Realized PnL | Per-trade |
|---|---:|---:|---:|---:|
| H4 + taker fees (v4) | 23,832 | 34.7% | $+793.74 | +3.33¢ |
| **H4 + maker fees (this run)** | **23,910** | **34.7%** | **$+837.98** | **+3.50¢** |
| Δ | +78 | 0.0pp | **+$44.24** | +0.17¢ |

paper_sweep (194 non-overlapping 30-min windows, $20 + caps):

| Variant | Total PnL | Mean / window | Median / window | % positive |
|---|---:|---:|---:|---:|
| H4 + taker | $+503.39 | +$2.59 | $-0.23 | 48.5% |
| **H4 + maker** | **$+509.92** | **+$2.63** | $-0.21 | 49.5% |
| Δ | +$6.53 | +$0.04 | +$0.02 | +1.0pp |

## Why the empirical impact is smaller than the naive estimate

The H4 filter deliberately selects price extremes. For ≤9¢ or ≥91¢
prints (which dominate H4 entries outside the KXLOWT* branch), the
parabolic fee formula bottoms out at the `ceil()` floor on **both**
schedules:

```
taker fee at p = 0.05:   ceil(0.07   · 0.05 · 0.95 · 100) = ceil(0.3325) = 1¢
maker fee at p = 0.05:   ceil(0.0175 · 0.05 · 0.95 · 100) = ceil(0.0831) = 1¢
```

You can't pay less than 1¢. The "1/4 fee" multiplier is real on
midrange prints (where both schedules are well above the ceiling),
but H4 filters out most midrange prints. The handful of KXLOWT*
midrange entries inside H4 are where the actual fee savings land
— hence the +$44 delta, not +$284.

The +78 *extra* trades the maker run admitted are marginal entries
that didn't clear the 5¢ edge threshold under taker but did under
maker. They contributed ~+$44 of the gain in aggregate (+0.56¢
per extra trade — break-even-plus, as expected at the threshold).

## Implication for the strategy roadmap

Maker-only execution **is not the dominant lever** for H4 in the
backtest sample. The single-day variance of ±$10 swamps the +$44
maker improvement over 6 days. This is consistent with the
broader v3 finding that fees account for 33% of the v3 loss
(when there was no H4 filter), but only ~6% of the v4 win
(because H4 already avoids the high-fee zone).

What this means concretely:

- **Maker fee model stays implemented** (cheap; useful diagnostic).
  Code lives in `internal/edge/fee.go` with full unit test
  coverage. `--maker-fees` flag on `edge-scan`.
- **Maker-only execution as the primary thesis is downgraded.**
  Pre-this-run we estimated +$284 from fees; post, it's +$44.
  The "fix maker execution" item in the priority order moves
  below the alpha-side items (forward validation, ECMWF blend,
  Bayesian on live data).
- **The bigger live-bot gap remains**: today's live recorder
  pulls GFS+ECMWF but MOS was fit on GFS-only, so all live
  trades fall back to naive `p_fair`. Re-fitting MOS on
  GFS+ECMWF (ADR-0009 Track A) is now the highest-leverage
  lever for the live forward-validation period.

## Caveats

The +$44 number is a *fee-elimination upper bound*. Real maker-only
execution requires:

1. A queue-position fill simulator (Phase 2 ROADMAP). The current
   backtest assumes every taker print in the tape would have
   filled our resting maker order at the same price. That's
   optimistic — in practice resting orders fill on a subset of
   the prints (adverse-selection skew).
2. Order management (cancel-and-replace cadence, inventory
   capacity per ticker). These are `internal/exec.PaperExec`
   responsibilities (Phase 1 ROADMAP).

Until those land, treat +$44 as the **ceiling** of what maker-only
can add to H4 in this 6-day window. The realistic number, after
adverse-selection adjustment, is probably lower.

## Artifacts

- **Backtest CSV**: `reports/backtest-realized-h4-maker/edges-2026-04-29_2026-05-04.csv`
- **Backtest summary**: `reports/backtest-realized-h4-maker/summary-2026-04-29_2026-05-04.md`
- **Reproducibility**:
  ```bash
  ./bin/edge-scan --data ./data --start 2026-04-29 --end 2026-05-04 \
    --mos data/calibration/mos-coefficients.json \
    --climate data/calibration/obs --truth data/calibration/obs \
    --bayesian-kernel data/calibration/bayesian-kernel.json \
    --hourly-obs data/calibration/obs \
    --entry-filter h4 \
    --maker-fees
  ```
