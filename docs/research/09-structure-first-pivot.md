# 09 — Structure-first pivot: market-agnostic opportunity capture

**Date**: 2026-06-09
**Status**: PROPOSAL — becomes binding only via an accepted ADR; supersedes nothing yet.
**Builds on**: [forward week 1 verdict](../../reports/phase0.5/2026-05-26-forward-week1-verdict.md),
[v4 H4 filter](../../reports/phase0.5/2026-05-19-v4-h4-filter.md),
[floor estimate v2](../../reports/phase0.5/2026-05-05-floor-estimate-v2.md),
[01-kalshi-landscape](01-kalshi-landscape.md) §3 whitespace, [SYNTHESIS](SYNTHESIS.md) deferred-decisions.

---

## 0. Epistemic status — what this proposal trusts, and how much

Nothing below is exempt from the house retraction culture. Inputs ranked by
provenance; **no Grade-3/4 claim is load-bearing** — the gates exist precisely
because the priors are weak.

| Grade | Provenance | Claims in this doc | Trust posture |
|---|---|---|---|
| 1 | Mechanically verified this session (greps, builds, file sizes, live probe) | Capability map (§4); no exec code wired; recorder outage; HEAD compile break | Act on it |
| 2 | Our tape + our code, reproducible commands | All PnL/Brier numbers in §1 | Trust the *direction*, audit the engine: one methodology retraction already happened upstream of these numbers, and `edge-scan`'s realized-PnL path has never been independently re-derived. See verification debt below |
| 3 | Inference/diagnosis | "Market = same feed at zero latency"; "directional weather can't be flagship" | A resource-allocation judgment on confounded forward data, **not a proof** — the §7 side track is the hedge, and a passing v5 reverses the demotion |
| 4 | April-2026 LLM web-research corpus (docs/research/01–08); citations unverified | Butterfly-mispricing prevalence, "78% of thin arbs fail to execute", maker/taker loss-rate study, repricing-lag windows, practitioner edge claims | Hypotheses about *where to look*, nothing more. Phase B measures; if the whitespace isn't there, G-B fails and we stop |

