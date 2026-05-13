# Strategy

> The edge thesis, the gate that the bot has to pass before live
> trading, and the failure modes we refuse to have. Written
> 2026-05-13 to reflect the post-canary state (ADR-0009 fired) and
> the response (ADR-0010 + the planned validation harness in the next
> ADR slot after 0011). Refreshed same day post-Bucket-A close: see
> [ADR-0011](./decisions/0011-journal-backpressure.md),
> [ADR-0012](./decisions/0012-time-source-discipline.md), and
> [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md) for
> the canonical references. The Phase 2 structural decisions
> (ADR-0014–0020) are **proposed in
> [audit/02-architecture.md](../audit/02-architecture.md), pending
> per-decision sign-off** — none are authoritative yet.

## In one paragraph

`isobar` is a solo-operator weather-bracket trading bot targeting
Kalshi's `KXHIGH{CITY}` and `KXLOWT{CITY}` daily-temperature contracts
for NYC, Miami, Chicago, and LAX, with a $200 starting bankroll and a
New-York-compliant posture (we only subscribe to series Kalshi serves
to NY retail). The bot does **not** trade real money today. Phase 0
records the live tape; Phase 0.5 measured calibration against held-out
ground truth and found the canary
([reports/phase0.5/2026-05-05-floor-estimate-v2.md](../reports/phase0.5/2026-05-05-floor-estimate-v2.md)).
Phase 0.6 builds the response: T−3h Bayesian intraday update
([ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md)) plus an
empirical validation harness (the next ADR slot after the journal
backpressure ADR-0011, planned as ADR-0012). The bot's success
criterion is **resume-grade Sharpe over moonshot returns**: the
strongest outcome is a documented forecasting-research artifact even
if calibration ultimately cannot beat the Kalshi market mid in the
tradeable midrange.

## Thesis

Kalshi's daily-extremes weather brackets price discrete-temperature
outcomes — for example, "`KXHIGHNY: TMAX in [60, 65)°F`" — for one
station on one civil date. Retail flow dominates these books.

The naive estimator — "fraction of GFS ensemble members whose
forecast daily extreme lands in the bracket" — has two structural
biases that a calibrated estimator can correct:

1. **Per-station, per-season forecast bias.** A summer KMIA TMAX
   forecast is not the same problem as a winter KMDW TMAX forecast;
   a single global fraction-in-bucket estimator cannot fix that.
2. **Ensemble underdispersion.** A 31-member GFS control+perturbation
   ensemble is centred close to the deterministic forecast and has
   narrower spread than the predictive distribution of the
   atmosphere. The naive count systematically underweights the tail
   brackets.

MOS-style per-bin linear correction (`μ_cal = a + b · forecast`, σ
from holdout residuals) addresses both biases in principle. ECMWF IFS
member blending and an intraday Bayesian update against the day's
running TMAX or TMIN are the named upgrades when the simplest fit is
not enough. The reference architecture is laid out in
[docs/CALIBRATION.md](./CALIBRATION.md).

The thesis is empirical, not assumed. The verdict from the v2 floor
estimate (n = 299 verifiable bracket outcomes, bootstrap 95% CIs):

| Comparison (tradeable [35, 65)¢ zone, n = 79) | Δ Brier | CI95 | Verdict |
|---|---:|---|---|
| MOS over naive | +0.060 | [+0.008, +0.113] | MOS wins |
| MOS over climate-shrunk-naive | −0.011 | [−0.056, +0.035] | tied |
| MOS over Kalshi market mid | **−0.100** | [−0.188, −0.013] | **market wins** |

Source: `reports/phase0.5/2026-05-05-floor-estimate-v2.md`
§"Bootstrap 95% CIs on Brier deltas". Same report §"By lead-hours
bucket" shows the gap concentrates at lead [3, 12)h — the close-to-
settlement window where intraday observations move the price. The
T−3h Bayesian update in ADR-0010 directly targets that bucket.

`KXHURRICANE*` was named as a secondary signal in ADR-0004 (two-signal
scope) and is acknowledged for hurricane season, but no Go package
under `internal/` implements any hurricane-pricing logic today, and
it is not part of Phase 0.5 or Phase 0.6. It is treated as future
work; see [docs/ROADMAP.md](./ROADMAP.md).

