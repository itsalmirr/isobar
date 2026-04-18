# Kalshi bot landscape survey (as of April 2026)

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

Caveat: GitHub is badly polluted with SEO-bait "polymarket-kalshi-arbitrage-bot" empty repos (keyword-stuffed READMEs, no code). I've filtered those out.

## 1. GitHub repo inventory

**Serious, non-abandoned projects** (ordered by signal, not stars):

| Repo | Lang | Last commit | Strategy | Quality signal | What's missing/broken |
|---|---|---|---|---|---|
| [rodlaf/KalshiMarketMaker](https://github.com/rodlaf/KalshiMarketMaker) | Python | active (2026-04) | MM (Avellaneda-Stoikov derivative) | 196★, companion [HN thread #43073377](https://news.ycombinator.com/item?id=43073377), author posted detailed retro | Single worker per market, no inventory netting across correlated markets; author admits literature assumes liquid/stable books |
| [orangejuicetin/kalshi_market_maker](https://github.com/orangejuicetin/kalshi_market_maker) | Python | stale | MM via websocket feed | Real code, from a running algo. [Log-odds-space quoting insight](https://news.ycombinator.com/item?id=43073378) | Stale; no backtest harness; no correlated-market hedging |
| [suislanchez/polymarket-kalshi-weather-bot](https://github.com/suislanchez/polymarket-kalshi-weather-bot) | Python | active | **Weather KXHIGH + GFS 31-member, Kelly sizing** — direct overlap with isobar thesis | Claims "$1.8k highest profit" (unaudited); 8% edge threshold | **No statistical calibration of ensemble vs realized freq; no microclimate/METAR nowcast** |
| [OctagonAI/kalshi-trading-bot-cli](https://github.com/OctagonAI/kalshi-trading-bot-cli) | TS | active | LLM-driven "deep research" | 218★, 66 forks, Kelly + 5-gate risk | LLM-as-trader is vibes-based; no out-of-sample evidence; hosted-service funnel |
| [ryanfrigo/kalshi-ai-trading-bot](https://github.com/ryanfrigo/kalshi-ai-trading-bot) | Python | active | 5-LLM ensemble consensus | 362★ hype-driven | Same pathology; no PnL record |
| [taetaehoho/poly-kalshi-arb](https://github.com/taetaehoho/poly-kalshi-arb) | Rust | 2025-12 | Cross-venue arb | 428★ but shallow | No title-matching / resolution-criteria verification — the hardest part |
| [Jon-Becker/prediction-market-analysis](https://github.com/Jon-Becker/prediction-market-analysis) | Python | active | **Largest public PM dataset** | 2.9k★, legit | Research only — reference dataset for calibration work |
| [evan-kolberg/prediction-market-backtesting](https://github.com/evan-kolberg/prediction-market-backtesting) | Python | active | Nautilus Trader adapter for Kalshi+Polymarket | 628★, serious infra | Weather/commodity instruments need custom definitions |
| [ammario/kalshi](https://github.com/ammario/kalshi) | **Go** | 2026-03 | Client library | Only serious Go client, 10★ | Thin; isobar may need to extend or roll its own |
| [geckoape99/kalshi-autosniper-bot](https://github.com/geckoape99/kalshi-autosniper-bot) | Python | 2026-04 | New-market listing sniper | 54★ | Race-to-zero; dominated by pro MMs |

**Adjacent worth studying:**
- [betcode-org/flumine](https://github.com/betcode-org/flumine) — Betfair trading framework, 229★, production-grade
- [CarlosIbCu/polymarket-kalshi-btc-arbitrage-bot](https://github.com/CarlosIbCu/polymarket-kalshi-btc-arbitrage-bot) — has a [`thesis.md`](https://github.com/CarlosIbCu/polymarket-kalshi-btc-arbitrage-bot/blob/main/thesis.md) laying out the Coinbase-lead-lag logic isobar pursues; worth reading as prior-art check

**Missing across the ecosystem:** Go implementations beyond thin clients; rigorous statistical calibration (Brier/reliability plots); queue-position-aware order routers; systematic handling of settlement-window microstructure.

## 2. What strategies people claim work

**Strong evidence:**
- **Market making (as a Maker).** [Bürgi-Deng-Whelan paper](https://www.karlwhelan.com/Papers/Kalshi.pdf) studied 300k+ contracts: takers lose ~32% on average; makers lose ~10% on average. [Kalshi rebate programs](https://help.kalshi.com/incentive-programs/liquidity-incentive-program) (60% rebate on $750–2000 fees, 80% above) tip it.
- **HN commenter [claimed #1 on Kalshi volume leaderboard](https://news.ycombinator.com/item?id=43083269)** running a working MM.
- **Polymarket arbitrage:** [$40M extracted April 2024–April 2025](https://www.chaincatcher.com/en/article/2212288) per IMDEA Networks research.

**Medium evidence:**
- **Weather ensemble edge.** [Kalshi Weather Edge](https://www.kalshiweatheredge.com/), [suislanchez bot](https://github.com/suislanchez/polymarket-kalshi-weather-bot) converge on GFS 31-member + METAR → 8–10% edge. Multiple independent practitioners — generally a good sign, but no audited PnL.
- **Arbitrage 12–20% monthly.** Plausible at small size with fast execution, but [a 2025 study found 78% of low-volume arb ops failed to execute](https://newyorkcityservers.com/blog/prediction-market-arbitrage-guide).
- **NCAAB NO-side + NBA thin markets.** One practitioner [claims 74% win rate +10% ROI on NCAAB NO](https://4amclub.substack.com/p/how-to-make-money-trading-on-kalshi).

**Weak evidence:** LinkedIn "I made $972 with AI weather" posts. Survivorship bias heavy.

**What clearly fails:** [HackingTheMarkets' ESPN-winprob NFL bot](https://hackingthemarkets.com/anatomy-of-a-kalshi-nfl-trading-bot/) — pros have feeds ≥30s faster. Any retail-feed sports strategy is adversely selected.

Segment rollup:
- **Crypto (BTC hourly/15-min):** picked-over. [Coinbase integrated Kalshi Jan 2026](https://www.coindesk.com/markets/2026/01/27/coinbase-rolls-out-prediction-market-to-u-s-customers) — lead-lag collapsing.
- **Weather:** still has edge per practitioners (see §5).
- **Politics/elections:** MM-dominated on majors.
- **Sports:** dominated by pros; longshot-fade is the only retail-accessible edge.
- **Econ data (CPI/NFP/FOMC):** [CME-FedWatch ↔ Kalshi basis at 1.5–4.5%](https://newyorkcityservers.com/blog/best-prediction-market-bots-tools).
- **Commodities (new April 2026):** [Kalshi Commodities Hub launched](https://www.bloomberg.com/news/articles/2026-04-15/kalshi-expands-commodities-predictions-market-on-war-volatility) — nat gas, coffee, copper, grains, lithium, nickel — thin books, Jump/Susquehanna warming up. Pre-saturation window.

## 3. Conspicuous whitespace

Rarely or never implemented publicly:

1. **Rigorous calibration of GFS ensemble → Kalshi bracket probabilities.** Everyone dumps raw ensemble counts; nobody publishes reliability diagrams or isotonic-calibrated outputs.
2. **Settlement-window microstructure (last 30 min).** No public repo models drift and wide-spread behavior before `market_determined` events.
3. **Cross-asset basis trades:** Kalshi FOMC vs SOFR/FF futures, Kalshi CPI vs TIPS breakevens, Kalshi Brent vs ICE Brent.
4. **Correlation-aware MM across bracketed markets.** All public MMs treat each bracket as independent. Netting inventory across a basket should halve capital-at-risk.
5. **Commodities hub bots.** Launched April 15, 2026. No repos yet.
6. **Queue-position-aware order routers.**
7. **NWS-product parsing beyond daily climate reports.** Hourly NDFD/METAR anomaly detection (sensor outages, CLI-vs-realtime drift).
8. **Death-clause / void-rule arbitrage guardrails** post-Khamenei controversy.

## 4. Kalshi-specific gotchas

- **Void/scratch rules cost a trader $30k.** [Adhi Rajaprabhakaran's postmortem](https://fiftycentdollars.substack.com/p/i-lost-30k-due-to-kalshis-void-rules): Kalshi resolves scratched sports markets at "last traded fair price," not void. Sportsbook mental model = blowup.
- **Khamenei-death controversy (March 2026).** [~$54M in YES positions frozen](https://www.washingtonpost.com/technology/2026/03/04/kalshi-khamenei-bet-controversy/). Lesson: read the resolution criterion, not the market title.
- **Fee formula is parabolic.** Taker `ceil(0.07 × N × P × (1−P))`; Maker `ceil(0.0175 × N × P × (1−P))`. Max at 50¢, near-zero at extremes.
- **Rebate tiers matter.** 60% back on $750–2000 monthly fees, 80% above. Below tier = structurally uncompetitive vs qualified MMs.
- **Rate limits:** Basic tier is 20 reads/sec per [docs](https://docs.kalshi.com/getting_started/rate_limits). Needs exponential backoff + jitter.
- **Auth:** RSA-PSS, signed `timestamp + method + path-sans-query`. Common footgun: signing with query string → silent 401s.
- **Legacy/`_dollars` field renames** per [changelog](https://docs.kalshi.com/changelog) broke clients multiple times in 2025–26.
- **Ohio $5M fine (April 2026)** and broader state enforcement is platform-risk tail.

## 5. Sharp-vs-retail read

**Picked over / sharp-dominated (avoid without real infra):**
- KXBTC (hourly, 15-min) — Coinbase/Kalshi integration Jan 2026 closed naïve lead-lag.
- NFL/NBA/MLB moneyline — Susquehanna, Jump, Kalshi Trading internal desk.
- FOMC headline markets — CME-FedWatch arb tightened to 1.5–4.5%.
- Election majors — MM-dominated.

**Fresher / retail mispricings still visible:**
- **Daily weather KXHIGH / KXRAIN for non-primary cities** (beyond NYC/ORD/MIA/AUS). Same-day, post-10am, METAR-resolved but brackets not re-priced. 8–10% gross edge per practitioners.
- **NCAAB NO-side** and low-volume sports props.
- **Newly-listed commodities markets (April 2026).**
- **Bracketed mutually-exclusive markets** where retail buys all brackets summing to >$1.00 (butterfly mispricing).
- **Culture/entertainment** — tiny size, high dispersion.
- **Settlement-window mispricings (T-30min to T-0)** — retail impatience.

## Top 5 directions nobody is publicly pursuing

1. **Calibrated GFS-ensemble trading with a reliability layer.** Raw 31-member counts → isotonic regress against historical Kalshi resolution → trade only where calibrated edge > fees + 2σ. Every public bot skips calibration.
2. **METAR-vs-CLI drift trades** in 2–6h pre-settlement window.
3. **Bracket-basket butterfly arbitrage** on KXHIGH / KXCPI / sports-spread markets. Sum-of-YES breaks $1.00 on thin markets.
4. **Cross-asset basis:** Kalshi CPI vs inflation swaps; KXFED vs SOFR/FF futures.
5. **Kalshi commodities hub** — thin books + retail + Jump still calibrating = highest-quality stale-pricing window. 3–6 month shelf.
