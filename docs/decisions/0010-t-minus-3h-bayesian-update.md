# 0010 — T−3h Bayesian update of MOS prior via intraday observations

**Status:** Proposed
**Date:** 2026-05-13
**Authors:** isobar team
**Triggered by:** ADR-0009 Track B

## Context

ADR-0009 named two parallel tracks to close the Phase 0.5 gap discovered
by [`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](../../reports/phase0.5/2026-05-05-floor-estimate-v2.md).
That report found, with statistical significance (bootstrap 95% CIs on
n=79 tradeable rows):

- MOS over naive: **+0.060 Brier** [CI95 +0.008, +0.113] — MOS calibration
  is real
- MOS over market: **−0.100 Brier** [CI95 −0.188, −0.013] — MOS loses to
  market mid by ~10pp Brier in the tradeable [35, 65)¢ core
- Lead-stratified, the gap concentrates at lead [3, 12)h (close to
  settlement): market Brier 0.280 vs MOS 0.397 in the 25-row
  tradeable+near-settlement cell — the market wins by ~12pp where it
  matters most

The lead-time pattern is informative: at lead [12, 24)h and [24, 48)h
MOS beats market by ~22pp Brier. The market is wider than us when it is
forecasting tomorrow's high a day out, then tightens past us as the day
unfolds. The mechanism is obvious in hindsight — by mid-afternoon, the
day's running max already constrains the final TMAX enormously, and the
market price reflects that intraday observation. Our MOS prediction was
fitted at training time on 12z GFS forecasts; nothing in the inference
path updates it as the trading day progresses.

This ADR addresses Track B from ADR-0009: a state-update mechanism that
folds intraday observations into the MOS prior so that at T−3h our σ
collapses to something competitive with the market's implied uncertainty.

### What data we have today

- **Open-Meteo `historical-forecast-api`** exposes `hourly_2m_temperature`
  for past and current dates. Latency is ~1-2h behind real time but
  sufficient for a 3-12h-lead-out trade window — at lead 3h, the latest
  observation we can read is from ~T−5h, which still constrains tail
  outcomes.
- **Open-Meteo `forecast-api`** (live polls in `data/openmeteo/`) also
  carries the deterministic hourly forecast for the current day, which
  we already record. Recovery of running max/min from this stream is
  the path of least resistance for Phase 0.5.

What we explicitly do not have yet:

- **METAR / ASOS direct feed** — the lowest-latency public source for
  US station temperatures (~5 minute latency). Worth adding as a
  follow-up once the Bayesian update justifies its complexity, but
  not required for this foundation.

### Hypothesis

Let R(t) be the running extremum (max for TMAX, min for TMIN) of the
day's observed hourly temperature trajectory through time t. Let Δ be
the residual `actual_extremum − R(t)`. For TMAX, Δ ≥ 0 because TMAX is
monotonic non-decreasing in the running maximum. For TMIN, Δ ≤ 0.

Empirically, the distribution of Δ conditional on `hours_remaining`
(time from t to civil-day close) is much tighter than the prior MOS σ.
A back-of-envelope from physics: typical diurnal temperature range is
~15-25°F, but the standard deviation of "TMAX − running_max_at_T−3h"
across a season should be ~1-2°F. The intuition: by T−3h on a typical
day, either the running max equals the final TMAX (the day already
peaked) or the temperature is still climbing on a known trajectory.
Both regimes are much more constrained than a 24h-ahead forecast.

A precision-weighted Gaussian update of the MOS prior by the
"observation forecast" `μ_obs = R(t) + E[Δ]`, `σ²_obs = Var[Δ]`,
should yield a posterior with σ in the range needed to match the
market's accuracy at near-settlement leads.

## Decision

Build `internal/calibration/bayesian` as a standalone package that
performs precision-weighted Gaussian posterior updates of an MOS prior
using a kernel JSON of empirical residual statistics. The package
implements the standard conjugate Gaussian update:

```
1/σ²_post = 1/σ²_prior + 1/σ²_obs
μ_post    = σ²_post · (μ_prior/σ²_prior + μ_obs/σ²_obs)
```

where the observation distribution is constructed from intraday data
plus a precomputed kernel:

```
μ_obs  = R(t) + E[Δ | h_remain, station, variable]
σ²_obs = max(Var[Δ | h_remain, station, variable], MinObsVariance)
```

R(t) is the running max (for TMAX) or running min (for TMIN) of the
observed hourly temperature through trade time t. h_remain is the
integer hours from t to civil-day close. E[Δ] and Var[Δ] are empirical
moments fitted by `cmd/calibrate-bayesian` against historical paired
(forecast trajectory, final extremum) data.

The package is a leaf with three pieces:

1. **Math primitives.** `Update(prior, obs, kernel) → posterior` is
   pure: no I/O, no globals. Edge cases (h_remain=0, missing kernel
   entry, degenerate prior) are explicit and documented. The fitting
   side is the responsibility of `cmd/calibrate-bayesian`.
2. **Kernel schema.** Versioned JSON keyed by `"STATION/VARIABLE"` then
   by integer `hours_remaining`, holding `{e_delta, var_delta, n}`
   per bin. `LoadKernel` validates schema version, minimum bin n, and
   non-negative variance.
3. **CLI scaffold.** `cmd/calibrate-bayesian` defines the flags
   (`--obs`, `--truth`, `--out`, `--fit-start`, `--fit-end`) and the
   pipeline shape, but the actual fitting body is deferred to a
   follow-up commit. Today's scaffold lets ADR-0010 and `CALIBRATION.md`
   reference the binary path without it being vaporware.

This branch is **foundation only**. Integration into the scan path
happens in a follow-up commit, gated on ADR-0011 validation
(below).

### Math sketch

For TMAX:

```
prior:   μ_0 = MOS μ for (station, valid_date, "tmax"), σ_0 = MOS σ
obs:     R(t)  = max{T(s) : s ∈ [day_open, t]}, h_remain = day_close − t
         μ_obs = R(t) + E[Δ | h_remain, station, "tmax"]
         σ²_obs = max(Var[Δ | h_remain, station, "tmax"], σ²_floor)

precision update:
         τ_post = 1/σ²_0 + 1/σ²_obs
         μ_post = (μ_0/σ²_0 + μ_obs/σ²_obs) / τ_post
         σ²_post = 1/τ_post
```

For TMIN: mirror — `R(t) = min`, `Δ ≤ 0`. The kernel fitter handles the
sign; the package just trusts whatever E[Δ] the kernel provides.

### Edge cases

- **h_remain = 0.** Settlement time has arrived; R(t) is the answer
  modulo our own data latency. Skip the kernel and return
  `Posterior{Mu: R(t), Sigma: TerminalSigma, Method: "obs-only"}`.
  `TerminalSigma` is a small constant (`0.5°F`) reflecting Open-Meteo
  station alignment error and timing slack relative to the official
  CLI bulletin (NWS computes TMAX/TMIN from automated 5-min METAR; our
  hourly proxy is slightly coarser).

- **Kernel has no entry for `(station, variable, h_remain)`.** Return
  the prior unchanged with `Method: "prior-only"`. This is the
  fail-safe path — we never make σ tighter without empirical
  justification. Includes the "no kernel loaded at all" case.

- **`prior.Sigma <= 0`.** Defensive: caller shouldn't pass invalid;
  if they do, return the prior unchanged with `Method: "prior-only"`.

- **`Var[Δ]` from the kernel is below `MinObsVariance`
  (= 0.25°F²).** Clamp to the floor. Without a floor, an empirically
  noisy bin with two coincidental samples and Var≈0 would collapse the
  posterior to a point estimate. The floor amounts to a "you cannot
  predict tomorrow's TMAX to better than ±0.5°F from the running
  max alone" prior, which is conservative but not implausible.

### Kernel schema

```json
{
  "schema_version": "isobar.bayesian/v1",
  "fit_window": "2024-04-01..2026-05-04",
  "entries": {
    "KNYC/tmax": {
      "0":  {"e_delta": 0.1, "var_delta": 0.4, "n": 612},
      "1":  {"e_delta": 0.3, "var_delta": 0.9, "n": 612},
      "...": "..."
    },
    "KNYC/tmin": { "...": "..." },
    "...": "..."
  }
}
```

Keys are STATION/VARIABLE (slash, mirroring filesystem layout). h_remain
is an int, so bins are 1-hour wide. Subdaily resolution is unnecessary
at our use case (we trade at most ~3 snapshots/day per market).

## Alternatives considered

- **Direct linear-regression update.** Fit `final_extremum =
  a + b·R(t) + c·forecast_extremum + ε` per (station, variable,
  h_remain) and use the fitted residual stdev as σ. Considered. It is
  approximately equivalent to the Gaussian Bayes update with `b=1`,
  but loses the explicit prior/observation decomposition that lets us
  reason about which input is doing the work. The decomposition is
  useful for debugging (`Method` field on the posterior, prior_weight
  / obs_weight ratios). We can revisit if the Bayes form
  underperforms.

- **Kalman filter over hourly state.** Treat T(t) as an OU process,
  filter against the hourly observation, project to T_close. More
  principled but requires a state-space model (drift, diffusion) per
  (station, season) that we don't have data to fit. The
  precision-weighted update is the conjugate-Gaussian limit and is
  the right scaffold.

- **Hourly Open-Meteo deterministic forecast as the observation.**
  Use the live `hourly_2m_temperature` from the current-day forecast
  poll (which we already record) instead of going to the historical
  archive. Considered for the integration commit. Open-Meteo's hourly
  output for "now" is effectively a nowcast derived from observations
  + 1-2h NWP roll-forward — close enough to a real observation for
  Phase 0.5. Trade-off: latency improves to ~0min (we already poll
  every 5min), but Open-Meteo's nowcast accuracy at the exact station
  point is the failure mode. The kernel fitter built today is
  agnostic to the upstream source — the integration commit picks the
  source and updates `data/calibration/bayesian-kernel.json`
  accordingly.

- **Use METAR / ASOS directly now.** Lower latency (~5min), higher
  accuracy (the NWS uses METAR to compute CLI TMAX/TMIN, so this
  closes the source-mismatch error). Rejected as scope creep on a
  foundation branch. Worth a separate ADR if the Bayesian gate is
  cleared and we want to squeeze the last ~0.3°F.

- **Per-month conditioning instead of per-station-variable.** Bin
  h_remain × month-of-year instead of h_remain × station. Smaller
  bins, more bias from regional climate differences. Per-station is
  the right cut for v1; we can sub-stratify by season if bin counts
  allow it after a full year of data.

## Consequences

Good:

- Targets the diagnosed weakness directly. Phase 0.5 v2 showed the
  market wins at lead [3, 12)h; this update is specifically designed
  to compress the prior σ during exactly that window.
- Composes additively with ADR-0009 Track A (ECMWF blend). Track A
  upgrades the prior; Track B updates whatever prior comes in. Both
  improvements can ship without conflict.
- The `Method` field on the posterior lets us audit which inputs were
  load-bearing across the held-out evaluation set. If `prior-only` is
  firing on >20% of T−3h scans, the kernel coverage is the binding
  constraint and the right fix is more kernel bins, not better math.
- Schema-versioned kernel JSON matches the MOS coefficients pattern
  (`isobar.mos/v1` → `isobar.bayesian/v1`). Future migrations follow
  the same shape.

Bad:

- Two failure modes that the foundation alone can't validate:
  1. The empirical Var[Δ] might be smaller than the MOS prior σ even
     at large h_remain, because the kernel is fitted on past truth
     and overfits to per-station diurnal patterns. The MinObsVariance
     floor is a coarse defense; the proper defense is the held-out
     evaluation in ADR-0011.
  2. The "observation forecast" carries upstream-source error
     (Open-Meteo hourly point error at the airport vs the NWS CLI
     extremum). If that bias is comparable to the empirical Var[Δ],
     the posterior is biased and we won't notice until we backtest
     against real CLI bulletins.
- Adds a new on-disk artifact (`data/calibration/bayesian-kernel.json`)
  with its own fit lifecycle. The MOS coefficients have a yearly refit
  cadence; the Bayesian kernel may need quarterly or seasonal refits.
  Schedule TBD in the integration commit.
- The cmd/calibrate-bayesian binary is a scaffold today, not a
  working fitter. Anyone reading this ADR and trying to run the
  binary gets "not yet implemented" — intentional, but it does mean
  the integration commit has both a math change and a binary
  implementation change.

Neutral:

- No change to the `internal/calibration/mos` package or any other
  caller in this commit. Pure addition.
- The Gaussian conjugate-Bayes update is textbook (see Gelman et al.
  "Bayesian Data Analysis" ch. 2.4 or Murphy ML ch. 4.6). No novel
  math to review.

## Reversal cost

Low. If the empirical evaluation in ADR-0011 shows the Bayesian update
doesn't beat the market in the tradeable+near-settlement cell with
statistical significance, we leave the package in place, do not flip
the integration flag, and proceed to ADR-0009 Track C (full NOAA GEFS
ensemble with ensvar-driven σ) or pivot to research-only framing per
the v2 report's recommendation. The bayesian package is self-contained
and adds no operational dependency until the integration commit lands.

## Gate criteria (for ADR-0011)

Before flipping the integration flag and emitting bayesian-updated
probabilities to the scan output, the calibration-eval tool (planned
ADR-0011) must show, on 90+ days of held-out CLI ground-truth data:

- **Primary:** MOS+Bayesian beats market mid by ≥3pp Brier in the
  tradeable [35, 65)¢ + lead [3, 12)h cell, with bootstrap 95% CI
  lower bound > 0.
- **Secondary:** MOS+Bayesian beats MOS-alone by ≥2pp Brier in the
  same cell, with bootstrap 95% CI lower bound > 0. (If this fails,
  the kernel is not extracting information from the observation; the
  upgrade is purely cosmetic.)
- **Sanity:** No regression on the MOS-alone-already-winning cells
  (lead [12, 24)h and [24, 48)h, mid <15¢). Posterior σ must not
  inflate when the kernel has insufficient data; the `prior-only`
  fallback is the defense and we verify it fires correctly.

If any gate fails, the integration commit does not land, and ADR-0011
documents the failure with the bootstrap CIs.

## Canary

Once integrated and running on a live tape, two production canaries:

- **`prior-only` rate.** Should be <30% of trade events at T−3h. If
  higher, the kernel has poor coverage and the math is being bypassed;
  refit with longer fit-window or coarser bins.
- **Posterior σ collapse rate.** Posterior σ at h_remain ∈ [3, 6]
  should compress to ~0.7-1.0°F (vs MOS prior σ ~1.5°F). If posterior
  σ is >1.3°F on average in that window, the obs precision is too low
  and the kernel may need refit.

Both canaries land in the integration commit's PR description; this
foundation branch only writes the math + tests.

## References

- ADR-0009 — Track B (this is the implementation)
- ADR-0008 — MOS calibration this Bayesian update wraps
- `reports/phase0.5/2026-05-05-floor-estimate-v2.md` — the negative
  finding that motivated Track B specifically
- Gelman, Carlin, Stern et al. "Bayesian Data Analysis", 3rd ed.,
  ch. 2.4 (conjugate Gaussian model with known variance) — the
  textbook reference for the precision-weighted update math
- Murphy, "Machine Learning: A Probabilistic Perspective", ch. 4.6
  (Bayesian inference for the parameters of a Gaussian)
- Open-Meteo Historical Forecast API:
  `https://historical-forecast-api.open-meteo.com/v1/forecast` —
  source of `hourly_2m_temperature` used for kernel fitting
- ADR-0011 (planned) — held-out evaluation gate this foundation
  blocks integration behind
