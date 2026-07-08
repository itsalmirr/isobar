# `ISOBAR_LIVE_ALLOW` — forensic trace and recommendation

**Decision**: STRIP from docs and `.env.example` now. Re-introduce in a dedicated ADR when `cmd/bot` + `internal/exec` land.
**Date**: 2026-05-13
**Context**: Phase 0 audit ([audit/00-map.md](00-map.md) §7) found that `ISOBAR_LIVE_ALLOW` is documented as a live-trade gate in two places but never read in any Go file. A documented kill-switch that does nothing is worse than no kill-switch.

---

## 1. Citations

Every reference to `ISOBAR_LIVE_ALLOW` in the repo:

- **[docs/research/04-dashboard-architecture.md:81](../docs/research/04-dashboard-architecture.md:81)** — listed inline in the planned route table:
  > `POST /control/bot/:action       same; gated by ISOBAR_LIVE_ALLOW=1`

- **[docs/research/04-dashboard-architecture.md:89](../docs/research/04-dashboard-architecture.md:89)** — W4 milestone description:
  > "two-phase confirm on `/control/bot/*` (second POST with `confirm=<random_token_shown_in_UI>`) — prevents reload-replay from starting live trade; **`ISOBAR_LIVE_ALLOW=1` env gate**; Tailscale Serve config."

- **[docs/decisions/0003-dashboard-stack.md:38](../docs/decisions/0003-dashboard-stack.md:38)** — Decision section:
  > "Live-trade endpoints are gated by (a) `ISOBAR_LIVE_ALLOW=1` env var and (b) two-phase confirm token shown in the UI. Reload-replay cannot start live trading."

- **[audit/00-map.md:251,393](00-map.md:251)** — Phase 0 finding (reproduces the gap; not a separate doc-author claim).

`.env` does **not** contain `ISOBAR_LIVE_ALLOW` (verified by grep, did not cat). `.env.example` does not contain it either. The gate is documented in two Markdown files and lives in zero `.go` or `.env*` files.

## 2. Intended semantics

The two doc sites together describe `ISOBAR_LIVE_ALLOW=1` as a **process-wide hard prerequisite for `POST /control/bot/*` endpoints** — specifically the live-trade start/stop control plane in the planned dashboard. It is:

- **Not** a per-station or per-strategy gate
- **Not** a kill-switch with graceful drain ([docs/RISK.md L0](../docs/RISK.md) reserves `./halt` for that)
- **Not** a staging-vs-prod separator (Tailscale Serve handles network exposure)

The intended composition is *defense in depth*: env var (operator-set, must survive a deliberate `export`) **plus** a UI-shown two-phase confirm token to defeat browser-reload replay (0003-dashboard-stack.md:38-40; 04-dashboard-architecture.md:89). The semantics map to: "operator declared, at shell-launch time, that this process is allowed to send orders; even then a per-action human-in-the-loop token is required."

## 3. Current state

The gate has **no callsite and no place to plug in**. Confirmed absences:

- No `cmd/bot`, `cmd/trader`, `cmd/dashboard` (verified by `ls`)
- No `internal/dashboard`, `internal/exec`, `internal/risk`, `internal/signal`, `internal/controlstore`, `internal/busevents` — all six packages named by the dashboard ADR and Phase 1 roadmap are missing
- No `os.Getenv("ISOBAR_LIVE_ALLOW")` anywhere; the only `os.Getenv` calls across `internal/` and `cmd/` are `ISOBAR_DATA_DIR`, `ISOBAR_LOG_LEVEL`, and the `env.Load` self-shadow check ([internal/config/config.go:153,160](../internal/config/config.go); [internal/env/env.go:24](../internal/env/env.go); the calibrate-* mains)
- [internal/kalshi/client.go:244-245](../internal/kalshi/client.go) — `kalshi.Client.CreateOrder` exists but is referenced only from `client_test.go` (`:95, :123, :502`). No `cmd/*/main.go` imports the symbol; `cmd/tape/main.go:142` only uses the client for read-only REST in the rediscovery loop
- The L0 `./halt` kill-switch in [docs/RISK.md:30-43](../docs/RISK.md) is also unimplemented — grep for `"./halt"`, `HaltFile`, `halt-file` returns nothing in non-doc code. So **two** advertised gates have zero implementation

