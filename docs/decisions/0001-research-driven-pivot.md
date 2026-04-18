# 0001 — Pivot strategy after 8-agent research wave

**Status:** Accepted
**Date:** 2026-04-18
**Authors:** isobar team

## Context

The original Phase-0 design (see `docs/STRATEGY.md` at commit `b7ba2d9`)
proposed two trading signals:

1. **Weather brackets** priced as `p_fair = ensemble_members_in_bracket / 31`
   with an 8¢ edge threshold.
2. **BTC-hourly lead-lag** between Coinbase spot and Kalshi `KXBTCD`.

Two parallel research waves (8 agents, reports saved at
`docs/research/01`–`08`) produced a convergent indictment of both signals:

- Raw ensemble fractions are systematically biased probabilities.
  Scheuerer & Büermann (2014, arXiv:1407.0058) document **+34% CRPS
  improvement from NGR calibration**. Binomial sampling noise at N=31,
  P=0.5 is **±9%** — wider than the 8¢ threshold.
- Kalshi's fee schedule `ceil(0.07·N·P·(1-P))` **peaks parabolically at
  P=0.5**, exactly where bracket signals fire. A worked example shows
  a 6¢ raw edge shrinking to 2.4¢ after round-trip fees, and inverting
  if calibration bias is ≥3¢.
- Coinbase integrated Kalshi in January 2026 (see `docs/research/01`
  CoinDesk citation), compressing BTC-hourly lead-lag to sub-second.
  From a cloud VM, our round-trip latency floor is 10–50 ms — we are
  structurally behind any serious competitor.
- The atmospheric Lyapunov exponent λ ≈ 0.2 day⁻¹ (Palmer 2000 *Reports
  on Progress in Physics*; Zhang et al. 2019 *J. Atmos. Sci.*) means
  forecast error doubles every ~5 days. Weather signals beyond ~8 days
  lead are noise.

See `docs/research/SYNTHESIS.md` for the full argument.

## Decision

We rewrite `docs/STRATEGY.md` around **two calibrated signals**:

- **`weather-calibrated`** — NGR/EMOS fit per (station, season, lead)
  bin on ≥2y of paired GEFS + NWS-CLI archive; blended with ECMWF IFS
  (51 members, free); wrapped with conformal prediction intervals;
  EVT/POT for tail brackets; intraday Bayesian update at T−3h using
  observed METAR trajectory; Lyapunov 8-day hard gate on signal
  emission; fee-aware edge floor `> 0.07·2P(1-P) + 1¢` per side.
- **`hurricane-landfall`** — NHC ATCF advisories + HAFS/HWRF + NDBC SST
  nudging with a regime-switch HMM for storm-intensity state; timed
  for the 2026 Atlantic season (June 1).

Phase 0.5 gate is strengthened from "`>10 brackets/day with >8% edge
after fees`" to "**`>10 events/day with >5% calibrated edge after
fees` AND calibrated Brier-skill > market-implied on OOS 90-day
held-out**". Signals that fail the Brier gate are disabled.

## Alternatives considered

- **Stay with the naive thesis and measure it.** Rejected — the research
  is clear enough that Phase 0.5 would only confirm the loss. Spending
  a month on that measurement is negative-EV vs. spending it on
  calibration.
- **Pivot to CPI/NFP nowcasting as the primary signal.** Rejected for
  now — a Fed working paper shows Kalshi already beats Bloomberg
  consensus on headline CPI, i.e. DRW / Jane Street / Citadel are
  already trading it. We'd be a latecomer with $200. Deferred to a
  potential Phase 4 after hurricane infra proves itself.
- **Pivot to Kalshi Commodities Hub (launched April 2026).** Genuine
  whitespace (thin books, MMs still calibrating) but requires new
  market-type plumbing. Deferred 3 months.

## Consequences

Good:
- Signals are now grounded in peer-reviewed research, not hope.
- The two-signal scope is small enough for a solo developer with a
  6-month runway.
- Hurricane season starts June 1 — natural deadline keeps us honest.
- Resume narrative is coherent and measurable.

Bad:
- ~3 weeks of previous scaffolding (BTC-hourly plumbing, naive
  ensemble-fraction edge computation) is abandoned.
- Calibration work (NGR, conformal, EVT) requires a historical archive
  that has to be backfilled before Phase 1.
- Hurricane-landfall is seasonal — we get one chance a year.

Neutral:
- The `tail-MM` signal (power-law low-volume bracket fee capture)
  looks attractive but is deferred to keep scope tight (see ADR-0004).

## Reversal cost

Low-to-medium. The two-signal framework is general — if, during
Phase 0.5, one signal fails its Brier gate, the other continues.
If both fail, we reconsider. The infrastructure (journal, kalshi
client, retry, openmeteo) is signal-agnostic and remains useful.

Canary: if `cmd/analyze` Phase-0.5 gate report shows calibrated
Brier-skill ≤ market-implied on held-out data after NGR is
implemented, that's a kill signal.

## References

- `docs/research/SYNTHESIS.md` — consolidated decision doc.
- `docs/research/02-weather-thesis-audit.md` — critical audit of the
  naive thesis.
- `docs/research/05-literature-review.md` — 10 citations grounding the
  calibration approach.
- `docs/research/07-atmospheric-physics.md` — predictability ceilings.
- `docs/research/08-evt-statmech.md` — Lyapunov + EVT justification.
- Git commit `4258365` — "W1: complete research pivot".
