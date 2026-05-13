# 0003 — Dashboard = stdlib Go + htmx + SSE + Lightweight Charts + SQLite WAL

**Status:** Accepted
**Date:** 2026-04-18
**Authors:** isobar team

## Context

The operator dashboard must:

- Start/stop the recorder + individual sources + individual strategies
  while the bot is running.
- Show real-time charts (market prices, edge signals, PnL, fill rates,
  reconnects, ensemble-vs-market overlays).
- Display portfolio state (positions, open orders, daily PnL).
- Be operable by one person without a Kubernetes cluster.
- Look like **signal, not hobbyism**, to a quant/eng interviewer.

Research (`docs/research/04-dashboard-architecture.md`) surveyed the
open-source trading-bot dashboard space (Freqtrade's `frequi`, Hummingbot,
OctoBot, Nautilus, Jesse) and the contemporary Go-web ecosystem.

## Decision

We build the dashboard as **a single Go binary** exposing:

- Stdlib `net/http` + `html/template` + Server-Sent Events (flushable
  writer).
- **htmx** (14 KB, vendored) + htmx SSE extension for DOM fragment
  updates.
- **TradingView Lightweight Charts** (45 KB, Apache-2.0, vendored) for
  streaming market-data charts.
- **`modernc.org/sqlite`** (pure-Go, no CGO) with WAL mode as the
  control-plane + state store.
- All static assets bundled into the binary via `go:embed`.
- **Tailscale Serve** for remote access — never a public bind.

Live-trade endpoints are gated by (a) `ISOBAR_LIVE_ALLOW=1` env var
and (b) two-phase confirm token shown in the UI. Reload-replay cannot
start live trading.

**Total added dependency: one** — `modernc.org/sqlite`.

## Alternatives considered

- **Go backend + React/Vite frontend** (Freqtrade / OctoBot shape).
  Rejected — violates isobar's stdlib-first doctrine, introduces a
  Node toolchain, and "React trading UI" is table stakes rather than
  interview signal. Hummingbot is actively migrating *away* from a
  similar React/Streamlit shape because the UI layer became a
  maintenance tax.
- **Grafana + Prometheus.** Rejected as primary UI — Grafana is
  read-only; the operator controls (start/stop/toggle) would still
  need to be built separately. Good fit later as a *complementary*
  metrics dashboard.
- **TUI (Bubble Tea / tview).** Rejected — can't candlestick-chart
  in a terminal, and can't screenshot for a README.
- **WebSockets instead of SSE.** Rejected — data flow is server→browser
  one-way; SSE re-uses HTTP auth and proxies; the 3ms latency gap
  doesn't matter for an operator dashboard.
- **NATS / Redis pub-sub for internal fan-out.** Rejected — extra
  infrastructure for a single-box deployment. `internal/busevents`
  in-process channel fan-out is simpler.
- **TinyGo WASM canvas.** Rejected — not production-ready in 2026.

## Consequences

Good:
- One `systemd` unit, one port, one file to back up.
- Binary size stays <20 MB; no Docker Compose.
- Interview sentence writes itself: *"stdlib Go + SSE + htmx +
  TradingView Lightweight Charts + SQLite WAL + Tailscale Serve,
  all `go:embed`'d."*
- Control plane is durable (SQLite) — operator toggles survive
  restarts; browser reload can't re-fire live trades.

Bad:
- htmx is less well-known than React; onboarding a second developer
  requires learning it (small cost: ~1 hour).
- Server-held state for multi-pane views requires more discipline
  than SPA-style client state.

Neutral:
- TradingView's Lightweight Charts library is Apache-2.0 — license-
  compatible, but if that ever changed we'd need to swap (uPlot or
  ECharts are comparable fallbacks).

## Reversal cost

Medium. Swapping Lightweight Charts for another library is ~1 day;
swapping htmx for plain vanilla JS is ~1–2 days; swapping SSE for
WebSockets is ~1 day. Walking the whole stack back to React + Vite
would be a ~1-week rewrite — that's the scenario we're avoiding.

Canary: if dashboard development exceeds 2 weeks (we planned 4, each
"dashboard week" has a hard gate), we reconsider.

## References

- `docs/research/04-dashboard-architecture.md` — full survey and
  reasoning.
- `docs/ARCHITECTURE.md` §"Future (Phase 1+) shape" — directory layout.
- Scheduled for W11 per the 12-week plan.

---

## Note (2026-05-13) — `ISOBAR_LIVE_ALLOW` deferred

The Decision section above names `ISOBAR_LIVE_ALLOW=1` as the env-var gate for `/control/bot/*` endpoints. Phase 0 audit (2026-05-13) found the variable is unread anywhere in the Go codebase; the dashboard, `cmd/bot`, and `internal/exec` packages this ADR depends on are themselves not yet implemented.

The specific env-var name in this ADR is therefore non-binding and the live-trade gating mechanism is deferred to the ADR that lands `cmd/bot` (anticipated slot 0013+). See `audit/live-allow-trace.md` for the trace and decision.