Where the gate *would* naturally live if it existed: a `func mustLiveAllowed() bool { return os.Getenv("ISOBAR_LIVE_ALLOW") == "1" }` guard at the entrypoint of a future `internal/exec.KalshiExec.Submit(ctx, order)` method (docs/ROADMAP.md:144-147), called from a future `cmd/bot/main.go` between `internal/signal.Tick` and `internal/risk.Gate` (docs/RISK.md:81-82). None of those exist.

## 4. Decision matrix

| | Wire it up now | Strip from docs + .env |
|---|---|---|
| **Files changed** | New: `internal/exec/{exec.go, exec_test.go}` (or `internal/risk/live_gate.go`), `cmd/bot/main.go` skeleton, `.env.example` line. | Edit `docs/research/04-dashboard-architecture.md:81,89`. ADR-0003 is immutable per [docs/decisions/README.md:7-8](../docs/decisions/README.md) policy — leave intact, add forward note. No code change. |
| **Tests required** | Property test: gate=false ⇒ zero `CreateOrder` calls regardless of signal/risk state; gate=true + halt file ⇒ zero calls (composes with L0); gate flipping mid-process is honored on the next tick. Mock around `CreateOrder` since no `exec` interface exists yet. | None. Possibly a CI lint asserting `ISOBAR_LIVE_ALLOW` no longer appears in mutable docs (cheap, optional). |
| **New decision artifacts** | An ADR specifying composition order (env vs `./halt` vs `--live` flag vs two-phase token). Without it we are inventing API on a doc fragment. | A short forward note appended after ADR-0003 in `docs/decisions/README.md` recording the deferral. |
| **Risk** | Premature commitment to one mechanism before the trade entrypoint exists. The W11 dashboard ADR may want a richer model (e.g., env + sqlite-stored operator flag, so reboots don't silently re-enable). | Loses the visible signal that the team has thought about live-trade gating. Mitigated by RISK.md L0–L4 already documenting the full hierarchy. |
| **Time** | ~0.5 day code + tests, but pollutes the calibration sprint. | ~15 min of doc edits. |

## 5. Recommendation

**Strip `ISOBAR_LIVE_ALLOW` from docs now. Re-introduce it in a dedicated ADR when `cmd/bot` + `internal/exec` land.**

Evidence:

- [ADR-0009](../docs/decisions/0009-mos-canary-fired-escalate-calibration.md) and [ADR-0010](../docs/decisions/0010-t-minus-3h-bayesian-update.md) put the project squarely on a calibration-validation path with a Phase 0.5 v3 report blocking any Phase 1 trade work.
- The canary forecasts ≥4 weeks of calibration before a gate re-eval (ADR-0009:161-164).
- ROADMAP Phase 1 exit criteria require `internal/exec`, `internal/risk`, and `internal/signal` packages that do not exist.

A documented gate with zero callsite *and* a sibling unimplemented gate (`./halt`, docs/RISK.md:30-43) creates exactly the false-safety surface the audit was right to flag. The honest fix is to make docs match code today and re-add both gates atomically in the ADR that introduces their callsite — the only place we can actually unit-test "gate=false ⇒ no `CreateOrder`".

Stripping now also de-risks the W11 dashboard ADR from being constrained by a sentence written in a research note before the trade entrypoint had a shape.

## 6. Action

1. Edit [docs/research/04-dashboard-architecture.md:81,89](../docs/research/04-dashboard-architecture.md) to remove the env-var name and replace with `"live-trade gating mechanism TBD in the ADR that lands cmd/bot"`.
2. Leave ADR-0003 intact (immutable per policy). The Decision section's mention of `ISOBAR_LIVE_ALLOW` becomes a historical consequence superseded by a future ADR.
3. Add a one-line "**Note (2026-05-13)**" addendum at the END of `docs/decisions/0003-dashboard-stack.md` (not inside the immutable Decision block — appended below) stating: "Live-trade gating mechanism deferred to the ADR that lands `cmd/bot` + `internal/exec`. This ADR's specific gate names are non-binding."
4. Update `audit/00-map.md` to note the resolution.

A future ADR (slot 0013+ after journal-backpressure 0011 and calibration-eval 0012) will define the actual gate.
