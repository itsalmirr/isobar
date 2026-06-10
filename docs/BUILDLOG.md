# BUILDLOG — structure-first build loop

Coordination journal between autonomous build runs (hourly scheduled task
`isobar-structure-first-build`) and interactive sessions. **Newest entry
first.** One terse entry per run: timestamp, done (commit hashes), next,
blockers. Trust this file over re-deriving state; never duplicate work a
parallel session marks IN FLIGHT.

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
