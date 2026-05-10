# 0008 — Use deterministic GFS historical forecasts for first-pass NGR/MOS

**Status:** Accepted
**Date:** 2026-05-02
**Authors:** isobar team

## Context

Phase 0.5 calibration needs paired `(forecast x, observed truth y)` data
across 2 years to fit NGR per (station, season, lead) bin. ADR-0007
solved the y side: NWS CLI bulletins, parsed and stored at
`data/calibration/obs/K*.csv`. The x side — historical ensemble
forecasts — is what this ADR addresses.

The Phase 0 recorder captures the live 31-member GFS ensemble via
Open-Meteo's `ensemble-api`. But that endpoint only serves a rolling
~3-month window of recent forecasts; it cannot return historical
ensemble runs from 2024 or earlier. The recorder will accumulate
ensemble data going forward, but at ~1 paired observation per
(station, day), reaching the ~50-pair-per-bin minimum for robust NGR
fits would take 6+ months of continuous recording across 4 stations
× 4 seasons. That's too slow for a W10 Phase 0.5 gate.

We probed three alternative sources of historical ensemble data:

1. **Open-Meteo `historical-forecast-api`** — supports past dates
   2016-01-01 to ~today, but exposes only the **deterministic** GFS
   control run, not the 30 perturbation members.
2. **Open-Meteo customer (paid) historical API** — may include ensemble
   members, requires API key + paid plan. Not viable for a $200
   research budget.
3. **NOAA NOMADS / S3 `noaa-gefs-pds`** — free, contains the full
   ensemble in GRIB2 format. Each daily run is ~30 GB. Point
   extraction at our 4 lat/lon coords requires GRIB libraries
   (cfgrib / pygrib / wgrib2) and meaningful download time. Real
   engineering, not a one-line fetch.

## Decision

**For first-pass calibration we ship deterministic-only NGR/MOS** using
Open-Meteo's `historical-forecast-api`. Data layout mirrors the
existing CLI obs tree:

- `scripts/calibration-fetch-forecast.sh` — fetches daily TMAX/TMIN
  forecasts (in °F, local timezone for civil-date alignment) for the
  same 2y window and 4 stations as ADR-0007.
- `data/calibration/forecast/K{STA}.csv` — columns `valid_date,
  gfs_tmax_f, gfs_tmin_f`. One row per civil date.
- `data/calibration/forecast/MANIFEST.json` — sha256 + row count per
  file, fetched_at, source endpoint.

The first NGR fitter consumes these CSVs and produces calibrated
`(μ, σ²)` per (station, season). Without ensemble spread we lose the
σ-modulation that full NGR uses (`σ² = c + d·ensemble_var`); we replace
it with a residual-based estimate (`σ² = stdev_of_residuals` per bin).
This is **MOS** (Model Output Statistics) more than NGR, but the same
shape: a per-bin affine correction of forecast mean plus a residual-
based uncertainty estimate.

The deferred work — full GEFS ensemble backfill — is its own ADR-0009
when we get there. The trigger is one of:

- MOS calibration shows insufficient improvement (Brier-skill
  improvement <2pp vs raw forecast on the OOS holdout) → we need the
  ensemble spread term.
- Live ensemble data accumulates enough by W6-7 that we can compare
  MOS vs full-ensemble NGR on the W10 cohort.
- We discover an ensemble-bearing free archive (e.g. ECMWF AIFS
  reanalysis, Copernicus CDS).

## Alternatives considered

- **Wait for live recorder to accumulate enough ensemble data.**
  Rejected. ~6-12 months minimum to fill all (station, season) bins.
  Loses the W10 gate timeline.
- **NOAA GEFS GRIB backfill now.** Rejected for first pass. Real
  engineering work (GRIB tooling, S3 transfer, point extraction at
  scale). Useful as a follow-up if MOS underperforms.
- **Pay for Open-Meteo customer plan.** Rejected. ~$30-50/month
  ongoing cost is high relative to bot bankroll, and we don't yet
  know whether ensemble vs deterministic is the binding constraint.
- **Use ECMWF IFS deterministic instead of GFS.** Considered. ECMWF
  IFS is generally more accurate than GFS at 1-3d lead. Open-Meteo's
  historical-forecast-api supports `models=ecmwf_ifs025`. Deferred to
  ADR-0009 — we'll add IFS as a second model in the blend if
  GFS-only MOS underperforms; the data layout is identical.

## Consequences

Good:

- One bash script + Open-Meteo public API gets us 730 paired days × 4
  stations = ~2,920 paired observations, immediately. Plenty for
  per-(station, season) MOS fits with bins of ~180 days each.
- Sanity-check residuals on 728 days of KNYC data show reasonable
  bias (TMAX +0.31°F, TMIN −1.23°F) and stdev (TMAX 1.54°F, TMIN
  2.07°F) — well within calibratable range. KMIA has the largest
  TMAX bias (−2.08°F) which directly motivates per-station correction.
- Data layout matches ADR-0007. NGR fitter reads from
  `data/calibration/forecast/K*.csv` and `data/calibration/obs/K*.csv`
  by valid_date.

Bad:

- No ensemble σ. Our σ estimate is per-bin residual stdev rather than
  per-day ensemble spread. We lose the day-to-day uncertainty
  modulation that real NGR provides. For brackets at the steep part
  of the ensemble CDF, this matters.
- Lead time is implicit. Open-Meteo's historical-forecast-api returns
  "the latest available forecast" for each timestamp. We can't easily
  request a 24h-lead forecast vs a 72h-lead forecast. Our MOS
  calibration is therefore at "latest lead" only — which approximates
  the operational use case (we run signals close to settlement) but
  doesn't let us study lead-time degradation.
- KMIA has 7 days missing from CLI ground truth (MM bulletins or
  catalog gaps). Pair count is 721 vs 728 for the others. NGR will
  drop these rather than impute.

Neutral:

- The deterministic GFS run is the same data as `temperature_2m`
  (control) in our live ensemble polls. So our live-side data and
  historical backfill use the same forecast lineage. No model-version
  drift to worry about within Phase 0.5.

## Reversal cost

Low. The forecast CSVs share the obs/ schema's date-keyed shape. If we
later acquire full ensemble data, we either:

- Replace the CSVs with ensemble-aware versions (columns expand), OR
- Add a parallel `data/calibration/forecast-ensemble/` tree.

Either way, the NGR fitter signature (`fit(forecast.csv, obs.csv) →
coefficients`) doesn't change.

## Canary

After NGR/MOS fits land:

- If calibrated Brier-skill on 90-day OOS holdout < raw forecast
  Brier-skill + 2pp, we got insufficient lift. File ADR-0009 to add
  ensemble spread.
- If KMIA TMAX residual after calibration drops from −2.08°F bias to
  <0.5°F mean residual, MOS is doing its job.

## References

- `scripts/calibration-fetch-forecast.sh` — implementation.
- `data/calibration/forecast/K*.csv` — output, 730 rows × 4 stations.
- ADR-0001 — `weather-calibrated` requires NGR-style calibration.
- ADR-0007 — y-target sourcing.
- Open-Meteo Historical Forecast API:
  `https://historical-forecast-api.open-meteo.com/v1/forecast`.
- NOAA GEFS Public Data Set on AWS:
  `s3://noaa-gefs-pds/` (deferred to ADR-0009).
- Glahn & Lowry (1972), "The Use of Model Output Statistics (MOS) in
  Objective Weather Forecasting" — original MOS reference.
