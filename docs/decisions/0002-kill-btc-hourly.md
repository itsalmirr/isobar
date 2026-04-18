# 0002 — Kill BTC-hourly, delete `internal/coinbase`

**Status:** Accepted
**Date:** 2026-04-18
**Authors:** isobar team

## Context

The `arbhunter`-descended BTC-hourly thesis assumed that Kalshi's
`KXBTCD` hourly contracts lag Coinbase spot by tens of seconds on
decisive moves, allowing a maker-only execution to capture fees plus
a small directional edge. This was plausible in 2022–2023.

Wave-1 and Wave-2 research converged on three reasons to abandon it:

1. **Venue coupling.** Coinbase launched their prediction-markets
   product *on Kalshi infrastructure* in January 2026 (see
   `docs/research/01-kalshi-landscape.md`, CoinDesk citation). This
   compresses the lead-lag to well under one second because flow is
   now cross-posted by the same firm.
2. **Latency floor.** Our Go bot runs from a cloud VM, accessing Kalshi
   via REST. Round-trip latency is 10–50 ms. Any competitor with
   co-located infrastructure + FIX access is 1–2 orders of magnitude
   ahead of us.
3. **Fee reality.** The 6¢ raw edge the original thesis assumed is
   inside Kalshi's fee band at typical P levels, and the lead would
   have to be ≥5s sustained to pay for itself net of fees.

Keeping the Coinbase recorder "just in case" carries:

- A `coder/websocket` dependency surface we still maintain.
- Disk cost (journal writes) ongoing.
- A goroutine + config block + tests we keep compiling.
- Mental overhead — the existence of a dead code path invites "maybe we
  should try X" temptation.

## Decision

We **delete `internal/coinbase`** entirely (`ws.go`, `ws_test.go`),
remove `startCoinbase` from `cmd/tape/main.go`, drop the `Coinbase`
struct from `internal/config`, strip the `coinbase:` block from
`config/config.example.yaml`, and update every doc that referenced
Coinbase.

This is a **hard kill**, not a feature flag.

## Alternatives considered

- **Keep recording, don't develop the signal.** Rejected — the cost of
  carrying dead code compounds; we don't have separate budget for
  "passive infrastructure hygiene".
- **Measure lead-lag in Phase 0.5 before killing.** Rejected —
  multiple independent research reports triangulated on the same
  verdict; measurement would only confirm and cost a month. The
  research is our measurement.
- **Keep `internal/coinbase` as a reusable public-WS template** for
  future adapters. Rejected — the kalshi subscriber shape
  (`SubscriberOptions` / `Run` / reconnect-on-error) combined with
  the new `internal/retry` already gives us the template. Any future
  public-WS adapter can follow the same shape without coinbase as
  scaffolding.

## Consequences

Good:
- ~300 LOC removed (package + tests). Simpler mental model.
- Test suite gets faster (no WS mocks for coinbase).
- Focus concentrated on the two signals that have a real edge path.

Bad:
- If a future signal *does* need crypto-market correlates (e.g. for
  KXFED rate-cut markets that correlate with BTC), we re-add. The
  `internal/retry` package makes that re-addition cheap (one
  subscriber file, no reconnect reinvention).

Neutral:
- Historical tape data that was recorded under the Coinbase source is
  preserved in `data/coinbase/…` on whatever machine was running it.
  We don't delete recorded journals; we only delete the recorder.

## Reversal cost

Low. Resurrecting the Coinbase WS subscriber is a ~200-LOC file that
follows the Kalshi pattern. Git history preserves the original.

Canary: none. This is a strictly additive regret test — if a future
strategy demonstrably needs crypto correlates, we rebuild.

## References

- `docs/research/SYNTHESIS.md` — decision context.
- `docs/research/01-kalshi-landscape.md` — Coinbase/Kalshi integration.
- `docs/research/05-literature-review.md` — latency analysis.
- Git commit `4258365` — implementation of this decision.