BTC-hourly was killed by [ADR-0002](./decisions/0002-kill-btc-hourly.md)
and is not coming back.

## Scope

| Item | Value | Source |
|---|---|---|
| Bankroll | $200 starting | memory `project_kalshi_bot.md` |
| Series subscribed | `KXHIGHNY`, `KXHIGHMIA`, `KXHIGHLAX`, `KXHIGHCHI`, `KXLOWTNYC`, `KXLOWTMIA`, `KXLOWTLAX`, `KXLOWTCHI` | [config/config.yaml:24-32](../config/config.yaml) |
| Stations polled | `KNYC` (Central Park), `KMIA` (Miami Intl), `KLAX`, `KMDW` (Chicago Midway per ADR-0007 §"Neutral") | [config/config.example.yaml:70](../config/config.example.yaml); active config drift tracked in RISK.md |
| Forecast horizon | 3 days | [config/config.yaml:62](../config/config.yaml) (`forecast_days: 3`) |
| Live ensemble | GFS (`gfs_seamless`) + ECMWF IFS (`ecmwf_ifs025`) — combined ~80-member | [config/config.yaml:51](../config/config.yaml); [internal/edge/openmeteo.go:122-128](../internal/edge/openmeteo.go) |
| Calibration ground truth | NWS CLI daily climate bulletins, latest issuance per civil date | [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md) |
| Compliance posture | NY-compliant: we only subscribe to series Kalshi serves to NY retail | memory `project_kalshi_bot.md` |
| Success metric | Resume-grade Sharpe over moonshot returns; documented forecasting-research artifact is an acceptable terminal state | memory `user_working_style.md`; v2 floor estimate §"Concrete next steps" #1 |

## Phase timeline

### Phase 0 — Recorder live (accomplished)

The `cmd/tape` recorder is running on Vultr Chicago, capturing the
live Kalshi WS (`orderbook_delta`, `trade`) and the multi-model
Open-Meteo ensemble (`gfs_seamless,ecmwf_ifs025`). Journals are
JSONL with rotation at 256 MiB or 1 h, gzip on rotate, batched
fsync. As of 2026-05-13: 798 MB on disk (`audit/00-map.md` §0).

Goroutine lifecycle, external integration surface, and storage
layout are documented in [docs/ARCHITECTURE.md](./ARCHITECTURE.md)
and `audit/00-map.md` §4–§8. No live execution; the recorder is
pure outbound.

### Phase 0.5 — Calibration research (gate failed)

The v2 floor estimate measured the original Phase 0.5 gate from
[ADR-0001](./decisions/0001-research-driven-pivot.md):

> "calibrated Brier-skill > market-implied on held-out 90-day OOS data"

MOS on deterministic-GFS does not clear that gate in the tradeable
[35, 65)¢ midrange (Brier 0.401 vs market 0.255). The canary set in
ADR-0008 §"Canary" fired; [ADR-0009](./decisions/0009-mos-canary-fired-escalate-calibration.md)
named two parallel response tracks.

### Phase 0.6 — Calibration v2 (in flight)

**Track A — ECMWF IFS blend.** Live recorder already ingests both
models per [config/config.yaml:51](../config/config.yaml). The
calibration corpus is still GFS-only (ADR-0008 used deterministic
GFS as the x-side); the planned upgrade fits MOS on the combined
member set or per-source blend. Estimated effort 1–2 days
(ADR-0009 §"Track A").

**Track B — T−3h Bayesian update.** Foundation lands in
`internal/calibration/bayesian/` (math + loader, schema-versioned
JSON kernel) plus a scaffold `cmd/calibrate-bayesian` whose fitter
body is the next commit. Math is the standard precision-weighted
Gaussian update of the MOS prior by an "observation forecast"
constructed from the day's running max or min plus an empirical
residual kernel. See [ADR-0010 §"Decision"](./decisions/0010-t-minus-3h-bayesian-update.md)
for the math and edge cases.

**Validation harness — next ADR slot (planned ADR-0012).** A
held-out evaluation tool that:

