# Risk register

> The risks a $200-bankroll solo Kalshi weather-bracket bot has to
> survive, ordered by severity. Each row is dispassionate: what would
> happen, how we would know, what is in place today. Rewritten
> 2026-05-13 to reflect the Phase 0 audit
> ([audit/00-map.md](../audit/00-map.md)) and ADRs 0007–0010.
> Status column refreshed post-Bucket-A close (same date) —
> [audit/01-line-audit.md](../audit/01-line-audit.md) appendix is the
> authoritative list of what shipped; [ADR-0011](./decisions/0011-journal-backpressure.md),
> [ADR-0012](./decisions/0012-time-source-discipline.md), and
> [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md)
> are the new canonical references.
>
> Phase 0 has no live execution. Most controls below are
> design-ahead for Phase 1+; the column "Status" labels which ones
> are shipped (`LIVE`), in the current P1 triage round
> (`IN PROGRESS`), waiting on a decision (`PENDING DECISION`),
> resolved into a deferred-capability ADR (`ACCEPTED`),
> deferred to observability work (`PHASE 6`), or operator-only with
> no code-level mitigation (`MONITORED`).
>
> Owner column is `solo-operator` for everything. This is a solo
> project; the owner column is preserved for parity with the
> standard risk-register schema and to flag that there is no
> escalation path.

## Severity legend

| Severity | Meaning |
|---|---|
| **P0** | Single-step failure that ends the project's credibility or burns the bankroll. Mitigation must be LIVE before any real-money trade. |
| **P1** | Operationally serious: data loss, silent misprice, unrecoverable state. Mitigation must be LIVE or IN PROGRESS before Phase 1 exec. |
| **P2** | Operationally annoying: rotation gaps, log noise, dev-time friction. Mitigation can be PHASE 6. |
| **P3** | Doc / hygiene. Operator vigilance. |

## Control hierarchy (Phase 1+ design)

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

No `internal/exec` package exists today (`audit/00-map.md` §3 names
only the recorder + calibration tools). The hierarchy is the design
constraint the Phase 1 exec branch will inherit.

---

## P0 — gating risks

### Calibration-eval harness not yet written

| Field | Value |
|---|---|
| Severity | **P0** (gating any real-money order) |
| Detection | Repository: no `cmd/calibrate-eval` or equivalent harness in `audit/00-map.md` §3; no v3 floor-estimate report under `reports/phase0.5/`. |
| Mitigation | Build it as the next ADR after 0011 (planned ADR-0012). Until the harness clears the four-criterion gate in [docs/STRATEGY.md](./STRATEGY.md) §"Decision gate", the T−3h Bayesian update from [ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md) is not wired into the scan path and no real-money order leaves the process. |
| Owner | solo-operator |
| Status | **IN PROGRESS** |

### Trading on a calibration that loses to the market

| Field | Value |
|---|---|
| Severity | **P0** |
| Detection | The v2 floor estimate already detected it: MOS Brier 0.401 vs Kalshi market 0.255 in the tradeable [35, 65)¢ cell with bootstrap 95% CI [−0.188, −0.013] excluding zero (`reports/phase0.5/2026-05-05-floor-estimate-v2.md` §"Bootstrap 95% CIs"). |
| Mitigation | The Phase 0.5 gate ([ADR-0001](./decisions/0001-research-driven-pivot.md), restated in [docs/STRATEGY.md](./STRATEGY.md) §"Decision gate") explicitly forbids real-money trading on a calibration that does not beat market mid by ≥ 3 pp Brier with 95% bootstrap CI lower bound > 0 over a 90-day held-out cell. The current calibration fails that gate; therefore the bot does not trade. |
| Owner | solo-operator |
| Status | **LIVE** (the policy is in place and being followed — Phase 0 is recorder-only) |

---

## P1 — operational / correctness

### Bankroll concentration

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Read `config.yaml` for `max_per_*` values; reconcile against `state.<format>` at start; daily PnL halt fires when cumulative loss exceeds `max_daily_drawdown_dollars`. |
| Mitigation | The cap ladder in [docs/STRATEGY.md](./STRATEGY.md) §"Sizing" (`$10` per ticker, `$25` per series, `$50` portfolio-wide on a `$200` bankroll, ~25%). Sizing uses fractional Kelly (0.5 ×) with the mutually-exclusive-bracket correlation correction `ρ_ij = −1/(K − 1)` so standard Kelly does not oversize on correlated brackets. Reservation price / half-spread use bounded-payoff `σ²_bin(P) = P(1 − P)/√t_days` rather than naive `P(1 − P)`. Enforcement point will be `internal/risk.Gate(ctx, order, state)`; not present today. |
| Owner | solo-operator |
| Status | **DESIGN-AHEAD** (Phase 1+) |

