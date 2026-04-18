# Research synthesis — critical verdict and direction

> Distillation of two research waves (8 agent reports) into a single
> decision document. Source reports: `01-kalshi-landscape.md`,
> `02-weather-thesis-audit.md`, `03-alternative-markets.md`,
> `04-dashboard-architecture.md`, `05-literature-review.md`,
> `06-microstructure-math.md`, `07-atmospheric-physics.md`,
> `08-evt-statmech.md`.

## TL;DR

1. **The current naive weather thesis (`p_fair = members/31`, 8¢ edge
   threshold) would almost certainly lose money on Kalshi after fees.**
   Three independent research threads converged on the same indictment:
   ensemble fractions are uncalibrated (NGR/EMOS → +34% CRPS), fees
   peak parabolically at 50¢ mid, and Kalshi's designated MM program
   already runs calibrated multi-model stacks.
2. **BTC-hourly is dead alpha** at REST-from-cloud latency. Coinbase
   integrated Kalshi in January 2026; compressed lead-lag to sub-second.
   **Decision: kill and remove the Coinbase source.**
3. **Physics imposes a hard ceiling.** The atmosphere's Lyapunov
   exponent λ ≈ 0.2 day⁻¹ means forecast error doubles every ~5 days.
   By day 10, ensemble spread ≈ climatological σ. **Any weather signal
   beyond 8 days lead is structurally uneconomic.**
4. **The edge — if it exists — lives in three specific places:**
   (a) the T−3h intraday Bayesian window where MMs reprice on 6h NWP
   cadence but the temperature trajectory is already observed
   (+5–10¢/bracket); (b) the power-law tail of secondary-city low-
   volume brackets where retail dominates (+10–20¢/day/bracket);
   (c) hurricane landfall/category markets during 2026 Atlantic season
   where NHC cone (67th-percentile) systematically mis-prices tails.
5. **Dashboard stack is settled.** Single Go binary; stdlib HTTP +
   `html/template` + htmx + SSE + TradingView Lightweight Charts +
   `modernc.org/sqlite` WAL for control plane + Tailscale Serve for
   remote. Everything vendored via `go:embed`. No Node, no Docker
   Compose, no React.

## What the math actually says

After two research waves, these claims are settled enough to design
around:

### The naive thesis has three compounding bugs

