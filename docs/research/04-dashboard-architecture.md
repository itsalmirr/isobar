# Dashboard + real-time UI architecture (wave 1, agent 4)

*Raw research output. Synthesized conclusions live in `docs/research/SYNTHESIS.md`.*

## Ranked shortlist

### #1. Go stdlib HTTP + `html/template` + htmx + SSE + TradingView Lightweight Charts, embedded via `go:embed`

**Pros.** Single static binary. Zero Node toolchain, zero npm supply-chain surface. SSE fits the "server pushes ticks to browser" shape perfectly; [WebSockets vs SSE](https://ably.com/blog/websockets-vs-sse) benchmarks show the 3 ms latency gap is irrelevant for a one-operator dashboard ([dev.to](https://dev.to/polliog/server-sent-events-beat-websockets-for-95-of-real-time-apps-heres-why-a4l)). htmx is 14 KB, declarative, and composes with templates you already know ([htmx SSE extension](https://htmx.org/extensions/sse/), [Three Dots Labs — Live updates with Go, SSE, htmx](https://threedots.tech/post/live-website-updates-go-sse-htmx/)). [Lightweight Charts](https://github.com/tradingview/lightweight-charts) is the TradingView-built, Apache-2.0, 45 KB canvas library purpose-built for streaming financial data.

**Cons.** htmx swap granularity is per-fragment; raw tick streams at thousands/sec need batching on the server side (fine: batch every 100 ms). No SPA-style client state, so multi-pane state must be server-held.

### #2. Go REST+WS backend + Vite/React + Lightweight Charts (Freqtrade / Nautilus / OctoBot shape)

**Pros.** What every other OSS trading UI ships: [Freqtrade's frequi (Vue)](https://www.freqtrade.io/en/stable/freq-ui/), [Nautilus community admin (FastAPI+React)](https://github.com/Black101081/Nautilus-Web-Interface), [OctoBot octo-ui-2 (React)](https://github.com/techfreaque/octo-ui-2). Ceiling is unlimited.

**Cons.** Second toolchain (Node+Vite+TS), second build target, second deploy artifact. An interviewer sees a CRA app and shrugs — "React trading UI" is table stakes, not signal. [Hummingbot is abandoning Streamlit](https://github.com/hummingbot/dashboard) precisely because the UI layer became the maintenance tax.

### #3. Grafana + Prometheus (metrics) + tiny Go `/control` endpoint

**Pros.** Zero charting code, pre-baked alerting. A real Grafana+Prom+Freqtrade reference exists ([thraizz/freqtrade-dashboard](https://github.com/thraizz/freqtrade-dashboard)).

**Cons.** Grafana is read-only by design; start/stop of a strategy is not a Grafana primitive, so you end up building the control plane anyway. Two extra services to deploy — fails "operable by one person." Good as a *complement* later; bad as the primary UI.

**Rejected outright:** Bubble Tea TUI (can't screenshot); TinyGo+WASM (still "almost ready" in 2026); Datastar (compelling but htmx ecosystem is deeper today); NATS/Redis pub-sub (extra infra for 1-box).

## The recommendation

**Ship Option #1: a single Go binary exposing SSE + HTMX-rendered HTML, with TradingView Lightweight Charts hydrated from a `/stream/chart` SSE endpoint, state in an embedded SQLite (WAL mode), and Tailscale Serve for remote access.**

Why this wins for *this* project:

1. **Matches isobar's stated doctrine** — stdlib-first, no external deps beyond minimal, `internal/` only. React SPA violates every one.
2. **One binary, one process, one `systemd` unit.** The bot *is* the dashboard. Adding `internal/dashboard` + an SSE broker is ~500 LOC, not a platform migration. `go:embed` ships HTML/CSS/JS alongside Go code with `FROM scratch` Docker-ability.
3. **SSE is the right protocol** — server→client one-way; control is classic HTTP POST.
4. **Lightweight Charts is industry-standard** — Apache-2.0, 45 KB, purpose-built for streaming.
5. **SQLite WAL gives control plane and state store in one file** — unlimited concurrent readers, one writer, durable, zero ops.
6. **Interview optics.** "Single Go binary, SSE + htmx, Apache-2.0 TradingView charts, SQLite control plane, Tailscale-fronted" is a *sentence*. "React + Vite + Zustand + Tanstack Query + tRPC + Docker Compose" is not.

## File layout & dependencies

```
cmd/
  tape/main.go                   # existing recorder; add --dashboard flag
  dashboard/main.go              # standalone dashboard binary (optional)
internal/
  dashboard/
    server.go                    # http.ServeMux, routes, graceful shutdown
    sse.go                       # SSE hub: topic → []chan []byte, drop-on-slow
    control.go                   # POST /control/* handlers
    auth.go                      # bearer-token middleware + CSRF for POSTs
    views.go                     # html/template rendering
    state.go                     # read-only projections of bot state
  controlstore/
    store.go                     # modernc.org/sqlite + WAL pragma
    schema.sql                   # embedded via go:embed
    migrations.go
  busevents/
    bus.go                       # in-process fan-out: Publish(topic, event)
web/
  templates/ (layout, index, fragments)
  static/
    htmx.min.js                  # 14 KB, vendored
    htmx-sse.js                  # SSE extension
    lightweight-charts.standalone.production.js  # ~45 KB, vendored
    app.css                      # hand-written ~200 lines
    chart.js                     # ~100 lines SSE→series.update() glue
```

`go get` (full list): `modernc.org/sqlite` — pure-Go SQLite, no CGO. That's it. Everything else (SSE, templates, http, embed) is stdlib. htmx and Lightweight Charts are vendored static JS.

Routes:
```
GET  /                          index
GET  /stream/events             SSE: fills, reconnects, queue-full, logs
GET  /stream/chart/:bracket     SSE: {time, price, yes_bid, yes_ask, ensemble_p}
GET  /stream/portfolio          SSE: positions + PnL, 1 Hz
POST /control/source/:name      body: {enabled: bool}
POST /control/strategy/:name    body: {enabled: bool}
POST /control/recorder/:action  action in {start, stop}
POST /control/bot/:action       same; gated by ISOBAR_LIVE_ALLOW=1
```

## Four-week roadmap with gates

- **W1 — Read-only skeleton.** `internal/busevents` pub/sub; `internal/dashboard/server.go` with `/` and `/stream/events`; htmx + SSE wired; first tile: "coinbase WS: connected Xs ago, Y msgs/s". *Gate: browser shows live tick counters for 10 min, no reload.*
- **W2 — Charts.** Lightweight Charts hydrated from `/stream/chart/:bracket`; server batches at 10 Hz; overlay Kalshi yes-bid/ask vs ensemble `p`. *Gate: 6-hour session, smooth chart, flat memory (pprof).*
- **W3 — Control plane + SQLite.** `internal/controlstore` with WAL, `state(key,value,updated_at)` table; `/control/*` endpoints write SQLite; bot polls the table every 500 ms; bearer-token middleware. *Gate: toggle Coinbase off from browser → journal stops ≤1s → toggle back → reconnect event visible. Never reboots the process.*
- **W4 — Portfolio + hardening.** `/stream/portfolio` tile: positions, open orders, daily PnL; two-phase confirm on `/control/bot/*` (second POST with `confirm=<random_token_shown_in_UI>`) — prevents reload-replay from starting live trade; `ISOBAR_LIVE_ALLOW=1` env gate; Tailscale Serve config. *Gate: `make all` green, coverage ≥70%, binary size <20 MB.*

## Interview narrative (verbatim worth preserving)

> "The recorder is a single Go binary. It embeds its own operator dashboard — stdlib `net/http`, Server-Sent Events, htmx for the DOM, TradingView's Apache-licensed Lightweight Charts for the market panels, all assets `go:embed`'d into the binary. State and control flow through a WAL-mode SQLite file the bot polls every 500 ms, so toggling a data source or strategy is durable across restarts and can't be replayed by a browser reload. Live-trade endpoints are gated by an env var plus a two-phase confirm token. Remote access is Tailscale Serve, not a public bind. The whole dashboard is about 1500 lines of Go plus 300 lines of hand-written JS/CSS — no Node toolchain, no Docker Compose, no React, no Redis. One `systemd` unit, one port, one file to back up. I chose SSE over WebSockets because data flow is one-way and SSE re-uses HTTP auth and proxies; I chose htmx over React because the interaction surface is fragment swaps, not SPA state; I chose Lightweight Charts because it's the library TradingView uses, purpose-built for streaming financial data."

## Sources

- [Freqtrade freqUI docs](https://www.freqtrade.io/en/stable/freq-ui/)
- [Hummingbot dashboard](https://github.com/hummingbot/dashboard)
- [OctoBot octo-ui-2 (React)](https://github.com/techfreaque/octo-ui-2)
- [Three Dots Labs — Live updates with Go, SSE, htmx](https://threedots.tech/post/live-website-updates-go-sse-htmx/)
- [htmx SSE extension](https://htmx.org/extensions/sse/)
- [Lightweight Charts — TradingView](https://www.tradingview.com/lightweight-charts/)
- [Lightweight Charts GitHub](https://github.com/tradingview/lightweight-charts)
- [Lightweight Charts docs](https://tradingview.github.io/lightweight-charts/)
- [WebSockets vs SSE — Ably](https://ably.com/blog/websockets-vs-sse)
- [SQLite WAL documentation](https://sqlite.org/wal.html)
- [Grafana+Prometheus+Freqtrade dashboard](https://github.com/thraizz/freqtrade-dashboard)
- [Tailscale Serve](https://tailscale.com/docs/features/tailscale-services)
- [Go embed package](https://pkg.go.dev/embed)
- [kalshi-dash (Lightweight Charts example)](https://github.com/jsteng19/kalshi-dash)