- Sweeps the corpus by `(station, variable, lead_bucket, mid_bucket)`.
- Reports bootstrap 95% CIs on Brier deltas.
- Has a CLI gate that returns nonzero if any gate criterion fails.

Until that tool runs and clears its primary gate, the T−3h Bayesian
update is not wired into the live scan path. The package compiles
and its tests pass; integration is gated.

**Bucket A boundary event — PR#9 paper-trader lineage retired.**
The Bucket A close force-pushed `main` to drop 11 commits of
`cmd/paper-trader` + `cmd/fillability` + `cmd/score` + `cmd/settle` +
`cmd/backfill-score` + `internal/papertrade` work merged via PRs
#7–#9 ahead of the audit (out of scope for Phase 1's "no new
capability" framing; binaries no longer compiled against the
post-Bucket-A `internal/edge` API). Nothing is lost:
[ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md)
catalogues commit SHAs, PR refs, and audit backup branches
(`audit/2026-05-13` + `audit/2026-05-13-post-bucket-a`) for
cherry-pick + migration when the `cmd/bot` ADR unblocks the
live-money path.

### Decision gate (before any real-money order)

The bot does not place a real-money order until **all four** of
these hold on a single held-out evaluation report:

1. **MOS+Bayesian beats Kalshi market mid by ≥ 3 pp Brier** in the
   tradeable [35, 65)¢ + lead [3, 12)h cell, with bootstrap 95% CI
   lower bound > 0. (ADR-0010 §"Gate criteria" primary.)
2. **MOS+Bayesian beats MOS-alone by ≥ 2 pp Brier** in the same
   cell, with bootstrap 95% CI lower bound > 0. If this fails, the
   Bayesian upgrade is cosmetic and the prior is doing the work.
   (ADR-0010 §"Gate criteria" secondary.)