### Single-station correlation (KNYC mispriced → multi-bracket loss)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | A persistent residual bias on `KNYC` after MOS would show in `data/calibration/forecast/KNYC.csv` vs `data/calibration/obs/KNYC.csv` joined by `valid_date` — a non-zero mean of `(forecast − obs)` over the holdout window per (station, season) bin. Audit by running `cmd/calibrate-fit` periodically and inspecting the per-bin `a`/`b` coefficients. |
| Mitigation | Eight subscribed series concentrate exposure on four cities (NY, MIA, LAX, CHI). The per-ticker and per-series caps in L1 are partly motivated by this: a $10 cap per ticker means two brackets going wrong on the same KNYC TMAX is at most $20 loss. MOS bins are per-station so a per-city forecast bias is at least *fittable*; before MOS, the naive estimator was strictly worse on `KMIA` TMAX (−2.08°F bias per [ADR-0008](./decisions/0008-deterministic-historical-forecast.md) §"Good"). |
| Owner | solo-operator |
| Status | **MONITORED** (MOS handles the systematic-bias piece; the cap structure handles the concentration piece) |

### Calibration drift (MOS coefficients age)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Refit cadence: `cmd/calibrate-fit` outputs `data/calibration/mos-coefficients.json` with `fit_window` and per-bin sample counts. A monthly comparison of fresh-fit vs in-use coefficients gives a drift signal (max absolute change in `a` or `b` per bin). |
| Mitigation | MOS is a per-`(station, season)` linear correction (ADR-0008 §"Decision"); seasons are stable over the kind of timescale this bot lives on, but the climate regime is not. Plan: refit monthly, with a fresh `MANIFEST.json` per refit so historical reports remain reproducible. The refit cadence is not yet on a calendar — it lives in operator discipline today. |
| Owner | solo-operator |
| Status | **MONITORED** (no code-level alarm; refit cadence is operator-driven) |

### MOS in-sample σ over-confidence (training-residual stdev too tight)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | The pre-Bucket-A `Fit` derived σ from training residuals, which under-estimates predictive spread by construction. Tradeable-midrange Brier inflates accordingly. The new `SigmaSource` field on Coefficients records `holdout`, `training-fallback`, or `pooled` so any consumer can audit the provenance. |
| Mitigation | Bucket A8 reworked `Fit` to compute σ from the OOS holdout when `NHoldout ≥ MinHoldoutForSigma` (default 30); falls back to a training-residual fallback below that threshold with `SigmaSource: training-fallback` so the consumer can downgrade confidence. A `FitModelSet` guard prevents applying a GFS-only fit to a multi-model poll. Clock injection per [ADR-0012](./decisions/0012-time-source-discipline.md) makes `Fit` deterministic — identical pairs → identical Coefficients JSON byte-for-byte (regression test `TestFit_DeterministicAcrossCalls`). |
| Owner | solo-operator |
| Status | **LIVE** (A8; holdout-σ + clock injection) |

### T-bracket low-side resolution (KXLOWT* resolved as one-sided high)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Pre-Bucket-A the resolution logic treated every threshold bracket as one-sided "≥ Lo", which is correct for `KXHIGH*` but inverts `KXLOWT*`. Symptom would be reversed `outcome` columns in `data/calibration/*` for KXLOWT brackets. The new `ResolveWithSiblings` uses first-pass sibling collection to detect low-side T-brackets by their sibling shape. |
| Mitigation | Bucket A10 shipped `ResolveWithSiblings` in [internal/edge/resolution.go](../internal/edge/resolution.go); `scan.go` does a first-pass sibling collection so `KXLOWT*` resolves correctly via sibling-based direction inference. Exercised by `TestResolveWithSiblings_{LowSide,HighSide}Detected`. |
| Owner | solo-operator |
| Status | **LIVE** (A10) |

