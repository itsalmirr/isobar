# 0015 — Deterministic matching engine + fill simulator on the recorded tape

- **Date**: 2026-07-03
- **Status**: Proposed
- **Decider**: Solo operator (pending acceptance in a live session)
- **Numbering note**: ADR-0014 reserved 0015+ for audit proposals "if/when
  they land"; none have, so this ADR claims 0015.

## Context

Two forces converge on the same artifact.

**The gate needs it.** ADR-0014's G-B gate is pre-registered as "≥25
executable structural instances/week · net ≥2¢ after fees **at displayed
size** · median persistence ≥3s · **capturable EV** ≥$10/week". Every
bolded term is a fill question, not a detection question. The current
backtest prices trades against trade prints only (`internal/edge/scan.go`
reads `trade` frames; `cmd/edge-scan` documents "does not simulate maker
fill probability" as a known limitation). Measuring *capturable* EV
honestly requires replaying the recorded book and matching hypothetical
orders against displayed liquidity under an explicit latency model.
Without that, G-B numbers are aspirational.

**The portfolio needs it.** The repo's engineering record (recorder →
journal → replay → calibration → gated verdicts) reads as serious
infrastructure, but the canonical trading-systems artifact — a limit
order book and matching engine with published invariants and latency
numbers — is absent. Building it *on the real tape* rather than
synthetic input is the differentiator over every tutorial LOB.

**What the data plane already provides** (verified against source
2026-07-03; full seam map in the session record):

- Append-only journal with envelope `{t, m, src, k, v, p}` —
  `internal/journal/journal.go:79-87`. `t` (wall unix-ns UTC) is the only
  cross-file ordering axis; `m` resets per process and must not be
  compared across files.
- A tape-ingestion surface that is already the "shared input layer for
  any future offline tool": `replay.ForEachFrame` / `ListJournals` / `Open`
  (`internal/replay/reader.go`).
- A book-reconstruction kernel with a snapshot-first lifecycle state
  machine: `replay.ApplyFrameToBooks` (`internal/replay/orderbook.go`).
- Three recorded frame types (`orderbook_snapshot`, `orderbook_delta`,
  `trade`) whose JSON field names are the de-facto ABI (frozen in §Feed
  contract below).

**What the tape does NOT contain** — these gaps dictate the design:

1. **No order-level events.** Kalshi's public WS carries aggregated
   per-level deltas (`price_dollars`, signed `delta_fp`, `side`), not
   add/modify/cancel with order IDs. Queue position is unrecoverable
   from the tape; a fill simulator must synthesize it conservatively.
2. **No usable sequencing.** `KalshiFrame.Seq`/`Sid` are decoded but read
   by no code path (`internal/replay/kalshi.go:15-16`); journal drops
   under backpressure (ADR-0011) leave no gap marker in the tape. A
   dropped delta silently corrupts a reconstructed book today.
3. **Float-approximate quantities.** `Book` is `map[int]float64` with
   `+=` delta accumulation and a float `<= 0` deletion cutoff. Exact
   resting depth needs integer arithmetic.
4. **Resubscribe stub snapshots** (`market_id=""`) that would wipe a book
   to empty if applied naively (OPERATIONS §WS notes).
5. **Non-monotonic wall time.** NTP steps can move `t` backwards;
   `cmd/analyze` counts such regressions rather than assuming order.

## Decision

Build a deterministic matching engine and fill simulator **inside this
repo**, consuming the recorded tape through `internal/replay`. Four new
packages plus one CLI, in dependency order:

1. **`internal/fixedpoint`** — deterministic decimal parsing/formatting.
   Prices are integer cents (1..99); quantities are integer
   centi-contracts (int64, `"12.50"` → 1250) parsed digit-wise from the
   wire strings — **no float64 anywhere in engine state or arithmetic**.
   Fuzz targets on the parsers.
