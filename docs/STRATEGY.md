# Bot strategy

> The edge thesis, the execution doctrine, and the failure modes we refuse to
> have. If a reader disagrees with any of this in writing, stop and resolve
> the disagreement before committing code to Phase 1.

## Thesis in one paragraph

Kalshi weather-bracket markets price discrete-temperature outcomes
(`[60,65)°F`, `[65,70)°F`, …) for a location and date. Retail flow
dominates these books. NOAA's GFS ensemble publishes 31 forecast paths,
ECMWF IFS publishes 51, and ML models (GenCast, AIFS) publish more —
but **raw member fractions are uncalibrated probabilities**, systematically
biased by under-dispersion and by grid-vs-station error. After
**NGR/EMOS calibration**, **ECMWF blending**, and an **intraday Bayesian
update at T−3h** that incorporates the observed temperature trajectory,
our posterior for a bracket can be 5–15 pp tighter than the market's,
which updates on a 6-hour NWP cadence. That gap is the bot's home.

Secondary thesis: **hurricane-landfall markets** (`KXHURRICANE*`). NHC
updates the cone on 3-hour advisories; models contain the signal
earlier. The cone is the 67th-percentile envelope, so tail brackets
(extreme landfall zones, Cat-5 intensity) are intrinsically mispriced.
NDBC-buoy SST nudging drops intensity MAE from 9 mph to ~7.5 mph at 24h.
Seasonal (June–November); perfect fit for a 6-month runway.

Both theses are *measured* in Phase 0.5, not assumed. Gate:
**`>10 events/day with >5% calibrated edge after fees` on at least
one signal**, with **calibrated Brier-skill beating market-implied
on held-out 90-day OOS data**. If calibration fails the Brier gate,
the signal is disabled — no negotiation.

**BTC-hourly is killed.** Research (waves 1+2) converged: Coinbase
integrated Kalshi in Jan 2026, compressing lead-lag to sub-second.
At REST-from-cloud latency we are 10–50 ms behind serious competitors.
`internal/coinbase` and the `coinbase` config block are removed.

## The two signals

### `weather-calibrated` — calibrated ensemble vs. bracket YES

**Pricing model (do NOT use raw fraction — it's miscalibrated by
10–15 pp at bracket boundaries; see `docs/research/02-weather-thesis-audit.md`).**

Pipeline:

1. Fetch GEFS (31 members) + ECMWF IFS (51 members) for the target
   station and date.
2. Apply **NGR / EMOS** per `(station, season, lead-time)` bin, fit on
   ≥2 years of paired archive-vs-NWS-CLI observations. Produces a
   calibrated predictive CDF, not a raw member count.
3. Blend the two models (inverse-variance weighted).
4. Apply **EVT / Peaks-Over-Threshold (GPD)** for tail brackets where
   raw count is 0–1 members — extrapolates tail probability from
   20y of station history.
5. At T−3h, perform a **Bayesian update** using observed METAR/ASOS
   trajectory: `P(finalT | obs) ∝ P(obs | finalT) · P(finalT | ensemble)`.
   This is the largest single-shot edge (+5–10¢/bracket at settlement).
6. Wrap the predictor with **conformal prediction intervals**
   (distribution-free coverage guarantees). Only bet when
   `bracket width > conformal width + bid-ask`.

The resulting `p_fair` is a calibrated posterior probability with
known coverage, not a raw member fraction.

**Edge.**

```
edge_yes = p_fair - (yes_ask + fee_round_trip)
edge_no  = (1 - p_fair) - (no_ask + fee_round_trip)
```

`fee_round_trip` is the sum of maker/taker fees actually incurred on
open + close at the assumed fill prices. Never a flat constant — fees depend
on price, per Kalshi schedule.

**Entry.**

- **Fee-aware edge floor** (do NOT use fixed 8¢):
  `edge > 0.07 · 2 · P · (1−P) + 1¢` per side. Forces the bot to skip
  P≈0.5 brackets unless edge is large, because that's where Kalshi's
  fee curve peaks.
- **Lyapunov 8-day gate**: no signal emitted for brackets with
  lead_time > 8 days. Forecast error doubles every ~5 days
  (λ ≈ 0.2 day⁻¹); beyond 8 days any "edge" is noise.
- Enter when all gates pass AND the book has `min_size` at the far-touch.
- Quote maker-only. **Post-only** (`PostOnly: true`) on every order —
  exchange rejects rather than crosses.
- Reservation price + half-spread per `docs/research/06-microstructure-math.md`:
  `σ²_bin(P) = P(1−P)/√t_days` (NOT P(1-P) alone — bounded-payoff
  correction).
- One order per `(strategy, market, side)` at a time.

**Exit.**

- If `p_fair` migrates back toward market, cancel + re-quote at the new
  fair.
- Hold to expiry by default — the edge is realized at resolution, not by
  round-tripping the book.
- Stop-loss: if `p_fair` inverts past the market mid (market was right, we
  were wrong), close at best available price.

**Known caveats.**

- Member counts ≠ 31 for some GFS variables; always verify N from the
  response, do not hardcode.
- Ensemble doesn't model microclimate (shadow, cloud cover anomalies near
  the official station); edge is weakest for bracket boundaries close to
  the ensemble median.
- GFS refreshes on a 00/06/12/18 UTC cadence; around each refresh there's
  a brief window where member fields may be partially updated. Gate signal
  emission on "fully-updated" (all expected hours present for the target
  day).

### `hurricane-landfall` — NHC advisory + models vs. bracket YES

**Pricing model.** For a landfall-zone bracket (or category-at-landfall
bracket) with advisory time `T`:

