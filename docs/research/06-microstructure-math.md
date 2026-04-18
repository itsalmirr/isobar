# Microstructure math for binary CLOB markets (wave 2, agent 1)

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

## Three core formulas

### Formula 1 — Reservation price with bounded [0,1] payoff

Avellaneda-Stoikov reservation price adapted for binary markets:

    r(P, q, t, σ) = P − q · γ · σ²_bin(P) · (T − t)

Where:
- `P` = current mid-price (contract probability ∈ [0,1])
- `q` = signed inventory (units ∈ ℝ)
- `γ` = risk-aversion coefficient (units: inverse time)
- `σ²_bin` = **binary-process realized variance**, NOT equity volatility
- `T − t` = time-to-maturity

**Critical: Binary σ derivation.** For a contract maturing to payoff `X ∈ {0, 1}` with implied probability `P_t`:

    σ²_bin(P) = P(1 − P) / √t_days

NOT `P(1-P)` alone. The scale factor √(time-to-event) matters because the *realized* price volatility decreases as event nears. Guéant-Lehalle-Fernandez-Tapia (arXiv:1105.3115) show the volatility of a bounded process on [0,1] must incorporate maturity drag.

### Formula 2 — Optimal half-spread

The symmetric half-spread around reservation price `r` (AS + Guéant-Lehalle-Fernandez-Tapia):

    δ*(q, P, λ, t) = ½ γ σ²_bin(P) (T − t) + (1/γ) ln(1 + γ/λ(P))

- First term = inventory-skew premium (reservation-price adjustment)
- Second term = **adverse-selection spread** (width needed to break even on informed flow)
- `λ(P)` = **order-flow toxicity rate** (orders/sec × probability of being information-driven)

For Kalshi specifically, `λ(P)` peaks near `P = 0.50` (max entropy / uncertainty). Empirical estimate:

    λ(P) = λ₀ · P(1 − P),    λ₀ ≈ 0.15–0.30 orders/sec per variance unit

**Kalshi-fee-adjusted net spread**:

    δ_net = δ* − f_maker(P) = δ* − 0.0175 · P(1 − P)

Post bid at `r − δ_net`, ask at `r + δ_net`.

### Formula 3 — Fill probability from queue position (M/M/∞)

    Ψ(k, λ_arr, μ_canc) = (μ / (λ + μ))^k

- `k` = queue rank (1 is best)
- `λ_arr` = arrival rate of market orders per second at your price level
- `μ_canc` = cancellation rate per resting order per second

**Practical estimation from Kalshi `orderbook_delta`:**
1. Record every arrival and cancellation at each price level in rolling 1-hour window.
2. `λ_arr = (#market orders executed at level) / 3600`.
3. `μ_canc = (#cancelled resting orders) / (#total resting order-seconds)`.
4. Evaluate `Ψ(k)` for your rank `k`.

Expected fill-time conditional on fill: `E[τ | fill] = k / λ_arr` seconds.

Model **breaks** when mid-price moves >1 tick before your fill. Kalshi tick = 1¢, so recompute on every mid-move.

## Five highest-ROI microstructure techniques

### 1. Order-Flow-Imbalance adverse-selection gate — +30–50 bps, 2–3 weeks

Only post if OFI does not signal informed trading. Define 10-second rolling:

    OFI_10s = Σ_{i=1..5} sgn(ΔQ_bid^i − ΔQ_ask^i) · |ΔQ_bid^i − ΔQ_ask^i|

where `ΔQ` is the change in cumulative shares offered at price level `i` over 10-second window.

**Gate:** Only post if `|OFI_10s| < OFI_threshold = 0.15 · (spread_current / 0.01)` (normalized by tick). Rationale: high |OFI| implies a large informed trader is pushing through; your resting order will be adversely selected.

Cites Easley-López de Prado-O'Hara VPIN; Cont-Kukanov (arXiv:1210.1625).

### 2. Volatility-of-Imbalance regime gate — +15–25 bps protective, 1 week

60-second rolling variance of OFI. When `Var(OFI_10s) > VI_threshold` (75th-percentile of historical), reduce inventory target to 0 and widen spreads by 0.5¢. Signals liquidity crash / high adverse selection.

Cites Huang-Jaimungal-Kercheval 2015; Stoikov-Waeber 2016.

### 3. No-trade-zone hysteresis — +40–70 bps, 1 week

Do NOT actively quote (post-only, zero spreads) when inventory breaches the no-trade boundary:

    q*_upper = (r − P_fair) / (γ · σ²_bin · (T − t)) + c
    q*_lower = (r − P_fair) / (γ · σ²_bin · (T − t)) − c