2. **`internal/lob`** — the order book. Domain-informed structure: a
   binary contract trades at 1–99¢, so each side is a **fixed
   `[100]Level` array indexed by price** (O(1) level access, no tree), a
   64/128-bit occupancy bitmap for O(1) best-price scan, and an
   index-linked order arena (slice-backed intrusive lists, no per-order
   heap allocation) for price-time priority. Invariants — never-crossed
   after match, quantity conservation, no negative resting size — are
   enforced in debug builds and property-tested with seeded generators
   (stdlib only, matching house test style).
3. **`internal/engine`** — single-writer match loop (commands in, events
   out; no locks or channels in the core, same single-owner discipline
   the journal writer already uses). Input: command journal; output:
   event journal; a chained SHA-256 over the canonical event encoding is
   the **state hash**. Same input ⇒ byte-identical hash, enforced by a
   `make determinism` target replaying a committed fixture tape (CI is
   currently dead org-side per BUILDLOG 2026-06-10, so the gate is
   local + pre-commit, like everything else).
4. **`internal/enginefeed`** — the tape adapter, two modes:
   - **Mirror mode (correctness harness):** derive synthetic per-level
     flow from snapshots/deltas, feed the engine, and assert the
     engine's book equals the independently-reconstructed `replay.Book`
     top-of-book **at every frame** over real recorded days. This turns
     terabytes of real market data into a continuous cross-validation
     oracle — the test a synthetic-input LOB cannot have.
   - **Fill-sim mode (the G-B instrument):** inject hypothetical IOC/taker
     orders (from the structure scanner's detections) against
     reconstructed displayed liquidity, under an explicit latency
     parameter and conservative queue assumptions (join behind all
     resting size; consume only from observed negative deltas and trade
     prints at the level). Maker fills are modeled pessimistically or
     not at all in v1 — G-C's taker/IOC-only rail (ADR-0014) means taker
     fidelity is what matters first.
5. **`cmd/sim`** — CLI: `sim mirror --tape …` (cross-validation report),
   `sim fill --tape … --orders …` (fill report for G-B), `sim bench`
   (replay throughput). Emits event journals via `internal/journal`
   (same envelope, `src="sim"`).

**Sequence-gap hygiene precedes trust:** the feed adapter starts reading
`KalshiFrame.Seq`/`Sid` and surfaces gap counts per (ticker, session);
fill-sim results carry the gap count as a data-quality caveat, and days
with gaps above a threshold are excluded from G-B measurement. Stub
snapshots (`market_id=""`) are recognized and skipped.

**Feed contract (frozen here).** Envelope: `t` int64 unix-ns UTC wall,
`m` int64 process-mono ns (not cross-file comparable), `src`, `k`
(transport tag, `"ws"` for Kalshi), `v` schema version, `p` raw payload.
Kalshi payload: `type` ∈ {`orderbook_snapshot`, `orderbook_delta`,
`trade`}, with `msg` bodies as parsed today — snapshot:
`market_ticker`, `market_id`, `yes_dollars_fp`/`no_dollars_fp` as
`[[price_dollars, qty], …]` string pairs; delta: `market_ticker`,
`price_dollars`, `delta_fp` (signed string), `side` ∈ {`yes`,`no`};
trade: `market_ticker`, `taker_side`, `yes_price_dollars`/
`no_price_dollars`, `count`, `ts_ms` (exchange-side unix-millis, the
pairing axis — envelope `t` is recorder-side). Ordering axis for replay:
`t`, tolerating regressions; per-file state resets on `firstInFile`.

**Published numbers or it didn't happen:** `go test -bench -benchmem`
results (0 allocs/op target on the match hot path, orders/sec, p50/p99
apply latency) committed to the README with the benchstat methodology,
plus a pprof flamegraph artifact. The existing bench files' prose
expectations (~14M frames/scan, ~99% deltas) become measured baselines.

**Hard rail unchanged:** this is all offline replay. Nothing in these
packages touches order placement; ADR-0014's Phase-C sign-off rail is
unaffected.

## Milestones (each gated on `make all` green + its own acceptance test)

- **M1 `fixedpoint` + `lob`**: property tests green over ≥1M randomized
  ops; benches committed; 0 allocs/op on add/cancel/match.
- **M2 `engine`**: determinism gate in `make` (fixture tape → golden
  hash); command/event schemas documented in the package doc.
- **M3 mirror mode**: engine book == replay book at every frame across
  ≥3 full recorded days, gap-free sessions; divergence count published.
- **M4 fill-sim mode**: wired to structscan detections; produces the
  G-B "capturable EV" column with latency sensitivity (0/250/1000 ms).
- **M5 showcase pass**: README architecture diagram (90-second
  senior-engineer read), benchmark table, flamegraph, this ADR linked.

## Alternatives considered

- **Standalone repo ("mini-exchange" project)** — rejected for now:
  `internal/` packages are unimportable across modules, so it would
  duplicate the reader and the envelope contract; and it would decouple
  the engine from G-B, turning the fill simulator back into a toy.
  Promotion to its own repo later is mechanical (expose the reader,
  move packages) — reversal cost of *this* choice is low.
- **Double-entry ledger service first (Option B)** — deferred, not
  rejected: the engine needs position/balance accounting eventually,
  and a ledger grown from the engine's event stream (same event-sourced
  substrate) is a stronger follow-up than a cold start. Revisit after M5.
