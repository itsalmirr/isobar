# Risk register

> The risks a $200-bankroll solo Kalshi weather-bracket bot has to
> survive, ordered by severity. Each row is dispassionate: what would
> happen, how we would know, what is in place today. Rewritten
> 2026-05-13 to reflect the Phase 0 audit
> ([audit/00-map.md](../audit/00-map.md)) and ADRs 0007–0010.
>
> Phase 0 has no live execution. Most controls below are
> design-ahead for Phase 1+; the column "Status" labels which ones
> are shipped (`LIVE`), in the current P1 triage round
> (`IN PROGRESS`), waiting on a decision (`PENDING DECISION`),
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

### Forecast-source outage (Open-Meteo down — what happens to live trade?)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | The poller emits a warn log on a failed fetch and skips to the next 30 m tick. The audit map ([§6](../audit/00-map.md)) flagged the missing retry; the symptom of a multi-hour outage is a multi-hour gap in `data/openmeteo/<date>/*.jsonl`. |
| Mitigation | Two layers: (1) record-side — implement a bounded exponential-backoff retry in the Open-Meteo poller so a transient failure is retried (≤ 5 attempts, capped at 30 s between tries, ctx-aware so SIGTERM still works). (2) Trade-side — the L3 signal-sanity gate's `staleness_max` (default 30 s for WS-derived state, larger for ensemble polls) refuses to emit a signal on stale ensemble data. Both layers are design-ahead; the record-side fix is part of the P1 triage round. |
| Owner | solo-operator |
| Status | **IN PROGRESS** (record-side retry); **DESIGN-AHEAD** (L3 staleness gate, Phase 1+) |

### Kalshi WS disconnect storm

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `cmd/tape` slog stderr lines `ws subscriber reconnecting` clustered within a short window. Per-reconnect counter logged in `audit/00-map.md` §5. |
| Mitigation | `internal/retry` provides context-aware exponential backoff with jitter
(`initial_backoff: 500ms`, `max_backoff: 30s`, `max_attempts: 0` = forever) per [config/config.yaml:42-46](../config/config.yaml). Subscriber re-establishes via `kalshi.SetTickers/Reload`. There is no circuit breaker on a sustained 5xx run; for Phase 0 (recorder-only) the right behaviour is to keep trying. Phase 1 exec will need a "stop new orders if WS has been disconnected for > N seconds" interlock; that lives in the L2 reconciliation design. |
| Owner | solo-operator |
| Status | **LIVE** (recorder side); **DESIGN-AHEAD** (exec-side interlock) |

### Silent journal drop (frame loss on burst)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `internal/journal.Write` is non-blocking ([journal.go:124](../internal/journal/journal.go)); on a full 16 384-item queue, the frame is dropped and a per-source counter increments. A 60 s warn log reports the drop count. Audit map [P1 #4](../audit/00-map.md). |
| Mitigation | Three pieces, all under the next ADR (the journal-backpressure ADR-0011 per the user instruction): (1) raise the drop event to a tagged structured log so a host-side monitor can alarm on it, (2) emit the drop counter to a sidecar JSON file that `tape-health.sh` can sample, (3) decide whether to switch the recorder to blocking-with-timeout when the queue is consistently saturated. The ADR will pick the trade-off (data fidelity vs producer-blocking) for the recorder vs trader separately. |
| Owner | solo-operator |
| Status | **IN PROGRESS** |

