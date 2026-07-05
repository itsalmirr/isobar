# BUILDLOG — structure-first build loop

Coordination journal between autonomous build runs (hourly scheduled task
`isobar-structure-first-build`) and interactive sessions. **Newest entry
first.** One terse entry per run: timestamp, done (commit hashes), next,
blockers. Trust this file over re-deriving state; never duplicate work a
parallel session marks IN FLIGHT.

---

## 2026-07-03 ~21:30 ET — reduction Phase 2: consolidation + complexity (interactive session)

**Done (same uncommitted tree):** operator authorized "reduce + maintainability" →
executed Phase 2 per protocol. Full record: **`reduction/02-consolidation.md`**.
- `edge.Scan` cyc 47→17/14 (scanner struct, processFrame/processTrade split,
  naiveEnsembleStats, Spec.Variable, entry-filter registry, sync.Map→map);
  `priceDollars` cyc 22→≤9 with a new 17-case contract test (had none).
- `cmd/edge-scan`: stdout/markdown counts share one writer; **bug fixed** —
  Top-brackets section hardcoded 5¢, ignoring `-edge-threshold-cents`
  (audit/01:846); Result now echoes EdgeThresholdCents. First CLI tests (4).
- Calibration track: `obscsv.ReadFile` absorbs 4 hand-rolled read loops
  (incl. edge truth loader); forecast CSV moved onto obscsv aliases;
  duplicate hourly test deleted; canonicalStations → market.AllStationCodes;
  climate seasonWindow extracted. −65 prod LOC.
- Infra track: kalshi.LoadPEM shared by tape+peek; tape writeFrame helper;
  OpenMeteo defaults single-sourced in config (poller now fail-fast).
  **Live-recorder bug fixed**: client-Timeout misclassified as shutdown
  (aborted whole fetch cycle unretried, uncounted — regression test added);
  plus hardening: runRediscovery could outlive jrn.Close (child errgroup
  now scopes both).
- **All 5 Phase-1b OPERATOR-CALLs resolved**: Market 16 fields deleted/10 kept,
  Station 5 fields deleted, OpenMeteoLocation.Series deleted, KalshiFrame.ID
  deleted (ADR-0015 reserves Seq/Sid only — reservation comment added),
  Posterior.PriorWeight kept (ADR-0014 freeze). Boot schema → v2 (station
  entries slimmed; OPERATIONS.md updated).
- Net: prod 11,198→11,194 (deletions paid for decomposition), test +250
  (10,279→10,529). `make all` green; `make deadcode` == Phase-1 kept table.

**Next:** operator commit; Phase 2b candidate queued (shared civil-day/
running-extremum core for fit.go vs hourly.go + the openmeteo Add(24h)
DST discrepancy — needs cross-package golden tests first).

---

## 2026-07-03 ~18:15 ET — reduction Phase 1b: residual sweep clean (interactive session)

**Done (same uncommitted tree):** residual pass over everything `deadcode`
can't see (consts/types/fields/config keys/flags/test helpers). Only 3
clear-cut deletions remained (−8 LOC: dead `kalshi.BaseURL` const, unread
`DailyTruth.Date` field, dead `maxB` computation in ResolveWithSiblings) —
3 of 5 sweep areas reported honest empty outcomes. Fresh `make deadcode`
output now equals the Kept table exactly. **5 judgment items escalated as
OPERATOR-CALL** (see `reduction/01-deadcode.md` §Phase 1b): Market's 13
unread ABI-mirror fields, Station registry fields, `OpenMeteoLocation.Series`
config key, `KalshiFrame.ID`, `Posterior.PriorWeight`. `make all` green.
Final: **11,198 prod / 10,279 test LOC** (from 11,739/10,771 pre-sweep).

**Next:** operator decides the 5 OPERATOR-CALLs + commit + ADR-0015.

---

## 2026-07-03 ~17:30 ET — reduction Phase 1 executed: dead-code sweep (interactive session)

**Done (working tree, UNCOMMITTED — same tree as the 16:45 polish pass):**
- Operator authorized Phase 1 of the reduction protocol
  (`reduction/00-baseline.md` had been waiting since 2026-05-13).
  Full record: **`reduction/01-deadcode.md`**.
- **−1,025 LOC** (−533 prod / −492 test), `make all` green throughout.
- Headline: `internal/kalshi` order-management/portfolio REST surface
  deleted (−888 LOC; arbhunter carry-over, zero production callers —
  survivors: `DiscoverOpenTickers`, `GetMarketsPage`, WS, auth, retry
  core). **ADR-0014's no-order-placement hard rail is now structural.**
- Also: dead fee wrappers (F24), `StationByLocation`/`BySeries` (F26),
  `Resolution.Width`, orphaned types/consts/response wrappers + their
  single-subject tests.
- Two baseline hypotheses **refuted by verification**: env multi-line
  branch is LIVE (the .env inline RSA PEM depends on it — baseline §4
  predates the PEM move); `Book.BestNoBid` anchors the ask/bid parity
  property test. Both kept; documented in 01-deadcode.md.