- **Reuse `replay.Book` as the engine book** — rejected: float
  quantities, no depth-ordered walk, no per-level FIFO. `replay.Book`
  stays what it is (a reconstruction diagnostic); the mirror harness
  cross-validates against it instead, which converts the redundancy
  into a test oracle.
- **Order-level tape from a richer feed** — not available: Kalshi's
  public WS does not expose per-order events. Synthesizing queue
  position conservatively and labeling it as a model is the honest
  option, and the pessimistic-fill bias makes G-B numbers conservative
  rather than flattering.

## Consequences

- Good: G-B's "capturable EV at displayed size" gets a real instrument
  instead of trade-print optimism; the same artifact is the canonical
  quant-infra portfolio piece, grounded in real captured data; the
  event-sourcing philosophy (ADR-0011/0012 lineage) extends naturally —
  all engine state derivable from journals, byte-identical on replay.
- Bad: real scope (~4-6 focused weeks solo alongside detector work);
  fill realism is bounded by aggregate-level data — maker-fill fidelity
  is unknowable from this tape and must stay labeled as modeled;
  determinism gating stays local-only until org Actions billing is
  fixed (BUILDLOG 2026-06-10).
- Neutral: `internal/replay` gains an exported frame-shape surface
  (currently unexported structs re-declared per consumer); the engine's
  gap detection will likely expose recorder drop windows we currently
  cannot see — that is a feature, but expect it to dirty some
  historical days for M3/M4.

## Reversal cost

Low. All packages are additive beside the existing data plane; nothing
existing is modified except promoting frame-shape types to an exported
package. If the engine is abandoned, the fixedpoint/lob/property-test
work still hardens replay, and G-B falls back to trade-print
approximation with the limitation documented.

## References

- ADR-0011 (journal backpressure — drops leave no tape marker),
  ADR-0012 (time-source discipline), ADR-0014 (structure-first pivot;
  G-B/G-C gate definitions and the taker/IOC rail)
- `internal/journal/journal.go:79-87` (envelope),
  `internal/replay/reader.go` (tape surface),
  `internal/replay/kalshi.go:12-18` (frame; unused `Seq`/`Sid`),
  `internal/replay/orderbook.go` (reconstruction kernel + float caveats),
  `internal/edge/poll_index.go:151-162` (trade shape),
  `docs/decisions/0006-peek-probe-kalshi-naming.md` (channels recorded)
- LMAX Disruptor (single-writer event loop pattern); Nygard ADR format