3. **At least 90 days of held-out CLI ground-truth coverage** in
   that cell (n ≈ 600+ rows across the four stations × two
   variables). (Phase 0.5 v2 §"Concrete next steps" #4.)
4. **No regression on cells MOS already wins** (lead [12, 24)h and
   [24, 48)h, mid < 15¢). The `prior-only` fallback in the Bayesian
   package must fire correctly when the kernel has insufficient
   data. (ADR-0010 §"Gate criteria" sanity.)

If all four pass: write the v3 floor estimate report, then proceed
to Phase 1 scope review (execution code, risk gates wired, paper
period). If any fail: file the next ADR after ADR-0012 escalating
to ADR-0009 Track C (NOAA GEFS GRIB backfill for full-ensemble
NGR) or, per the v2 report's recommendation, pivot the project
framing to "forecasting research artifact" without further
calibration spend.

## Pipeline summary

The probability flow lives in `internal/edge/scan.go` and is
documented in detail in [docs/CALIBRATION.md](./CALIBRATION.md). One
sentence per stage:

1. **Live recorder** (`cmd/tape`) streams Kalshi `orderbook_delta` +
   `trade` and polls Open-Meteo every 30 minutes for the GFS+ECMWF
   ensemble across the four stations.
2. **Replay** (`internal/replay`) reconstructs the orderbook per
   bracket per snapshot time.
3. **Probability models in `internal/edge/scan.go`** emit four
   probabilities per row: `naive` (fraction of members in bracket),
   `MOS` (per-bin linear correction; [internal/calibration/mos](../internal/calibration/mos)),
   `climate-shrunk` (`p = 0.2 · climate + 0.8 · naive`;
   [internal/calibration/climate](../internal/calibration/climate)),
   and `market` (Kalshi mid). MOS uses `FitModelSet` stamping so the
   scan refuses to apply a GFS-only fit to a multi-model poll.
4. **Bayesian update** (`internal/calibration/bayesian`) updates the
   MOS prior at trade time by precision-weighted Gaussian
   conjugation against the day's running extremum. *Foundation only
   today*; integration into scan is gated on the validation harness
   above.
5. **Edge** (`internal/edge/scan.go` `edge_yes`, `edge_no` columns)
   subtracts the round-trip Kalshi fee and emits per-bracket rows
   for the v2 report's stratifications.

## Entry rules (Phase 1+ design)

The bot does not have execution code today. The rules below are
the design constraints that Phase 1 will wire into
`internal/exec` (and that the v2 floor estimate already filters
against at the analysis layer).

- **Fee-aware edge floor.** `edge > 0.07 · 2 · P · (1 − P) + 1¢`
  per side. Cited in ADR-0001 §"Phase 0.5 gate revision"; this is
  not a fixed 8¢ floor. The shape rewards quoting away from the
  steep fee region around P ≈ 0.5.
- **Lyapunov horizon ceiling.** No signal on brackets with
  `lead_time > 8` days. The live recorder fetches `forecast_days:
  3` ([config/config.yaml:62](../config/config.yaml)), so the
  ceiling does not bind in Phase 0, but the constant is encoded in
  RISK.md L1 §`max_lead_time_days: 8`.
- **Maker-only by default.** Every order is `PostOnly: true`.
  Crossing rejects.
- **One live order per `(strategy, market, side)`.** No order
  stacks; reconciliation after disconnect is the expensive part of
  exec, and stacks make it worse.
- **Cancel-before-replace.** Kalshi has no modify API; emulating
  one across a network blip loses audit traceability.
- **Deterministic `ClientOrderID`.** `<strategy>-<ticker>-<run>-<side>`
  so retries reuse the same ID and Kalshi de-dupes.
- **Hold-to-expiry default.** The Phase 0.5 measurement uses
  realized-PnL at settlement; round-tripping the book is not the
  edge.

## Sizing (Phase 1+ design)

Position sizing is separate from signal emission. It will live in
`internal/risk` (does not exist today). Default rule:
fractional Kelly (0.5 ×) on `edge_after_fees`, denominator the
implied variance of the bracket outcome, with the mutually-
exclusive-bracket correlation correction `ρ_ij = −1/(K−1)`
mandatory for any KXHIGH/KXLOWT portfolio (otherwise Kelly
oversizes on correlated bets — see RISK.md L1).

| Cap | Default at Phase 1 launch | Source |
|---|---:|---|
| Max notional per ticker | $10 | RISK.md L1; sized for $200 bankroll |
| Max notional per series | $25 | RISK.md L1 |
| Max notional portfolio-wide | $50 | RISK.md L1; scales with bankroll |
| Max contracts per order | 100 | RISK.md L1 |
| Max daily drawdown before halt | 10% of bankroll ($20 today) | RISK.md L4 |

Each cap will live in `config.yaml`, not in code constants.

## Failure modes we refuse to have

Cross-linked with [docs/RISK.md](./RISK.md). Quick list:

1. **Stale state.** After reconnect, no new orders until the bot
   has reconciled open orders + positions from Kalshi REST. Hard
   wired into the exec loop (Phase 1+).
2. **Double sends.** Every order carries a `ClientOrderID`; retries
   reuse it; Kalshi de-dupes.
3. **Silent fill loss.** Fills observed on WS are persisted to disk
   *before* in-memory position state updates. The recorder's
   journal-write path is non-blocking and drops on a full queue;
   [ADR-0011](./decisions/0011-journal-backpressure.md) is now
   accepted and Bucket A1+A2 shipped the per-source counter +
   sampled debug log + alarm hook + boot-time tripwire test
   (`cmd/tape/main_test.go::TestProdJournalHasAlarmHookWired`).
   The exec path will not adopt the non-blocking pattern — it
   will choose blocking-with-deadline once `cmd/bot` lands.
4. **Clock drift.** Signed requests fail if local clock drifts > 1
   min from Kalshi server time. Drift > 250 ms warns; > 1 s halts
   trading (RISK.md L4).
5. **Schema changes on Kalshi.** Recorder journals raw payloads;
   decoders are versioned; unknown fields are preserved. A schema
   bump should produce a decode warning, not a silent misprice.
6. **Kill switch.** `./halt` file present ⇒ no orders leave the
   process (RISK.md L0).
7. **`ISOBAR_LIVE_ALLOW`.** Previously documented as a live-trade
   gate in ADR-0003; the audit found the variable was unread by any
   Go file. The reference is now stripped from mutable docs per
   [audit/live-allow-trace.md](../audit/live-allow-trace.md); ADR-0003
   retains the historical Decision text (immutable per project
   policy) with a forward-note appendix. The live-trade gating
   mechanism is deferred to the dedicated ADR that lands `cmd/bot`
   + `internal/exec`; today the only operator-facing gate is the
   `./halt` file convention (L0), which is sufficient because no
   live-order code exists.

## What we will not do

- **Not** place market orders. Ever.
- **Not** chase fills. If a quote fills adversely, we accept, log,
  and wait. Revenge trading is how $200 becomes $0.
- **Not** run multiple strategies against the same market without
  a coordinator that enforces per-ticker caps across them.
- **Not** trade a market whose resolution criteria we have not
  read in full and linked in code. ADR-0007 §"Neutral" already
  caught one such case (KXLOWTCHI resolves on KMDW, not KORD);
  see RISK.md "station-coordinate drift".
- **Not** promote a calibration upgrade past the integration flag
  without the validation harness clearing its primary gate. The
  v2 report's recommendation is that this discipline matters more
  than the final P/L.

## Decision log

Every strategy-level change goes into `docs/decisions/<NNNN>-<slug>.md`
with **Context / Decision / Alternatives / Consequences / Reversal
cost** sections. Current canon for the post-Phase-0 calibration arc:

- [ADR-0001](./decisions/0001-research-driven-pivot.md) — original
  two-signal scope and Phase 0.5 gate
- [ADR-0004](./decisions/0004-two-signal-scope.md) — weather +
  hurricane scope; hurricane is paper-only today
- [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md) —
  NWS CLI bulletins as the canonical y-target
- [ADR-0008](./decisions/0008-deterministic-historical-forecast.md)
  — deterministic GFS as the first-pass x-side; with the canary
- [ADR-0009](./decisions/0009-mos-canary-fired-escalate-calibration.md)
  — canary fired; escalate via ECMWF blend (Track A) and T−3h
  Bayesian (Track B)
- [ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md) —
  T−3h Bayesian update math and gates
- [ADR-0011](./decisions/0011-journal-backpressure.md) — journal
  backpressure (accepted; landed via Bucket A1+A2)
- [ADR-0012](./decisions/0012-time-source-discipline.md) —
  time-source discipline: Clock interface + clockcheck analyzer
  (accepted; landed via Bucket A4+A8)
- [ADR-0013](./decisions/0013-retire-pr9-paper-trader-lineage.md) —
  retire the PR#9 paper-trader / scoring lineage at the Bucket A
  force-push; recovery paths catalogued
- ADR-0014 through ADR-0020 — Phase 2 structural decisions
  **proposed in [audit/02-architecture.md](../audit/02-architecture.md),
  pending per-decision sign-off** (error-handling discipline,
  schema-migration policy, station-registry consolidation,
  `internal/edge` directional-split deferral, role boundaries,
  CLI test discipline, `internal/`-only rule). None are accepted yet.
- Calibration validation harness (still planned; ADR slot pending
  per the post-Phase-2 sequence) — the primary gate the bot must
  clear before any real-money trade.

Decisions are never silent. A strategy is a hypothesis;
hypotheses deserve timestamped evidence.

## References

- [ADR-0007](./decisions/0007-calibration-ground-truth-cli.md),
  [ADR-0008](./decisions/0008-deterministic-historical-forecast.md),
  [ADR-0009](./decisions/0009-mos-canary-fired-escalate-calibration.md),
  [ADR-0010](./decisions/0010-t-minus-3h-bayesian-update.md)
- [Phase 0.5 v2 floor estimate](../reports/phase0.5/2026-05-05-floor-estimate-v2.md)
  — empirical basis for the canary
- [docs/CALIBRATION.md](./CALIBRATION.md) — engineer-readable
  companion to the calibration pipeline
- [docs/ARCHITECTURE.md](./ARCHITECTURE.md) — package layout +
  invariants
- [docs/RISK.md](./RISK.md) — risk register
- [audit/00-map.md](../audit/00-map.md) — Phase 0 territory map
- [audit/strategy-risk-reconciliation.md](../audit/strategy-risk-reconciliation.md)
  — memo backing this rewrite