### Station-coordinate drift (CHI → ORD vs KMDW)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | [config/config.yaml:64](../config/config.yaml) currently encodes `lat: 41.9742, lon: -87.9073` (ORD), while [config/config.example.yaml:70](../config/config.example.yaml) encodes `lat: 41.7861, lon: -87.7522` (KMDW). [ADR-0007 §"Neutral"](./decisions/0007-calibration-ground-truth-cli.md) confirms Kalshi resolves `KXHIGHCHI`/`KXLOWTCHI` on Midway. Symptom of unresolved drift is a persistent residual on CHI brackets relative to other stations in the per-station Brier breakdown of `reports/phase0.5/`. |
| Mitigation | (1) Pin the canonical station list — name, lat, lon, ICAO — in `internal/market/stations.go` (single source of truth, per [docs/ARCHITECTURE.md](./ARCHITECTURE.md) §"market"). (2) At `cmd/tape` boot, validate every configured location's `(lat, lon)` against the registry within an epsilon (~0.05°); refuse to start on mismatch. (3) Fix the active `config/config.yaml` to KMDW coords. The active config has not been switched yet; the example file is correct. |
| Owner | solo-operator |
| Status | **IN PROGRESS** (registry sentinel + boot-time check is the P1 triage item; config swap is pending) |

### Secret leakage (`.env` perms, PEM on disk)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `stat -f%Mp%Lp /Users/wh0o0pz/dev/isobar/.env` returns the file mode. `git log --all -p -- .env` returns empty (audit §8: never committed). |
| Mitigation | (1) `.env` mode is now `0o600` per the P1 triage round (audit P1 #6). (2) PEM-on-disk path is operator-set; the recommended convention is `./secrets/kalshi.pem` with `0o600`; `.gitignore` covers `secrets/`, `*.pem`, `*.key`, `.env*`. (3) Pending: a boot-time refusal-to-start check that `.env` (when present) and the PEM file (always) are no more permissive than `0o600`. Without that check, a future `chmod` regression is silent. |
| Owner | solo-operator |
| Status | **PARTIAL** (`.env` mode is fixed; startup-time refusal check is pending) |

### Deploy-path drift (memory vs loop-prompt — pin to canonical systemctl)

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | `audit/00-map.md` §10: operator memory `project_isobar_prod_deploy.md` says `/root/isobar/...` on Vultr with `isobar-tape.service`; `scripts/loop-prompt.txt:3-5` references `/opt/isobar/...` and a separate `isobar` user. One is wrong; rolling forward on the wrong assumption breaks the next restart. |
| Mitigation | (1) Commit canonical `deploy/isobar-tape.service` + `deploy/install.sh` to the repo so the deployment is reproducible from `main` without operator-memory dependence. (2) Strip `/opt/isobar/...` from `scripts/loop-prompt.txt` or update it to match the systemd unit file. (3) Pin the install layout in [docs/OPERATIONS.md](./OPERATIONS.md) (note: that doc was last touched 2026-04-22 and predates the prod deploy; updating it is on the same P1 triage round). |
| Owner | solo-operator |
| Status | **IN PROGRESS** |

### `ISOBAR_LIVE_ALLOW`: doc'd kill-switch that does nothing

| Field | Value |
|---|---|
| Severity | **P1** |
| Detection | Audit §7 + §14 P3 #8: the string `ISOBAR_LIVE_ALLOW` appears in `docs/research/04-dashboard-architecture.md` and `docs/decisions/0003-dashboard-stack.md` as the gate that "must be set to `1` for live trading to occur", but `grep -r ISOBAR_LIVE_ALLOW internal/ cmd/` returns nothing. The check does not exist. |
| Mitigation | Two options, picked when Phase 1 exec scope review starts: (a) implement the check in `internal/config` so that any execution code refuses to send orders when `ISOBAR_LIVE_ALLOW != "1"`, defaulting to off; or (b) strip the reference from the docs and rely on the `./halt` file convention alone. Today the situation is the worst of both — operator may believe a gate is in place when it is not. |
| Owner | solo-operator |
| Status | **PENDING DECISION** |

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
- **`ISOBAR_LIVE_ALLOW` as currently documented.** It is documented
  but unimplemented (`audit/00-map.md` §7); see the P1 entry above.
  Currently not a control we can rely on.

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
- ADR-0011 (in flight per user instruction) — journal
  backpressure / silent frame drop mitigation
- ADR-0012 (planned next slot) — calibration validation harness
