# Roadmap

> Where `isobar` is going, phase by phase, with explicit gates between phases.
> Each phase ends with a written decision — build, iterate, or kill — so capital
> is never committed on hope.

## Guiding principles

1. **Evidence before commitment.** Every phase produces a written artifact
   (report, dashboard, postmortem) that a reader can audit without access to
   the code or the author.
2. **Small reversible steps.** Anything that touches real money is gated by
   at least one layer of simulation proving the behavior matches expectation.
3. **Correctness beats latency.** We do not compete with HFT. A few hundred
   ms of latency is irrelevant if the edge comes from better pricing. We
   optimize for *reproducibility* and *auditability* first.
4. **No silent failure.** The bot fails loudly (`slog.Error` + halt) rather
   than degrading into unknown states. Degraded trading with an unknown state
   is how accounts get drained.
5. **Stdlib first.** Each third-party dependency is an audit burden. Only
   `coder/websocket`, `yaml.v3`, `golang.org/x/sync`, and `testify`
   (test-only) clear the bar.

---

## Phase 0 — Data collection *(complete)*

**Goal.** Record synchronized market data from two sources (Kalshi WS +
Open-Meteo REST) so Phase 0.5 can measure edges empirically.

*Scope note:* the Coinbase WS source was removed after research
determined BTC-hourly lead-lag is dead alpha at REST-from-cloud
latency (see `docs/research/SYNTHESIS.md`).

**Deliverables — shipped.**
- [x] `cmd/tape` orchestrator with one goroutine per source
- [x] Crash-safe rotating JSONL journal (`internal/journal`)
- [x] Kalshi REST client + authenticated WS subscriber
- [x] Open-Meteo 31-member GFS ensemble REST poller
- [x] Paired (wall, monotonic) timestamping for cross-source alignment
- [x] Rolling ticker re-discovery (4h cycle) — fixed the "tape dies
      after settlement" failure mode after Apr 29-30 capture went dead
- [x] Continuous capture verified: 5 full bracket lifecycles caught
      May 2-6 with `NRestarts=0` and zero dropped frames

**Open carry-overs.**
- [ ] ECMWF IFS endpoint added to live polling (51-member ENS now
      free on AWS; ADR-0009 trigger)
- [ ] Recorder integration test (Pass 3 §1.c)
- [ ] Prometheus-ish metrics + watchdog (Pass 3 §1.b)

---

## Phase 0.5 — Decision-gate report *(v2 verdict: NOT MET)*

**Status.** Three iterations of the gate report shipped. Current
verdict is in the v2 report at
[`reports/phase0.5/2026-05-05-floor-estimate-v2.md`](../reports/phase0.5/2026-05-05-floor-estimate-v2.md):
**MOS calibration does not beat Kalshi market mid in the tradeable
[35,65)¢ zone**, with statistically significant Brier deltas on n=299
verifiable bracket outcomes.

**Goal (originally).** Produce a written decision-gate report proving
that at least one **calibrated** signal survives real fees on real data.

**Deliverables — shipped.**
- [x] `cmd/calibrate-fit` — OLS-fit MOS coefficients per (station,
      season, variable) bin → JSON output
- [x] `cmd/calibrate-parse-cli` — NWS CLI bulletin zip → daily TMAX/TMIN
      CSV (canonical y-target per ADR-0007)
- [x] `cmd/edge-scan` — joins recorded book state to ensemble polls,
      applies all probability models (naive, MOS, climate-shrunk,
      market), emits per-bracket edge with full provenance columns
- [x] `internal/calibration/cliparse` — NWS CLI bulletin parser
- [x] `internal/calibration/mos` — OLS bias correction + Φ-based
      bracket probability
- [x] `internal/calibration/climate` — historical-frequency baseline
      with seasonal window + ShrinkP helper
- [x] Three Phase 0.5 reports (original retracted, interim, v2 current
      — see `reports/INDEX.md`)

**Exit criteria — current verdict: NOT MET.**

The hard gate from ADR-0001 was:
> `>10 events/day with >5% calibrated edge after fees` on **at least one**
> signal, AND calibrated Brier-skill > market-implied on held-out
> 90-day OOS data.

The v2 report (n=299, bootstrap 95% CIs in tradeable zone):

| Comparison | Brier delta | CI95 | Verdict |
|---|---:|---|---|
| MOS vs naive | +0.060 | [+0.008, +0.113] | MOS wins |
| MOS vs shrunk | −0.011 | [−0.056, +0.035] | tied |
| MOS vs market | **−0.100** | [−0.188, −0.013] | **MOS LOSES** |

Calibrated Brier-skill is *not* > market-implied in the tradeable
midrange. Mean post-fee MOS edge in tradeable zone is negative.

**What ADR-0008's canary requires now.** Per ADR-0008's "Canary"
section: if MOS doesn't beat the gate, file ADR-0009 to add ensemble
spread (or escalate to ECMWF blend / T−3h Bayesian). ADR-0009 is the
next document on the queue.

**Phase 0.5 next iteration — what could change the verdict.**
- ECMWF IFS blend (free since Oct 2025; ADR-0001 named this; 1-2 days)
- T−3h Bayesian update with hourly ASOS observations — targets the
  near-settlement bucket where the market currently wins (Pass 3 §2.g)
- Full NGR via NOAA GEFS GRIB historical backfill (ADR-0009 trigger,
  larger engineering investment)
- Larger sample (n=600+) at continuous-capture quality from the
  May-onward Vultr tape

