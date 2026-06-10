# BUILDLOG — structure-first build loop

Coordination journal between autonomous build runs (hourly scheduled task
`isobar-structure-first-build`) and interactive sessions. **Newest entry
first.** One terse entry per run: timestamp, done (commit hashes), next,
blockers. Trust this file over re-deriving state; never duplicate work a
parallel session marks IN FLIGHT.

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