### Orderbook snapshot-error state machine (delta-before-snapshot)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Pre-Bucket-A the `internal/replay` orderbook could apply delta frames against an empty book, leaving it in a silently-divergent state. Bucket A3 introduced an `uninitialized` state + typed `ErrBookUninitialized` + counters `RejectedDeltasTotal()` and `SnapshotErrorsTotal()` so the divergence is observable. |
| Mitigation | Bucket A3 shipped the state machine: delta-before-snapshot rejects with `ErrBookUninitialized` and increments `RejectedDeltasTotal()`; snapshot-apply errors increment `SnapshotErrorsTotal()`; logs are suppressed past the first to avoid flooding. Covered by orderbook tests asserting exactly-once counter semantics under repeated rejections. |
| Owner | solo-operator |
| Status | **LIVE** (A3; typed error + counters + log-suppress) |

### `bin/` tracked-vs-gitignore drift

| Field | Value |
|---|---|
| Severity | **P2** |
| Detection | Phase 0 inferred `bin/` was tracked in git; closer audit confirmed it is not. Risk now is a future regression — someone commits a built binary by accident. Detection is the CI tripwire and the pre-commit hook. |
| Mitigation | Lane D shipped a CI step in `.github/workflows/ci.yml` plus a `.githooks/pre-commit` hook that fails if any path under `bin/` is in the tracked tree. Local pre-commit catches at commit time; CI is the merge-time backstop. |
| Owner | solo-operator |
| Status | **LIVE** (Lane D; CI + pre-commit hook) |

### PR#9 paper-trader lineage retired (deferred capability)

