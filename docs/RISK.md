# Risk

> The risk controls that separate a trading bot from a money-burning
> goroutine. Each control is stated as an **invariant** that must hold at
> runtime, with an enforcement point in the code (Phase 1+) and a test that
> would fail if it regressed.
>
> Phase 0 has no live execution, so most controls here are design-ahead —
> but their tests will exist *before* any order-placement code is merged.

## The hierarchy

```
┌──────────────────────────────────────────────────────────────────┐
│ L0 — Physical kill switch (./halt file present ⇒ no orders)      │
├──────────────────────────────────────────────────────────────────┤
│ L1 — Pre-trade caps: per-ticker, per-series, per-portfolio       │
├──────────────────────────────────────────────────────────────────┤
│ L2 — State reconciliation: no new orders until warm-start passes │
├──────────────────────────────────────────────────────────────────┤
│ L3 — Signal sanity: NaN / empty book / stale quote ⇒ no signal   │
├──────────────────────────────────────────────────────────────────┤
│ L4 — Post-trade monitoring: daily drawdown halts, reconciliation │
└──────────────────────────────────────────────────────────────────┘
```

An order can only leave the process when **every** layer above passes.
Each layer fails closed: any error or ambiguity results in no order.

## L0 — Kill switch

**Invariant.** If a file named `./halt` exists in the bot's working
directory, no order is transmitted to Kalshi.

- Checked at the start of each `Signal.Tick`.
- Checked again immediately before every `KalshiExec.Submit`.
- File existence is queried with `os.Stat` — fast, atomic enough for our
  purposes. TOCTOU window is tolerable (worst case: one extra order while
  the operator is creating the file, which is why we check again at submit).

**Test.** `touch halt` mid-tick ⇒ zero order submissions, even if the
strategy emits quotes.

## L1 — Pre-trade caps

Every cap is a constant read from `config.yaml`:

```yaml
risk:
  max_per_ticker_contracts: 100
  max_per_ticker_notional_dollars: 10
  max_per_series_notional_dollars: 25
  max_portfolio_notional_dollars: 50
  max_order_contracts: 100
  # Fee-aware edge floor: the minimum calibrated edge needed to justify
  # a trade scales with bracket mid-price because Kalshi fees peak at 50c.
  # Implementation: edge > 0.07 * 2 * P * (1-P) + 1c per side.
  fee_aware_edge_floor: true
  # Lyapunov horizon: forecast error doubles every ~5 days on a single
  # station. Past 8 days, no calibration rescues the signal.
  max_lead_time_days: 8
```

**Invariants.**

1. After this order fills completely, no cap is exceeded.
2. Caps are computed on **projected exposure**, not currently-filled
   exposure — a resting order counts.
3. If any cap would be exceeded, the order is skipped with a warn log
   (not an error — caps are expected to bite).
4. **Sizing uses Kelly with the mutually-exclusive-bracket correlation
   correction** `ρ_ij = −1/(K−1)` for a K-bracket set. Standard Kelly
   over-sizes on correlated bets; this correction is mandatory for
   any KXHIGH/KXLOW/KXHURRICANE portfolio. Allocate fractional
   Kelly (0.5×).
5. **Reservation price / half-spread use bounded-payoff σ:**
   `σ²_bin(P) = P(1−P)/√t_days`. Naive σ = P(1-P) over-estimates
   volatility away from expiry and under-estimates at expiry; same γ
   across all maturities = systematically wrong sizing.

**Enforcement point.** `internal/risk.Gate(ctx, order, state) error`.
Called by `cmd/bot` between signal emission and exec submission.

**Tests.**
- Table-driven: for each cap, a case that's within budget and a case
  that breaches by one contract.
- Property test: any sequence of orders gated by `Gate` results in a
  state where no cap is ever breached (invariant holds across the full
  sequence).

## L2 — State reconciliation

**Invariant.** After process start (or reconnect that missed fills),
the bot does not place a new order until it has:

1. Loaded `state.sqlite` (open orders, positions, last-seen fill IDs).
2. Fetched open orders + positions from Kalshi REST.
3. Diffed the two. If the diff is non-empty, either a reconciliation
   action is taken or the bot halts with a non-zero exit code for human
   intervention.

