# Strategy + Risk reconciliation memo

**Date:** 2026-05-13
**Author:** isobar team
**Scope:** Map every load-bearing claim in `docs/STRATEGY.md` and
`docs/RISK.md` (both last touched 2026-04-18) against the current
ground truth — four ADRs ratified since (0007–0010), the Phase 0.5 v2
floor estimate, the Phase 0 audit map, and the live code under
`config/`, `internal/`, and `cmd/`.

Action column legend:

- `KEEP` — claim is still true; preserve in the rewrite.
- `UPDATE` — partly true; rewrite to reflect the current state.
- `STRIKE` — now false; remove from the rewrite (call out below).
- `ADD` — load-bearing fact that the old doc never carried; insert in
  the rewrite.

---

## A. `docs/STRATEGY.md` (current file)

### A.1 Thesis paragraph

| Claim (existing doc) | File:line | ADR / fact that supersedes | Current truth | Action |
|---|---|---|---|---|
| "Kalshi weather-bracket markets price discrete-temperature outcomes … For a location and date." | [docs/STRATEGY.md:9-10](docs/STRATEGY.md) | [config/config.yaml:24-32](config/config.yaml) (KXHIGH/KXLOWT prefixes); ADR-0006 (naming) | True. Subscribed series are the eight KXHIGH/KXLOWT prefixes across NY/MIA/LAX/CHI. | KEEP |
| "NOAA's GFS ensemble publishes 31 forecast paths, ECMWF IFS publishes 51, and ML models (GenCast, AIFS) publish more" | [docs/STRATEGY.md:11-13](docs/STRATEGY.md) | [config/config.yaml:51](config/config.yaml) (`model: gfs_seamless,ecmwf_ifs025`); [internal/edge/openmeteo.go:122-128](internal/edge/openmeteo.go) | GFS+ECMWF wired in the live recorder; GenCast/AIFS not used. | UPDATE — drop ML models, keep GFS+ECMWF |
| "raw member fractions are uncalibrated probabilities, systematically biased" | [docs/STRATEGY.md:13-15](docs/STRATEGY.md) | ADR-0008; v2 floor estimate §"What we now believe" | Confirmed empirically — naive Brier 0.493 vs MOS 0.401 in tradeable cell. | KEEP |
| "After NGR/EMOS calibration, ECMWF blending, and an intraday Bayesian update at T−3h … our posterior … 5–15 pp tighter than the market's" | [docs/STRATEGY.md:15-18](docs/STRATEGY.md) | ADR-0008, ADR-0009, ADR-0010, v2 floor estimate | Aspirational not realized. Built MOS (not full NGR) on deterministic GFS only; canary fired; ECMWF blend + T−3h Bayesian are next steps, not present-tense. | UPDATE — frame as "planned next iteration", state we currently lose to market by ~10pp Brier in tradeable mid |
| "Secondary thesis: hurricane-landfall markets (`KXHURRICANE*`)" entire paragraph | [docs/STRATEGY.md:20-25](docs/STRATEGY.md) | ADR-0004 (two-signal scope) | Hurricane signal is paper-only; out of season; no code under `internal/` references hurricane parsing; the v2 floor estimate and ADR-0009 do not touch it. | STRIKE for this rewrite — Phase 0 + 0.5 + 0.6 are weather-only; hurricane remains a roadmap item but does not belong on the front page of STRATEGY.md |
| "Both theses are *measured* in Phase 0.5, not assumed. Gate: …" | [docs/STRATEGY.md:27-31](docs/STRATEGY.md) | ADR-0009; v2 floor estimate §"What this means for Phase 0.5" | Gate has been **tested and failed** by MOS-on-deterministic-GFS. Rewrite to reflect the canary fired plus the new gate criteria for v2 calibration (ADR-0010 §"Gate criteria"). | UPDATE |
| "BTC-hourly is killed." | [docs/STRATEGY.md:33-36](docs/STRATEGY.md) | ADR-0002 | True and permanent. | KEEP |

### A.2 `weather-calibrated` signal section