- `make deadcode` added (advisory; kept-list in 01-deadcode.md is the
  diff baseline).

**Next:** unchanged (operator commit + ADR-0015 decision, then queue).

---

## 2026-07-03 ~16:45 ET — review + polish pass; ADR-0015 proposed (interactive session)

**Done (working tree, UNCOMMITTED — operator review + commit pending):**
- Multi-agent review of the whole repo: 47 raw findings → 36 deduped →
  **22 adversarially confirmed** (2 refuted, 12 real-but-not-worth-it,
  e.g. map→array book rejected as fighting a deliberate design). All 22
  applied across 27 files; `make all` green (lint+clockcheck+vet+race+build).
- Durability/correctness: journal writer no longer nil-panics on failed
  size-rotate re-open; write/flush/fsync failures now counted
  (`WriteErrorsTotal` + optional `OnWriteError` hook — separate from
  ADR-0011 drop accounting); gzip-on-rotate fsyncs before removing the
  plain copy; openmeteo client-Timeout no longer misclassified as ctx
  cancel (was: never retried + silently abandoned the rest of the tick);
  replay reader no longer aborts the whole run on one oversized line or
  crash-truncated .gz; truth CSV loader fails loud on parse errors and
  no longer panics on short rows; NO-side entry edge and PnL now price
  the NO leg identically (1¢ divergence gone).
- Reuse/simplify: new `internal/calibration/obscsv` (one header-aware
  obs reader replaces 4 divergent parsers; calibrate-bayesian's
  positional parser eliminated), new `internal/jsonatomic` (2 atomic
  writers collapsed), `market.StationTZ` (3 copy-pasted tz maps deleted),
  production-dead `pollModelSet` deleted, `Scan()` split into pure
  helpers (applyMOS/applyBayes/applyClimate/scoreBacktestRow) each with
  unit tests; 9 end-to-end Scan tests unchanged as characterization gate.
- Perf: scan hot path stdlib json → sonic; Sscanf → ParseFloat;
  collectSiblings decodes ticker-only; ApplyFrameToBooks decodes each
  book frame once — bench: 719.5→376.1 ns/op, 8→4 allocs/op.
- Net: production code +800/−554 tracked (+~150 LOC shared packages),
  tests +452/−110 plus new property/regression suites.
- **ADR-0015 PROPOSED** (`docs/decisions/0015-…`): deterministic matching
  engine + fill simulator on the recorded tape — doubles as the G-B
  "capturable EV at displayed size" instrument. Data-plane seam map
  captured in the ADR; key gap found: `KalshiFrame.Seq/Sid` decoded but
  never read → no sequence-gap detection anywhere (engine must add it).

**Next:** operator reviews + commits this tree; accepts/rejects ADR-0015;
queue otherwise unchanged (hand-audit script top; detectors; universe
wiring; prod redeploy remains the calendar long pole).

---

## 2026-06-10 ~13:30 ET — cleanup run (interactive session)

**Done:**
- `92eaeb4` fix(kalshi): **recorder starvation fix LANDED** — reviewed and
  committed the parallel session's working-tree edits (reconnect-time
  `RefreshTickers`, `retry.ResetAfter`, OPERATIONS §15 postmortem).
  Queue item "universe → cmd wiring" is now **UNBLOCKED**.
- `ec07cfb` chore(lint): errorlint + gofumpt + gosec — `make lint` green.
- CI/build hygiene (this commit): `go mod tidy` (CI tidy-check was red —
  x/tools is a direct dep via tools/clockcheck); CI now pins
  golangci-lint **v2.12.2** (was v2.1 — 11-minor drift vs local) via
  action v9; Go version sourced from go.mod; **clockcheck now runs in CI**
  (was local-only — ADR-0012 was unenforced on push).
- Docs accuracy pass (this commit): README intro/status rewritten to the
  post-pivot record (fwd wk1 row added, G-A/B/C gates listed); ROADMAP
  pivot banner + Phase 0.5 closed; CHANGELOG **0.5.0 cut** (backfilled
  ~7 weeks / 123 commits, Unreleased reset); stale local branches pruned
  (`claude/loving-tesla-c0615a`, `audit/2026-05-13` — both fully merged).
- govulncheck: **no vulnerabilities**. `go mod verify`: ok.
- **CI discovery (post-push):** GitHub Actions has **never run** on this
  repo — every run since the 2026-04-18 scaffold push ends in
  `startup_failure` with zero jobs planned. Actions is enabled, the YAML
  parses, all action versions valid → the failure is org-level (billing /
  spending limit on the private nolmelabs repo; hosted runners refuse to
  start). The ci.yml fixes above are correct but moot until that's fixed.
  **OPERATOR ACTION:** nolmelabs org → Settings → Billing & plans →
  verify payment method / Actions spending limit, then re-run any
  workflow. Until then `make all` + the pre-commit hook are the only
  enforcement; treat green CI as non-existent, not passing.

**Recorder status:** fix is committed on main; **prod redeploy still
pending** — operator action per OPERATIONS §15 open items (journalctl
forensics on the 05-20→24 window, discovery-freshness check at boot,
cross-compile + redeploy + restart unit). G-A clock does not start until
wide recording runs healthy in prod.