**Fee schedule check (2026-06-09, primary-source, partial):** the general
formulas in `fee.go` — taker `0.07·P·(1−P)`, maker `0.0175·P·(1−P)`, ceil to
the next cent per trade, maker charged only on resting orders that execute —
are corroborated by Kalshi's help center and multiple independent secondaries.
**But the help center confirms per-market fee exceptions exist** ("some markets
have fees that are different... special events"), and the research corpus
claims 3.5% taker on sports indices. `fee.go` currently applies one flat
schedule to every series — fine for 8 weather series, **silently wrong for a
cross-category scanner**.

**Verification debt — must clear before G-B's numbers are trusted:**
1. Fetch the official fee-schedule PDF (`kalshi.com/docs/kalshi-fee-schedule.pdf`;
   rate-limited at check time) and encode per-series multipliers into `fee.go`,
   re-checked at detector time, not assumed.
2. Hand-audit ~20 random rows of the realized-PnL pipeline (trade → pairing →
   resolution → fee → PnL) against raw tape + CLI truth — the Grade-2 numbers
   all flow through one unaudited engine.
3. Empirically establish WS subscription caps / frames-sec ceilings on a wide
   universe (Phase A day 1).
4. Per-series settlement-rule checklist before any detector class goes live (§8).

## 1. Why the current plan cannot be the flagship

The current plan is a *forecasting* plan: build a temperature model that beats the
Kalshi mid, then trade the difference. After four gate evaluations on real tape,
the evidence against it is consistent and, in the tradeable zone, statistically
significant:

| Evidence | Result |
|---|---|
| Floor v2 (n=299, tradeable [35,65)¢ zone) | MOS Brier 0.401 vs market 0.255 — **market wins by 10pp, CI excludes 0** |
| Full-universe realized backtests (6-day OOS tape) | naive −$9,198 → MOS −$6,113 → MOS+Bayes −$4,964. Every calibration layer helps; none reaches zero |
| v4 H4 filter (the only positive) | +$794 in-sample, **carried by one day (May 4 = +$583)**, discovered by subset-mining the same window it's scored on; Sharpe 0.68 with CI [−1.2, +2.6] |
| Forward week 1 | **−$30.24**, 22.3% win rate; the 24 paper positions expected +$9.45, realized −$0.05 |
| Mechanism (v3 diagnosis) | The market ingests the same public METAR/ASOS feed we do, at zero latency. We are not informationally ahead; we are the same information, later |

The forward week was confounded (MOS disabled by the model-set mismatch, recorder
thrashing 3 of 5 days, n=15 windows), so "directional weather is dead" is *not*
proven. But "directional weather has failed every pre-registered gate it has been
given" *is* proven, and the structural reason — same public feed, worse latency —
will not improve with more calibration effort. A strategy whose best case is
"tie the market on its own information" is a research artifact, not a flagship.

**Disposition**: directional weather is demoted to a side experiment (§7). The
calibration stack is *not* wasted — it gets a new job in §4.1.

## 2. What "scan everything in real time" must NOT mean

The tempting generalization — point a fair-value model at every sport/crypto/econ
market and trade the gaps — fails for the same reason weather did, in every
domain at once. The landscape research already priced this in:

- Sports: pros with ≥30s-faster feeds; "any retail-feed sports strategy is adversely selected"; fees 3.5–7%.
- Econ: Kalshi CPI already beats Bloomberg consensus; counterparties are DRW/Jane Street/Citadel.
- Crypto: killed by ADR-0002; sub-second venue coupling since the Coinbase integration.

A generic *directional* scanner is a machine for finding our own mispricings.
Each flagged "opportunity" would need a domain model good enough to out-forecast
the resident sharp — N hard problems, not one.

The defensible meaning of "market-agnostic, real-time, all the time" is different:
**hunt mispricings that are provable from the price sheet itself, without any
fair-value model.** Those exist, our own research flagged them as whitespace
nobody publicly exploits, and one of them ("tail-mm") was explicitly parked with
the revisit trigger "after the Phase 0.5 gate" — **that trigger has now fired.**

## 3. The three opportunity classes, ranked by defensibility

Ordered by how little belief each requires.

### 3.1 Deterministic-settlement capture (outcome locked, price not converged)

Once the running ASOS extremum crosses a threshold, every bracket below/above it
is *decided* — TMAX is a running max; it cannot un-cross. If `KXHIGHDEN T78` YES
still asks 93¢ after the running max hit 79°F, that is 7¢ gross / ~6¢ net with
near-zero model risk. The same logic applies to any market whose resolution
variable is monotone-in-time (crypto "touch" ladders, sports clinch scenarios).

This is **not** the calibrated-probability game we lost: it targets quotes that
are *strictly stale against public information*, where the only question is
whether anyone else swept them first. The v3 diagnosis says the market is fast
on primary cities at mid prices — fine: the detector measures *where it isn't*
(secondary brackets, off-hours, settlement window T−30min, which 01-landscape
flags as "retail impatience" whitespace).

Unique asset we already own: the CLI-truth corpus quantifies
`P(CLI TMAX ≥ x | ASOS running max ≥ x)` — i.e., an *empirical* certainty table
for the residual settlement risk (sensor corrections, CLI-vs-ASOS divergence).
Nobody else has shown that work publicly.

### 3.2 Internal-consistency arbitrage (no model, no truth feed)

Provable from the orderbook alone, for any event group on the exchange:

- **Exhaustive bracket sums.** K mutually exclusive brackets: buying NO across
  all legs profits when `Σ(yes bids) > 100¢ + Σfees`; buying YES across all legs
  profits when `Σ(yes asks) < 100¢ − Σfees`. Retail longshot bias pushes thin
  bracket families to `Σmids > 100` (01-landscape §3.3 "butterfly mispricing").
- **Monotonicity on threshold ladders.** `P(X≥70) ≥ P(X≥72)` always. Executable
  violation: `ask(T70 YES) < bid(T72 YES) − fees` → buy YES T70 + buy NO T72;
  minimum payoff covers cost. Ladders exist in weather Ts, crypto strikes,
  sports spreads, econ prints — the detector is identical everywhere.
- **Complement pairs.** Two-outcome events where `ask(A) + ask(B) < 100 − fees`.

These are arbs by construction. The honest caveats, pre-registered as the things
Phase B must measure (01-landscape cites a 2025 study: **78% of low-volume arb
opportunities failed to execute**):
size at touch, persistence vs our 10–50ms REST latency, and **leg risk** (a
K-leg basket part-filled is a naked position — mitigation: IOC all legs, size to
min displayed depth, prefer 2-leg structures first).

### 3.3 Stale-book / event-window repricing lag

Books that demonstrably reprice slower than a public reference: settlement-window
drift, thin secondary-city brackets after an obs lands, newly-listed series
(Commodities Hub, Apr 2026 — landscape gave it a "3–6 month shelf"; the clock
started 2 months ago). This class shades back toward needing a reference model,
so it is third priority and only the *mechanical* variants (reference = another
Kalshi market or a settlement-source number, not a forecast) are in scope.

**Out of scope, explicitly**: directional sports/econ/crypto, cross-venue
(Polymarket etc. — Phase 4+ remains), anything needing a forecast to disagree
with the market.

## 4. Architecture: one pipeline, pluggable detectors

The capability map (audit of 2026-06-09) found the data plane is already
market-agnostic — `internal/kalshi` (WS subscribes arbitrary tickers),
`internal/journal`, `internal/replay` (generic book reconstruction),
`fee.go` (universal schedule) — while everything weather-specific lives in
`internal/edge` + `internal/calibration`. So:

```
universe svc ──► tape (exists) ──► journal (exists) ──► replay/books (exists)
   │                                                        │
   │  REST discovery, all NY-eligible series,               ▼
   │  event-group topology (exhaustive sets,         detector framework (NEW)
   │  ladders, complements), eligibility gate        emits Opportunity records:
   │                                                 {ts, group, legs[], gross¢,
   └── 4h rediscovery (exists)                        fees¢, net¢, size@touch,
                                                      persistence_ms, class}
                                                            │
                                            ┌───────────────┴───────────────┐
                                       measurement reports            exec MVP (Phase C)
                                       (paper, pre-registered)        IOC, risk caps, kill-switch
```

### 4.1 What each existing asset becomes

| Asset | New role |
|---|---|
| `cmd/tape`, journal, replay, orderbook | Unchanged core. Config widens from 8 weather series to the full NY-eligible bracketed universe |
| `internal/kalshi` order/portfolio client (built, tested, never wired) | Phase C exec layer — this was the expensive part and it already exists |
| `fee.go` | Used verbatim by every detector (net-of-fee at quote time) |
| Bayesian `RunningExtremum` + hourly ASOS loader | Input to the §3.1 deterministic-settlement detector |
| CLI truth corpus + cliparse | The settlement-certainty table (`P(CLI agrees | running obs crossed)`) |
| MOS / climate / Bayesian calibration | Archived as the documented research artifact (STRATEGY.md's own off-ramp); optionally the §7 side track |
| `edge/ticker.go`, `stations.go`, openmeteo polling | Stay weather-scoped; new generic event-group parser is written beside them, not refactored in place |

### 4.2 New code (the actual build)

1. `internal/universe` — paginated discovery within 20 reads/sec; classifies
   event groups (exhaustive set / ladder / complement) from series metadata +
   strike parsing; **automated NY-eligibility gate** (closes the RISK.md P1 gap
   where compliance is operator-discipline-only); per-category enable flags.
2. `internal/scan2` (name TBD) — streams replayed (later live) books per event
   group, runs the three detector families, emits `Opportunity` NDJSON +
   summary reports in the existing `reports/` conventions.
3. `cmd/bot` (Phase C only) — consumes live `Opportunity` records, applies the
   RISK.md cap ladder (`internal/risk.Gate`, designed but unbuilt), IOC
   multi-leg submit, reconcile-on-start, `./halt` kill-switch, alarms.

## 5. Phases and pre-registered gates

Gates are written down *before* measurement, per house rules. Numbers below are
proposed defaults — tune them in the ADR, then they freeze.

**Phase A — data plane back to health, then wide (≈1 week)**
Fix the WS frame-starvation outage (recorder has been effectively down since
2026-05-21; separate debug task). Widen config to the eligible universe; measure
disk/day and trim by category if needed; tape-health gate per the week-1 verdict
(≥80% of expected frames).
**Gate G-A**: 14 consecutive days, ≥95% recorder uptime, ≥80% expected frames,
≥300 markets recorded, eligibility checker green. *No strategy work counts
before G-A; un-recorded weeks are unfalsifiable weeks.*

**Phase B — detectors on tape, measurement only (≈2 weeks build, overlaps the G-A window)**
Run detectors over (a) the ~800MB existing weather tape — bracket sums, ladders,
and settlement-window stale quotes all exist there — and (b) the new wide tape.
Output: instances/week by class, net-¢ at displayed size, persistence
distribution, leg-count distribution.
**Gate G-B** (existence): on ≥14 days of healthy wide tape, structural classes
(§3.1+3.2) show **≥25 executable instances/week** with **net ≥2¢** after taker
fees at displayed size, **median persistence ≥3s** (≳60× our REST round-trip),
and **capturable EV ≥$10/week at our cap ladder**. If unmet → do not build
execution; publish the negative result (it is a genuinely novel measurement)
and reassess at the Commodities-Hub/hurricane seasonal windows.

**Phase C — execution MVP, canary money (≈2–3 weeks, gated on G-B)**
`cmd/bot` per §4.2.3. Canary: **$50 sub-bankroll**, 2 event groups, taker/IOC
only (arbs want immediacy; no resting quotes yet), every order logged against
its predicted capture.
**Gate G-C**: 14 live days, realized capture ≥50% of predicted, zero cap
violations, zero unhandled rejects, no manual interventions. Pass → full $200
and category expansion. Fail → halt, postmortem report, fix or kill.

**Phase D — parked until G-C** (in priority order): thin-bracket maker quoting
(the parked tail-MM; note: no rebate tier at our fee volume, so economics must
be re-measured from Phase B fill data), hurricane season piggyback,
Commodities Hub coverage, correlation-aware netting across brackets.

## 6. Expectation setting (the honest math)

$200 with the RISK.md cap ladder ($10/ticker, $25/series, $50 portfolio) cannot
produce income. Plausible Phase C outcomes if G-B passes at the threshold:
single-digit dollars per week, positive expectancy, low variance. The goals this
plan can actually deliver in ~90 days:

1. a measured, reproducible answer to "do executable structural mispricings
   exist on Kalshi at retail latency, at what frequency and size" — which
   nobody has published;
2. a live system whose realized PnL tracks its predicted capture (the
   resume-grade artifact: *provable* expectancy, however small);
3. capacity knowledge: structural capture scales with bankroll and with
   breadth (more event groups), not with model quality — so a passing G-C is a
   system worth funding, not a lottery ticket that worked.

If the honest answer at G-B is "the edge exists but not at our size/latency,"
that is the project's documented result, and it is a better one than another
quarter of calibration attrition.

## 7. The weather side track (optional, time-boxed)

The week-1 verdict's "highest-leverage move" (MOS refit on `gfs_seamless,ecmwf_ifs025`)
is ~1 day of work and is the only way the H4+calibration stack ever gets a fair
forward test. Worth doing **once, in parallel, at research priority**: refit,
let the forward clock run during Phases A–B (the bot is paper-only anyway), and
score it with zero additional effort when the wide tape is healthy. If its
pre-registered v5 gate fails on fair data, directional weather closes per the
existing off-ramp language. It does not block, gate, or share code with
Phases A–C.

## 8. Risks and kill criteria for the pivot itself

- **Thin-arb execution failure** (the 78% stat): G-B's persistence/size fields
  exist precisely to measure this before any build; G-C's 50%-of-predicted
  floor catches it live.
- **Leg risk**: 2-leg structures first; IOC everything; size to min leg depth;
  bracket baskets (K>3) only after slippage data exists.
- **WS/rate-limit ceilings on a wide universe**: measured day 1 of Phase A
  (subscription caps, frames/sec, disk/day); universe trimmed by liquidity
  rank if needed.
- **Compliance drift**: the automated eligibility gate + "never enable a
  non-NY series without re-check" stays absolute; sports categories enter the
  universe only if the gate proves them NY-served.
- **Detector-threshold overfitting**: thresholds (2¢, 3s, 25/week) are frozen
  in the ADR before the measurement window opens — same discipline that caught
  the retracted floor estimate.
- **Adversarial selection on stale quotes**: a "stale" quote may be stale
  because settlement rules differ from our assumption (void/scratch gotchas,
  the $30k case). Every detector carries a settlement-rule checklist per
  series before its class goes live.

## 9. Decision requested

Accept/reject via ADR-0014. Accepting means: Phase A starts immediately
(recorder fix is already underway as a separate task), STRATEGY.md gains a
"structure-first" section pointing here, directional weather is reframed per §7,
and the G-A/G-B/G-C numbers freeze as written (or as amended in the ADR).