**Rationale.** Ghosts ruin you. A resting order we forgot about, an
unknown position, a fill we missed: every one of these is a ticking
bomb. A reconciliation-first startup eliminates the class.

**Test.** Process killed mid-tick → restart: reconciliation loop either
cleans up or halts; never silently proceeds.

## L3 — Signal sanity

**Invariants (strategy-agnostic preconditions).**

- Mark price is finite (`!math.IsNaN && !math.IsInf`).
- Bid < Ask (book is not crossed).
- At least one level on each side with non-zero quantity.
- Most recent frame timestamp for the market is within `staleness_max`
  (default 30s for WS-derived state).
- For `weather`: ensemble response has N members matching the
  expected count (N = 31 for GFS); if N differs, log and skip the
  signal for this refresh.
- For `hurricane-landfall`: only emit during active NHC advisories
  (AL or EP basin); skip when regime-switching HMM posterior over
  {weakening, strengthening, steady} is within 15 pp of uniform
  (i.e., "uncertain" state).

**Enforcement point.** Baked into each `Signal.Tick` prologue.
Centralized precondition checks in `internal/signal/guard.go`.

**Tests.** Fuzz: random `MarketState` inputs; assert no quotes are
emitted from states that fail any precondition.

## L4 — Post-trade monitoring

**Daily drawdown halt.**
- If intraday PnL ≤ `-max_daily_drawdown_dollars` (default: 10% of
  bankroll), the bot enters a persisted `halted_today` state and
  refuses to place new orders until the operator clears it.
- Persisted so restarts don't un-halt.

**Reconciliation loop (steady-state).**
- Every 60s: re-fetch open orders + positions, diff against in-memory.
- Non-empty diff for >60s ⇒ halt (treat as L0 until resolved).

**Clock drift.**
- Every 60s: time a round-trip to Kalshi's REST, compare server time
  from response `Date` header to local wall clock.
- Drift > 250ms ⇒ warn log.
- Drift > 1s ⇒ halt.

## Explicit non-controls (things we do **not** rely on)

- **Kalshi server-side rejection** is not a risk control. We refuse to
  send an order that should be rejected; we don't let the exchange be
  our conscience.
- **Paper-trade PnL** is not evidence that live trading is safe. Only
  Phase 2's fill-sim-matches-reality comparison counts.
- **Static config caps without tests** are not caps; they're
  aspirations. Every cap ships with a property test.

## Risk-change procedure

Changing a risk parameter (raising a cap, relaxing a precondition,
disabling a check) requires:

1. A `docs/decisions/<date>-risk-<slug>.md` entry with: current value,
   new value, rationale, reversal plan.
2. A PR with the code change + updated test expectations. The PR body
   must reference the decision doc.
3. Review by someone other than the author (even in a solo setup,
   explicit self-review with a 24-hour cooling-off period is policy).

Risk parameters are never tweaked "just for this session".

## Incident response (Phase 3+)

**If the bot loses money faster than expected during the trading day.**
1. `touch halt` to freeze new orders.
2. Cancel resting orders via Kalshi's UI (the bot does not cancel on
   halt — it stops acting, doesn't panic-cancel).
3. Pull a postmortem draft:
   - which strategy.
   - which tickers.
   - what changed in the last 24h (commits, config, market regime).
4. File `docs/postmortems/<date>-<slug>.md` within 48h.
5. No restart until the postmortem identifies a root cause **and** a
   test that would have caught it is merged.

**If reconciliation diverges.**
1. Halt (already automatic per L4).
2. Export state snapshot: `bot diff-state > state.json`.
3. Manual reconciliation via Kalshi UI.
4. Restart with `bot --reconcile-from=state.json` (Phase 3 flag to be
   built) which replays fills against a clean state.

## Glossary of impact

- **Hard halt.** Process exits 1; systemd / operator must restart.
- **Soft halt.** Process stays up, ignores ticks; cleared by removing
  `./halt`.
- **Warn.** Logged, recorded, but no impact on trading.
- **Panic.** Never acceptable outside programmer-bug scenarios;
  appearance in an error path is itself a bug to fix.