| Field | Value |
|---|---|
| Severity | **P2** |
| Detection | When `cmd/bot` work begins, the paper-trader / fillability / score / settle / backfill-score binaries plus `internal/papertrade` need cherry-pick + migration from `refs/pull/{7,8,9}/head`. Without that, the live-money path has no precursor scaffolding and any migration question (e.g. `market.ParseTicker` → `edge.ParseTicker` semantics) gets relitigated under deadline. |
| Mitigation | [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md) documents the 11 commit SHAs, PR refs (#7/#8/#9), recovery paths in priority order (GitHub PR refs → reflog SHAs → audit backup branches `audit/2026-05-13` + `audit/2026-05-13-post-bucket-a`), and the full API-migration surface (`market.*` → `edge.*` rename map) so the eventual `cmd/bot` PR is mechanical, not exploratory. |
| Owner | solo-operator |
| Status | **ACCEPTED** (deferred capability per ADR-0013; revisits when `cmd/bot` unblocks) |

### Forecast-source outage (Open-Meteo down — what happens to live trade?)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | The poller emits structured per-attempt slogs (Bucket A6); `FetchFailuresTotal()` exposes the cumulative count of locations that exhausted retries. Symptom of an outage beyond the retry budget is a gap in `data/openmeteo/<date>/*.jsonl` plus a non-zero `FetchFailuresTotal` reading. |
| Mitigation | Two layers: (1) record-side — bounded exponential backoff with jitter (1 s → 2 s → 4 s, `max_attempts: 4`, ctx-aware so SIGTERM still works); covered by 7 new tests in `internal/openmeteo/client_retry_test.go` (5xx storm + 429 + ctx-cancel cases). (2) Trade-side — the L3 signal-sanity gate's `staleness_max` (default 30 s for WS-derived state, larger for ensemble polls) refuses to emit a signal on stale ensemble data; remains design-ahead. |
| Owner | solo-operator |
| Status | **LIVE** (record-side retry, A6 in [audit/01-line-audit.md](../audit/01-line-audit.md) appendix); **DESIGN-AHEAD** (L3 staleness gate, Phase 1+) |

### Kalshi WS disconnect storm

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `cmd/tape` slog stderr lines `ws subscriber reconnecting` clustered within a short window. Per-reconnect counter logged in `audit/00-map.md` §5. Frame-starvation (silent TCP-half-open) is now detected by the app-level liveness watchdog. |
| Mitigation | `internal/retry` provides context-aware exponential backoff with jitter
(`initial_backoff: 500ms`, `max_backoff: 30s`, `max_attempts: 0` = forever) per [config/config.yaml:42-46](../config/config.yaml). Subscriber re-establishes via `kalshi.SetTickers/Reload`. Bucket A5 added an app-level liveness layer in [internal/kalshi/ws.go](../internal/kalshi/ws.go): periodic ping with read-deadline + watchdog that forces a reconnect on starvation (covered by two new tests — frame-starvation reconnect + ping failure). There is still no circuit breaker on a sustained 5xx run; for Phase 0 (recorder-only) the right behaviour is to keep trying. Phase 1 exec will need a "stop new orders if WS has been disconnected for > N seconds" interlock; that lives in the L2 reconciliation design. |
| Owner | solo-operator |
| Status | **LIVE-IMPROVED** (recorder side, A5); **DESIGN-AHEAD** (exec-side interlock) |

### Silent journal drop (frame loss on burst)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `internal/journal.Write` is non-blocking ([journal.go](../internal/journal/journal.go)); on a full 16 384-item queue, the frame is dropped and a per-source counter increments. A 60 s warn log reports the cumulative count; a sampled debug log + edge-triggered `OnDropAlarm` callback now fire on sustained-rate breaches per [ADR-0011](./decisions/0011-journal-backpressure.md). |
| Mitigation | Bucket A1+A2 shipped the ADR-0011 design end-to-end: per-source counter, sampled debug log, panic-safe alarm callback, plus `cmd/tape` wires the hook to a `data/alarms/<date>.jsonl` sink. Tripwire test `TestProdJournalHasAlarmHookWired` in [cmd/tape/main_test.go](../cmd/tape/main_test.go) fails the build if `journal.Open` is ever called without `OnDropAlarm`. The Phase 6 Prometheus sink plugs into the same seam. |
| Owner | solo-operator |
| Status | **LIVE** (A1+A2; tripwire active) |

### Station-coordinate drift (CHI → ORD vs KMDW)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Canonical station registry now lives in [internal/market/stations.go](../internal/market/stations.go); boot-time `validateStationCoords` in [internal/config/config.go](../internal/config/config.go) refuses to start on mismatch. [ADR-0007 §"Neutral"](./decisions/0007-calibration-ground-truth-cli.md) confirms Kalshi resolves `KXHIGHCHI`/`KXLOWTCHI` on Midway; CHI is patched to canonical KMDW and the cutover is marked in a boot frame on the journal. Symptom of unresolved drift would be a persistent residual on CHI brackets relative to other stations in the per-station Brier breakdown of `reports/phase0.5/`. |
| Mitigation | Bucket A1+A2 shipped: (1) canonical station list — name, lat, lon, ICAO — pinned in `internal/market/stations.go` (single source of truth, per [docs/ARCHITECTURE.md](./ARCHITECTURE.md) §"market"). (2) `cmd/tape` boot validates every configured `(lat, lon)` against the registry within an epsilon (~0.05°); refuses to start on mismatch. (3) Active `config/config.yaml` patched to KMDW; cutover marked via boot frame. Cross-package consolidation of three remaining drifting station maps (`internal/edge/{scan,poll_index,ticker}`) is proposed in [audit/02-architecture.md](../audit/02-architecture.md) §1.1, pending per-decision sign-off. |
| Owner | solo-operator |
| Status | **LIVE** (A1+A2; registry sentinel + boot-time check + config swap) |

### Secret leakage (`.env` perms, PEM on disk)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `stat -f%Mp%Lp /Users/wh0o0pz/dev/isobar/.env` returns the file mode. `git log --all -p -- .env` returns empty (audit §8: never committed). [internal/env.CheckMode](../internal/env/env.go) runs at startup and returns an error if the file is group- or world-readable; the recorder refuses to boot in that state. |
| Mitigation | Bucket A5 shipped the boot-time refusal-to-start check (`CheckMode`); `.env` mode is `0o600`; the check is documented in [docs/OPERATIONS.md](./OPERATIONS.md). PEM-on-disk path is operator-set; recommended convention is `./secrets/kalshi.pem` with `0o600`; `.gitignore` covers `secrets/`, `*.pem`, `*.key`, `.env*`. The same `CheckMode` predicate applies to the PEM file when loaded. A future `chmod` regression now surfaces as a refused boot, not silent. |
| Owner | solo-operator |
| Status | **LIVE** (A5; chmod + startup refusal check) |

### Deploy-path drift (memory vs loop-prompt — pin to canonical systemctl)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `audit/00-map.md` §10: operator memory `project_isobar_prod_deploy.md` says `/root/isobar/...` on Vultr with `isobar-tape.service`; `scripts/loop-prompt.txt:3-5` references `/opt/isobar/...` and a separate `isobar` user. One is wrong; rolling forward on the wrong assumption breaks the next restart. SSH-from-Claude is classifier-rejected, so the operator must paste the `systemctl cat` output for the audit to commit ground truth. |
| Mitigation | (1) Commit canonical `deploy/isobar-tape.service` + `deploy/install.sh` to the repo so the deployment is reproducible from `main` without operator-memory dependence. (2) Strip `/opt/isobar/...` from `scripts/loop-prompt.txt` or update it to match the systemd unit file. (3) Pin the install layout in [docs/OPERATIONS.md](./OPERATIONS.md). One force-push to `main` was authorized on 2026-05-13 (recorded in [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md)); future force-pushes require explicit per-message authorization. |
| Owner | solo-operator |
| Status | **PENDING — awaiting `systemctl cat` paste from operator** |

### `ISOBAR_LIVE_ALLOW`: live-trade gate (resolved — stripped from mutable docs)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | The string `ISOBAR_LIVE_ALLOW` no longer appears in mutable docs ([audit/live-allow-trace.md](../audit/live-allow-trace.md) records the trace and decision). ADR-0003 retains the original Decision text (immutable per project policy) with a forward-note appendix recording the deferral. |
| Mitigation | Stripped from mutable docs and `.env.example`; the live-trade gating mechanism will be re-introduced in a dedicated ADR when `cmd/bot` + `internal/exec` land (anticipated slot per the post-audit ADR sequence). Until then the `./halt` file convention (L0) is the only operator-facing gate, which is sufficient because no live-order path exists. |
| Owner | solo-operator |
| Status | **RESOLVED** (Lane D stripped from mutable docs; future ADR will re-introduce when `cmd/bot` lands) |

### NY compliance regression

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Kalshi's per-state series-eligibility list is enforced by them, not by us. A series we subscribe to could be pulled from NY retail mid-session; symptom would be a `403` on a per-series REST call or a WS subscription rejection on reload. |
| Mitigation | (1) Subscribe only to series Kalshi serves to NY retail (the eight prefixes in [config/config.yaml:24-32](../config/config.yaml) are all currently NY-eligible per memory `project_kalshi_bot.md`). (2) The `kalshi.rediscover_interval: 4h` cadence will catch series being pulled because `DiscoverOpenTickers` will stop returning them. (3) For Phase 1 exec, the L2 reconciliation refuses to place new orders against a market whose series is no longer in the discovery response. (4) An operational rule: never enable a non-NY series in `config/config.yaml` without re-checking eligibility. |
| Owner | solo-operator |
| Status | **MONITORED** (no automated check; operator discipline + Kalshi's own series-eligibility enforcement) |

---

## L0–L4 control invariants (Phase 1+ design-ahead)

The risks above are the environment; the controls below are how the
exec loop will be wired when it ships. All five layers are
**DESIGN-AHEAD** — no `internal/exec` package exists today.

**L0 — kill switch.** `./halt` present in cwd ⇒ no order is
transmitted. Checked at the start of each `Signal.Tick` and again
immediately before every `KalshiExec.Submit` (`os.Stat`, TOCTOU
acceptable: worst case is one extra order during file creation).

**L1 — pre-trade caps.** The cap ladder is config-driven:

```yaml
risk:
  max_per_ticker_contracts: 100
  max_per_ticker_notional_dollars: 10
  max_per_series_notional_dollars: 25
  max_portfolio_notional_dollars: 50
  max_order_contracts: 100
  fee_aware_edge_floor: true   # edge > 0.07 * 2 * P * (1-P) + 1c per side
  max_lead_time_days: 8        # Lyapunov ceiling; recorder fetches only 3 today
  kelly_fraction: 0.5          # mandatory < 1 with mutually-exclusive correction
```

Caps are evaluated on **projected exposure** (resting orders count,
not just filled). Sizing uses fractional Kelly (≤ 0.5 ×) with the
mutually-exclusive-bracket correlation correction
`ρ_ij = −1/(K − 1)` — standard Kelly oversizes on correlated bets;
the correction is mandatory for any KXHIGH/KXLOWT portfolio.
Reservation price and half-spread use the bounded-payoff variance
`σ²_bin(P) = P(1 − P) / √t_days` — naive `P(1 − P)` is wrong away
from expiry and at expiry both, in opposite directions. Enforcement
point will be `internal/risk.Gate(ctx, order, state) error`, called
between signal emission and exec submission; cap breaches are warn
+ skip (caps are expected to bite). Tests: table-driven per-cap
within-budget vs breach-by-one-contract, plus a property test that
no order sequence ever leaves caps breached.

**L2 — state reconciliation.** Process start (or reconnect that
missed fills) ⇒ no new orders until the bot has (1) loaded the
exec-state store, (2) fetched open orders + positions from Kalshi
REST, (3) diffed the two; non-empty diff ⇒ either reconciliation
or hard halt for human intervention. The exec-state store's
persistence format is a Phase 1 design choice (recorder is JSONL
today; SQLite or BoltDB are candidates).

**L3 — signal sanity.** Strategy-agnostic preconditions checked in
each `Signal.Tick` prologue, centralised in
`internal/signal/guard.go`: mark finite, book not crossed, both
sides non-empty, most recent frame within `staleness_max`, ensemble
member count within expected range (~30 GFS + ~50 ECMWF per
[internal/edge/openmeteo.go:122-128](../internal/edge/openmeteo.go)),
MOS `FitModelSet` matches the poll's model columns (mismatch
refuses to predict; enforced in `internal/calibration/mos`).

**L4 — post-trade monitoring.** Three independent triggers:
(a) intraday PnL ≤ −`max_daily_drawdown_dollars` (10% of bankroll =
$20) ⇒ persisted halt that restarts do not clear; (b) every 60 s,
re-fetch open orders + positions from REST, diff against in-memory;
non-empty diff for > 60 s ⇒ halt; (c) every 60 s, REST round-trip
to compare server time vs local wall clock; drift > 250 ms warns,
> 1 s halts.

---

## Explicit non-controls (things we do not rely on)

- **Kalshi server-side rejection.** Not a risk control. The bot
  refuses to send an order that should be rejected; we do not let
  the counterparty be our conscience.
- **Paper-trade PnL.** Not evidence that live trading is safe. Only
  Phase 2's fill-sim-matches-reality comparison counts.
- **Static config caps without tests.** Not caps; aspirations. Every
  cap ships with a property test.
- **`ISOBAR_LIVE_ALLOW` as previously documented.** Stripped from
  mutable docs per [audit/live-allow-trace.md](../audit/live-allow-trace.md);
  ADR-0003 retains the historical Decision text with a forward note.
  The variable name is non-binding and the gate is deferred to the
  ADR that lands `cmd/bot` + `internal/exec`. Not a control we
  currently rely on.

## Risk-change procedure

Changing a risk parameter (raising a cap, relaxing a precondition,
disabling a check) requires:

1. A `docs/decisions/<NNNN>-risk-<slug>.md` entry with: current
   value, new value, rationale, reversal plan.
2. A code change PR with updated test expectations. The PR body
   must reference the decision doc.
3. Explicit self-review with a 24-hour cooling-off period (the
   solo-operator analogue of two-reviewer policy).

Risk parameters are never tweaked "just for this session".

## Incident response (Phase 3+ design)

**If the bot loses money faster than expected during a trading day.**

1. `touch halt` to freeze new orders.
2. Cancel resting orders via Kalshi's UI. (The bot does not panic-
   cancel on halt — it stops acting, not undoing.)
