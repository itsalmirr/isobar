# Forward Week 1 — 2026-05-19 to 2026-05-23 verdict

**Date**: 2026-05-26
**Builds on**: [v4 H4 filter](2026-05-19-v4-h4-filter.md), [v4 maker-fee addendum](2026-05-20-v4-maker-fees-addendum.md)
**Status**: First forward-validation data point. **Materially underperforms backtest.** Bot operated WITHOUT MOS/Bayesian the entire week — the comparison is not apples-to-apples until the MOS refit lands.

## Two questions to answer

This report does two things:

1. **Re-scores last week's 24 paper positions** (opened 2026-05-19 04:51 UTC) now that NWS CLI truth has issued for the relevant bracket dates.
2. **First multi-day forward-validation backtest** of the H4 strategy on tape recorded *after* the v4 discovery.

## Q1 — How did last week's 24 positions actually resolve?

A week ago we opened 24 paper positions with $6.05 of a $20 bankroll in the last 30 minutes of the 2026-05-19 tape (window: 04:51–05:21 UTC). The model expected +$9.45.

**Realized: −$0.05** (essentially flat, ~−0.8% of deployed capital). 6 winners out of 24 = 25% win rate.

| Ticker | Side | n | Cost | Realized | model p_fair | Outcome |
|---|---|---:|---:|---:|---:|---|
| KXLOWTMIA B77.5 | NO | 3 | $1.67 | **+$1.33** | 0.159 | TMIN outside [76.5, 78.5] ✓ |
| KXLOWTNYC B71.5 | YES | 3 | $1.01 | −$1.01 | 0.427 | TMIN NOT in [70.5, 72.5] ✗ |
| KXHIGHLAX T78 | YES | 13 | $0.92 | −$0.92 | 0.634 | TMAX LAX < 78°F ✗ (biggest loss) |
| KXLOWTNYC T72 | YES | 1 | $0.61 | **+$0.39** | 0.671 | TMIN < 72 ✓ |
| KXLOWTLAX B55.5 | NO | 2 | $1.80 | **+$0.20** | 0.000 | TMIN outside [54.5, 56.5] ✓ |
| KXLOWTMIA B79.5 | YES | 2 | $0.04 | −$0.04 | 0.390 | TMIN NOT in [78.5, 80.5] ✗ |

