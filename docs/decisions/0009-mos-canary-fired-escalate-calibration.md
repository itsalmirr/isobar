# 0009 — MOS canary fired; escalate calibration via ECMWF blend or full ensemble NGR

**Status:** Accepted (Proposed → Accepted on first iteration)
**Date:** 2026-05-05
**Authors:** isobar team
**Triggered by:** ADR-0008 canary

## Context

ADR-0008 accepted deterministic-only GFS as the x-side for a first-pass
MOS calibration. The decision included an explicit canary:

> If calibrated Brier-skill on 90-day OOS holdout < raw forecast Brier-skill
> + 2pp, we got insufficient lift. File ADR-0009 to add ensemble spread.

The Phase 0.5 v2 report (`reports/phase0.5/2026-05-05-floor-estimate-v2.md`,
n=299 verifiable bracket outcomes, bootstrap 95% CIs) found:

| Comparison (tradeable [35,65)¢ zone, n=79) | Δ Brier | CI95 | Verdict |
|---|---:|---|---|
| MOS vs naive | +0.060 | [+0.008, +0.113] | MOS wins |
| MOS vs climate-shrunk-naive | −0.011 | [−0.056, +0.035] | **TIED** |
| MOS vs market | **−0.100** | [−0.188, −0.013] | **MOS LOSES** |

Two failures relative to ADR-0001's gate criterion:

1. **MOS does not beat market in the tradeable midrange.** The gate
   ("calibrated Brier-skill > market-implied on OOS") fails with
   statistical significance.
2. **MOS does not earn its complexity over a 5-line shrinkage estimator.**
   This was not previously asserted as a gate but is a clear sign that
   the variance-reducing structure of MOS (per-bin OLS) is not adding
   information beyond what climate frequency + naive ensemble already
   provide.

ADR-0008's canary explicitly fires.

## Decision

We escalate the calibration approach along two parallel tracks. Either
or both can succeed; the project's next gate report (v3) evaluates
whichever ships.

**Track A — ECMWF IFS blend.** As of October 2025 the ECMWF Real-time
Catalogue is CC-BY-4.0 on AWS, providing 51-member ENS at 0.25° native
resolution alongside the 31-member GFS we already use. ADR-0001 named
this as the second model in the calibration blend. The data layout is
identical to GEFS; we add IFS as a parallel ensemble source and refit
MOS (or its successor) on the combined member set.

Estimated effort: 1-2 days plumbing + refit. No new training data
needed beyond the historical-forecast-api fetcher pattern from ADR-0008.

**Track B — T−3h Bayesian update.** The v2 report's lead-time
stratification showed the market wins most decisively at lead [3, 12)h
(close to settlement). At T−3h, hourly ASOS observations of the
day's actual temperature trajectory are available; a Kalman-style
update collapses forecast σ from ~1.5°F (12z forecast) to ~0.7°F
(post-observation posterior). This directly targets the bucket where
the market currently outperforms us.

Estimated effort: 2-3 days (state estimation + tape replay path +
integration into edge-scan).

**Track C (deferred unless A and B fail) — Full NGR with ensemble
spread.** The "real" NGR formulation `σ² = c + d·ensemble_var`
requires historical ensemble member values, which Open-Meteo's free
historical-forecast-api does not expose. The path is NOAA GEFS GRIB
archive on `noaa-gefs-pds` S3, point-extracted at our four station
coordinates, decoded via cfgrib/pygrib. Larger engineering investment
(~3-5 days) and a meaningful operational dependency on GRIB tooling.

We defer Track C unless Tracks A and B both fail to close the
tradeable-zone gap. The decision tree:

- v3 report after Track A + B → Brier-skill > market in [35,65)¢
  with bootstrap CI lower bound > 0 → gate met, proceed to Phase 1
  scope review
- v3 report after Track A + B → still losing to market → escalate
  to Track C, or pivot project framing to "forecasting research
  artifact" per the v2 report's recommendation

## Alternatives considered

- **Accept the failed gate as final, pivot the project to a
  research-only narrative without further calibration work.** Tempting
  given the W2-5 budget already spent, but two methodological
  improvements (ECMWF blend, T−3h Bayesian) are explicitly named in
  ADR-0001 as part of the original `weather-calibrated` design. We
  haven't actually built the documented signal yet — we built the
  simplest possible substitute (MOS on deterministic GFS) per ADR-0008,
  and it failed. Skipping the named upgrades would prematurely close
  the door on the original thesis.

