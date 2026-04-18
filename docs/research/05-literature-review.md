# Literature review — prediction-market alpha, market-making, weather post-processing, and hostile views

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

## Part 1 — Ten highest-value papers/sources

**1. Gneiting, Raftery, Westveld & Goldman (2005), "Calibrated Probabilistic Forecasting Using Ensemble Model Output Statistics and Minimum CRPS Estimation."** *Monthly Weather Review* 133(5). DOI: 10.1175/MWR2904.1. The foundational EMOS/NGR paper: it shows raw ensembles are under-dispersed and biased, and that a simple normal regression on ensemble mean+spread produces dramatically better-calibrated temperature forecasts. Directly relevant because isobar's Phase-0 `p_fair = fraction of members in bracket` assumes the ensemble is calibrated — it almost certainly is not. *How we'd use it:* fit EMOS/NGR on the Phase-0 tape's Open-Meteo ensemble + METAR ground truth before computing bracket probabilities.

**2. Raftery, Gneiting, Balabdaoui & Polakowski (2005), "Using Bayesian Model Averaging to Calibrate Forecast Ensembles."** *Monthly Weather Review* 133(5). DOI: 10.1175/MWR2906.1. BMA as an alternative to EMOS when members are exchangeable but have distinct kernels; yields predictive PDFs rather than just a corrected mean/variance — exactly what a bracket book needs. *How we'd use it:* A/B BMA vs NGR on the same tape and pick whichever gives lower Brier on historical bracket outcomes.

**3. Rasp & Lerch (2018), "Neural Networks for Postprocessing Ensemble Weather Forecasts."** *Monthly Weather Review* 146(11). arXiv:1805.09091. Shows a small feed-forward NN beats EMOS by ~10-15% CRPS with station embeddings. Important because it gives a concrete next step after EMOS if the basic calibration works. *How we'd use it:* Phase-4 upgrade once we have >6 months of paired ensemble/observation data.

**4. Bi, Xie, Zhang, Chen, Gu & Tian (2023), "Accurate medium-range global weather forecasting with 3D neural networks" (Pangu-Weather).** *Nature* 619, 533–538. DOI: 10.1038/s41586-023-06185-3. Together with Lam et al. (2023) "GraphCast" (*Science* 382, 1416–1421, DOI:10.1126/science.adi2336) and Kurth et al. "FourCastNet" (arXiv:2202.11214), the ML-weather trio that beats IFS on many variables. Pangu and GraphCast weights are publicly released. *How we'd use it:* treat Pangu/GraphCast as a 32nd "member" appended to GFS ensemble; re-EMOS the combined set.

**5. Avellaneda & Stoikov (2008), "High-frequency trading in a limit order book."** *Quantitative Finance* 8(3). DOI: 10.1080/14697680701381228. The reservation-price + inventory-penalty quoting framework everyone adapts. The binary/bounded-payoff adaptation is non-trivial — σ of the underlying ≠ σ of the price process when price is bounded [0,1]. *How we'd use it:* inventory-adjusted maker quotes around `p_fair` rather than the fixed `floor(p_fair*100) ± 1` in current STRATEGY.md.

**6. Wolfers & Zitzewitz (2006), "Interpreting Prediction Market Prices as Probabilities."** NBER WP 12200. https://www.nber.org/papers/w12200. Canonical reference on when market prices are ≈ probabilities and when they're not (risk aversion, longshot bias, limits to arb). Gives the theoretical floor on how much calibration bias to expect on Kalshi. *How we'd use it:* prior on expected mispricing magnitude; informs whether 8¢ edge threshold is too loose or too tight.

**7. Levitt (2004), "Why are Gambling Markets Organised So Differently from Financial Markets?"** *Economic Journal* 114(495). DOI: 10.1111/j.1468-0297.2004.00207.x. Shows NFL bookmakers deliberately mis-set lines because bettors are systematically biased (favorites); the house exploits this rather than balancing the book. Transfers to Kalshi because retail flow there shows similar patterns. *How we'd use it:* if we detect a Kalshi bracket with persistent one-sided retail flow, take the other side structurally — don't assume balanced-book mean reversion.

**8. Snowberg & Wolfers (2010), "Explaining the Favorite–Long Shot Bias: Is It Risk-Love or Misperceptions?"** *Journal of Political Economy* 118(4). DOI: 10.1086/655844. Concludes the bias is driven by misperception of probabilities, not risk preferences — meaning it's persistent and exploitable if fees permit. The critical finding: bias is measured on horse racing with ~17% takeout; Kalshi fees are much lower, so a smaller bias is still exploitable. *How we'd use it:* test for longshot bias on Kalshi low-volume brackets (<5¢ or >95¢) in Phase 0.5; if present, bias quotes accordingly.

**9. Barber & Odean (2000), "Trading Is Hazardous to Your Wealth."** *Journal of Finance* 55(2). DOI: 10.1111/0022-1082.00226. And Barber, Huang, Odean & Schwarz (2022), "Attention-Induced Trading and Returns: Evidence from Robinhood Users." *Journal of Finance* 77(6). DOI: 10.1111/jofi.13183. Hostile literature: retail traders systematically lose, and the losses accrue disproportionately on attention-driven assets. Kalshi weather brackets during a heatwave = textbook attention spike. *How we'd use it:* a prior that the retail *counterparty* is there; but also a warning that we are retail-sized and subject to the same biases unless disciplined.

