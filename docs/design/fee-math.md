# Design — `internal/fees` package

> Kalshi's fee formula lives in three research docs but not in code.
> This design locks down the API and publishes test vectors **before**
> we implement, so any implementation that passes the vectors is
> correct by construction.
>
> Status: proposed, not implemented. Implementation targets W9 but
> the risk module in W5 will depend on this, so it lands earlier.

## Why a dedicated package

Fee math gets consumed by four layers:

1. `internal/signal/*` — fee-aware edge floor gates signal emission.
2. `internal/risk` — pre-trade check includes post-fee EV.
3. `internal/microstructure` — optimal-spread math subtracts maker
   fee from the raw A-S half-spread.
4. `cmd/analyze` — Phase 0.5 simulated PnL must apply real fees.

Having three or four copies of `ceil(0.07·N·P·(1-P))` scattered across
these packages is a recipe for silent drift. **One implementation,
one test-vector set, everyone imports it.**

## Kalshi's fee formula

From [Kalshi's fee schedule](https://help.kalshi.com/trading/fees)
and independently reproduced in Bürgi-Deng-Whelan
"[Makers and Takers: The Economics of the Kalshi Prediction Market](https://www.karlwhelan.com/Papers/Kalshi.pdf)":

```
taker fee (per side, in dollars) = ceil(0.07 · N · P · (1 − P) · 100) / 100
maker fee (per side, in dollars) = ceil(0.0175 · N · P · (1 − P) · 100) / 100
```

Where:

- `N` = number of contracts traded on that side
- `P` = fill price in dollars, `P ∈ (0, 1)` (or in cents, `1..99`)
- The ceil is to the nearest **cent** — Kalshi rounds up to the next
  integer cent per side

Equivalent formulation in integer cents (what our API uses):

```
taker cents = ceil(7    · N · (P_cents/100) · (1 − P_cents/100))
maker cents = ceil(1.75 · N · (P_cents/100) · (1 − P_cents/100))
```

### Dimensional check (essential — this is a common bug)

Test: N=10, P=0.47 (47¢), taker.

- `0.07 × 10 × 0.47 × 0.53 = 0.17437` **dollars** per side
- `= 17.437` cents per side
- `ceil(17.437) = 18` cents per side

This matches the worked example in
`docs/research/02-weather-thesis-audit.md` §"Risk 3" — which is the
ground-truth we fix the implementation against.

Note: the formula's raw output is in **dollars**, despite looking
"small enough to be cents". Getting this wrong is how you'd under-fee
by 100×; the test vectors below catch it immediately.

### The fee parabola

`P × (1−P)` peaks at `P = 0.5` with value `0.25`. Fees at extreme
prices (`P ∈ {0.01, 0.99}`) are ~100× smaller than at the mid. This
is the single most important fact about Kalshi's fees:

**Bracket signals near `P = 0.5` pay the maximum fee.** Signals near
the extremes pay almost nothing. `internal/fees.EdgeFloor` exposes
this dependency directly so callers can filter signals before sizing.

## API

```go
// Package fees implements Kalshi's per-side maker/taker fee formula
// exactly as published in their fee schedule.
//
// All prices are integer cents in [1, 99]. Degenerate inputs
// (contracts ≤ 0, price outside [1, 99]) return 0 — callers must
// not be computing fees on markets that aren't tradable.
package fees

// Role identifies which side of a fill the fee applies to.
type Role int

const (
    RoleMaker Role = iota
    RoleTaker
)

// Per returns the per-side fee in integer cents (rounded up, as
// Kalshi does). Formula:
//
//   taker: ceil(7    · N · p · (1−p))  cents
//   maker: ceil(1.75 · N · p · (1−p))  cents
//   where p = priceCents / 100
//
// Returns 0 for degenerate inputs.
func Per(role Role, contracts, priceCents int) int

// RoundTrip sums the fees for both sides of a round-trip trade:
// enter at enterPrice playing enterRole, exit at exitPrice playing
// exitRole. Fee is in integer cents.
func RoundTrip(
    contracts int,
    enterRole Role, enterPrice int,
    exitRole  Role, exitPrice  int,
) int

// EdgeFloor returns the minimum gross edge, in cents per contract,
// for a round-trip trade to have non-negative expectancy with 1¢
// slack. Uses taker-taker fees (worst-case execution) so the floor
// is conservative.
//
//   floor = ceil(round_trip_taker_taker_cents / N) + 1
//
// Callers should reject signals whose gross edge per contract is
// ≤ EdgeFloor(...).
func EdgeFloor(contracts, priceCents int) int
```

Everything is `int`. No `float64` at the API surface — prevents
accidental dollar-vs-cent errors. Internal computation uses
`math.Ceil(float64)` then converts back.

## Test vectors (the proof)

Any implementation that passes these is correct. We commit this table
to `internal/fees/testdata/kalshi-fee-vectors.csv` and the Go test
reads it.

### `Per` — taker

| N   | P (cents) | computation            | expected cents |
|-----|-----------|------------------------|----------------|
| 1   | 50        | ceil(7 × 0.25)         | 2              |
| 1   | 10        | ceil(7 × 0.09)         | 1              |
| 1   | 90        | ceil(7 × 0.09)         | 1              |
| 1   | 1         | ceil(7 × 0.0099)       | 1              |
| 1   | 99        | ceil(7 × 0.0099)       | 1              |
| 10  | 50        | ceil(7 × 10 × 0.25)    | 18             |
| 10  | 47        | ceil(7 × 10 × 0.2491)  | 18             |
| 10  | 99        | ceil(7 × 10 × 0.0099)  | 1              |
| 100 | 50        | ceil(7 × 100 × 0.25)   | 175            |
| 100 | 1         | ceil(7 × 100 × 0.0099) | 7              |
| 100 | 99        | ceil(7 × 100 × 0.0099) | 7              |
| 100 | 47        | ceil(7 × 100 × 0.2491) | 175            |

### `Per` — maker

| N   | P (cents) | computation               | expected cents |
|-----|-----------|---------------------------|----------------|
| 1   | 50        | ceil(1.75 × 0.25)         | 1              |
| 1   | 10        | ceil(1.75 × 0.09)         | 1              |
| 10  | 50        | ceil(1.75 × 10 × 0.25)    | 5              |
| 10  | 47        | ceil(1.75 × 10 × 0.2491)  | 5              |
| 100 | 50        | ceil(1.75 × 100 × 0.25)   | 44             |

### `Per` — degenerate (must return 0)

| N   | P (cents) | expected |
|-----|-----------|----------|
| 0   | 50        | 0        |
| -5  | 50        | 0        |
| 10  | 0         | 0        |
| 10  | 100       | 0        |
| 10  | -1        | 0        |
| 10  | 101       | 0        |

### `RoundTrip`

| N   | enterRole | enterP | exitRole | exitP | computation                                | expected |
|-----|-----------|--------|----------|-------|--------------------------------------------|----------|
| 10  | Taker     | 47     | Taker    | 53    | Per(T,10,47)+Per(T,10,53) = 18+18          | 36       |
| 10  | Maker     | 47     | Taker    | 53    | Per(M,10,47)+Per(T,10,53) = 5+18           | 23       |
| 10  | Maker     | 47     | Maker    | 53    | Per(M,10,47)+Per(M,10,53) = 5+5            | 10       |
| 100 | Taker     | 50     | Taker    | 50    | 175+175                                    | 350      |
| 1   | Taker     | 50     | Taker    | 50    | 2+2                                        | 4        |

### `EdgeFloor`

| N   | P (cents) | RT taker-taker | per-contract | +1 slack | expected |
|-----|-----------|----------------|--------------|----------|----------|
| 10  | 50        | 36             | 3.6 → ceil 4 | 4+1      | 5        |
| 100 | 50        | 350            | 3.5 → ceil 4 | 4+1      | 5        |
| 1   | 50        | 4              | 4 → ceil 4   | 4+1      | 5        |
| 10  | 10        | 14             | 1.4 → ceil 2 | 2+1      | 3        |
| 10  | 90        | 14             | 1.4 → ceil 2 | 2+1      | 3        |
| 100 | 1         | 14             | 0.14 → ceil 1| 1+1      | 2        |
| 10  | 47        | 36             | 3.6 → ceil 4 | 4+1      | 5        |

**Reading this table:** at `P=50¢` we need ≥ 5¢ gross per contract
just to break even on worst-case execution. At `P=10¢` or `P=90¢`
the floor drops to 3¢. This is the *shape* of what the strategy
wants — prefer extreme-price brackets, avoid coin-flip brackets
unless edge is unusually large.

## Why this lives in Go only (not Python)

Fee math is *runtime* code — it runs every time the signal fires,
every time risk evaluates a prospective order, every time the
analyzer simulates PnL. It must not cross the ADR-0006 boundary.

A Python copy of these formulas would be fine for a Jupyter
exploration of "what happens to our edge distribution at different
price levels?" — but that copy doesn't ship to production. The one
in `internal/fees` is the authoritative source; any notebook
duplicate is explicitly a toy.

## Verification plan

Implementation is not this doc's job; **but** the implementation is
trivially verifiable against this spec:

1. Copy the tables above into `internal/fees/testdata/vectors.csv`.
2. Write a single table-driven test that reads each row, calls the
   named function with the stated inputs, and asserts equality to
   the expected cents.
3. Add a property test: `Per(Maker, N, P) ≤ Per(Taker, N, P)` for
   all valid N, P. Makers always pay less than takers at the same
   fill.
4. Add a property test: `Per(r, N, P) == Per(r, N, 100-P)` — the
   formula is symmetric about `P=50`.
5. Add a benchmark: `BenchmarkPer` — fee math runs on every signal
   tick; sub-100ns is required. Regression-gate it.

## Non-goals

- **Historical fee-schedule backfill.** Kalshi has changed the
  multipliers before. When they do again, we bump the schema version
  (`v2`) and accept the old config for replay analysis of pre-change
  data. Not today.
- **Rebate accounting.** The maker rebate program (60% back on
  $750–2000 monthly fees, 80% above per Kalshi's Liquidity Incentive
  Program) is a post-trade settlement concern. At $50–$200 of
  capital we never reach the tier; when we do, a new design doc
  covers it.
- **FIX pricing** or sub-cent ticks. Kalshi's spot-contract API
  uses 1¢ ticks; this package encodes that assumption.

## References

- `docs/research/02-weather-thesis-audit.md` §Risk 3 — worked example.
- `docs/research/05-literature-review.md` — fee-parabola citation.
- `docs/research/06-microstructure-math.md` — maker fee in the A-S
  half-spread adjustment.
- `docs/STRATEGY.md` — where the fee-aware edge floor is stated.
- [Kalshi fee schedule](https://help.kalshi.com/trading/fees).
- [Kalshi fee schedule PDF](https://kalshi.com/docs/kalshi-fee-schedule.pdf).
- [Bürgi-Deng-Whelan empirical paper](https://www.karlwhelan.com/Papers/Kalshi.pdf).
