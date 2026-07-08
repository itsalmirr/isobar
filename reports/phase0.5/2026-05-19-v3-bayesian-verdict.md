# Phase 0.5 v3 — T−3h Bayesian update verdict (held-out backtest)

**Date**: 2026-05-19
**Kernel fit window**: 2024-04-29 → 2026-01-31 (21 months)
**Backtest window**: 2026-04-29 → 2026-05-04 (held out; 89-day gap from fit end)
**Status**: **GATE NOT MET.** Bayesian update closes 46% of the loss-vs-naive gap but does not close the loss-vs-market gap.

This report supersedes nothing — it stands alongside
[`2026-05-05-floor-estimate-v2.md`](2026-05-05-floor-estimate-v2.md) as
the dollar-quantified follow-up that ADR-0009 and ADR-0010 named as
the next gate evaluation. The v2 report measured Brier vs market mid;
this one measures realized PnL under a held-out backtest of three
stacked entry strategies on the same 6-day tape.

## Headline

| Strategy | Trades entered | Win rate | Realized PnL (6 days) | Avg PnL / trade |
|---|---:|---:|---:|---:|
| Naive ensemble fraction | 106,840 | 35.6% | $-9,198.02 | -8.61¢ |
| MOS calibration | 105,417 | 34.2% | $-6,113.05 | -5.80¢ |
| **MOS + T−3h Bayesian** | **101,600** | **37.2%** | **$-4,963.86** | **-4.89¢** |
| (implicit "do nothing") | 0 | — | $0.00 | 0 |

**Each calibration layer reduces the loss but none becomes profitable.**

```
   $0 ─── do nothing
        ┊
$-4,964 ─── MOS + Bayesian   ← this report's headline
        ┊ (-$1,149 vs MOS-only; +19% improvement)
$-6,113 ─── MOS only
        ┊ (-$3,085 vs naive; +33% improvement)
$-9,198 ─── naive
```

Cumulative engineering reduces loss by **$4,234** vs naive (a 46% closure of the loss-to-baseline gap), but **the strategy still loses to "no trading" by $4,964** over the 6 days.

## Daily PnL by strategy

| valid_date | Naive | MOS | MOS+Bayes |
|---|---:|---:|---:|
| 2026-04-29 | $-1,021.23 | $-251.50 | $-394.50 |
| 2026-04-30 | $-3,126.53 | **$+1,002.13** | $+737.62 |
| 2026-05-02 | $-1,564.33 | $-1,770.04 | **$-734.72** ← Bayes saved $1,035 vs MOS |
| 2026-05-03 | $-3,190.13 | $-3,124.58 | $-2,280.83 ← Bayes saved $844 vs MOS |
| 2026-05-04 | $-295.80 | $-1,969.06 | $-2,291.43 |

**Net Bayes-over-MOS swing**: +$1,149 (positive on May 2-3, modest negative elsewhere). The win is concentrated on the days where the morning MOS prior was *most* wrong and the intraday observation correctly shrank the posterior toward the realized extremum.

## How much did the kernel actually update the prior?

`ObsWeight` is the posterior's weight on the observation (1.0 = obs dominates, 0.0 = MOS prior dominates). Distribution across the 133,330 paired trades where Bayesian fired (99.0% of paired trades):

| Percentile | ObsWeight |
|---:|---:|
| p5  | 0.041 |
| p25 | 0.114 |
| p50 | 0.283 |
| p75 | 0.529 |
| p95 | 0.782 |

By `h_remain` (hours from trade time to civil-day close, station-local):

| h_remain | n trades | mean ObsWeight | % volume |
|---:|---:|---:|---:|
| 1 | 1,544 | **0.722** | 1.2% |
| 4 | 1,821 | 0.700 | 1.4% |
| 8 | 8,564 | 0.613 | 6.4% |
| 12 | 12,356 | 0.288 | 9.3% |
| 16 | 6,946 | 0.097 | 5.2% |
| 20 | 2,244 | 0.126 | 1.7% |
| 23 | 2,035 | 0.093 | 1.5% |

The kernel is doing exactly what ADR-0010 predicted: weight on observation rises monotonically as hours-remaining shrinks. By the last 4 hours of the civil day, the observation dominates (weight ~0.70); by the first 4 hours (early morning, h≥20), the MOS prior dominates (weight ~0.10).

**The structural limit on the improvement is trade volume by `h_remain`.** 57% of trades fall in h=8–14 where the posterior is a 30–60% blend — neither the prior nor the observation is in full control. To get bigger improvements we'd need a tighter prior (ECMWF blend), a tighter kernel (more historical data, more bins, or a multivariate variant), or a strategy that concentrates entries in the high-h_remain or low-h_remain regimes where one signal clearly dominates.

## Why the Bayesian still loses to market mid

The v2 floor report identified the loss source as: "the market absorbs intraday observation that our prior doesn't see." The Bayesian update inserts exactly that information — but with two structural lags:

1. **ASOS METARs arrive at ~hh:51**, i.e. up to 51 minutes after the actual observation time. The market sees the same feed (or directly via NWS) and acts on it immediately. We're feeding a kernel that was *fit* on ASOS-derived running extrema; at scan time the kernel applies to ASOS-derived running extrema. Same latency, same feed — no edge.
2. **Bracket boundaries are integer °F**, but our kernel σ_obs floor is 0.5°F (`bayesian.TerminalSigma`). For a trade at h=2 with σ_obs ~ 0.7°F, the posterior is `Φ((bracket - μ) / σ)` which is ~0.5 within ±0.3°F of the boundary. The market's traders see the same observations and price the same probability. We're not adding information the market doesn't have — we're just being mathematically rigorous about a probability the market already prices correctly.