**Decision tree.**
- If T−3h Bayesian or ECMWF blend close the tradeable-zone gap → re-run
  the gate, write v3 report, possibly proceed to Phase 1
- If they don't close the gap → log the negative result via a new ADR;
  pivot project framing to "forecasting research artifact with
  documented failed trading gate" per Pass 3 §3 W11-W12; do not commit
  capital
- Either way: **no $50 deployment until a future report shows the gate
  is met**

**Artifacts that exist now.**
- [x] `reports/phase0.5/2026-05-05-floor-estimate-v2.md` — current verdict
- [x] `reports/phase0.5/2026-05-02-pipeline-validation.md` — interim status
- [x] `reports/phase0.5/2026-05-02-floor-estimate_RETRACTED.md` — original
- [x] `reports/INDEX.md` — chain of supersedes
- [x] `data/edge-scan-v2.csv` — 407 rows of underlying brackets
- [x] `data/calibration/mos-coefficients.json` — fitted coefficients

---

## Phase 1 — Live bot, minimum viable shape

**Goal.** A maker-only, paper-trading bot that consumes the same feeds and
emits orders to a simulator. The production code path is exercised from day
one; only the execution adapter is swapped.

**Deliverables.**
- `internal/signal` — `type Signal interface { Tick(MarketState) []Quote }`.
  Each strategy is one implementation. Hot-swappable; dead strategies are
  removed, not left to rot behind flags.
- `internal/exec` — two implementations behind one interface:
  - `PaperExec` — deterministic fill simulator (Phase 1).
  - `KalshiExec` — real REST order placement (Phase 3).
- `internal/state` — SQLite WAL store for warm-start:
  - open orders, positions, last-seen fills, strategy variables.
  - Recovery invariant: after crash + restart, in-memory state equals the
    state on Kalshi's side within N seconds (N = enforced by reconciliation
    loop).
- `internal/risk` — hard caps before every order leaves:
  - max position per ticker, per series, per portfolio.
  - max daily PnL drawdown.
  - max notional exposure.
  - session kill-switch file (`./halt`), checked each tick.
- Metrics: Prometheus-style counters exposed on `/metrics` (no push, no
  external deps — just `net/http` + `expvar`).

**Exit criteria.**
- `cmd/bot` starts, loads `Signal` + `PaperExec`, runs against the live feeds,
  and reconciles state after simulated crashes (killed mid-tick, restarted
  cleanly).
- Property tests cover: no orders placed when kill-switch present; no
  position exceeds risk caps; warm-start state equals pre-crash state.

---

## Phase 2 — Realistic paper trade

**Goal.** Prove the simulator matches reality. The bot runs unchanged; only
the fill model sharpens.

**Deliverables.**
- Queue-position aware fill simulator (`internal/paper`):
  - Quote enters at the back of the book.
  - Fill probability = f(trade volume, distance to front of queue, queue
    decay rate observed in Phase 0 data).
- Adverse-selection model: if a quote fills, mark-to-market the 60s post-fill
  mid as realized PnL — matches how toxic flow feels in production.
- Paper session runs ≥14 days; PnL curve + fill distribution logged.
- Nightly `cmd/analyze --live` compares paper fills against what would have
  filled on `arbhunter`-style aggressive execution, to sanity-check the queue
  model.

**Exit criteria.**
- Paper Sharpe ≥ 1.0 over ≥14 days, computed on daily returns.
- Max drawdown ≤ 30% of 14-day PnL.
- Zero risk-cap violations in the 14-day window.
- Fill-model residual (paper fills − shadow aggressive fills) has no
  systematic bias > 10% of quoted volume.

---

## Phase 3 — Live, $50 → $200

**Goal.** Run the exact same bot, exact same strategy, against real money.
Scale only after reality matches paper.

**Deliverables.**
- `KalshiExec` wired in, with `--live` flag required to enable.
- Canary ticker list (1–2 markets max for first 24h).
- Alerting: slog → webhook → phone on any `ERROR`-level event.
- Daily reconciliation report (positions, fills, PnL vs. paper prediction).

**Exit criteria to scale from $50 → $200.**
- 14 consecutive trading days with:
  - Live PnL within ±30% of paper-predicted PnL.
  - Zero unexpected order rejections.
  - Zero risk-cap violations.
  - No manual intervention required outside regular trading hours.

**Stop-loss criteria (automatic halt, manual restart required).**
- Any day with drawdown > 2× 14-day paper 95th-percentile drawdown.
- Reconciliation mismatch (positions-we-think vs. positions-Kalshi-reports)
  persisting > 60s.
- Clock drift > 250ms sustained (kills signed requests).

---

## Phase 4+ — Out of scope for now

Only speculated here for context. Not on the critical path.

- Multi-venue (Polymarket) for the same brackets.
- Machine-learned queue model trained on Phase 2 data.
- CME micro-weather hedging for bracket exposure.

We do not plan for Phase 4 while Phase 0.5 is unanswered.

---

## Release cadence & branching

- `main` is always green: `make all` passes.
- Feature branches merge via PR with CI green + 1 review (or self-review
  justification in the PR body when solo).
- No long-lived branches. If a branch can't merge in 3 days, the scope was
  wrong — break it up.
- Tags `vPHASE.X.Y` mark phase completion. The Git tag message is the short
  version of that phase's exit-criteria check.

## Definition of "done"

A phase is done when:

1. All exit-criteria boxes are checked in a commit on `main`.
2. A written report (`docs/reports/<phase>.md`) exists and was read by the
   project owner.
3. The previous phase's code that is now dead has been deleted, not left
   behind "just in case".
