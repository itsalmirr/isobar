# Pipeline Validation — 2026-05-02

**Status:** Pipeline built and deployed; floor verdict deferred.
**Author:** isobar team
**Supersedes:** [`2026-05-02-floor-estimate_RETRACTED.md`](./2026-05-02-floor-estimate_RETRACTED.md)

## What this is

This is an interim status doc, not a Phase 0.5 floor estimate. The floor estimate that was originally published earlier today is retracted (see link above for the retraction header explaining why). What remains true is that the data pipeline is built, tested, and now collecting fresh production data; what is not true is any claim about whether calibration produces a tradeable edge in the actually-tradeable price zone.

## What's verified

End-to-end pipeline:
- **Recorder** (`cmd/tape`) deployed on Vultr with rolling re-discovery; subscribed to 96 weather brackets across 4 cities; first frames flowing as of 2026-05-02 15:30 UTC; first natural test of rolling re-discovery happens at midnight Eastern when today's brackets settle and tomorrow's open
- **Calibration** (`cmd/calibrate-fit` + `internal/calibration/mos`) fits 32 bins (4 stations × 4 seasons × 2 vars) on 730 days × 4 stations of paired (GFS-deterministic, NWS-CLI) data, with documented OLS recovery + bias correction + handling of degenerate cases
- **Edge-scan** (`cmd/edge-scan` with `--mos`) joins recorded book state to ensemble polls, applies calibration, and emits per-bracket edge with both naive and MOS-calibrated probabilities — fee-symmetry identity verified on 154/154 rows
- 13 packages green, 0 lint, all binaries build via `make build-all`

Calibration behaviour:
- MOS correctly shrinks naive's overconfident extremes (the 100% → 95-99% pattern that the +12pp Brier-skill over naive comes from)
- Per-station bias corrections work as designed: KMIA's −2.08°F TMAX bias is captured by an intercept of +10.02 / slope 0.908 in `KMIA|MAM|tmax`, adding ~+2°F at typical Miami spring forecasts
- Across all 4 stations, post-MOS residual stdev is in 1.0–2.1°F per bin, in line with literature expectations for 1-day lead deterministic forecasts

## What's NOT verified

The directional question "does MOS-calibrated probability beat market mid in the tradeable [30,70]¢ midrange?" is **not** answered by the data we have. The verification subset (n=73 against CLI ground truth, of which only n=20 fall in the tradeable midrange) is too small to support either direction at conventional statistical confidence — analytic SE on Brier differences at n=20 is roughly 0.16, which is comparable in magnitude to any plausible effect.

The retracted report's headline ("MOS Brier-skill +53.5% vs market") was an artifact of selection bias toward deeply-OTM brackets (47% of verification rows had mid <15¢). When restricted to the actually-tradeable midrange, the picture inverts directionally but with no statistical force in either direction. See the retraction header for the stratified table.

## What's gated on

A v2 report will replace this one when all three conditions hold:

1. **n ≥ 200 verifiable brackets in the [30,70]¢ stratum** with both YES bid and NO bid present simultaneously for ≥30 min, settlement verifiable from NWS CLI
2. **Naive-shrunk-to-climatology baseline added** to the comparison (`0.2·climate_p + 0.8·naive_p` or similar, single line of code). This is the test of whether MOS earns its complexity, separate from whether shrinkage in general helps
3. **Lead-time stratification** (snapshots at multiple hours-before-settlement bins, not just end-of-window)

ETA: 2-3 weeks of continuous Vultr capture under the rolling-rediscovery fix shipped today, assuming ~10-18 verifiable [30,70]¢ brackets per day.

## Open items (for the next work session)

- **Kalshi fee schedule verification.** Pass 3 review (§0.1) claims Kalshi published a Feb 5 2026 fee schedule with separate maker (`0.0175·C·P·(1-P)`) and taker (`0.07·C·P·(1-P)`) rates, both rounded up to 1¢ minimum. Our `internal/market/fee.go` currently models taker only and assumes both legs of a round-trip pay taker rates. If maker fees are real and ~25% of taker, our round-trip fee model overstates by a meaningful fraction. Kalshi's API does not expose `maker_fee_rate` on market responses; the help-center page is a JS-rendered SPA. **Manual browser check required to confirm before any further fee-aware methodology work.**
- **Climate-shrunk-naive baseline implementation.** ~50 LOC. Per Pass 3 §4.b, this is the highest-leverage methodology addition: if MOS doesn't beat naive-shrunk in the tradeable zone, the entire MOS package is statistical theater. Don't run another verification pass without this baseline included.
- **ECMWF IFS ingestion.** ~150 LOC. ECMWF Real-time Catalogue is now CC-BY-4.0 on AWS (`registry.opendata.aws/ecmwf-forecasts/`) with 51-member ENS at 0.25°. Adding IFS to the calibration training set is the single biggest model-quality upgrade available, called out by ADR-0001's "ECMWF blend" target.
- **Walk-forward NGR fit harness.** ~150 LOC. Validates parameter stability across regimes. 90-day single holdout is enough to fit but not to validate stability across seasons.
- **Murphy decomposition + log score + CRPS in evaluation.** ~130 LOC. Reports REL/RES/UNC separately so we can distinguish "MOS is miscalibrated, recalibrate" from "MOS has no information beyond climate, kill."
- **Order-placement smoke test against `demo-api.kalshi.co`.** ~4h. Surfaces production bugs in the auth/signing/idempotency path before live deployment.
- **lead_hours column in edge-scan output.** ~10 LOC. Lets analysis stratify by lead time without requiring a full multi-snapshot refactor.

## Reproducibility

Same as the retracted report's reproducibility section:

```bash
./bin/calibrate-fit --forecast-dir data/calibration/forecast \
                    --obs-dir data/calibration/obs \
                    --out data/calibration/mos-coefficients.json \
                    --holdout-days 90

./bin/edge-scan --data ./data \
                --out data/edge-scan-mos.csv \
                --mos data/calibration/mos-coefficients.json
```

Coefficients SHA: see `data/calibration/mos-coefficients.json` `fitted_at`.
Tape data: 6 dates under `data/kalshi/` and `data/openmeteo/`.

## References

- ADR-0001 — Phase 0.5 gate criteria
- ADR-0007 — CLI bulletins as y-target
- ADR-0008 — Deterministic GFS for first-pass MOS
- Pass 3 review (`docs/research/SYNTHESIS-pass3.md`, pending file) — methodology critique
- `2026-05-02-floor-estimate_RETRACTED.md` — what we originally published and why it was wrong