Put simply: **the T−3h Bayesian update was supposed to absorb the market's information advantage, and it did — but the market doesn't have an *information* advantage we can compete on, it has a *latency-zero ingestion-of-the-same-public-feed* advantage**. We have a ~minutes-scale latency penalty (Open-Meteo polling + edge-scan pairing) that the kernel can't compensate for.

## What this means for the project

By the project's own discipline ([ADR-0001](../docs/decisions/0001-pivot-strategy-after-research.md) gate, [ROADMAP Phase 0.5](../docs/ROADMAP.md)):

> Gate: calibrated Brier-skill > market-implied on held-out 90-day OOS data.

The held-out PnL is still **negative versus "no trading"** ($-4,964), and the implicit Brier-vs-market comparison remains negative because the calibrated `p_fair` continues to lose to market mid (you can't win money on a strategy that beats only its own less-calibrated alternatives). **The Phase 0.5 gate remains not met.**

That said: the v3 result is **strictly stronger** than v2.

- v2 measured calibration in Brier units, found MOS −10 pp vs market in tradeable [35,65)¢
- v3 measures dollars on held-out tape, finds MOS+Bayes loses ~$4,964 vs the implicit zero-trade baseline
- The Bayesian update is doing real work (+$1,149 vs MOS-only, +$4,234 vs naive)
- Neither closes the market-mid gap

## Concrete next levers, ordered

Per ADR-0009's escalation order, with the v3 evidence factored in:

1. **ECMWF IFS blend in the calibrator** (still un-tried) — **the only remaining lever that could plausibly close the $3,344 fee-free mispricing gap**. Inverse-variance weighting of GFS+ECMWF should tighten the MOS *prior* σ, which would (a) keep the prior more competitive on high-h_remain trades and (b) reduce reliance on the Bayesian kernel for the 8-14h volume bucket where the posterior is currently a 30-60% blend. Estimated cost: ~150 LOC, ~1 day. Expected impact: hard to predict without trying — if the prior σ drops by 30%, the prior becomes informative at h ≥ 12 where it currently isn't.

2. **Maker-only execution** — *measured, not enough*. Fee-free upper bound from the v3 CSV (sum back the `fee_cents` column on every entered trade):
   ```
   observed (taker fees):  -$4,963.86
   fee-free upper bound:   -$3,343.88
   total fees paid:         $1,619.98   ← 33% of the loss
   avg fee/trade:           1.59¢
   ```
   Maker-only execution eliminates the $1,620 of fees and would marginally improve fills (~$300 from posting 1¢ better than touch), but the **fundamental mispricing remains $3,344 over 6 days**. Maker-only is worth doing — it shifts the verdict from −$4,964 to ~−$3,000 — but it does **not** make the strategy profitable on its own. The calibrated p_fair is genuinely wrong, not just over-friction'd.

   Per-day fee-free PnL:
   | Date | PnL ¢ | fees ¢ | fee-free ¢ |
   |---|---:|---:|---:|
   | 2026-04-29 | -39,450 | +30,460 | -8,990 ≈ flat |
   | 2026-04-30 | +73,762 | +33,466 | +107,228 |
   | 2026-05-02 | -73,472 | +16,159 | -57,313 |
   | 2026-05-03 | -228,083 | +43,002 | -185,081 |
   | 2026-05-04 | -229,143 | +38,911 | -190,232 |

   The fee-free loss is concentrated on May 3-4 — those days are *structurally* mispriced, not fee-burdened.

3. **Tighten the entry threshold from 5¢ to 10¢ or 15¢.** Edge-distribution from v3 shows ~9% of entries are at edge=0¢ (marginal); these are pure noise. Tighter threshold → fewer entries → higher selectivity → maker-only execution becomes easier (fewer orders to manage). Computable from the existing CSV in seconds without re-scanning.

4. **Build the paper trader** (Phase 1 from ROADMAP). Even at current (negative) edge, a paper-only `cmd/bot` lets us validate:
   - The maker-only fill model
   - Risk caps + kill-switch behavior
   - Reconciliation under simulated crash/restart
   These are engineering-ready regardless of whether the strategy is profitable yet. **None of this Phase 1 code commits capital.**

5. **Full NGR via NOAA GEFS historical backfill** (multi-week). Reserved if 1+2+3 don't close the gap.

## Verdict for capital deployment

**Do not commit capital.** The gate is not met. The repo continues as a forecasting-research artifact with a documented partial improvement.

What the v3 evidence supports:

- Continuing engineering on levers 1–3 above (ECMWF blend, maker-only fees, threshold tuning) — these all have clean offline measurement and reversible.
- Building the **paper-only** `cmd/bot` + `internal/exec.PaperExec` (lever 4) — this is engineering work that does not touch real money and is a known precondition for any future live deployment.

What the v3 evidence does *not* support:

- Wiring `KalshiExec` to live orders.
- Funding a Kalshi account beyond what's needed for paper-trading API access.
- Treating the repo as a deployed bot.

## Artifacts

- **Kernel**: `data/calibration/bayesian-kernel.json` (184 bins, 21-month fit)
- **Backtest CSV**: `reports/backtest-realized-bayes/edges-2026-04-29_2026-05-04.csv` (134,689 paired trades, 31 columns incl. new `p_fair_bayes`, `bayes_obs_weight`, `bayes_h_remain`)
- **Summary**: `reports/backtest-realized-bayes/summary-2026-04-29_2026-05-04.md`
- **Comparison runs**: `reports/backtest-realized/` (MOS-only), `reports/backtest-realized-naive/` (no calibration)