**Next (queue order unchanged from run #2):**
1. (b)(ii) hand-audit script (top item).
2. (d) detectors in `internal/structscan` on existing weather tape.
3. universe → cmd wiring + config widening — unblocked as of `92eaeb4`.
4. Operator: NY-eligibility confirmations for expansion series; prod
   redeploy (the calendar long pole).

---

## 2026-06-09 ~20:55 ET — manual run #2 (interactive session)

**Done:**
- `23edc77` feat(universe): catalog discovery + structural classification +
  fail-closed eligibility gate. Public `/events`+`/series` (fee params from the
  exchange), MECNET-authoritative mutex detection, conservative exhaustive-tiling
  check, ladders/complements, Unknown groups kept for no-silent-truncation
  reporting. 14 tests green on real-payload fixtures. No internal/kalshi import —
  builds independently of the WS repair.
- **Queue reorder:** took (c) discovery layer before (b)(ii) hand-audit — universe
  gates the G-A calendar (the long pole); the hand-audit only gates *trust* in
  G-B numbers later. (b)(ii) is now the top queue item.

**Recorder status:** parallel session now editing cmd/tape/main.go, ws.go(+tests),
retry.go(+tests), OPERATIONS.md, CLIENT.md — fix + postmortem in progress; tape
at 14.8MB today and climbing. Still theirs; do not touch.

**Next (in order):**
1. (b)(ii) hand-audit script: independently re-derive ~20 random realized-PnL
   rows vs edge-scan CSV (see kick-off entry below for spec).
2. (d) detectors in `internal/structscan` — CAN START NOW on existing weather
   tape: KXHIGH/KXLOWT events are MECNET mutex sets, so bracket-sum and
   complement detectors have real input via internal/replay. Detector order and
   Opportunity record schema: kick-off entry below.
3. Wire universe → cmd (discovery CLI subcommand or tape integration) + widen
   config — **BLOCKED until the recorder session's commits land** (cmd/tape and
   internal/kalshi are theirs right now).
4. Operator action queued: confirm NY eligibility for candidate expansion series,
   then add them to the gate allowlist (gate fails closed until then).

---

## 2026-06-09 ~20:35 ET — manual kick-off (interactive session)

**Done:**
- `52e7cf4` fix(edge-scan): Ts→TS missed call site — HEAD compiles again
- `c79f4e0` docs(research): proposal 09 landed (incl. §0 epistemic grading)
- ADR-0014 accepted + index row + STRATEGY.md pointer + this BUILDLOG (this commit)
- Fee-source probe: **public API exposes per-series `fee_type`/`fee_multiplier`**
  (`GET /trade-api/v2/series/KXHIGHNY` → `"fee_type":"quadratic","fee_multiplier":1`,
  no auth needed). Universe svc reads fees from the API; the fee-schedule PDF is
  unnecessary (and is 429/Vercel-challenged headless anyway). Series payload also
  carries `category`, `settlement_sources`, `contract_terms_url`; market payload
  carries `floor_strike`, `market_type`, `event_ticker`, `liquidity_dollars` —
  enough for event-group classification.

**Queue item (a) recorder — IN FLIGHT in a parallel session, do not duplicate:**
local `./bin/tape` capturing since 18:22 ET (15k+ frames, healthy sustained rate,
debug log at /tmp/tape-debug-*.log); no fix committed yet (branch
`claude/loving-tesla-c0615a` empty). Check `git log` + worktrees before touching.
Prod box (isobar-prod) presumably still down — redeploy only after the fix lands.
**As of 20:40 ET their session is editing `internal/kalshi/ws.go` + `internal/retry/retry.go`
directly in the MAIN working tree.** New-package work (queue items 1–3 below) does NOT
conflict: proceed, but scope builds/tests to your own packages (`go test ./internal/universe/...`
etc., not `./...`) while their edits are in flight, and never stage files you didn't author.

**Next (in order):**
1. (b)(ii) hand-audit script: independently re-derive ~20 random realized-PnL
   rows (trade → pairing → resolution → fee → PnL) from raw tape + CLI truth;
   report mismatches vs edge-scan CSV output.
2. (c) `internal/universe`: paginated discovery via public `/series` + `/markets`
   (verified live), event-group classification (exhaustive set / ladder /
   complement from `floor_strike`/`market_type`/`event_ticker`), per-series fee
   params, category enable flags. **NY-eligibility: no eligibility field in the
   public payload — needs an authed/empirical probe; the gate must fail closed
   (only series the operator has confirmed NY-served get enabled).**
3. (d) `internal/structscan` detectors over existing tape, in order:
   complement-pair → bracket-sum → ladder-monotonicity → deterministic-settlement
   (reuse `RunningExtremum` + hourly ASOS loader; build CLI-vs-ASOS
   settlement-certainty table from data/calibration/obs).
4. Widen config + G-A clock once (a) lands.

**Blockers:** none for code work; G-A/G-B calendars blocked on (a) landing.
