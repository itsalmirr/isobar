# 0014 — Accept the structure-first pivot; demote directional weather to a side track

- **Date**: 2026-06-09
- **Status**: Accepted
- **Decider**: Solo operator (verbal acceptance 2026-06-09; build loop authorized same day)
- **Numbering note**: `audit/02-architecture.md` tentatively sketched "ADR-0014–0020"
  for Phase 2 structural decisions but never landed them as files. This ADR claims
  0014; the audit proposals renumber from 0015 onward if/when they land.

## Context

Four gate evaluations on real tape ran against the directional-weather thesis:

1. Floor v1 (2026-05-02): claimed gate MET — **retracted same day** (OTM selection bias).
2. Floor v2 (2026-05-05, n=299): gate **NOT MET** — MOS loses to the Kalshi mid by
   10pp Brier in the tradeable [35,65)¢ zone, bootstrap CI excludes zero.
3. v3 Bayesian (2026-05-19): gate **NOT MET** — full-universe realized −$4,963.86;
   diagnosis: the market ingests the same public METAR feed at zero latency.
4. v4 H4 (2026-05-19): first positive backtest (+$793.74) but **NOT YET PROVEN** —
   one day carried it, filter mined from the scored window, Sharpe CI includes zero.

The only forward evidence lost money (week 1: −$30.24 at 22.3% win rate; the 24
re-scored paper positions: expected +$9.45, realized −$0.05), albeit confounded
(MOS disabled by the model-set mismatch; recorder thrashing 3 of 5 days).

[docs/research/09-structure-first-pivot.md](../research/09-structure-first-pivot.md)
analyzes this record (§1), rejects the naive generalization — a multi-domain
*directional* scanner re-derives the same failure against sharper counterparties
(§2) — and proposes capturing mispricings provable from the price sheet itself
(§3): deterministic-settlement capture, internal-consistency arbitrage
(exhaustive bracket sums, threshold-ladder monotonicity, complement pairs), and
stale-book repricing lag. The research corpus had already flagged these as
unexploited whitespace (01 §3) and parked tail-MM with a revisit trigger of
"after the Phase 0.5 gate" — which has now fired in the failing direction.

## Decision

We will build the structure-first scanner per proposal 09, phases A→B→C, on the
existing market-agnostic data plane (`internal/kalshi`, `journal`, `replay`).
We will not build directional fair-value models for new domains. Directional
weather is demoted to a time-boxed side track: one MOS refit on
`gfs_seamless,ecmwf_ifs025`, one fair forward test against the existing v5 gate,
then close per the existing off-ramp if it fails.

**Frozen gate numbers** (pre-registered here; measurement may not move them):

- **G-A** (data plane): 14 consecutive days, ≥95% recorder uptime, ≥80% of
  expected frames/day, ≥300 markets recorded, eligibility checker green.
- **G-B** (opportunity existence, measured on ≥14 days of healthy wide tape):
  ≥25 executable structural instances/week · net ≥2¢ after fees at displayed
  size · median persistence ≥3s · capturable EV ≥$10/week at the RISK.md cap
  ladder. Unmet → no execution build; publish the negative measurement.
- **G-C** (live canary, $50 sub-bankroll, taker/IOC only): 14 days, realized
  capture ≥50% of predicted, zero cap violations, zero unhandled rejects.

**Hard rail**: Phase C (any code path that can place a real order) additionally
requires explicit operator sign-off in a live session. Autonomous build runs
never wire, call, or test order placement against the real API.

**Fee modeling**: per-series `fee_type`/`fee_multiplier` read from the public
API (verified present on `/trade-api/v2/series/KXHIGHNY`, 2026-06-09:
`"fee_type":"quadratic","fee_multiplier":1`), replacing the flat hardcoded
schedule for cross-category scanning. `fee.go`'s formula stays; the multiplier
becomes data.

## Alternatives considered

- **Continue calibration-only** (ADR-0009 Track C, GEFS GRIB backfill) — rejected
  as flagship: the same-information-later disadvantage is structural; more
  calibration cannot fix latency-zero ingestion of a shared public feed. Retained
  only as the side track's existing scope.
- **Multi-domain directional scanner** — rejected: each domain needs a model that
  out-forecasts a sharper resident (research 01/03; ADR-0002 precedent). A
  generic directional scanner is adverse selection with extra steps.
- **Declare research artifact and stop** — premature: the structural whitespace
  is unmeasured, the data plane makes measuring it cheap, and a negative G-B is
  itself a publishable result.

## Consequences

- Good: one detector family covers every Kalshi vertical (the "any
  sport/weather/crypto" requirement); data plane reused verbatim; the
  calibration corpus gets a second life as the settlement-certainty table;
  pre-registered gates preserve the house epistemics.
- Bad: the opportunity classes are Grade-4 hypotheses until Phase B (proposal 09
  §0); thin-arb execution risk is real (the cited-but-unverified "78% fail to
  execute"); wide recording costs disk; G-B is calendar-bound (≥14 days of tape),
  not effort-bound.
- Neutral: weather-only packages (`edge/ticker.go`, `calibration/*`,
  `openmeteo`) freeze rather than delete; ADR-0004's two-signal scope is
  superseded in spirit — "signals" become "detector classes."

## Reversal cost

Low. Detectors and the universe service are additive packages beside `internal/edge`;
no existing capability is removed. If G-B fails, the cost was the recorder fix
(needed anyway), a detector library, and a novel negative measurement. The
canary for "this pivot was wrong" is G-B itself — that is what it is for.

## References

- [docs/research/09-structure-first-pivot.md](../research/09-structure-first-pivot.md) — the accepted proposal
- [reports/phase0.5/2026-05-05-floor-estimate-v2.md](../../reports/phase0.5/2026-05-05-floor-estimate-v2.md), […v3-bayesian-verdict.md](../../reports/phase0.5/2026-05-19-v3-bayesian-verdict.md), […v4-h4-filter.md](../../reports/phase0.5/2026-05-19-v4-h4-filter.md), […forward-week1-verdict.md](../../reports/phase0.5/2026-05-26-forward-week1-verdict.md) — the empirical record
- [docs/research/01-kalshi-landscape.md](../research/01-kalshi-landscape.md) §3, [03-alternative-markets.md](../research/03-alternative-markets.md), [06-microstructure-math.md](../research/06-microstructure-math.md) — whitespace + fee math
- ADR-0002 (kill BTC), ADR-0004 (two-signal scope), ADR-0009 (canary escalation)