| Bug | Magnitude | Source |
|---|---|---|
| Raw ensemble fractions are uncalibrated | Scheuerer & Büermann 2014: +34% CRPS from NGR alone | [arXiv:1407.0058](https://arxiv.org/abs/1407.0058) |
| σ for bounded [0,1] payoff is `P(1-P)/√t_days` not `P(1-P)` | Systematic sizing error at all maturities | Guéant-Lehalle-Fernandez-Tapia [arXiv:1105.3115](https://arxiv.org/abs/1105.3115) |
| Kalshi fee `ceil(0.07·N·P·(1-P))` peaks at 50¢ | Bracket signals fire exactly where fees are highest | [Kalshi fee schedule](https://help.kalshi.com/trading/fees); [Whelan "Makers and Takers"](https://www.karlwhelan.com/Papers/Kalshi.pdf) |

Net effect compounded: the 8¢ edge threshold in `STRATEGY.md` is
inside the noise band (binomial sampling noise at P=0.5 on N=31 is ±9%).
**We would lose money with high confidence if we shipped it.**

### Physics sets the ceiling

From the atmospheric-physics research (wave 2, agent 2):

| Horizon | Temperature RMSE | Hurricane track | Hurricane intensity |
|---|---|---|---|
| 24h | 1.5–2.0°C (AIFS) / 2.3–2.8°C (GFS) | 35 nm | 9 mph MAE |
| 48h | 2.2–2.8°C | 55 nm | — |
| 72h | 2.8–3.5°C | 90–110 nm | 2–3× worse on RI events |
| 14d | ~climatological σ | — | — |

A 5°F bracket at 72h has ±0.5–0.7°F margin over intrinsic error.
Calibration can exploit that sliver; **nothing can overcome the floor.**

### The predictability horizon chart

Edge drops below fees at specific leads:

| Signal | Lead | Fee floor | Min edge needed | Verdict |
|---|---|---|---|---|
| KXHIGH | 0–1 d | 0.5–1.5¢ | 2¢ | **viable (NGR + T−3h Bayes)** |
| KXHIGH | 2–6 d | 1–2¢ | 3¢ | viable with calibrated blend |
| KXHIGH | 7–10 d | 1–2¢ | 4¢ (chaos floor) | marginal |
| KXHIGH | 10+ d | 1–2¢ | >6¢ (unachievable) | **DO NOT TRADE** |
| Hurricane landfall | 0–2 d | 0.5–1¢ | 2¢ | **viable (GEV/POT)** |
| Hurricane landfall | 3–6 d | 1–2¢ | 4¢ | viable with regime-HMM |
| Hurricane landfall | 7+ d | 1–2¢ | unachievable | **DO NOT TRADE** |
| BTC hourly | any | 0.5–1¢ | 1.5–2.5¢ | **DEAD** |

## Decisions and their justifications

### Decision 1 — Pivot strategy to two calibrated signals

**`weather-calibrated`** (salvage the existing path, hardened):
- NGR/EMOS calibration per (station, season, lead) bin on ≥2 years of
  paired GEFS archive vs NWS-CLI observations.
- ECMWF IFS ensemble (51 members, free) as second model; weighted blend.
- **Intraday Bayesian update at T−3h** — largest single-shot edge
  identified (+5–10¢/bracket at settlement).
- **Conformal prediction intervals** wrapping the blended predictor.
- **EVT/POT** for tail brackets (0/31 or 1/31 member cases).
- **Lyapunov 8-day gate** hard-coded in signal emission.
- Fee-aware edge floor: `edge > 0.07·2P(1−P) + 1¢` per side.
- **Kill-gate**: calibrated Brier skill must beat market-implied on
  OOS 90-day held-out data, else the signal is disabled.

**`hurricane-landfall`** (new, seasonal, June 1 start):
- NHC ATCF a-deck/b-deck advisories (every 3h near landfall).
- GEFS 30 members + HAFS-A/B + HWRF via NOAA NOMADS.
- **SST-nudged intensity ML model** on 10y NOAA archive;
  intensity MAE 9 mph → ~7.5 mph at 24h.
- EVT/POT for Cat-5 bracket tail pricing.
- Regime-switch HMM for weakening/strengthening/steady states.

### Decision 2 — Kill `btc-hourly`, delete `internal/coinbase`

Research converged unambiguously. Coinbase integrated Kalshi in Jan
2026, compressing lead-lag to sub-second. At REST-from-cloud latency
we are 10–50 ms behind any serious competitor. Phase 0.5 would have
confirmed what the research already does; skip measurement, remove code.

### Decision 3 — Signals first, dashboard after

12-week plan:

- **W1:** prune Coinbase + save research + update strategy docs.
- **W2–5:** weather-calibrated signal (NGR, ECMWF blend, conformal,
  EVT, Lyapunov gate, T−3h Bayes).
- **W6–8:** hurricane-landfall signal (NHC parser, HAFS/HWRF, SST
  intensity, EVT, HMM). Timed for June 1 season start.
- **W9:** microstructure + risk (reservation price, half-spread, OFI,
  no-trade zone, CUSUM, Kelly with ρ=−1/(K−1)).
- **W10:** Phase 0.5 gate report. Hard gate: >10 events/day with >5%
  calibrated edge after fees on at least one signal.
- **W11–12:** dashboard + paper/live execution.

### Decision 4 — Dashboard is the htmx + SSE stack

One Go binary, stdlib HTTP + html/template + htmx + htmx-SSE +
Lightweight Charts + modernc.org/sqlite WAL + go:embed + Tailscale
Serve. Dependencies added: **one** (`modernc.org/sqlite`). See
`04-dashboard-architecture.md`.

## Decisions deferred

- **`tail-mm` signal** — power-law low-volume bracket maker-quoting.
  Structurally interesting (+10–20¢/day/bracket in pure fees, near-
  zero directional risk). Parked in research; revisit after Phase 0.5
  gate if capital-efficiency beats primary signals.
- **CPI/NFP nowcasting** — attractive but sharp-dominated from day
  one (DRW / Jane Street / Citadel already trade it). Defer to
  Phase 4 once infrastructure is proven.
- **Kalshi Commodities Hub** (launched Apr 2026) — genuine whitespace
  but needs new market-type plumbing. Track, revisit in 3 months.
- **ClimODE / radar-inversion / GraphCast-self-hosted** — Phase 4+
  optionality. Documented in research, not on the 12-week plan.

## Things we explicitly refuse to do

From the hostile-check portions of waves 1 and 2:

- **RL for quoting.** Needs 10k+ fills to train; a $200 bot gets
  ~300/month. Exploration cost erodes the 10–20% upside over closed-
  form Avellaneda-Stoikov.
- **Full PINNs.** 10–100× training cost over non-physics DL; soft-loss
  adds noise under realistic observation error. 2024–25 surveys show
  gains evaporate in practice.
- **Self-hosted GraphCast.** 80 GB VRAM; $500–1000/month cloud.
  ECMWF AIFS is operational (Feb 2025) and free to research users.
- **Fokker-Planck hurricane PDE.** No ground-truth PDFs to validate
  against; Markovian assumption violated by persistent steering.
- **Kolmogorov-complexity efficiency metric.** Uncomputable;
  compression-proxies are noisy; captured better by entropy + Hurst.
- **Rough-volatility fBm** on temperature. H≈0.1 is a tick-level
  microstructure artifact; hourly METAR gives H≈0.5 (Brownian) with
  wide CIs.
- **<10¢ contracts** (Whelan: retail loses >60%); **RFQ parlays**
  (Sportico: retail-hostile); **AI-agent-trades-Kalshi** (Pierno:
  lost 65% in 60 days); **follower-count/viral markets** (no data
  feed, ambiguous resolution).

## Must-haves before live execution

Compiled from both waves' "implementation priority" tables:

| Technique | Phase | Why it's non-negotiable |
|---|---|---|
| NGR/EMOS calibration | 0.5 must | Raw fractions are wrong by 10–15 pp at bracket boundaries |
| Fee-aware edge floor | 0.5 must | Current `EDGE_MIN=8¢` inverts edge at P≈0.5 |
| Lyapunov 8-day gate | 0.5 must | Removes structurally-uneconomic trade universe |
| σ² = P(1-P)/√t correction | 1 must | Naive σ at all maturities = systematically wrong sizing |
| Kelly with ρ=−1/(K−1) | 1 must | Standard Kelly is wrong on mutually exclusive brackets |
| CUSUM change-point on PnL | 1 must | Edge degradation bleed without it |
| Kill-switch file (`./halt`) | 0 | Already in plan; preserve |
| Reconciliation on warm-start | 1 must | Ghost-order class of bugs |

## Resume narrative

The single coherent sentence the research supports as the endpoint of
this work:

> "Built a probabilistic-forecaster-vs-event-contract framework on
> Kalshi: calibrated GEFS+ECMWF ensemble (NGR + EMOS + conformal
> intervals) for weather brackets with intraday T−3h Bayesian update,
> plus HAFS/HWRF+NDBC-SST-nudged hurricane landfall model over the
> 2026 Atlantic season. All trading maker-only, Avellaneda-Stoikov
> adapted for bounded [0,1] payoffs with σ²=P(1-P)/√t, Kelly-sizing
> corrected for mutually-exclusive bracket correlation, real-time
> dashboard in stdlib Go with embedded SSE + htmx + TradingView
> Lightweight Charts. N events/season, Brier skill +X over NHC cone,
> $Y PnL over Z months."

That's the destination. The 12-week plan is how we get there.
