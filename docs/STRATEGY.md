# Bot strategy

> The edge thesis, the execution doctrine, and the failure modes we refuse to
> have. If a reader disagrees with any of this in writing, stop and resolve
> the disagreement before committing code to Phase 1.

## Thesis in one paragraph

Kalshi weather-bracket markets price discrete-temperature outcomes
(`[60,65)°F`, `[65,70)°F`, …) for a location and date. Retail flow dominates
these books. NOAA's GFS ensemble publishes 31 equiprobable forecast paths —
so for any bracket, a fair YES price is just the fraction of ensemble members
whose forecast lands inside the bracket. When that fraction diverges from
the market mid by more than fees + slippage, there is an edge. The ensemble
updates every 6h and micro-updates continuously; the market typically does
not react instantly. That lag is the bot's home.

Secondary thesis: BTC hourly `KXBTCD` contracts lag Coinbase spot by tens of
seconds on decisive moves. A measured lead-lag + maker-only execution is
profitable if (and only if) fees can be paid from the lead. This is an
`arbhunter`-style strategy we already know how to operate.

Both theses are *measured* in Phase 0.5, not assumed. If the measurement
fails the gate (`>10 brackets/day with >8% edge after fees`), the strategy
is killed without ceremony.

## The two signals

### `weather` — NOAA ensemble vs. bracket YES

**Pricing model.**

For a bracket `B = [lo, hi)` over location `L` on date `D`:

```
p_fair = (1 / N) * Σ_i  1{ x_i ∈ B }
```

where `x_i` is ensemble member i's forecast for L at the resolution time
(e.g., the `temperature_2m_max` value for D). N = 31 for GFS ensemble.

**Edge.**

```
edge_yes = p_fair - (yes_ask + fee_round_trip)
edge_no  = (1 - p_fair) - (no_ask + fee_round_trip)
```

`fee_round_trip` is the sum of maker/taker fees actually incurred on
open + close at the assumed fill prices. Never a flat constant — fees depend
on price, per Kalshi schedule.

**Entry.**

- Enter when `edge_side > EDGE_MIN` (default 8¢) and the book has size
  (`min_size` contracts at the far-touch).
- Quote maker-only, `yes_price = floor(p_fair * 100) - 1` or `+1`
  depending on side (joining, not stepping).
- One order per bracket per ensemble refresh.

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

### `btc-hourly` — Coinbase spot vs. `KXBTCD`

**Pricing model.**

For a KXBTCD bracket `[lo, hi)` with expiry `T`:

```
p_fair = P[ BTC_spot(T) ∈ [lo, hi) | BTC_spot(now), σ, time_to_T ]
```

Use a calibrated Black-Scholes-style log-normal evaluator with σ fit from
Coinbase trailing realized vol. The model is deliberately *simple*; the
edge isn't cleverness, it's speed-of-reaction relative to Kalshi retail.

**Entry / Exit / Caveats.** Inherited nearly verbatim from `arbhunter` —
see `internal/signal/btchourly` docstring in Phase 1 for the full spec.

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
