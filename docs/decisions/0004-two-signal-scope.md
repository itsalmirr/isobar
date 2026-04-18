# 0004 — Scope to two signals (weather-calibrated + hurricane-landfall); defer tail-MM

**Status:** Accepted
**Date:** 2026-04-18
**Authors:** isobar team

## Context

The Wave-1 alternative-markets research (`docs/research/03`) ranked
non-BTC market categories and flagged five plausible candidates:

1. Macro releases (CPI/NFP/FOMC) — sharp-dominated from day one.
2. Hurricane landfall — sparse field, seasonal, perfect fit for a
   small-bot runway.
3. Sports (NBA/NFL moneylines) — requires Betfair Live API + plumbing.
4. Mention markets (Trump/earnings) — regulatory risk + subjective
   resolution.
5. Oscars/awards — narrow, seasonal, low velocity.

The Wave-2 EVT/stat-mech research (`docs/research/08`) surfaced a sixth:

6. **`tail-mm`** — power-law low-volume bracket maker-quoting. The
   Kalshi bracket-volume distribution is power-law in the tail
   (α ≈ 2–3 on secondary cities like KXHIGHPHX/KXHIGHSAT); those books
   are retail-dominated. A trivial Avellaneda-Stoikov maker-quoter
   adapted for bounded [0,1] payoffs captures 10–20¢/day/bracket in
   pure maker fees at near-zero directional risk. **Structural**, not
   one-shot: as volume grows, new brackets enter the tail.

Tail-MM is attractive for three reasons:

- It's **always-on** (not seasonal like hurricane).
- It's **uncorrelated with forecasting alpha** — a great portfolio
  diversifier.
- The execution math (`docs/research/06`) is already derived.

But scope creep is real. Each additional signal adds:

- A new `Signal` implementation + tests.
- New risk caps (per-signal + portfolio-level correlation with Kelly
  ρ = −1/(K−1)).
- Dashboard tiles.
- Backtest validation.
- Live-canary attention in Phase 3.

With a 12-week plan and a solo developer, three signals through
Phase 0.5 would stretch the calibration work dangerously thin.

## Decision

We scope Phase 0.5 to **two signals**:

- `weather-calibrated` (W2–5).
- `hurricane-landfall` (W6–8, timed for June 1 Atlantic season start).

`tail-mm` is **parked**, not killed. It is documented in
`docs/research/08` with full math and implementation sketch. It will
be revisited at **Phase 0.5 gate review (W10)** if:

- One or both primary signals fail their Brier-skill gate, leaving
  capacity to repurpose.
- One primary signal passes but capital-efficiency is bottlenecked by
  event frequency (e.g. hurricane's 6–12 events/year is low).
- The dashboard + microstructure infrastructure is sufficiently
  reusable that adding `tail-mm` costs <2 weeks.

If all three conditions are met we add `tail-mm` in a W13–14 bridge
between Phase 0.5 and Phase 1.

## Alternatives considered

- **Ship three signals now.** Rejected — Phase 0.5 gate is the
  binding constraint, and splitting calibration effort 3 ways weakens
  the strongest signal.
- **Ship `tail-mm` instead of `hurricane-landfall`.** Rejected —
  hurricane is seasonal with a hard deadline that keeps us honest;
  tail-MM is always-on and can be added any time.
- **Ship only `weather-calibrated`.** Rejected — Brier-gate failure
  on weather would leave us with nothing to show for Phase 0.5.
  Hurricane is a structural hedge.

## Consequences

Good:
- Scope is tight enough to hit the June-1 hurricane deadline.
- Each signal gets enough calibration attention to pass a meaningful
  Brier gate.
- Two-signal portfolio already stresses the Kelly ρ = −1/(K−1)
  correction we want to exercise.

Bad:
- If both primary signals fail their Brier gates, we have no Plan B
  ready; `tail-mm` would be a ≥2-week build from scratch at that
  point.
- The "always-on fee-capture" mental comfort of `tail-mm` is deferred
  to Phase 2+.

Neutral:
- The research document for `tail-mm` is durable — if scope opens up,
  we know exactly what to build.

## Reversal cost

Low. Adding `tail-mm` post-W10 is a known-scope build. The dashboard,
microstructure math, and risk hierarchy are all signal-agnostic.

Canary: at W10 Phase-0.5 gate review, if both primary signals fail
their Brier gates, we explicitly re-open this decision.

## References

- `docs/research/03-alternative-markets.md` — candidate survey.
- `docs/research/08-evt-statmech.md` — tail-MM math.
- `docs/research/SYNTHESIS.md` — consolidated reasoning.
- `docs/ROADMAP.md` — 12-week schedule with gates.