| Claim (existing doc) | File:line | ADR / fact that supersedes | Current truth | Action |
|---|---|---|---|---|
| "Pricing model (do NOT use raw fraction — it's miscalibrated by 10–15 pp at bracket boundaries; see `docs/research/02-weather-thesis-audit.md`)" | [docs/STRATEGY.md:43-44](docs/STRATEGY.md) | v2 floor estimate table §"By market-mid bucket" | Direction is right; magnitude is "Brier 0.493 vs MOS 0.401" in tradeable cell — re-cite the v2 numbers, not the research-essay range. | UPDATE |
| Pipeline step 1 — "Fetch GEFS (31 members) + ECMWF IFS (51 members)" | [docs/STRATEGY.md:48-49](docs/STRATEGY.md) | [config/config.yaml:51](config/config.yaml); [internal/edge/openmeteo.go:122-128](internal/edge/openmeteo.go) | Live recorder pulls both; calibration was fit on GFS deterministic only (ADR-0008). The ECMWF blend in calibration is **planned** (ADR-0009 Track A). | UPDATE — split the pipeline into "live recorder" (multi-model) and "calibration fit" (GFS-only today, ECMWF blend planned) |
| Pipeline step 2 — "Apply NGR / EMOS per `(station, season, lead-time)` bin, fit on ≥2 years of paired archive-vs-NWS-CLI observations" | [docs/STRATEGY.md:50-52](docs/STRATEGY.md) | ADR-0007; ADR-0008; [internal/calibration/mos/mos.go](internal/calibration/mos/mos.go) | Partly true. Calibrator is **MOS** not full NGR (ADR-0008 §"Decision" §3); bins are `(station, season)` not `(station, season, lead-time)` — the lead dimension was deferred (ADR-0008 §"Bad"). y-target is CLI bulletins per ADR-0007. | UPDATE — say "MOS" not "NGR/EMOS"; drop lead-time stratification from pipeline (still on the roadmap); cite ADR-0007 + ADR-0008 |
| Pipeline step 3 — "Blend the two models (inverse-variance weighted)" | [docs/STRATEGY.md:53](docs/STRATEGY.md) | ADR-0009 Track A | Not yet built. Track A is "in flight" per ADR-0009 — estimated 1-2 days. | UPDATE — list as a planned step under Phase 0.6 |
| Pipeline step 4 — "Apply EVT / Peaks-Over-Threshold (GPD) for tail brackets" | [docs/STRATEGY.md:54-56](docs/STRATEGY.md) | No ADR; no code | Pure planning text. No Go file mentions GPD or POT; the v2 floor estimate makes no reference. Removing this from STRATEGY.md is appropriate for the current state. | STRIKE — not part of Phase 0.5 or 0.6; move to a future-work appendix or drop |
| Pipeline step 5 — "At T−3h, perform a Bayesian update using observed METAR/ASOS trajectory" | [docs/STRATEGY.md:56-58](docs/STRATEGY.md) | ADR-0010 | ADR-0010 confirmed the **mechanism** (precision-weighted Gaussian) and replaced METAR/ASOS with Open-Meteo hourly observation as the v1 source (ADR-0010 §"Alternatives considered" — METAR deferred). Foundation lives in `internal/calibration/bayesian/` and `cmd/calibrate-bayesian/` (scaffold; fitter body deferred to follow-up commit). Not yet integrated into scan. | UPDATE — describe what ADR-0010 actually says, cite the foundation/integration split, name the upcoming validation-harness ADR slot |
| "This is the largest single-shot edge (+5–10¢/bracket at settlement)" | [docs/STRATEGY.md:58](docs/STRATEGY.md) | No measured evidence | Unsubstantiated. v2 floor estimate shows the **market wins** at lead [3, 12)h by 12pp Brier — the T−3h Bayesian update is the proposed response, not the demonstrated source of edge. | STRIKE the "+5–10¢/bracket" number; replace with a hypothesis framing tied to ADR-0010 §"Hypothesis" |
| Pipeline step 6 — "Wrap the predictor with conformal prediction intervals" | [docs/STRATEGY.md:59-61](docs/STRATEGY.md) | No ADR; no code | Pure planning text. No conformal-prediction implementation in Go files. | STRIKE — not part of any roadmap milestone we are currently building |
| "`p_fair` is a calibrated posterior probability with known coverage" | [docs/STRATEGY.md:63-64](docs/STRATEGY.md) | v2 floor estimate; ADR-0009 | `p_fair_mos` and `p_fair_climate_shrunk` are emitted in [internal/edge/scan.go](internal/edge/scan.go); neither has measured coverage beyond Brier in the v2 report. "Known coverage" overstates the current state. | UPDATE — say "OLS-fit MOS posterior plus a climate-shrunk-naive alternative", cite the report |
| "edge_yes = p_fair − (yes_ask + fee_round_trip)" formula | [docs/STRATEGY.md:67-71](docs/STRATEGY.md) | [internal/edge/scan.go](internal/edge/scan.go); [internal/edge/fee_test.go](internal/edge/fee_test.go) | True at the math layer. Code uses real Kalshi fee schedule per Pass 3 §0.1 + ADR-0001 fee-aware-floor. | KEEP |
| "Fee-aware edge floor (do NOT use fixed 8¢): `edge > 0.07 · 2 · P · (1−P) + 1¢`" | [docs/STRATEGY.md:78-82](docs/STRATEGY.md) | ADR-0001 §gate change | Still the design. Phase 0 has no live execution so floor is unenforced at runtime; it filters the v2 report's tradeable cell at [35,65)¢. | KEEP, with explicit "design-ahead, not enforced until exec ships" caveat |
| "Lyapunov 8-day gate: no signal emitted for brackets with lead_time > 8 days" | [docs/STRATEGY.md:83-85](docs/STRATEGY.md) | ADR-0004 §"Two-signal scope"; [config/config.yaml:62](config/config.yaml) (`forecast_days: 3` — recorder doesn't even fetch 8d) | Defensive ceiling; recorder pulls only 3 forecast days, so the gate doesn't bite today, but the constant is correct as a roadmap. | KEEP |
| "Quote maker-only … `PostOnly: true` on every order" | [docs/STRATEGY.md:88-92](docs/STRATEGY.md) | No exec code yet | Design constraint for Phase 1+ exec. No `KalshiExec.Submit` exists in `internal/`; this is doctrine, not behaviour. | KEEP, framed as "Phase 1+ exec doctrine" |
| "σ²_bin(P) = P(1−P)/√t_days (NOT P(1-P) alone — bounded-payoff correction)" | [docs/STRATEGY.md:90-92](docs/STRATEGY.md) | No code yet | Design constraint for future sizing module. | KEEP as doctrine |

### A.3 `hurricane-landfall` signal section (lines 115–146)

| Claim (existing doc) | File:line | ADR / fact that supersedes | Current truth | Action |
|---|---|---|---|---|
| Entire `hurricane-landfall` design — NHC ATCF, HAFS, EVT/POT, HMM, NDBC nudging | [docs/STRATEGY.md:115-146](docs/STRATEGY.md) | ADR-0004; `audit/00-map.md` shows no hurricane code | Speculative; out of season; no Go package implements any of it; not on the Phase 0.5 or 0.6 work plan. | STRIKE for this rewrite — keep the option open in a "Future work" appendix line if needed, but do not present it as a co-equal signal in 2026-05 |

### A.4 Execution doctrine + Sizing + Failure modes (lines 148–225)

| Claim (existing doc) | File:line | ADR / fact that supersedes | Current truth | Action |
|---|---|---|---|---|
| Execution doctrine — maker-only, post-only, one-order, cancel-before-replace, deterministic client IDs, fee-aware pricing | [docs/STRATEGY.md:150-163](docs/STRATEGY.md) | No exec code yet | All design-ahead. Re-state as doctrine; the file `internal/exec/` does not exist in the repo. | KEEP, mark "Phase 1+, design-ahead" |
| Sizing caps table — $10/ticker, $25/series, $50/portfolio, 100 contracts/order, 10% daily drawdown | [docs/STRATEGY.md:174-184](docs/STRATEGY.md) | Memory `project_kalshi_bot.md` ($200 bankroll); ADR-0001 | Caps make sense for a $200 bankroll; document them as the proposed Phase 1 starting caps. | KEEP, restate as proposed-not-enforced until Phase 1 exec lands |
| Failure modes list — stale state, double-sends, silent fill loss, clock drift, schema changes, kill switch, unbounded retries | [docs/STRATEGY.md:188-211](docs/STRATEGY.md) | Audit map §6, §14; ADR-0005 (`retry/`) | Most are still-relevant design constraints; the kill switch references `./halt`. ISOBAR_LIVE_ALLOW is documented but unimplemented in any Go file — audit P3 item 8. | UPDATE — cross-link to RISK.md and the audit map; note ISOBAR_LIVE_ALLOW is a doc'd kill-switch that does nothing |
| "What we will not do" list | [docs/STRATEGY.md:213-225](docs/STRATEGY.md) | n/a | Still the right ethic. | KEEP |
| Decision-log section | [docs/STRATEGY.md:227-238](docs/STRATEGY.md) | `docs/decisions/0001-0010` show the policy is followed | Working as designed. Mention 0007-0010 as the canon for the post-Phase-0 decisions. | UPDATE with current ADR list |

### A.5 Missing from current STRATEGY.md (ADDs)

| Topic | ADR / source | Why it belongs in the rewrite |
|---|---|---|
| **Current state of the gate (canary fired)** | ADR-0009; v2 floor estimate | Reader needs to know in para 2 that the bot does not trade real money today, and why. |
| **Phase 0 / 0.5 / 0.6 timeline as a tree** | ADR-0008 → ADR-0009 → ADR-0010 | The decision history is itself part of the strategy; the original doc was written before any of this happened. |
| **NY compliance posture** | memory `project_kalshi_bot.md` | Original doc never named the regulator. Single sentence at top. |
| **Solo operator / resume-grade Sharpe priority** | memory `user_working_style.md`; ADR-0009 §"Concrete next steps" #1 | Original doc never named the success criterion in plain English. |
| **Concrete v2 gate language (≥3pp Brier vs market, 95% bootstrap CI, 90+ days)** | ADR-0010 §"Gate criteria" | Operationalises "doesn't trade until calibration beats market". |
| **Pipeline diagram pointer** | `docs/CALIBRATION.md`; `docs/ARCHITECTURE.md` | Avoid duplicating the calibration pipeline picture; cross-link. |
| **Station list + the live CHI bug** | [config/config.yaml:64](config/config.yaml); audit P1 #1 | The active config still resolves CHI to ORD coordinates (41.9742/-87.9073), not Midway. Documented as in-flight P1 in RISK.md; the strategy doc just lists the four stations and points at the risk register. |

---

## B. `docs/RISK.md` (current file)

### B.1 The L0–L4 control hierarchy (lines 12–148)

| Claim (existing doc) | File:line | ADR / fact that supersedes | Current truth | Action |
|---|---|---|---|---|
| L0 — `./halt` file kill switch | [docs/RISK.md:30-43](docs/RISK.md) | No exec code yet | Doctrine, not implemented. There is no `KalshiExec.Submit` to gate. Restate as design-ahead. | KEEP, scope as Phase 1+ |
| L1 — Pre-trade caps table (per-ticker, per-series, portfolio, drawdown) | [docs/RISK.md:46-62](docs/RISK.md) | Same as above | Doctrine. Match STRATEGY.md sizing table. | KEEP |
| L1 §4 — "Sizing uses Kelly with the mutually-exclusive-bracket correlation correction `ρ_ij = −1/(K−1)`" | [docs/RISK.md:73-76](docs/RISK.md) | No code | Doctrine, mathematically correct, not implemented. | KEEP, design-ahead |
| L1 §5 — "Reservation price / half-spread use bounded-payoff σ" | [docs/RISK.md:77-80](docs/RISK.md) | No code | Doctrine. | KEEP |
| L2 — State reconciliation (warm-start) | [docs/RISK.md:91-108](docs/RISK.md) | No code | Doctrine; `state.sqlite` does not exist in the repo (audit §4). The journal model is JSONL, not SQLite. | UPDATE — replace `state.sqlite` with whichever persistence Phase 1 picks; or just call it "exec-state store, format TBD in Phase 1" |
| L3 — Signal sanity, hurricane regime gate | [docs/RISK.md:110-130](docs/RISK.md) | ADR-0004 (two-signal scope); audit | Weather sanity rules are still correct; hurricane regime gate is irrelevant to current scope. | UPDATE — keep weather rules, drop hurricane-specific clauses |
| L4 — Daily drawdown, reconciliation loop, clock drift | [docs/RISK.md:132-148](docs/RISK.md) | No code | Doctrine. | KEEP |
| "Non-controls" section — Kalshi server-side rejection, paper-trade PnL, untested caps | [docs/RISK.md:150-158](docs/RISK.md) | n/a | Still the right ethic. | KEEP |
| Risk-change procedure | [docs/RISK.md:160-172](docs/RISK.md) | `docs/decisions/` index | Currently followed for ADRs; preserve. | KEEP |
| Incident response — `bot diff-state`, `bot --reconcile-from` | [docs/RISK.md:174-194](docs/RISK.md) | No code | Doctrine. | KEEP, scope Phase 3+ |
| Glossary — hard halt, soft halt, warn, panic | [docs/RISK.md:196-202](docs/RISK.md) | n/a | Useful. | KEEP |

### B.2 P1 risks discovered in Phase 0 audit that the current RISK.md does not name

| Risk | Source | Status | Why it must enter RISK.md |
|---|---|---|---|
| **Silent journal frame drop on burst** | `audit/00-map.md` §14 P1 #4; [internal/journal/journal.go:124](internal/journal/journal.go) | IN PROGRESS (per user; tracked under ADR-0011 journal-backpressure decision) | A 16 384-item queue silently drops on overflow with only a 60s warn log. For a recorder feeding a calibration corpus, dropped frames = unmeasurable bias. |
| **Open-Meteo: no retry on transient failure** | `audit/00-map.md` §6, §14 P1 #3; [internal/edge/openmeteo.go](internal/edge/openmeteo.go) | IN PROGRESS | A multi-hour Open-Meteo outage = silent multi-hour gap in the recorded ensemble corpus. P1 in audit. |
| **Station-coordinate drift (CHI → ORD vs KMDW)** | `audit/00-map.md` §9; [config/config.yaml:64](config/config.yaml) (still ORD coords); ADR-0007 §"Neutral" (KXLOWTCHI resolves on KMDW) | IN PROGRESS | Active config polls Open-Meteo at ORD coords; Kalshi resolves KXHIGHCHI/KXLOWTCHI on KMDW per ADR-0007. Systematic forecast bias on every CHI bracket recorded. Mitigation: pin a station-registry sentinel + boot-time check. |
| **Secrets at rest: `.env` perms** | `audit/00-map.md` §8 P1 | PARTIAL (file now `0o600`; startup check pending) | Operator vigilance only; startup check + audit-log entry mitigates regression. |
| **Deploy-path drift (memory says `/root/isobar/...`, `scripts/loop-prompt.txt` says `/opt/isobar/...`)** | `audit/00-map.md` §10; memory `project_isobar_prod_deploy.md` | IN PROGRESS | Two contradictory deployment paths. One is wrong; rolling forward will break a future restart. Mitigation: commit canonical systemd unit file + install script under `deploy/`. |
| **`ISOBAR_LIVE_ALLOW`: documented kill-switch with no code** | `audit/00-map.md` §7 P3 #8 | PENDING DECISION | Documented in `docs/research/04-dashboard-architecture.md` and ADR-0003 as a live-trade gate, but the string never appears in any `.go` file. Either implement before Phase 1 exec or strip from the docs. Worse than missing — it's a false comfort. |

### B.3 P2/P3 audit findings that are explicitly out-of-scope for this rewrite

| Finding | Source | Why excluded |
|---|---|---|
| `bin/` committed despite `.gitignore` | audit §1 + §14 P2 #5 | **Confirmed false** by user instruction. `git ls-files bin/` is empty; `/bin/` in `.gitignore` is working. The Phase 0 agent inferred wrongly. Local binaries exist but are not tracked. Not entered in the rewritten risk register. |
| `data/kalshi/2026-05-12/` uncompressed | audit §14 P2 #7 | Operational housekeeping; rotation script will run next cycle. Not a risk. |
| `cloud-id=...` stray in `.env` | audit §14 P3 #9 | Single-line config hygiene; not a risk-register item. |
| `docs/README.md` stale | audit §14 P3 #10 | Deferred per product lead (end of Phase 4). Out of scope per user instruction. |
| Branch protection not declared in repo | audit §14 P3 #14 | Repo-policy item; lives in `gh api`, not in code. Operator vigilance. |
| `cmd/peek` dead | audit §15 #16 | Dead code; not a risk to live trading. |

### B.4 Risks the current RISK.md is silent on (ADDs)

| Risk | Why it must enter RISK.md | Severity |
|---|---|---|
| Bankroll concentration | Memory `project_kalshi_bot.md` says $200 total; old RISK.md sets caps but never named the bankroll, so the cap-ratio is opaque to a fresh reader. | P1 |
| Single-station correlation (KNYC mispriced → multi-bracket loss) | Eight subscribed series concentrate across four cities; a systematic bias on one city flows through both H and L brackets. Audit §9 + ADR-0007 §"Neutral". | P1 |
| Calibration drift (MOS coefficients age) | ADR-0008 §"Bad"; refit cadence is not yet on a calendar. | P1 |
| Forecast-source outage (Open-Meteo down → live trade?) | Audit §6 (no retry); the recorder simply skips a 30m tick. Effect on a hypothetical live signal is unmodeled. | P1 |
| Kalshi WS disconnect storm | Audit §6 (reconnect tuning is `max_attempts: 0` — retry forever; correct, but no circuit breaker on repeated 5xx). | P2 |
| NY compliance regression | Memory `project_kalshi_bot.md`; Kalshi's NY-eligibility list is the counterparty's responsibility but bot-side risk if a series gets pulled mid-flight. | P1 |
| Kalshi schema change (counterparty rule change, contract spec change) | Tail risk; no mitigation possible from our side. Goes in "Known unknowns" tail. | n/a |
| Calibration-eval harness not yet written | ADR-0010 §"Gate criteria"; the v2 calibration cannot be promoted to live until the next-ADR-slot eval harness ships. | P0 — gating |

---

## C. Summary — STRIKEs to call out to the operator

When the operator reads the rewritten `STRATEGY.md` they will lose these
specific claims from the 2026-04-18 draft. Each strike is justified
above; restated here as a single checklist so the operator can object
to any of them in one place.

1. **STRIKE** — ML-model name-drops in the thesis paragraph ("GenCast,
   AIFS publish more"). Neither is wired in.
   *Source:* [docs/STRATEGY.md:13](docs/STRATEGY.md), audit §6 (no
   external dependency on GenCast or AIFS).
2. **STRIKE** — "Our posterior … 5–15 pp tighter than the market's" as
   a present-tense claim. The v2 floor estimate measures the opposite
   in the tradeable cell.
   *Source:* [docs/STRATEGY.md:17-18](docs/STRATEGY.md);
   `reports/phase0.5/2026-05-05-floor-estimate-v2.md` §"Stratified
   Brier Scores".
3. **STRIKE** — Secondary `hurricane-landfall` thesis as a co-equal
   signal in the document body. No Go file implements any of it; it is
   out of season; ADR-0004 names a two-signal scope but the second is
   not on the Phase 0.5 or 0.6 work plan.
   *Source:* [docs/STRATEGY.md:20-25, 115-146](docs/STRATEGY.md).
4. **STRIKE** — Pipeline step 4: EVT/POT tail-bracket extrapolation.
   No package implements it; not in the v2 report; not on the roadmap.
   *Source:* [docs/STRATEGY.md:54-56](docs/STRATEGY.md).
5. **STRIKE** — Pipeline step 6: conformal-prediction intervals. No
   package implements it; not on the roadmap.
   *Source:* [docs/STRATEGY.md:59-61](docs/STRATEGY.md).
6. **STRIKE** — "T−3h Bayesian update is the largest single-shot edge
   (+5–10¢/bracket at settlement)". Unsubstantiated by any report; the
   v2 estimate shows the market wins at lead [3, 12)h.
   *Source:* [docs/STRATEGY.md:58](docs/STRATEGY.md);
   `reports/phase0.5/2026-05-05-floor-estimate-v2.md` §"By lead-hours
   bucket".
7. **STRIKE** — "Calibrated posterior probability with known coverage"
   framing. The MOS posterior's coverage is not measured beyond Brier
   skill in the v2 report.
   *Source:* [docs/STRATEGY.md:63-64](docs/STRATEGY.md).
8. **STRIKE (from RISK.md)** — L3 hurricane-regime clause ("skip when
   HMM posterior is within 15 pp of uniform"). Hurricane is out of
   scope.
   *Source:* [docs/RISK.md:121-124](docs/RISK.md).

## D. Uncertainties the rewrite cannot resolve from the artifacts alone

- **Live-config CHI fix status.** The user instructions say "CHI
  fixed" but [config/config.yaml:64](config/config.yaml) still has the
  ORD coordinates `41.9742/-87.9073`. The example file
  [config/config.example.yaml:70](config/config.example.yaml) has the
  correct KMDW coords. Treating this as "IN PROGRESS" until a config
  commit lands.
- **ADR slot for the calibration validation harness.** ADR-0011 was
  referenced in `docs/CALIBRATION.md` and `docs/ARCHITECTURE.md` as the
  eval harness, but the user instructions pin ADR-0011 to "journal
  backpressure". I will refer to the eval harness by its function plus
  "next ADR slot (planned ADR-0012)" in the rewrite, without touching
  any existing ADR file.
- **`max_daily_drawdown_dollars` default.** Old RISK.md §L4 mentions
  "default: 10% of bankroll" but no `config.yaml` field carries it.
  Will restate as Phase 1 wiring item.
- **Whether `state.sqlite` survives the Phase 1 design.** Old RISK.md
  L2 names SQLite; the live codebase is JSONL-only. Treated as
  Phase 1+ design-ahead with the persistence format TBD.

---

## End of memo
