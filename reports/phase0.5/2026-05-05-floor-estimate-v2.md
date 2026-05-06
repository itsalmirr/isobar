# Phase 0.5 Floor Estimate — v2 — 2026-05-05

**Status:** Stratified analysis with climate-shrunk baseline. **Phase 0.5 gate is NOT met by MOS in the tradeable midrange.** Findings are statistically significant.
**Author:** isobar team
**Supersedes:** [`2026-05-02-pipeline-validation.md`](./2026-05-02-pipeline-validation.md)
**Retracted (for reference):** [`2026-05-02-floor-estimate_RETRACTED.md`](./2026-05-02-floor-estimate_RETRACTED.md)

## TL;DR

On n=299 verifiable bracket outcomes (5.6× more than the May 2 first pass), with a climate-shrunk-naive baseline added per Pass 3 §4.b:

- **MOS does help over naive** in the tradeable zone (+5.96 pp Brier improvement, bootstrap 95% CI excludes zero) — calibration is real
- **MOS does NOT earn its complexity over a 5-line climate-shrunk-naive baseline** (delta within CI; statistically tied). Pass 3's critique was correct
- **Both MOS and shrunk LOSE to Kalshi market mid in the tradeable zone** by ~9-10 pp Brier — bootstrap CIs exclude zero
- Phase 0.5 gate criterion ("calibrated Brier-skill > market-implied") is **NOT MET**

The honest verdict: at the current data and methodology, our calibration cannot extract edge from Kalshi weather brackets in the tradeable midrange. The market is genuinely better-informed than our naive ensemble or any simple shrinkage of it.

## Methodology

### Inputs

- **Tape**: 10 days of recorded data (Apr 18, Apr 22, Apr 29-May 6) — 678 MB Kalshi journals + 7.2 MB Open-Meteo polls
- **Y-target**: CLI bulletins extended through 2026-05-04 (734 days × 4 stations)
- **X-side training**: GFS deterministic historical forecasts (730 days × 4 stations)
- **MOS coefficients**: 32 bins fitted with 90-day OOS holdout (per ADR-0008)
- **Climate baseline** (NEW): empirical bracket-frequency lookup per (station, variable, season-window-around-target), shrunk with `p = 0.2·climate + 0.8·naive` per Pass 3 §4.b
- **Lead-hours column** (NEW): hours from book-state snapshot to bracket settlement-time (midnight local on bracket_date+1)

### Edge-scan output

407 rows × 27 columns including 100% baseline coverage on calibrated and climate-shrunk paths.

### Verification subset

299 rows where bracket date ≤ 2026-05-04 (in CLI ground-truth window) AND both MOS and climate-shrunk probabilities populated. **Yes-rate: 47%** (close to balanced, vs 47% in the prior n=73 set — same distribution).

### Statistical method

Bootstrap 95% confidence intervals on Brier-score differences, 10000 resamples with replacement, paired by row.

## Stratified Brier Scores

### By market-mid bucket (the most-load-bearing cut)

| Mid bucket | n | yes-rate | Naive | MOS | Shrunk | Market |
|---|---:|---:|---:|---:|---:|---:|
| ALL | 299 | 0.47 | 0.276 | **0.238** | **0.231** | 0.352 |
| [0, 15)¢ | 141 | 0.46 | 0.136 | 0.108 | 0.118 | 0.415 |
| [15, 35)¢ | 76 | 0.53 | 0.280 | 0.278 | 0.235 | 0.328 |
| **[35, 65)¢ (tradeable core)** | **62** | **0.42** | **0.493** | **0.401** | **0.411** | **0.255** |
| [65, 85)¢ | 16 | 0.44 | 0.559 | 0.463 | 0.454 | 0.317 |
| [85, 100)¢ | 4 | 0.75 | 0.638 | 0.634 | 0.455 | 0.223 |

Read this carefully:

- **In the OTM bucket [0, 15)¢ (141 rows):** all calibrators absolutely crush the market (Brier 0.11-0.13 vs market 0.42). This is where the +12 pp MOS-over-naive headline came from in the retracted report — and it's real, but it's also exactly where the market is selling lottery tickets the model can avoid
- **In the tradeable [35, 65)¢ bucket (62 rows):** the market wins. Market Brier 0.255, MOS 0.401, shrunk 0.411. The market is meaningfully better than any of our model variants
- **MOS vs shrunk is essentially tied across all buckets.** Sometimes MOS is slightly better, sometimes shrunk is. The 250 LOC of MOS package adds ≤1pp Brier vs a 5-line shrinkage estimator

