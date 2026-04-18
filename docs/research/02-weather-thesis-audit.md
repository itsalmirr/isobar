# Weather-bracket thesis critical audit

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

## Thesis (restated)
`isobar` claims that a 31-member GFS ensemble, queried via Open-Meteo, produces better calibrated bracket probabilities for Kalshi `KXHIGH*` contracts (resolved against a specific NWS ASOS/CLI report) than the market itself. The trading rule is: buy YES on `[lo, hi)` when `p_fair = (#members in bracket)/31` exceeds the ask by more than round-trip fees.

This is an attractive story and it has one large virtue (the data is free). It also has three structural problems that, taken together, make it unlikely the naive version produces positive expectancy after fees.

## Risk 1 — Raw ensemble fractions are *not* calibrated probabilities

The single most load-bearing assumption of `isobar` — that "fraction of members in bracket" equals "fair probability" — is known in the meteorological literature to be wrong, and the correction is not small. Operational ensembles (GEFS included) are systematically **underdispersive**: member spread is narrower than the actual forecast error, so the ensemble assigns probabilities that are too close to 0 or 1 at the tails and underweights brackets near the ensemble mean margins. This is textbook ([Royal Met. Society on underdispersion](https://www.rmets.org/metmatters/how-interpret-ensemble-forecast); [Ensemble Forecasting — Wikipedia](https://en.wikipedia.org/wiki/Ensemble_forecasting)).

The standard fixes — Nonhomogeneous Gaussian Regression (NGR / EMOS) and Bayesian Model Averaging (BMA) — have a well-documented payoff. Scheuerer & Büermann 2014 find NGR+ and spatial BMA *"improve upon the ensemble by 34% in CRPS and ~18% in MAE/RMSE"* for 2 m temperature ([arXiv 1407.0058](https://arxiv.org/abs/1407.0058); [Monthly Weather Review, 2015](https://journals.ametsoc.org/view/journals/mwre/143/3/mwr-d-14-00210.1.xml)). A 34% CRPS gain is roughly the same magnitude as the entire retail-vs-model edge we're hoping to capture. **A bot using raw fractions is competing against anyone using calibrated probabilities with one hand tied behind its back.** At a bracket boundary where the raw ensemble says 15/31 ≈ 48%, the calibrated number can easily be 55–60% or 40–35% — a 10–15 point difference in the exact region where trade signals fire.

Compounding this: Kalshi resolves on the NWS **CLI (Climatological Report)** for a specific ASOS station — KNYC (Central Park) for `KXHIGHNY` — measured midnight-to-midnight LST ([Kalshi Weather Markets Help](https://help.kalshi.com/markets/popular-markets/weather-markets); [minuteTemp settlement docs](https://minutetemp.com/docs/market-settlement)). Open-Meteo returns a GFS grid-cell value. Even a perfectly calibrated grid forecast has a non-trivial station-specific bias (urban-canyon heating at KNYC, sensor rounding, late-afternoon max windowing), which is exactly what GFS-MOS exists to correct ([NOAA MDL GFS-MOS](https://vlab.noaa.gov/web/mdl/gfs-mos); [NOAA GFS-MOS temperature guidance PDF](https://www.weather.gov/media/mdl/marinetpb2.pdf)). Ignoring MOS-style station correction is a second, stacked source of systematic error in the same direction as the dispersion problem.

## Risk 2 — The edge is already being farmed on adjacent venues

"Has this been tried?" — yes, repeatedly, and the Polymarket data is unkind to the naive thesis. Reported weather-arb bots on **Polymarket** use exactly the recipe in `isobar` (31-member GEFS via Open-Meteo, fraction-in-bracket = probability, trade when edge > ~8%) and have reportedly cleared five- and six-figure P&L on city-specific temperature markets ([Medium: "People Are Making Millions on Polymarket Betting on the Weather"](https://ezzekielnjuguna.medium.com/people-are-making-millions-on-polymarket-betting-on-the-weather-and-i-will-teach-you-how-24c9977b277c); [Dev Genius follow-up](https://blog.devgenius.io/found-the-weather-trading-bots-quietly-making-24-000-on-polymarket-and-built-one-myself-for-free-120bd34d6f09)). The most cited open-source implementation — [`suislanchez/polymarket-kalshi-weather-bot`](https://github.com/suislanchez/polymarket-kalshi-weather-bot) — uses the same raw-fraction approach, claims "highest profits $1.8k," and on inspection has **no verifiable P&L, is explicitly a simulation, and publishes no post-mortem**. Several "Kalshi Weather Edge" sites ([kalshiweatheredge.com](https://www.kalshiweatheredge.com/); [Predict & Profit](https://predictandprofit.io)) are thinly veiled course/subscription funnels rather than live strategies. The pattern is: the trick works well enough on less-liquid venues to generate content, and content brings copycats, which compresses the edge.

Kalshi specifically is a harder target than Polymarket for this strategy because (a) it has a **designated market-maker program** with obligations to post two-sided size and reprice into new GFS runs ([Kalshi Market Maker program](https://help.kalshi.com/en/articles/13823819-market-maker-program); [Kalshi Market Making blog](https://news.kalshi.com/p/market-making-on-kalshi)), (b) it hit **$50B annualized volume in 2025** with multiple professional MMs ([Bettors Insider Kalshi Review](https://bettorsinsider.com/predictions/reviews/kalshi/)), and (c) unlike Polymarket crypto punters, the counterparties include shops running ECMWF IFS (51 members, free via [ECMWF open data](https://www.ecmwf.int/en/forecasts/datasets/open-data)) plus GEFS plus MOS — a multi-model + calibrated stack. A single-model raw-fraction trader is the sucker at that table.

## Risk 3 — Fees eat the exact trades the strategy generates

Kalshi's taker fee is `ceil(0.07 × N × P × (1−P))` cents per side and is maximal at P = 0.5 ([Kalshi fees page](https://help.kalshi.com/trading/fees); [Whelan "Makers and Takers" paper](https://www.karlwhelan.com/Papers/Kalshi.pdf)). Bracket-trading signals by construction concentrate near P = 0.5 because that is where the market's uncertainty and the ensemble's uncertainty both live.

Worked example: 10 contracts, bracket at P_market = 0.47, p_fair = 0.53, so raw edge = 6¢/contract = 60¢ total. Per-side fee ≈ `ceil(0.07 × 10 × 0.47 × 0.53) = ceil(17.43¢) = 18¢` on entry, and if we exit at fair (P=0.53) similarly ≈ `ceil(0.07 × 10 × 0.53 × 0.47) = 18¢`. Round-trip = **36¢**, leaving **24¢** gross edge — **net edge per contract is 2.4¢, not 6¢**. That assumes (a) perfect exit at fair (not realistic — you exit at settlement 0 or 100), and (b) zero slippage. If the trade actually runs to resolution, there's no exit fee, which helps; but expected PnL = 10 × (0.53 − 0.47) × $1 − 18¢ entry fee − ~5¢ rounding drag = 60 − 18 − 5 = **37¢ per 10-lot**, i.e. ~3.7% on $4.70 at risk, which survives only if the 6¢ edge is *real after calibration bias*. Per Risk 1, a 6¢ raw-fraction edge routinely shrinks to 1–3¢ after NGR calibration — below the fee floor. **At the margin the strategy trades, fees do not reduce the edge, they invert it.**

## Concrete fixes (if we continue)

1. **Calibrate before trading.** Fit NGR/EMOS ([Wikipedia on NGR](https://en.wikipedia.org/wiki/Nonhomogeneous_Gaussian_regression); [Gneiting et al. via AMS](https://journals.ametsoc.org/view/journals/mwre/143/3/mwr-d-14-00210.1.xml)) to at least 2 years of GEFS forecasts vs CLI observations per station (KNYC, KORD, KMIA, KLAX, KDEN at minimum). This is ~5 station-models × a few hundred lines of R/Python; expected Brier improvement 15–30%.
2. **Station-level MOS/bias correction.** Use GFS-MOS text bulletins ([NOAA MDL MOS page](https://vlab.noaa.gov/web/mdl/mos); [Iowa State MOS archive](https://mesonet.agron.iastate.edu/mos/)) as a prior and blend, or fit your own linear MOS on the station. Do not trade the raw Open-Meteo grid value.
3. **Add ECMWF IFS ensemble (51 members, free).** Available through [ECMWF open data](https://data.ecmwf.int/) and via Open-Meteo's ECMWF endpoint. Multi-model blends beat any single model on CRPS by another 5–15%.
4. **Intraday Bayesian update.** Within 3 hours of close, the observed KNYC temperature trace plus persistence dominates any NWP — condition on it. This is where remaining edge against MMs likely lives, because MMs that reprice only on model runs miss the last-3-hour information.
5. **Avoid P ≈ 0.5 trades.** Fee-aware sizing: require `|p_fair − p_ask| > 0.07·(2P(1−P)) + 2·minimum_tick` *after* calibration. In practice this pushes trades into P ∈ [0.15, 0.35] ∪ [0.65, 0.85].

## Verdict

**Real with calibration, bordering on chimera without it.** The naive raw-ensemble-fraction thesis as currently framed in `isobar` will, in my judgment, lose money on Kalshi after fees. Not because the ensemble is uninformative — it is — but because (a) raw fractions systematically mis-price brackets near 50%, which is exactly where signals fire, (b) at least one pro cohort on Kalshi is already trading calibrated multi-model probabilities, and (c) Kalshi's fee curve peaks precisely at the strategy's home habitat.

**Minimum viable calibration before Phase 1 live trading:**

1. Two years of GEFS-archive vs NWS-CLI pairs at five target stations; fit NGR per (station, season, lead-time) bin.
2. Reliability diagrams and Brier-skill-score vs market-implied, on held-out 6 months. Go-live gate: calibrated probability must beat market-implied by ≥ 2 percentage points CRPS on OOS data.
3. ECMWF IFS ensemble integrated as second model; simple weighted blend.
4. Fee-aware edge floor: require post-calibration edge > `0.07·2P(1−P) + 1¢` per side.
5. Paper-trade for one full season (90 days) before risking capital; log every trade vs MM quote to detect if pros are front-running GFS runs on Kalshi.

If steps 1–2 show calibrated Brier skill ≤ market on held-out data, shut it down — that's the cleanest kill signal.

## Sources

- [suislanchez/polymarket-kalshi-weather-bot (GitHub)](https://github.com/suislanchez/polymarket-kalshi-weather-bot)
- [Medium: Polymarket weather trading write-up](https://ezzekielnjuguna.medium.com/people-are-making-millions-on-polymarket-betting-on-the-weather-and-i-will-teach-you-how-24c9977b277c)
- [Dev Genius: weather bots making $24k on Polymarket](https://blog.devgenius.io/found-the-weather-trading-bots-quietly-making-24-000-on-polymarket-and-built-one-myself-for-free-120bd34d6f09)
- [Open-Meteo Ensemble API docs](https://open-meteo.com/en/docs/ensemble-api)
- [NOAA NCEP GEFS product page (31 members, 0.25°, v12)](https://www.nco.ncep.noaa.gov/pmb/products/gens/)
- [NOAA EMC GEFS page](https://www.emc.ncep.noaa.gov/emc/pages/numerical_forecast_systems/gefs.php)
- [Scheuerer & Büermann 2014, "Spatial postprocessing of ensemble forecasts for temperature using NGR" (arXiv)](https://arxiv.org/abs/1407.0058)
- [Monthly Weather Review, 143(3), 2015 — NGR for temperature](https://journals.ametsoc.org/view/journals/mwre/143/3/mwr-d-14-00210.1.xml)
- [Wikipedia: Nonhomogeneous Gaussian regression](https://en.wikipedia.org/wiki/Nonhomogeneous_Gaussian_regression)
- [Royal Meteorological Society — how to interpret an ensemble forecast (underdispersion)](https://www.rmets.org/metmatters/how-interpret-ensemble-forecast)
- [NOAA MDL Model Output Statistics](https://vlab.noaa.gov/web/mdl/mos)
- [NOAA MDL GFS-MOS](https://vlab.noaa.gov/web/mdl/gfs-mos)
- [NOAA GFS-based MOS wind/temperature/dewpoint guidance (PDF)](https://www.weather.gov/media/mdl/marinetpb2.pdf)
- [Iowa State MOS archive](https://mesonet.agron.iastate.edu/mos/)
- [Kalshi Weather Markets — Help Center](https://help.kalshi.com/markets/popular-markets/weather-markets)
- [Kalshi Fees](https://help.kalshi.com/trading/fees)
- [Kalshi Fee Schedule (PDF, Feb 2026)](https://kalshi.com/docs/kalshi-fee-schedule.pdf)
- [minuteTemp: Kalshi market settlement documentation](https://minutetemp.com/docs/market-settlement)
- [Kalshi Market Maker program](https://help.kalshi.com/en/articles/13823819-market-maker-program)
- [Kalshi Liquidity Incentive Program](https://help.kalshi.com/en/articles/13823851-liquidity-incentive-program)
- [Kalshi news: Market Making on Kalshi](https://news.kalshi.com/p/market-making-on-kalshi)
- [Karl Whelan, "Makers and Takers: The Economics of the Kalshi Prediction Market" (PDF)](https://www.karlwhelan.com/Papers/Kalshi.pdf)
- [Bettors Insider Kalshi Review 2026 (volume/liquidity data)](https://bettorsinsider.com/predictions/reviews/kalshi/)
- [ECMWF open data](https://www.ecmwf.int/en/forecasts/datasets/open-data)
- [ECMWF data portal](https://data.ecmwf.int/)
- [ecmwf-opendata Python client (GitHub)](https://github.com/ecmwf/ecmwf-opendata)
- [Open-Meteo ECMWF API](https://open-meteo.com/en/docs/ecmwf-api)
- [Ensemble forecasting — Wikipedia](https://en.wikipedia.org/wiki/Ensemble_forecasting)
- [Kalshi Weather Edge (commercial)](https://www.kalshiweatheredge.com/)
- [Predict & Profit (commercial)](https://predictandprofit.io)