3. Pull a postmortem draft:
   - which strategy
   - which tickers
   - what changed in the last 24 h (commits, config, market regime)
4. File `docs/postmortems/<NNNN>-<slug>.md` within 48 h.
5. No restart until the postmortem identifies a root cause **and**
   a test that would have caught it is merged.

**If reconciliation diverges.**

1. Halt (automatic per L4).
2. Export state snapshot: `bot diff-state > state.json`.
3. Manual reconciliation via Kalshi UI.
4. Restart with `bot --reconcile-from=state.json` (Phase 3 flag to
   be built) which replays fills against a clean state.

## Glossary of impact

- **Hard halt.** Process exits 1; systemd / operator must restart.
- **Soft halt.** Process stays up, ignores ticks; cleared by removing
  `./halt`.
- **Warn.** Logged, recorded, but no impact on trading.
- **Panic.** Never acceptable outside programmer-bug scenarios;
  appearance in an error path is itself a bug to fix.

---

## Held for Phase 2 (Bucket B)

Phase 1's line audit surfaced 35 P1 findings. Bucket A closed 17 of
them (the rows flipped to `LIVE` / `LIVE-IMPROVED` / `RESOLVED`
above). The remaining ~18 are held for Phase 2; the authoritative
routing table is [audit/02-architecture.md §4.4](../audit/02-architecture.md)
which maps each Bucket B P1 onto a proposed ADR slot (ADR-0014
through ADR-0020). Those are reserved slots described as **proposal
only, pending per-decision sign-off** in audit/02-architecture.md;
none are accepted yet, and nothing in this document treats them as
authoritative.