**10. Kalshi fee schedule** (https://kalshi.com/docs/fees) and **Polymarket LP / market-making writeups** — specifically Paradigm's "Improving on Uniswap v3 for LMSR-style event markets" posts (https://www.paradigm.xyz/) and Hanson (2003), "Combinatorial Information Market Design," *Information Systems Frontiers* 5(1), DOI: 10.1023/A:1022058209073 — the LMSR foundation. Kalshi is CLOB not LMSR, but understanding LMSR quoting on Polymarket informs whether multi-venue arb is worth Phase-4 effort. *How we'd use it:* baseline for Phase-4 multi-venue feasibility; also: the Kalshi fee formula `fee = ceil(0.07 * contracts * price * (1-price) * 100)/100` means fees explode near 50¢ — current STRATEGY.md correctly refuses flat-fee approximation, but the signal-selection gate should explicitly avoid mid-book brackets unless edge >> 10¢.

**Honorable mentions:**
- Paul & Weinbach (2007), "Does Sportsbook.com set pointspreads to maximize profits?" *Journal of Prediction Markets* 1(3) — disposition/overround estimates, practitioner-facing.
- Manaster & Mann (1996), "Life in the Pits," *Review of Financial Studies* 9(3), DOI: 10.1093/rfs/9.3.953 — maker adverse-selection baseline.
- Hendershott & Menkveld (2014), "Price pressures." *Journal of Financial Economics* 114(3), DOI: 10.1016/j.jfineco.2014.08.001 — post-announcement reversion; relevant for NFP/CPI lag.

**Practitioner folklore, unverified:** Claims that "Kalshi reprice lag on 08:30 releases is ~200-800ms" circulate on Discord/Twitter but no peer-reviewed measurement exists. Treat as a hypothesis for Phase 0.5 to measure, not a fact.

## Part 2 — Five concrete techniques, ranked by cost/edge

1. **EMOS/NGR calibration on the ensemble before bracket probability** (Refs 1, 2). **Cost:** ~2 weeks. **Expected edge:** Gneiting et al. report Brier-skill improvements of 0.05–0.15 vs raw ensemble; on a bracket book this is the difference between an 8¢ edge and a 2¢ illusion. Highest-ROI technique on the list.

2. **Fee-aware edge threshold that scales with bracket center-price** (Kalshi fee schedule). **Cost:** ~2 days. **Expected edge:** not additional alpha, but prevents taking negative-expectancy trades at 40–60¢ mid where the fee curve peaks. Probably the bug most likely to silently drain $50 → $30.

3. **NFP/CPI reprice-lag microstructure study** (Hendershott-Menkveld as prior). **Cost:** ~1 week to build the event-window replay in `cmd/analyze`, then wait for ≥3 release cycles. **Expected edge:** practitioner-folklore size is 2–10¢ per bracket for ~30s post-release; unverified. Phase 0.5 should measure this explicitly before committing strategy code.

4. **Inventory-adjusted maker quoting (Avellaneda-Stoikov adapted for bounded payoffs)** (Ref 5). **Cost:** ~2 weeks. **Expected edge:** probably 10–20% improvement in fill-quality-adjusted PnL vs the flat `p_fair ± 1¢` rule, based on A-S benchmarks in equities; binary-market adaptation requires deriving the reservation price under a Beta rather than Gaussian posterior. Good Phase-2 upgrade, not Phase-1.

5. **Append ML-weather model (GraphCast or Pangu) as synthetic ensemble member, then EMOS the combined pool** (Ref 4). **Cost:** ~3 weeks. **Expected edge:** 10–20% CRPS improvement over IFS/GFS in medium range (3–7 days). Most Kalshi weather brackets are 0–2 day horizon where ML models' edge over GFS is smaller. Good Phase-4 bet, premature now.

## Part 3 — Three sharp warnings

1. **"Maker-only against retail is sustainable" is contested.** STRATEGY.md treats maker-only as a defensive posture that preserves edge. But Kalshi has professional MMs (Susquehanna, Jump subsidiaries are rumored/practitioner-folklore, unverified) whose quotes are adverse-selected *against isobar* whenever we're on the book longer than they are. Maker-only is necessary but not sufficient; the actual question is queue position and quote-lifetime, not maker vs taker. Phase 2's "queue-position aware fill simulator" is the right instinct — do not skip it.

2. **31-member ensemble + `p_fair = fraction in bracket` is a calibration fallacy.** With 31 members, the minimum non-zero probability you can quote is 3.2%, and bracket-edge estimates near a boundary have sampling noise of ~±9% (binomial at p=0.5). Current STRATEGY.md's 8¢ edge threshold is within the sampling noise envelope. Without EMOS/NGR (or at minimum a KDE over members), a large fraction of "edges" in Phase 0.5 will be noise. The hostile papers (Ref 9) on retail overconfidence apply to us too — we will see edges that don't exist.

3. **"Lead-lag BTC vs Coinbase" is likely dead alpha at a small-bot scale.** arbhunter-style leads of "tens of seconds" were plausible in 2022–2023; practitioner reports (unverified, Twitter 2024–2025) suggest the lead has compressed to sub-second on liquid BTC contracts. With Kalshi's REST-only order placement (no FIX, no colocation), a Go bot from a cloud VM is physically behind any serious competitor by 10–50ms of network. The BTC hourly thesis should be measured carefully in Phase 0.5 and probably killed unless the measured lag is >5s consistently.