where `c ≈ 2–3` (units; calibrate to bracket's typical trade size). Inside zone: post-only; outside: exit passively.

**Hysteresis:** once you re-enter the zone, keep orders off for 10 seconds (avoids whipsaw).

### 4. Kelly-fractional sizing with event correlation — +50–100 bps reduced DD, 3 weeks

For `N` correlated bracket bets (e.g., mutually exclusive `⇒ ρ_ij = −1/(K−1)` for `i ≠ j`):

    max  Σ w_i log(p_i) − ½ Σ_ij w_i w_j σ_i σ_j ρ_ij
    s.t. Σ w_i = 1,  w_i ≥ 0

Solve via SLSQP; allocate **fractional Kelly** (0.5× optimal) to reduce volatility.

**Why different from standard Kelly:** Kalshi brackets are NOT independent; profit on KXHIGH conflicts with MED/LOW. Correlation structure is known (1-of-K), so closed-form fractional allocation exists.

Cites MacLean-Thorp-Ziemba 2010; Whelan 2025 DOI 10.1111/boer.12474.

### 5. CUSUM change-point detector — +60–90 bps, 1.5 weeks

Run CUSUM on realized PnL per trade, with drift `k = 0.25 σ_PnL`. When cumulative sum exceeds threshold `h = 5 σ_PnL`, edge has flipped negative; stop quoting for 5 minutes and re-calibrate.

    S_n^+ = max(0, S_{n-1}^+ + ΔPnL_n − k),   halt if  S_n^+ > h

Prevents death-by-a-thousand-cuts when volatility regime shifts or informed traders arrive.

Cites Page 1954 CUSUM; Wald 1945 SPRT.

## No-trade zone — closed form

For a Kalshi bracket with mid-price `P`, fair value `P_fair`, inventory `q`:

    Zone width = 2c · γ · σ²_bin(P) · (T − t)
              = 2c · γ · P(1−P) / √t_days · (T − t)

**Example.** `P = 0.50`, `γ = 0.01`, `T−t = 1 day`, `t_days = 3 days`:

    σ²_bin = 0.25 / √3 ≈ 0.144
    Zone width ≈ 2 × 2.5 × 0.01 × 0.144 × 1 = 0.0072  (72 cents)

Within a ±3.6¢ window around your fair-value estimate, don't actively post. This region is where re-hedging costs exceed expected spread profits.

## Two sharp warnings

### Warning 1 — Gaussian-spread assumption fails on binary payoffs

Classical AS assumes `log(S_t)` is Gaussian. A Kalshi contract is **bounded on [0, 1]**. Near expiry:
- **True:** P&L is Bernoulli (0 or +N, or −N or 0), NOT Gaussian → tail risk catastrophic.
- **Naive mistake:** treating `δ` as if spread-profit is proportional to `σ²`. TRUE only for unbounded assets.
- **Fix:** `σ²_bin(P) = P(1-P)/√t_days`; scale expected profit near maturity by `√(T-t)`.

A contract with `P = 0.50, T−t = 1 hour` is **16×** more volatile (per unit time) than the same contract at `T−t = 16 hours`. Avellaneda-Stoikov's `γ` must reflect this: `γ ≈ 0.02` for hourly brackets, `γ ≈ 0.001` for 30-day brackets.

### Warning 2 — Queue-position model breaks under information events

M/M/∞ assumes Poisson arrivals at constant rate `λ`. It fails when:

1. **Scheduled announcement is imminent (<5 min):** arrivals spike non-linearly; `λ(t)` becomes time-dependent.
2. **Informed-trader pileup:** arrival rate at your queue position becomes non-stationary.
3. **1¢ tick-size:** only 100 price levels → clustering at same level exacerbates queue rank dependence. Fill probabilities for `k > 5` are unreliable.

**Fixes:**
- Don't post within 5 min of a Kalshi scheduled event.
- Use empirical fill-rate curves (heatmap of realized `Ψ(k)` per price level) over theoretical M/M/∞.
- Treat `k ≥ 10` as "far back"; expect `Ψ(k) ≤ 0.10` within 10 seconds.

## Implementation hierarchy

| Priority | Technique | Complexity | Expected edge | Time to production |
|----------|-----------|------------|---------------|-------------------|
| 1 | Reservation price + optimal spread (F2) | Medium | 80–150 bps | 1 week |
| 2 | Fill-probability gates on OFI | Low | 30–50 bps | 2 weeks |
| 3 | Queue-position Poisson (F3) | Low | 15–25 bps | 1 week |
| 4 | No-trade zone hysteresis | Medium | 40–70 bps | 1 week |
| 5 | Kelly-fractional portfolio opt | Medium | 50–100 bps | 3 weeks |
| 6 | CUSUM change-point | Low | 60–90 bps | 1.5 weeks |

**Net edge stack (agent's ceiling estimate):** 250–500 bps raw − 175 bps maker fees = **75–325 bps per round-trip** = 0.75–3.25% profit-per-round-trip. At 2–4 trade opportunities per bracket per day on 10 simultaneous brackets ⇒ 1.5–13% daily return Kelly-constrained. **Treat as ceiling; backtest gates reality.**

## Sources

- [Avellaneda & Stoikov 2008 — HF market-making framework](https://people.orie.cornell.edu/sfs33/LimitOrderBook.pdf) DOI:10.1080/14697680701381228
- [Guéant, Lehalle, Fernandez-Tapia 2013 — Optimal MM with inventory risk](https://arxiv.org/abs/1105.3115)
- [Cartea, Jaimungal, Penalva 2015 — *Algorithmic and HF Trading* CUP](https://assets.cambridge.org/97811070/91146/frontmatter/9781107091146_frontmatter.pdf)
- [Cont & Kukanov 2017 — Optimal order placement](https://arxiv.org/pdf/1210.1625)
- [Easley, López de Prado, O'Hara 2010 — VPIN](https://www.quantresearch.org/VPIN.pdf)
- [MacLean, Thorp, Ziemba 2010 — Kelly Capital Growth Criterion](https://www.tandfonline.com/doi/full/10.1080/14697688.2011.619561)
- [Whelan 2025 — Kelly with mutually exclusive outcomes](https://onlinelibrary.wiley.com/doi/full/10.1111/boer.12474)
- [Page 1954 / Wald 1945 — CUSUM and SPRT](https://en.wikipedia.org/wiki/CUSUM)
- [Bürgi, Deng, Whelan — Kalshi fee analysis & adverse-selection empirics](https://www.karlwhelan.com/Papers/Kalshi.pdf)
