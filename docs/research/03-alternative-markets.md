# Alternative high-alpha event markets (wave 1, agent 3)

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

Short answer: sports is where the money is (~90% of Kalshi volume), macro is where the defensible edge is, politics is where the narrative is, and entertainment / hurricanes are small but quirky-enough to have real alpha. Mention markets and logical-arb are emerging niches. Below is the ranked shortlist after filtering on edge × liquidity × data × resume-worthiness.

## 1. Macro Releases — CPI / NFP / Fed Funds (`KXCPI*`, `KXPAYROLLS`, `KXFED*`, `KXRATECUTCOUNT`)

**Edge mechanism.** On CPI release day, Kalshi resolves a distribution while Bloomberg consensus is a single point estimate. A [Fed working paper](https://www.federalreserve.gov/econres/feds/files/2026010pap.pdf) shows Kalshi already beats Bloomberg consensus on headline CPI with statistical significance — but the market itself is partially populated by retail who underweight ADP-implied priors and seasonal-adjustment idiosyncrasies. Structural edge is (a) running an ADP→BLS regression + Cleveland Fed nowcast + trimmed-mean CPI nowcast to produce your own distribution 24h before release, (b) quoting both sides of the ladder 5–15 minutes post-print while retail is still reading the headline. Print is at 08:30 ET; Kalshi ladders take 60–300 seconds to fully reprice, which is the window.

**Data source + cost + latency.** FRED (free, <1s), [Cleveland Fed Inflation Nowcasting](https://www.clevelandfed.org/indicators-and-data/inflation-nowcasting) (free, daily), BLS direct API (free, release instant), [CME FedWatch API](https://www.cmegroup.com/market-data/market-data-api/fedwatch-api.html) ($25/mo for intraday, free end-of-day; [pyfedwatch](https://github.com/ARahimiQuant/pyfedwatch) on GitHub reproduces it from free CME Fed Funds futures).

**Volume.** Kalshi has ~$450M open interest across FOMC rate decisions; individual FOMC strikes have cleared $100M in a single month. 24h economics volume is ~$2M, but it bursts to $10M+ on release days.

**Biggest risk.** Kalshi headline-CPI market is *already* beating Bloomberg — you are competing with well-funded macro desks, not retail. NFP median absolute miss is huge (~80k), so variance per trade is brutal.

**Resume-worthiness: 5/5.** "Built a CPI nowcaster using trimmed-mean disaggregation + ADP, hit Brier skill of X vs Bloomberg, ran it on Kalshi with $Y PnL over 10 release cycles" is a DRW / Jane Street / Citadel interview slam-dunk.

## 2. Sports — NBA Moneylines + In-Play (`KXNBAGAME*`, `KXNFLGAME*`)

**Edge mechanism.** Kalshi's NBA/NFL game markets are 90% of platform volume but pricing is mostly from retail + DK migrators. Persistent cross-venue dislocations exist vs. (a) Betfair Exchange, (b) Pinnacle sharp lines, (c) DraftKings B-book. Because Kalshi is a CLOB with lower fees on sports (3.5% taker on indices, 7% otherwise) and DK/FD are B-books with vig, same-game arbs at the 1–3% level appear and persist a few seconds to several minutes — longer than Polymarket↔Kalshi political arbs.

**Data source + cost + latency.** [Betfair Exchange API](https://developer.betfair.com/exchange-api/) Delayed key is free with 1–180s delay (not viable for in-play tail); Live key is a one-off £299. [The Odds API](https://the-odds-api.com/) gives 500 free calls/month across 40+ books. ESPN's undocumented endpoints expose play-by-play and win-probability for free.

**Volume.** Super Bowl Sunday 2026: $871M in a single day on Kalshi. Masters week: $545M.

**Biggest risk.** You're competing with every sportsbook's trading desk and dedicated arbitrage firms. Kalshi void rules (the $30k tennis case) will eat you if you don't read the resolution source meticulously.

**Resume-worthiness: 3–4/5.** Measurable and scalable but less intellectually glamorous than macro.

## 3. Hurricane Landfall & Category (`KXHURRICANE*`, `KXHURRICANELAND*`)

**Edge mechanism.** NHC issues advisories every 3h (every 2h when a storm is close) plus discussion products. Retail reads the cone and trades accordingly. You run a 30-member GEFS ensemble + HAFS/HWRF hurricane-specific models + past-24h-of-SST anomalies into a Bayesian update over landfall-location buckets. The cone itself is the 67th-percentile envelope, so there is intrinsic mispricing in the tails. Kalshi prices update slowly relative to the 5:00/11:00/17:00/23:00 UTC advisory drops because the retail trader is waiting for the *next advisory* when the *model runs* already contain the signal.

**Data source + cost + latency.** NHC ATCF a-deck / b-deck files (free, updated by cycle), NOAA NOMADS GEFS (free, ~6h cycle), HAFS output (free), NDBC buoys for SST (free). Total cost: $0. Latency: model outputs 2–4h after cycle time.

**Volume.** Climate/Weather is ~$43.6M weekly (April 2026 number). Named-storm markets during active landfall windows often see $500k–$5M per storm; seasonal June–November.

**Biggest risk.** Seasonality: maybe 6–12 tradable storms/year. Storm track uncertainty means variance of PnL per event is high even when the model is right.

**Resume-worthiness: 5/5.** Clean, quantitative, scientific, unusual.

## 4. Political "Mention" Markets (`KXTRUMPSAY`, `KXLASTWORDMENTION`, `KXEARNINGSMENTION*`)

**Edge mechanism.** Structural: these resolve on word frequency in a speech transcript. You have base-rate data (Trump says "tremendous" 62% of speeches ≥5min long; earnings calls for META mention "AI" in 98% of post-2023 calls). Retail trades emotionally. Fit a hierarchical Poisson model on historical transcripts. Thin books with maker-rebate math that favors the quant.

**Data source + cost + latency.** Rev.com public transcripts, White House press-pool archive, SeekingAlpha earnings-call transcripts, C-SPAN API — all free or low-cost.

**Volume.** 2026 Super Bowl mention markets cleared ~$3.55M per game during playoffs. Mid-six-figures for "what will Trump say this week" markets.

**Biggest risk.** Subjective resolution edge cases; regulatory risk (CNBC April 2026 flagged mention markets as investigation target).

**Resume-worthiness: 2/5.** "I built a Poisson model of Trump speeches" reads as a joke.

## 5. Oscars / Awards Entertainment (`KXOSCAR*`, `KXEMMY*`)

**Edge mechanism.** Gold Derby aggregates experts + ~10k user picks and publishes odds — the canonical best-available priors and they are free. Kalshi's Oscars markets underperform Gold Derby slightly on tail categories (Best Makeup, Best Sound). Structural edge: scrape Gold Derby hourly for the 19 non-Big-Five categories, fade Kalshi when it deviates >8pp from Gold Derby consensus on a category with <$50k volume. Kalshi called 19/24 categories correctly in 2026 — the 5 it missed were the small ones.

**Volume.** $48.4M total Oscars contracts traded on Kalshi in 2026, >$100M when you add Polymarket. Seasonal January–March.

**Resume-worthiness: 3/5.**

## Hostile-check / what to avoid

- **Follower-count & viral social markets** — no reliable data feed, resolution ambiguity.
- **<10¢ contracts** — [Whelan's Makers and Takers](https://www.karlwhelan.com/Papers/Kalshi.pdf) shows <10¢ buyers lose >60%.
- **RFQ parlays** — [Sportico documented heavy retail losses](https://www.sportico.com/business/sports-betting/2025/kalshi-parlay-combo-rfq-explainer-1234877038/).
- **Naive Kalshi↔Polymarket political arb** — disappears in seconds; surviving spreads are resolution-rule differences. One [Substack postmortem](https://fiftycentdollars.substack.com/p/i-lost-30k-due-to-kalshis-void-rules) documents $30k loss from misreading void rules.
- **AI-agent-trades-for-you** — [Andrew Pierno's experiment](https://blog.andrewpierno.com/i-let-an-ai-trade-real-money-on-kalshi-for-60-days-it-lost-65/) lost $65 in 60 days on weather.

## Cross-venue arbitrage summary

Persistent 1–5% spreads after fees exist in three places: (1) Polymarket↔Kalshi political markets due to trader-pool segmentation — [Rajiv Sethi "Limits of Arbitrage"](https://rajivsethi.substack.com/p/the-limits-of-arbitrage) documents how Polymarket's non-US users and Kalshi's US-only base keep spreads wide; (2) Kalshi↔Betfair Exchange on international soccer/tennis — Betfair is a true exchange so comparing mids is clean; (3) Kalshi↔DK prediction-market product, which is brand-new (launched Dec 2025) and still has shallow books.

## Opinion — with 6 months and $50–$200 risk budget

**Pick hurricane landfall (`KXHURRICANE*` family) first, with CPI (`KXCPI*`) as a piggyback starting Q3.**

Reasoning: with $200 total, you cannot compete in sports arb (fees + round-trip costs eat a $5 position), and you cannot compete against Jane Street / Susquehanna in CPI *from day one* — they are trading it now. But hurricane markets are (a) a natural extension of your existing weather-bracket code, (b) a sparse field of competitors because the modeling is domain-specific and the season is short, (c) a dataset (HAFS + GEFS ensembles) that *is* the payload-size retail can't process. The 2026 Atlantic season starts June 1 — six months is perfect timing for pre-season backtest on 2020–2025, then live in hurricane season.

Then, once your infra is proven and you have 2–3 months of live PnL, extend to CPI: same Bayesian framework, different feature set (ADP + Cleveland Fed nowcast). That tells a *single coherent story on a resume*: "I built a probabilistic-forecaster-vs-event-contract framework, initially for weather and hurricanes, then extended to headline CPI." That sentence is a job.

## Sources

- [Kalshi market-data page](https://kalshi.com/market-data)
- [Kalshi API rate limits](https://docs.kalshi.com/getting_started/rate_limits)
- [Kalshi fee schedule](https://kalshi.com/fee-schedule)
- [Fed working paper: Kalshi and the Rise of Macro Markets](https://www.federalreserve.gov/econres/feds/files/2026010pap.pdf)
- [NBER w34702](https://www.nber.org/system/files/working_papers/w34702/w34702.pdf)
- [Whelan "Makers and Takers"](https://www.karlwhelan.com/Papers/Kalshi.pdf)
- [CEPR "Economics of the Kalshi prediction market"](https://cepr.org/voxeu/columns/economics-kalshi-prediction-market)
- [Kalshi hurricane markets announcement](https://news.kalshi.com/p/hurricane-markets)
- [Kalshi Oscars recap](https://news.kalshi.com/p/2026-oscar-recap-kalshi-markets-predicted-80-percent-winners)
- [Kalshi mention markets](https://defirate.com/prediction-markets/mention-markets/)
- [CME FedWatch API](https://www.cmegroup.com/market-data/market-data-api/fedwatch-api.html)
- [Cleveland Fed Inflation Nowcasting](https://www.clevelandfed.org/indicators-and-data/inflation-nowcasting)
- [Betfair Developers — Exchange API](https://developer.betfair.com/exchange-api/)
- [NHC](https://www.nhc.noaa.gov/)
- [Rajiv Sethi: Limits of Arbitrage](https://rajivsethi.substack.com/p/the-limits-of-arbitrage)
- [QuantPedia: Systematic Edges in Prediction Markets](https://quantpedia.com/systematic-edges-in-prediction-markets/)
- [fiftycentdollars: $30k void-rules loss](https://fiftycentdollars.substack.com/p/i-lost-30k-due-to-kalshis-void-rules)
- [Andrew Pierno: AI-trades-Kalshi loss postmortem](https://blog.andrewpierno.com/i-let-an-ai-trade-real-money-on-kalshi-for-60-days-it-lost-65/)
- [Sportico: RFQ parlay losses](https://www.sportico.com/business/sports-betting/2025/kalshi-parlay-combo-rfq-explainer-1234877038/)
- [Fortune: Kalshi Super Bowl $871M](https://fortune.com/2026/02/10/kalshi-super-bowl-sunday-871-million-sports-gambling-michael-lewis-warning/)