### By lead-hours bucket (continuous capture made this possible)

| Lead | n | yes-rate | Naive | MOS | Shrunk | Market |
|---|---:|---:|---:|---:|---:|---:|
| [3, 12)h | 107 | 0.50 | 0.397 | 0.326 | 0.319 | 0.374 |
| [12, 24)h | 32 | 0.50 | 0.199 | 0.192 | 0.168 | 0.409 |
| [24, 48)h | 158 | 0.45 | 0.210 | 0.187 | 0.184 | 0.330 |

In the [12, 24)h bucket, **all our models beat the market** (0.17-0.20 vs 0.41). At [24, 48)h same pattern. At [3, 12)h (close to settlement), the market converges and beats us.

This is the lead-time effect we expected: as settlement approaches, the market incorporates real-time information (observed temperatures during the day) that our static daily forecast can't match.

### Tradeable × lead intersection (the really hard cut)

| Slice | n | yes-rate | Naive | MOS | Shrunk | Market |
|---|---:|---:|---:|---:|---:|---:|
| Tradeable [35,65)¢ + lead [3,12)h | 25 | 0.44 | 0.504 | 0.397 | 0.407 | **0.280** |
| Tradeable + lead [12,24)h | 11 | 0.36 | 0.391 | 0.446 | 0.324 | **0.317** |
| Tradeable + lead [24,48)h | 43 | 0.47 | 0.388 | 0.327 | 0.333 | **0.245** |

The market wins in every tradeable × lead bucket we have data for, and significantly so at lead [24, 48)h where n=43 is the largest cell.

## Bootstrap 95% CIs on Brier deltas (tradeable zone, n=79)

Positive delta = the second model has lower Brier (better):

| Comparison | mean Δ | CI95 lower | CI95 upper | significant? |
|---|---:|---:|---:|---|
| MOS vs naive | +0.060 | +0.008 | +0.113 | **yes** ✓ |
| MOS vs shrunk | −0.011 | −0.056 | +0.035 | **no** (tied) |
| shrunk vs naive | +0.070 | +0.047 | +0.094 | **yes** ✓ |
| MOS vs market | −0.100 | −0.188 | −0.013 | **yes** ✗ MOS loses |
| shrunk vs market | −0.090 | −0.173 | −0.009 | **yes** ✗ shrunk loses |

The story in one paragraph: **calibration of any kind helps significantly over naive, but MOS doesn't earn its complexity over a 5-line shrinkage estimator, and both calibration approaches lose significantly to the market mid in the tradeable zone.**

## Direction accuracy (tradeable zone, n=79)

| Predictor | Direction-correct |
|---|---:|
| Naive | 48.1% |
| **MOS** | **54.4%** |
| Shrunk | 50.6% |
| Market | 50.6% |

MOS does best on direction (the threshold-50% YES/NO call), but Brier — which weights confidence as well as direction — favors the market because the market's mid is a more informative *probability* even when the directional call is no better than 50/50.

## What we now believe

| Claim | Evidence |
|---|---|
| Pipeline is correct | ✓ All 13 packages green, 0 lint, 27-column CSV emits both calibrated and climate-shrunk values on 100% of valid rows |
| Calibration of some kind helps over naive | ✓ Bootstrap CI95 on (naive - shrunk) Brier: [+0.047, +0.094], excludes zero |
| **MOS does not earn its complexity** | ✓ MOS-vs-shrunk CI95: [−0.056, +0.035] includes zero. Pass 3 §4.b critique confirmed |
| **MOS loses to market in tradeable zone** | ✓ MOS-vs-market CI95: [−0.188, −0.013] excludes zero on the wrong side |
| Models beat market at OTM (mid <15¢) and long lead (12-48h) | ✓ Brier 0.10-0.20 vs market 0.33-0.42 in those buckets |
| Models lose to market at near-settlement (lead 3-12h) and tradeable mid | ✓ Market Brier 0.245-0.317 in tradeable+lead buckets |

## What this means for Phase 0.5

**The current MOS calibration does not clear the Phase 0.5 gate** as defined in ADR-0001:

> Phase 0.5 gate is strengthened from "10 brackets/day with >8% edge after fees" to "**>10 events/day with >5% calibrated edge after fees** AND **calibrated Brier-skill > market-implied on OOS 90-day held-out**". Signals that fail the Brier gate are disabled.

Both criteria fail in the tradeable zone:
- Calibrated Brier (MOS 0.401) > market-implied Brier (0.255) → wrong direction
- Mean post-fee MOS edge in tradeable zone is negative on average

But the gate was set against a hypothesis ("calibration produces a tradeable signal"), and we now have evidence the hypothesis is false **with the current calibration approach**. The next moves are clear from Pass 3:

1. **Add ECMWF IFS** to the calibration blend (Pass 3 §0.2). 51-member ENS at 0.25° on AWS is now free; ADR-0001 named this as the second-model addition. ~1-2 days of plumbing
2. **Move from MOS to full NGR with ensemble spread** (`σ² = c + d·ensvar`). Requires NOAA GEFS GRIB backfill for historical ensemble (ADR-0009 trigger). ~2-3 days
3. **T−3h Bayesian update** (Pass 3 §2.g). Real-time hourly ASOS observations should compress σ from ~1.5°F to ~0.7°F at T−3h, which is the regime where the market is currently winning. This is the single highest-leverage methodology addition for the close-to-settlement zone where the market wins
4. **Walk-forward parameter stability** (Pass 3 §2.d) — before any of the above, validate that our existing MOS coefficients aren't masking regime non-stationarity

What we do **not** do at this point: deploy capital. Not at $50, not at any level. The data says we don't have edge.

## Concrete next steps (priority order)

1. **Reframe the project narrative.** This is now a forecasting-research-with-failed-trading-gate project. The Phase 0.5 v2 report is a stronger resume artifact than a profitable bot would be (Pass 3 §4.f) — assuming we polish it
2. **ECMWF IFS ingestion** (~1-2 days). Single biggest model upgrade available. Pass 3 §0.2 + ADR-0001
3. **T−3h Bayesian update prototype** (~2 days). Targets the lead bucket where the market wins. Pass 3 §2.g
4. **Re-evaluate gate at n ≈ 600+** (continue Vultr capture to mid-May). With 4× current sample, we can stratify by 9 cells (3 mid × 3 lead) at n≈70 each
5. If after #2 + #3 the calibrated Brier still doesn't beat market in tradeable zone — **stop optimizing the model. Pivot to the maker-quoting / passive market-maker thesis (Pass 3 §4.d) at scale that makes sense (≥20-contract lots), or treat the project as a forecasting-research artifact (Pass 3 §3 W11-W12)**

## Reproducibility

```bash
# Refit on extended CLI window:
./bin/calibrate-fit --out data/calibration/mos-coefficients.json --holdout-days 90

# Re-scan with all baselines:
./bin/edge-scan --data ./data \
                --out data/edge-scan-v2.csv \
                --mos data/calibration/mos-coefficients.json \
                --climate data/calibration/obs

# Analysis (run the Python block in this repo's CI scripts when written):
# (Currently lives in the commit message accompanying this report)
```

## Open items

- **Kalshi fee schedule manual verification** still tagged from prior session (Pass 3 §0.1, 5.a). Browser check needed
- **`internal/calibration/climate` season-window default of ±30 days** is reasonable but unvalidated. Tune in v3
- **Edge-scan iterates per UTC date**, so each bracket gets ~1-2 snapshots (one per overlapping recording day). The lead_hours column lets us see how those distribute. For higher-resolution lead stratification we'd need multi-snapshot-per-day capture (Pass 3 §3 W2-W3)
- **`naive-shrunk-to-climatology` weight w=0.2** is a free hyperparameter. Could be cross-validated; deferred

## References

- ADR-0001 — Phase 0.5 gate criteria
- ADR-0007 — CLI bulletins as y-target
- ADR-0008 — Deterministic GFS for first-pass MOS
- Pass 3 review (planning notes) — methodology critique that drove this v2
- `2026-05-02-floor-estimate_RETRACTED.md` — what we originally claimed and why we corrected
- `2026-05-02-pipeline-validation.md` — interim status superseded by this report
- `data/edge-scan-v2.csv` — 407 rows of underlying data
- `data/calibration/mos-coefficients.json` — fitted coefficients (32 bins)