## Known unknowns

Risks we cannot quantify from inside the bot; no mitigation is
possible from our side. Listed here so the next reader (or the next
audit) does not mistake silence for absence.

- **Kalshi rule change (counterparty side).** A series might be
  withdrawn, resolution criteria might be edited, or the fee
  schedule might be revised. Mitigation: monitor Kalshi's
  announcements; refuse to trade a market whose resolution
  criteria we have not re-read since the last bot change. No code
  defence.
- **Kalshi cap-table or NY-eligibility change.** Out of scope to
  enforce ourselves. See NY-compliance row above for the
  monitor-only stance.
- **Kalshi API breaking change.** The recorder journals raw bytes
  before any decoding (`audit/00-map.md` §4), so a decode failure
  on a schema bump produces a decode warning, not a silent
  misprice. A new API version that requires a re-keying of the
  RSA-PSS auth would silently produce a 401-storm; the L4 clock-
  drift / reconciliation monitor would notice within 60 s, but the
  recorder side has no equivalent. Logged.
- **Open-Meteo terms-of-use change.** The ensemble feed is free
  today. A future paywall or rate-limit would force a re-evaluation
  of the calibration corpus's update cadence.
- **NWS CLI bulletin format change.** Parser lives in
  `internal/calibration/cliparse` with 7 fixture tests
  (`audit/00-map.md` §1) — a soft-shape change in the bulletin
  would surface as parse failures in `cmd/calibrate-parse-cli`
  during the next monthly refit; not silent, but the next refit is
  the detection cadence.