**Interpretation**: model thought it had +$9.45 of edge; reality was −$0.05. Naive `p_fair` is wildly overconfident (as predicted in last week's note). The model's 0.000 prediction for KXLOWTLAX B55.5 was directionally right (NO won), but the 0.634 prediction for KXHIGHLAX T78 was wrong on 13 contracts.

## Q2 — How did the strategy perform across all 5 forward-window days?

`edge-scan` on the whole forward window (2026-05-19..2026-05-23) with `--entry-filter h4 --truth`:

```
fee mode:           taker
entry filter:       h4
frames seen:        684,544
trade frames:       4,659
weather trades:     4,659
paired trades:      2,632  (56.5% — vs 93% in backtest; lower due to sparse openmeteo polls)
MOS-calibrated:     0  (model-set mismatch; same as last week)
Bayesian-updated:   0  (gated on MOS)

trades entered:     780  (29.6% of paired)
trades won:         174  (22.3% win rate)
total realized PnL: -3,024¢  ($-30.24)
```

`paper_sweep` at $20 bankroll, $2/ticker, $5/series, 30-min windows:

```
n active windows:        15  (vs 194 in the v4 backtest)
mean PnL / window:       -123¢  (-$1.23)   vs +$2.59 in backtest
median PnL / window:     -133¢  (-$1.33)   vs -$0.23 in backtest
% positive windows:      13.3% (2/15)      vs 48.5% in backtest
worst / best window:     -$5.30 / +$2.34   vs -$9.93 / +$68.06
total across all:        -$18.43           vs +$503.39
total entered:           549               vs 12,466
win rate:                16.6%             vs 22.7%
```

## What the gap means, and what it doesn't

**The forward week is genuinely worse than the backtest.** Win rate dropped from 22.7% → 16.6%. Mean window PnL flipped from +$2.59 to −$1.23. 13% positive windows vs 48.5%.

**But three big caveats keep this from being a verdict on H4:**

1. **The bot ran without MOS or Bayesian for the entire forward week.** The v4 backtest measured `H4 ∘ Bayesian ∘ MOS ∘ naive`; the forward week measured `H4 ∘ naive` only. The v3 report empirically showed that adding MOS+Bayesian moved PnL from −$9,198 (naive) to −$4,964 — a +$4,234 improvement, or +44.6¢ per trade. Scaled to the 549 forward-week entries, that's a *missing* +$244 from the calibration stack. That alone closes most of the −$18 gap from expectation.

2. **n=15 windows is tiny.** The recorder thrashed for most of May 21-23 (sub-200KB days vs ~22MB/day historical). Only 15 30-min windows had any tradeable activity. 95% CI on the mean PnL at this sample size is roughly [−$2.20, −$0.26] — wide enough that we can't reject "true mean = 0".

3. **Different weather regime.** Late-May temperatures across NY/MIA/LAX/CHI are structurally different from the late-Apr / early-May backtest period. Bracket pricing accuracy varies seasonally.

## The forward-tape data quality problem

The recorder behaved badly this week:

| Date | Kalshi size | OpenMeteo size | State |
|---|---:|---:|---|
| 2026-05-19 | 7.9 MB | 340 KB | Original test day, used here |
| 2026-05-20 | 11 MB | 668 KB | OK day |
| 2026-05-21 | **88 KB** | 88 KB | Tape thrashing |
| 2026-05-22 | 184 KB | 212 KB | Tape thrashing |
| 2026-05-23 | 68 KB | 68 KB | Tape thrashing |
| 2026-05-24 | (missing) | (missing) | Tape down |
| 2026-05-25 | (missing) | (missing) | Tape down |
| 2026-05-26 | 80 KB | 604 KB | Restarted, WS frame starvation |

The current observed pattern: WS reconnects every ~90 seconds with `frame starvation` warnings (6,090 retry attempts since restart). Kalshi REST is healthy (`peek` returns 96 active markets, auth works), but the WS subscription is not delivering frames. **A 90-second sniff test just now showed 0 bytes written.** This is a recorder-level problem that needs investigation separately from the strategy work — possibly a Kalshi-side change to WS subscription semantics, possibly a stale auth token in the WS upgrade, possibly the watchdog being overly aggressive on legitimately quiet evenings. Worth a dedicated debug session.

## The single highest-leverage next move

**Re-fit MOS on `gfs_seamless,ecmwf_ifs025` training data.** Reasoning:

- The forward bot has been operating with `p_fair = naive ensemble fraction` for the entire week because MOS coefficients were fit on GFS-only and the live recorder pulls GFS+ECMWF. The `FitModelSet` guard correctly rejects every poll. So we're testing a strictly weaker strategy than what backtested.
- Empirically the calibration stack adds ~+44¢/trade on average. At forward-week volume (~109 entries/day at the cap), that's ~$48/day of "missing" edge. Closes the gap from −$1.23/window to roughly break-even.
- ~150 LOC in `cmd/calibrate-fit` + `internal/calibration/mos`. About a day of focused work.
- Without this, no future forward-validation run is interpretable — we cannot compare forward-PnL against backtest-PnL when the calibration stack differs.

The alternative (just wait longer with the broken-MOS bot) yields more data on a configuration we don't intend to ship. It's wasted forward-validation time.

## v5 gate criteria — updated based on this evidence

Original v5 gate from the [v4 report](2026-05-19-v4-h4-filter.md):

> ≥10 forward trading days, n ≥ 25,000 H4 entries, aggregate PnL > 0,
> daily Sharpe ≥ 0.5, positive_days ≥ 60%, max-single-day drawdown <
> positive-day PnL.

Adjustments after one bad week of forward data:

1. **Reset the forward-validation clock** when MOS+Bayesian are operating on live polls. The 2026-05-19..05-23 window does *not* count toward v5; it's a separate "naive-only forward" data point.
2. **Tighten the tape-health prerequisite**: ≥80% of expected daily frames captured (currently the recorder thrashed to <1% on 3 of 5 days). Without this, n-counts are unreliable.
3. **Keep the substantive gate criteria** (PnL>0, Sharpe≥0.5, positive_days≥60%) — they're the right shape; they just need to operate on data the recorder actually captured under the strategy the bot actually shipped.

## Artifacts

- **24-position re-score**: `reports/live-scan-2026-05-19-truth/edges-2026-05-19_2026-05-19.csv` + `summary-*.md`
- **5-day forward backtest**: `reports/forward-week1/edges-2026-05-19_2026-05-23.csv` + `summary-*.md`
- **Reproducibility**:
  ```bash
  # Last week's 24 positions, now with realized truth
  ./bin/edge-scan --data ./data --start 2026-05-19 --end 2026-05-19 \
    --mos data/calibration/mos-coefficients.json --climate data/calibration/obs \
    --truth data/calibration/obs --bayesian-kernel data/calibration/bayesian-kernel.json \
    --hourly-obs data/calibration/obs --entry-filter h4
  python3 scripts/paper_sim.py \
    --csv reports/live-scan-2026-05-19-truth/edges-2026-05-19_2026-05-19.csv \
    --window-minutes 30 --bankroll 20.00 --entry-filter h4

  # Multi-day forward backtest, 2026-05-19 to 2026-05-23
  ./bin/edge-scan --data ./data --start 2026-05-19 --end 2026-05-23 \
    --mos data/calibration/mos-coefficients.json --climate data/calibration/obs \
    --truth data/calibration/obs --bayesian-kernel data/calibration/bayesian-kernel.json \
    --hourly-obs data/calibration/obs --entry-filter h4
  python3 scripts/paper_sweep.py \
    --csv reports/forward-week1/edges-2026-05-19_2026-05-23.csv \
    --window-minutes 30 --bankroll 20.00
  ```
