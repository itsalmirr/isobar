# Calibration

> How isobar turns a raw Open-Meteo ensemble forecast into a calibrated
> probability for one Kalshi bracket, what each probability column in
> the `edge-scan` output means, and the honest list of limitations
> driving the next-iteration plan.

This doc is the engineer-readable companion to
[`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](../reports/phase0.5/2026-05-05-floor-estimate-v2.md)
and the ADRs it cites. Read it before changing anything in
`internal/calibration/{mos,climate,bayesian}` or
`internal/edge/scan.go`'s probability flow.

## Why we calibrate

The naive estimator — "fraction of GFS ensemble members whose daily
extreme lands in the bracket" — works as a sanity floor, but it has
two structural problems for a research signal:

1. **Forecast bias.** GFS over- and under-predicts daily extremes by
   different amounts at each station and in each season (KMIA in
   summer vs KMDW in winter aren't the same forecast problem). A
   single global "members in bucket" estimator can't fix that.
2. **Underdispersion.** A 31-member control+perturbation ensemble is
   centered close to the deterministic forecast and has narrower
   spread than the predictive distribution of the real atmosphere.
   The naive count systematically under-weights the tail brackets.

MOS fixes both by fitting a per-bin linear model
`μ_calibrated = a + b·forecast` and learning the *predictive* σ from
residuals on held-out data. The climate baseline is a competing
explanation: maybe the seasonally-conditioned historical frequency
already does the work, and MOS is over-engineered.

The Phase 0.5 v2 report says climate-shrunk-naive ties MOS in the
tradeable midrange. ADR-0009 fired the canary; ADR-0010 proposes the
next step.

## Pipeline

```
cmd/calibrate-parse-cli              cmd/calibrate-fit          edge-scan
  raw NWS CLI bulletin zips     →    OLS per bin           →    paired with
  → KSTATION.csv truth files          → mos-coefficients.json    Open-Meteo polls
                                                                 → CSV with 4 columns
                                       data/calibration/obs/
                                       (truth) consumed by
                                       --climate
```

`scripts/calibration-fetch-cli.sh` pulls IEM AFOS archives;
`cmd/calibrate-parse-cli` extracts the canonical `(valid_date, tmax_f,
tmin_f)` from each bulletin. `cmd/calibrate-fit` joins those daily
truths with historical deterministic-GFS forecasts from
`scripts/calibration-fetch-forecast.sh`, fits MOS, and emits the
coefficients JSON consumed by `edge-scan --mos`.

## MOS math

See `internal/calibration/mos/mos.go` for the implementation; the
math is textbook Glahn-Lowry (1972) per ADR-0008.

For each bin `(station, season, variable)`:

```
μ_calibrated = a + b·forecast
predictive   ~ Normal(μ_calibrated, σ²)

P(bracket [Lo, Hi)) = Φ((Hi − μ)/σ) − Φ((Lo − μ)/σ)
```

`Φ` is the standard-normal CDF, computed via `math.Erf`. `a`, `b`,
and `σ` are fitted independently per bin.

**Fit.** `Fit(pairs, holdoutDays)` splits pairs by `valid_date`: the
trailing `holdoutDays` are held out, the rest are training. Within a
bin, `fitOLS` runs closed-form ordinary least squares on the training
fold:

```
b = (Σ x·y − n·x̄·ȳ) / (Σ x² − n·x̄²)
a = ȳ − b·x̄
σ_train = stdev(observed − (a + b·forecast))  on training fold
```

**Where σ comes from.** If the holdout has at least
`MinHoldoutForSigma = 30` pairs, σ is recomputed on the held-out fold
using the *fitted* `(a, b)`:

```
σ_holdout = stdev(observed − (a + b·forecast))  on holdout fold
```

The bin records `SigmaSource = "holdout"`. Otherwise the training-σ is
kept and `SigmaSource = "training-fallback"`. The honest difference:
training-σ understates predictive uncertainty because the same
residuals were used to choose `(a, b)`; holdout-σ doesn't have that
problem.

A bin is dropped from the output if it has fewer than
`MinTrainPerBin = 10` training pairs. Empty `Coefficients.Bins` is a
hard error.

**Season binning.** Months bucket as DJF / MAM / JJA / SON (Northern
Hemisphere meteorological convention).

**Predict.** `Coefficients.Predict(station, validDate, variable, forecast)`
returns `(μ, σ, ok)`. `ok=false` for unknown bins; the scanner falls
back to naive p_fair with `SkippedReason = "no_bin"`.

**FitModelSet guard.** The on-disk `Coefficients` JSON includes a
`fit_model_set` field stamped by `cmd/calibrate-fit`:

```go
coeffs.FitModelSet = []string{mos.ModelGFS}  // "gfs"
```

The scanner derives the observed model set from the Open-Meteo poll's
member-ID prefixes (`pollModelSetFromMembers` in `internal/edge/scan.go`). If
they don't match, MOS is *not* applied and the row records
`SkippedReason = "model_set_mismatch:<obs>vs<fit>"`. This catches
the latent bug where ECMWF-augmented polls would silently feed a
GFS-fit regression a different μ scale.

`LoadCoefficients` defaults a legacy empty `FitModelSet` to
`["gfs"]` with a one-line warning, so pre-FitModelSet files keep
working — re-fit to remove the warning.

## JSON schema

```jsonc
// data/calibration/mos-coefficients.json
{
  "schema_version": "isobar.mos/v1",
  "fitted_at": "2026-05-05T17:23:01Z",
  "training_window_end": "2026-02-04",
  "holdout_window_start": "2026-02-05",
  "fit_model_set": ["gfs"],
  "bins": {
    "KNYC|MAM|tmax": {
      "a": 1.84,
      "b": 0.971,
      "sigma": 2.47,
      "n_train": 184,
      "n_holdout": 30,
      "sigma_source": "holdout"
    },
    "KNYC|MAM|tmin": { /* ... */ },
    "KMIA|JJA|tmax": { /* ... */ }
    /* ... 32 bins total: 4 stations × 4 seasons × 2 variables */
  }
}
```

**Fields.**

| Field | Source | Notes |
|---|---|---|
| `schema_version` | `mos.CoefficientsSchemaVersion` | `LoadCoefficients` refuses mismatches |
| `fitted_at` | `time.Now().UTC()` at fit | audit trail |
| `training_window_end` / `holdout_window_start` | derived from input dates | inclusive `YYYY-MM-DD` strings |
| `fit_model_set` | stamped by `cmd/calibrate-fit` | currently `["gfs"]`; canonicalized sorted |
| `bins["STA\|SEASON\|VAR"]` | one per fitted bin | key format is `binKey()` |
| `bins[...].a`, `.b` | OLS intercept / slope | °F / dimensionless |
| `bins[...].sigma` | residual stdev | from holdout when `n_holdout ≥ 30`, else training |
| `bins[...].n_train`, `.n_holdout` | sample counts | audit + bin-quality eyeball |
| `bins[...].sigma_source` | `"holdout"` or `"training-fallback"` | empty in legacy files → treat as training-fallback |

Bumping `schema_version`: any change to the key format, `BinCoeffs`
field set, or the math semantics of `(a, b, σ)` requires bumping
`isobar.mos/v1` → `isobar.mos/v2`. Loaders must refuse the mismatched
version rather than silently re-interpret.

## Climate shrinkage

The competing "five-line baseline" lives in
`internal/calibration/climate/climate.go`. The idea: maybe the
historical empirical frequency of the bracket condition, restricted
to a calendar window around the target date, is already enough.
Shrink the naive ensemble fraction toward that prior:

```
p_shrunk = w · climate_p + (1 − w) · naive_p
```

with `DefaultShrinkage w = 0.2`. The package exposes:

- `BracketProb(station, variable, target, threshold, bracketAbove)`
  for one-sided thresholds.
- `RangeProb(station, variable, target, lo, hi)` for half-open
  `[lo, hi)` brackets (matches the scanner's `Resolution` shape,
  including ±Inf for open-ended sides).
- `ShrinkP(naive, climate, weight)` performs the convex combination
  and falls back to `naive` when `climate` is NaN.

**Season window.** `SeasonWindowDays = 30` is the half-width of the
calendar window used to restrict climatology. At Load-time the
implementation builds a per-day-of-year sorted slice of values from
days within ±30 of the target (circular calendar, so Dec wraps into
Jan); subsequent `BracketProb` / `RangeProb` calls are binary searches
on this slice, which is ~50-100× faster than a linear pass for our
calibration window sizes.

**Why we report it as a separate column.** `edge-scan --climate <dir>`
emits a `p_fair_climate_shrunk` column independently of MOS. The Phase
0.5 v2 report compares `MOS` vs `climate-shrunk` and finds the two
statistically tied in the tradeable midrange — a load-bearing signal
that MOS isn't earning its complexity. ADR-0009 names this finding
explicitly.

## Four output columns

For every paired trade `edge-scan` emits all four probabilities:

| Column | What it is | When to trust it |
|---|---|---|
| `p_fair_naive` | fraction of ensemble members landing in bracket | sanity floor; the lower bound calibration improves on |
| `p_fair_mos` | Φ-integral of `Normal(μ=a+b·f̄, σ)` over the bracket | best when (station, season) bin has fit + the poll model set matches `FitModelSet` |
| `p_fair_climate_shrunk` | `0.2·climate + 0.8·naive` | best when MOS isn't applicable (model-set mismatch, missing bin), or as critique baseline |
| `p_fair` | what the scanner used for edge math | MOS when `mos_applied=true`, otherwise naive (climate-shrunk is *reported*, not the trading model) |

`p_fair` is what `EdgeAfterFeeCentsMode` consumes; `p_fair_mos` and
`p_fair_climate_shrunk` are diagnostics. The scanner's reasoning
behind this asymmetry: MOS is the only calibrator we've intentionally
validated against a holdout fold, so it gets to drive the EV when
applicable; climate-shrunk is a comparator, not a candidate trading
model.

**Reading `SkippedReason`.** When MOS *isn't* applied, the per-row
`SkippedReason` says why:

- `"no_coeffs"` — `edge-scan` was run without `--mos`.
- `"no_bin"` — coefficients loaded but no fit for this
  `(station, season, variable)`.
- `"model_set_mismatch:<obs>vs<fit>"` — the observed poll's model
  set doesn't match `Coefficients.FitModelSet`. Aggregate counter
  in `Result.SkippedReason` keys on the same string.

Empty `SkippedReason` with `mos_applied=true` is the standard path.

## T−3h Bayesian update (forward-looking)

ADR-0010 proposes folding intraday observed temperature into the MOS
prior to compress σ in the near-settlement window where the Phase 0.5
v2 report shows the market beating us. The math is conjugate-Gaussian
precision-weighting:

```
1/σ²_post = 1/σ²_prior + 1/σ²_obs
μ_post    = σ²_post · (μ_prior/σ²_prior + μ_obs/σ²_obs)

μ_obs  = R(t) + E[Δ | h_remain, station, variable]
σ²_obs = max(Var[Δ | h_remain, station, variable], MinObsVariance)
```

Where `R(t)` is the running max (TMAX) or min (TMIN) of the day's
observed hourly trajectory through trade time `t`, and `E[Δ], Var[Δ]`
are empirical moments loaded from a kernel JSON.

`internal/calibration/bayesian` ships **the math + loader only**:
`Update(prior, obs, kernel) → Posterior`, `LoadKernel(path) → *Kernel`,
schema-versioned (`isobar.bayesian/v1`). The kernel fitter lives in
`cmd/calibrate-bayesian` and is a flagged scaffold today; the fit body
is the next commit, gated on the held-out evaluation harness proposed
in ADR-0011.

Integration into Scan is *not* in this iteration. The package compiles
and its tests pass; flipping it on requires ADR-0011's primary gate
(MOS+Bayesian beats market by ≥3pp Brier in the
tradeable+near-settlement cell with bootstrap 95% CI excluding zero).

See ADR-0010 §"Math sketch" for the full derivation, edge cases, and
canary list.

## References

- [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md) — NWS
  CLI bulletins as the canonical y-target
- [ADR-0008](./decisions/0008-deterministic-historical-forecast.md)
  — deterministic GFS for first-pass MOS, with the canary that fired
- [ADR-0009](./decisions/0009-mos-canary-fired-escalate-calibration.md)
  — MOS canary fired, escalate to ECMWF blend (Track A) and T−3h
  Bayesian (Track B)
- [ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md) — T−3h
  Bayesian update proposal (the bayesian package implements §"Decision")
- [Phase 0.5 v2 floor estimate](../reports/phase0.5/2026-05-05-floor-estimate-v2.md)
  — the empirical evidence for the canary decision

## Known limitations + mitigations

| Limitation | Status | Mitigation |
|---|---|---|
| In-sample σ understates predictive uncertainty | **fixed** (Lane A) | `Fit` uses holdout-σ when `n_holdout ≥ 30`; `SigmaSource` labels the source on the bin |
| No T−3h state update; we lose to market at lead [3, 12)h | **in flight** (Lane C foundation) | `internal/calibration/bayesian` foundation + `cmd/calibrate-bayesian` scaffold; integration gated on ADR-0011 |
| GFS-only fit; ECMWF blend not yet trained | **guarded** | `FitModelSet` stamp + scanner-side model-set guard refuse mismatched applications; recorder can switch to multi-model via `openmeteo.model` config when a multi-model fit is available |
| Climate-shrunk-naive ties MOS in tradeable mid | **diagnosed** | ADR-0009 names the canary; ADR-0010 (Bayesian) and ADR-0009 Track A (ECMWF) are the response. MOS is kept as the calibrated lower bound |
| Maker fee schedule (Feb 2026) not modeled | **open** | Phase 0 is hold-to-expiry, so the maker fee doesn't bind; open item in the v2 floor report |