- **Vultr Chicago region failure / Tailscale outage.** The recorder
  process would stop emitting frames. The host-side `tape-health.sh`
  smoke test is the only Phase 0 detector; no per-15-minute alarm
  exists. Phase 6 (observability) is the right venue.
- **Local disk full / I/O error during journal flush.** Journal
  writer panics on a sustained `Write` error; recorder process
  exits; systemd `Restart=on-failure` should restart with a fresh
  date directory. Not yet rehearsed against a real `ENOSPC`. Logged
  as a Phase 6 chaos-test target.

## References

- [docs/STRATEGY.md](./STRATEGY.md) — thesis + sizing + decision
  gate
- [docs/ARCHITECTURE.md](./ARCHITECTURE.md) — package layout,
  invariants, goroutine ownership
- [docs/CALIBRATION.md](./CALIBRATION.md) — calibration pipeline
- [audit/00-map.md](../audit/00-map.md) — Phase 0 territory map
  with §6 external integration surface, §8 secrets convention, §14
  P1/P2/P3 findings
- [audit/strategy-risk-reconciliation.md](../audit/strategy-risk-reconciliation.md)
  — memo backing the rewrite of this file
- [ADR-0001](./decisions/0001-research-driven-pivot.md) — Phase 0.5
  gate criteria
- [ADR-0005](./decisions/0005-extract-retry.md) — context-aware
  exponential backoff (used by Kalshi WS subscriber)
- [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md) —
  NWS CLI as y-target; KMDW resolution note
- [ADR-0008](./decisions/0008-deterministic-historical-forecast.md)
  — first-pass MOS with canary
- [ADR-0009](./decisions/0009-mos-canary-fired-escalate-calibration.md)
  — canary fired
- [ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md) — T−3h
  Bayesian update foundation
- [ADR-0011](./decisions/0011-journal-backpressure.md) — journal
  backpressure: drop-with-counter + sampled debug log + alarm hook
- [ADR-0012](./decisions/0012-time-source-discipline.md) —
  time-source discipline: Clock interface + clockcheck analyzer
- [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md) —
  retire the PR#9 paper-trader/scoring lineage at the Bucket A
  force-push; recovery paths catalogued
- [audit/01-line-audit.md](../audit/01-line-audit.md) appendix — Bucket A
  status (what shipped) + Bucket B held for Phase 2
- [audit/02-architecture.md](../audit/02-architecture.md) — Phase 2
  proposed structural decisions (ADR-0014–0020, **proposal only,
  pending per-decision sign-off**) + §4.4 Bucket B routing table
- [audit/live-allow-trace.md](../audit/live-allow-trace.md) —
  `ISOBAR_LIVE_ALLOW` deferral decision