1. Pull NHC ATCF a-deck (deterministic forecast) + b-deck (best-track
   analysis) for the active storm.
2. Ingest GEFS 30-member tropical ensemble + HAFS-A/B + HWRF via
   NOAA NOMADS (all free).
3. Run SST-nudged intensity ML model trained on 10y NOAA archive with
   NDBC-buoy real-time heat content. Drops intensity MAE from 9 mph
   to ~7.5 mph at 24h.
4. **EVT/POT** for Cat-5 tail pricing (extreme brackets).
5. Regime-switch **HMM** over weakening/strengthening/steady-state for
   the storm's current intensity phase.

**Why the edge exists.** NHC's cone is the 67th-percentile envelope
by construction — tails are structurally under-weighted by retail
reading the cone. NHC reprices on 3-hour advisory cadence (2-hour near
landfall); models reprice on 6-hour model cadence; realized-observation
signal (SST, METAR near the target coast) is continuous. The gap
between these three cadences is the edge.

**Entry.** Same gates as weather-calibrated (fee-aware floor,
Lyapunov-horizon adapted to hurricane predictability), plus:
- Only emit signals during active NHC advisories (AL or EP basin).
- Skip when HMM state is "uncertain" (transition probabilities
  within 15 pp of each other).

**Seasonality.** Atlantic season June 1 – November 30. 6–12 tradable
storms per year historically. Off-season is used for backtest /
calibration work against the 2020–2025 archive.

## Execution doctrine

1. **Maker-only by default.** Taking flow from retail in bracket books
   destroys the edge via fees. We sit on the book and wait.
2. **Post-only flag set on every order** (`PostOnly: true`). An order that
   would cross is rejected, not crossed silently.
3. **One live order per `(strategy, market, side)` at a time.** No
   stack-of-orders — too much to reconcile after a disconnect.
4. **Cancel-before-replace.** Price/size changes go via
   `CancelOrder` → wait for confirmation → `CreateOrder`. No "modify" API
   on Kalshi; emulating it loses audit traceability.
5. **Client order IDs are deterministic** —
   `<strategy>-<ticker>-<ensemble_run>-<side>` — so retries after
   network blips don't accidentally double-submit.
6. **Fee-aware pricing.** Every entry/exit quote has fees subtracted from
   the computed edge. Taker fees are treated as worst-case on any cross.

## Sizing

Position sizing is a separate concern from signal. Sizing lives in
`internal/risk`, not in the `Signal` implementations.

Default rule (Phase 1): Kelly-fraction of bankroll, capped at per-ticker
and per-portfolio notional limits. Kelly numerator is `edge_after_fees`;
denominator is the implied variance of the bracket outcome.

**Explicit caps, enforced pre-order:**

| cap                          | default (Phase 3 launch) |
| ---------------------------- | ------------------------ |
| max notional per ticker      | $10                      |
| max notional per series      | $25                      |
| max notional portfolio-wide  | $50 (scales with bankroll) |
| max contracts per order      | 100                      |
| max daily drawdown before halt | 10% of bankroll         |

Every cap is a constant in `config.yaml`, not a magic number in code.

## Failure modes we refuse to have

These are the bugs that blow up trading accounts. We design them out, not
around.

1. **Stale state.** After reconnect, the bot must not place new orders
   until it has reconciled open orders + positions from Kalshi REST. This
   is a hard-wired barrier in the exec loop, not a nice-to-have.
2. **Double-sends.** Every outbound order carries a
   `ClientOrderID`. Retries reuse the same ID; Kalshi de-dupes.
3. **Silent fill loss.** Fills observed on the WS feed are written to
   SQLite *before* the position is updated. If the process dies between,
   warm-start replays the fill from disk.
4. **Clock drift.** Signed requests fail if local clock drifts > ~1min
   from Kalshi server time. We monitor drift every minute against a
   REST health endpoint; drift > 250ms logs a warn, > 1s halts trading.
5. **Schema changes on Kalshi.** Payloads are journaled raw; decoders are
   versioned; unknown fields are preserved (`json.RawMessage` passthrough
   where possible). A schema bump in Kalshi's API should cause a decode
   warning, not a silent misprice.
6. **Kill-switch missing.** `./halt` file present ⇒ no orders leave the
   process. Checked both at tick-start and immediately before every order
   placement (TOCTOU gap acceptable here — worst case is one extra order).
7. **Unbounded retries.** Every network call has a context deadline. No
   `time.After` + `for {}` loops without a break condition.

## What we will *not* do

- **Not** place market orders. Ever, in any strategy.
- **Not** chase. If the quote gets filled adversely, we accept, log, and
  wait for the next signal. Revenge trading is how $50 becomes $0.
- **Not** run multiple strategies against the same market without a
  coordinator that enforces the per-ticker cap across strategies.
- **Not** trade a market whose resolution criteria we have not read in
  full and linked in code comments. Weather brackets occasionally have
  edge cases ("observed at which station at which time") that rewrite the
  payoff.
- **Not** add new strategies without first measuring the edge in Phase 0.5
  style analysis. Code-first strategies without measurement gates are
  how blown-up accounts get written.

## Decision log

Every strategy-level decision (add/remove a strategy, change a cap, switch
a pricing model) is recorded in `docs/decisions/<date>-<slug>.md` with:

- **Context.** What did we know before?
- **Decision.** What did we change?
- **Alternatives considered.** What did we *not* do and why?
- **Reversal cost.** If this turns out to be wrong, how hard is it to back out?

Decisions are never silent. A strategy is a hypothesis; hypotheses deserve
timestamped evidence.
