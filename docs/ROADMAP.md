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

## Phase 0 — Data collection *(current)*

**Goal.** Record synchronized market data from three sources for ≥48h so
Phase 0.5 can measure edges empirically instead of estimating them from vibes.

**Deliverables.**
- [x] `cmd/tape` orchestrator with one goroutine per source.
- [x] Crash-safe rotating JSONL journal (`internal/journal`).
- [x] Kalshi REST client + authenticated WS subscriber.
- [x] Coinbase public WS subscriber.
- [x] Open-Meteo ensemble REST poller.
- [x] Paired (wall, monotonic) timestamping for cross-source alignment.
- [ ] Continuous 48h recording with ≤0.1% dropped frames per source.

**Exit criteria.**
- ≥48h of uninterrupted data for each enabled source.
- Zero journal data loss after graceful shutdown (verified by shutdown test).
- All in-scope markets for configured series present for the full window.

**Risks & mitigations.**
- *Kalshi WS drops during overnight weather-bracket expiry:* reconnect with
  jittered backoff; subscribe to `ticker_v2` as a liveness canary.
- *Disk fills:* gzip-on-rotate + daily `du -sh data/` in the runbook.
- *Open-Meteo rate limits:* min `poll_interval` enforced (1m) in config
  validation; default is 30m.

---

## Phase 0.5 — Decision-gate report

**Goal.** Answer one question in writing: *do tradeable edges exist on
weather brackets, on BTC contracts, both, or neither?* Nothing else matters
until this is answered.

**Deliverables.**
- `cmd/analyze` binary that replays a recording directory and emits a
  Markdown report.
- `internal/replay`: streaming JSONL reader that reconstructs per-market
  orderbook state from `orderbook_delta` frames.
- `internal/edge`: edge calculators —
  - *Weather brackets:* ensemble-derived probability vs. market mid, for
    every `KXHIGH*`/`KXLOW*` bracket, every time the ensemble refreshes.
  - *BTC hourly:* Coinbase spot vs. Kalshi `KXBTCD` mid, computing
    simulated maker PnL with Kalshi's fee schedule applied.
- Lead-lag histograms (Coinbase → Kalshi) and (Open-Meteo → Kalshi).

**Exit criteria (the decision gate).**
> `>10 brackets/day with >8% edge after fees` on **at least one** strategy.

If **both** strategies pass, Phase 1 builds both behind a shared `Signal`
interface. If **one** passes, build only that one. If **neither** passes,
stop — record the negative result and move on.

**Artifacts that must exist before sign-off.**
- `reports/phase0.5/<date>/report.md` — narrative + charts.
- `reports/phase0.5/<date>/brackets.csv` — per-bracket edge rows, reproducible.
- Checked-in replay test that turns the CSV into the PnL summary.

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
