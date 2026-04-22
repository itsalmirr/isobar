# Glossary

> Terms used throughout the codebase and docs, defined once so we do not
> argue about them in PR comments.

## Market structure

**Bracket.** A discrete-outcome contract covering a range of a continuous
variable, e.g. `[65°F, 70°F)` for a weather market. Brackets in a series
are mutually exclusive and collectively exhaustive.

**Series.** A group of related markets with a common ticker prefix on
Kalshi. Example: `KXHIGHNY` = daily high temperature in NYC. Each series
contains many event tickers (one per day), each of which contains many
market tickers (one per bracket).

**Event.** A Kalshi grouping of markets that share a resolution time and
set. For weather, an event is `(series, date)`; the event's markets are
its brackets.

**Market.** The atomic tradable unit on Kalshi — a single YES/NO contract.

**Ticker.** The canonical Kalshi identifier for a market (e.g.
`KXHIGHNY-25APR18-B67.5`). We never substitute aliases.

**Tick size.** Minimum price increment (typically $0.01 on Kalshi).

## Pricing

**Mid.** `(bid + ask) / 2`. Only meaningful with a non-empty book on both
sides.

**Edge.** Expected profit per $1 of exposure after fees, assuming our
probability estimate is correct. Positive edge is necessary but not
sufficient — size, fill probability, and adverse selection matter too.

**Fair value.** Our model's central estimate of the true YES probability.
In the weather strategy, `fair = (members in bracket) / total members`.

**Maker fee / taker fee.** Kalshi's per-contract fee schedule; maker is
lower (and sometimes zero for inside-book quotes). Strategy docs always
assume worst-case taker fees for stop-loss calculations.

**Round-trip fee.** Fees paid to enter + exit a position. Always
conservative: assume taker on both sides unless we can prove otherwise.

## Signals & execution

**Signal.** A Phase 1+ interface: given a `MarketState`, emit zero or more
`Quote`s. Each strategy (e.g. `weather`, `btc-hourly`) is one
implementation.

**Quote.** An intent-to-quote: `(ticker, side, price, size)`. Transformed
by `exec` into a Kalshi `CreateOrderRequest` with deterministic client
order ID.

**Maker-only.** Orders placed with `PostOnly: true`. The exchange rejects
rather than crosses. Non-negotiable default.

**Cancel-before-replace.** Modifying a price or size means sending
`CancelOrder` → awaiting confirmation → sending a fresh `CreateOrder`.
No "modify in place".

**Post-only reject.** Kalshi API response when a post-only order would
have crossed. Treated as a normal "no fill" outcome, not an error.

## State & data

**Warm-start.** After a crash or graceful restart, the bot loads its
prior state from SQLite WAL + Kalshi REST reconciliation before placing
any new orders. Opposite of cold-start.

**Journal.** The on-disk append-only JSONL stream produced by
`internal/journal`. One file per source, per rotation window.

**Envelope.** The journal's on-disk record type: `{t, m, src, k, p}` where
`p` is the raw source payload. Stable schema.

**Frame.** The in-memory type passed to `journal.Write`. Contains the
same fields as the envelope plus a `Stamp` pair.

**Raw frame.** Exact bytes received from a source (WS message or REST
body), unparsed. We never decode on the recorder hot path.

**Replay.** Reading a journal back through a decoder + analyzer,
producing the same downstream state that would have been seen live. The
foundation of Phase 0.5 (and all future strategy backtests).

## Timing

**Wall time.** `UnixNano`, UTC, cross-process comparable. The only
timestamp we compare across sources.

**Monotonic time.** Nanoseconds since process start. For in-process
interval measurements only.

**Stamp.** A `(Wall, Mono)` pair sampled from a single `time.Now()` call.
Defined in `internal/clock`.

**Lead-lag.** The distribution of `(t_kalshi_reprice - t_signal_input)`
for paired events (e.g. an Open-Meteo ensemble refresh and the resulting
Kalshi reprice). Positive values mean Kalshi lags, which is the typical
edge-direction. For BTC the lead compressed to sub-second after
Coinbase's Kalshi integration in Jan 2026 — that signal is dead.

**Staleness.** Elapsed wall time since the most recent frame for a
given market. A precondition cap is enforced before signal emission
(default 30s for WS-derived state).

## Risk & process

**Cap.** A numeric limit enforced before each order (per-ticker,
per-series, portfolio, max-order-size).

**Kill switch / halt file.** `./halt` — file whose presence halts new
order submission. The simplest possible circuit breaker.

**Hard halt.** Process exits non-zero; requires external restart.

**Soft halt.** Process stays up, refuses to act; cleared by operator.

**Reconciliation.** Comparing our in-memory state against Kalshi's
authoritative state (via REST). Required on startup, re-run every 60s in
steady state.

**Bankroll.** Total USD balance in the Kalshi account. Sizing rules
reference it rather than a hardcoded dollar figure.

**Drawdown.** Peak-to-trough PnL loss over a window. Daily drawdown is
the primary steady-state risk metric; 14-day drawdown gates Phase 3
scale-ups.

## Tooling

**Tape.** `cmd/tape`, the Phase 0 recorder.

**Analyze.** `cmd/analyze`, the Phase 0.5 edge-measurement tool. MVP
today reports frame counts, gap distribution, monotonicity; orderbook
replay + modeled-vs-implied edge land with calibration (W10).

**Peek.** `cmd/peek`, ops-only Kalshi REST probe. Enumerates candidate
series names and market statuses to confirm catalog assumptions when
Kalshi adds or renames series (used in W1 to discover the asymmetric
`KXHIGH*` / `KXLOWT*` naming).

**Bot.** `cmd/bot`, the Phase 1+ live trader.

**Arbhunter.** Sibling project we're reusing code from (specifically the
Kalshi REST client + signer). The strategy differs: arbhunter was
BTC-only and aggressive; isobar is weather-first and maker-only.

## Organizational

**Phase.** A roadmap stage (0, 0.5, 1, 2, 3). Each phase has written
exit criteria and cannot be skipped.

**Decision doc.** A dated Markdown file under `docs/decisions/`
recording a non-obvious choice — context, decision, alternatives,
reversal cost. Mandatory for strategy- and risk-level changes.

**Postmortem.** Incident report under `docs/postmortems/`. Required
within 48h of any unexpected money-losing event or reconciliation
divergence.