- **Pay for Open-Meteo's customer historical API for full ensemble
  members.** Cost ~$30-50/month ongoing. ADR-0008 rejected this on
  bankroll grounds. Still the right call: NOAA NOMADS provides the
  same data free at the cost of GRIB tooling complexity, and we
  shouldn't pay an ongoing fee to avoid one-time engineering.

- **Switch from Brier to log-score as primary metric.** Pass 3 §2.f
  recommends this for OTM-skewed populations. Worth doing anyway as a
  reporting hygiene improvement, but it doesn't change the underlying
  finding: in the tradeable zone, MOS's predictive Gaussian is not
  better-informed than the market's mid. A different scoring rule
  reorders confidence calibration, not predictive accuracy.

- **Lower the bracket-mid filter to capture more sample.** Currently
  edge-scan filters one-sided books (`mid <= 0 || mid >= 100`). We
  could keep them and impute counter-side prices. Rejected: imputed
  prices are not tradeable, so any "edge" computed against them is
  fictional. The filter is correct.

## Consequences

Good:
- Tracks A and B are both inexpensive and target the diagnosed weakness
  (high σ at the tradeable mid; lead-time-dependent market advantage).
- ECMWF IFS adds genuine model diversity to the predictive blend, which
  is the variance-reducing structure that Track A (the deferred GEFS
  ensemble path) would also provide. Cheaper to test first.
- The decision tree has a clean failure mode (pivot to research
  artifact) so we don't spiral on calibration improvements indefinitely.

Bad:
- Both tracks delay any gate decision by ~1 week each. The W4 gate
  timing slips to W5 or W6.
- T−3h Bayesian has a non-trivial implementation surface: state
  estimation, replay-time computation of the post-observation
  posterior, integration with the existing MOS bin structure
  (the σ collapse should compose with MOS's bias correction, not
  replace it).
- Adding ECMWF members to a fit trained on GFS-only without
  re-architecting may produce noise. We may need a per-source bin
  structure (Gneiting et al. 2008's weighted EMOS) rather than naive
  member concatenation.

Neutral:
- The retracted Phase 0.5 v0 report and the v2 report both stay in the
  archive (see `reports/INDEX.md`). The retraction history strengthens
  the project narrative regardless of the v3 outcome.

## Reversal cost

Low if Tracks A and B succeed and the gate clears: the path forward
is well-documented in ADR-0001 + ADR-0004. Medium if both tracks fail:
either we file ADR-0010 to escalate to Track C (NOAA GEFS GRIB) or
explicitly pivot the project framing per Pass 3 §3 W11-W12. Both have
documented decision criteria.

## Canary

After Track A + Track B v3 report:

- If `MOS_with_ECMWF_and_T3h Brier ≥ market Brier` in the tradeable
  zone with CI95 lower bound > 0, gate is met → write v3 closing this
  ADR
- If not, file ADR-0010 (NOAA GEFS escalation OR project pivot to
  research-only)

Time-box: tracks A + B should ship within 4 weeks of this ADR. If
either misses that window without a documented reason, that's itself
the canary that the calibration thesis isn't viable at solo-developer
pace.

## References

- ADR-0001 — original two-signal scope including ECMWF blend
- ADR-0007 — y-target sourcing
- ADR-0008 — deterministic-GFS first-pass MOS, the ADR this canary fires from
- `reports/phase0.5/2026-05-05-floor-estimate-v2.md` — the negative finding
- Pass 3 review — methodology critique driving the v2 stratification
- `registry.opendata.aws/ecmwf-forecasts/` — ECMWF IFS open data on AWS
- `s3://noaa-gefs-pds/` — NOAA GEFS GRIB archive (Track C source)
- Gneiting, Raftery et al. (2005) "Calibrated Probabilistic Forecasting Using Ensemble Model Output Statistics and Minimum CRPS Estimation"
- Gneiting, Stanberry et al. (2008) "Assessing probabilistic forecasts of multivariate quantities, with an application to ensemble predictions of surface winds"
