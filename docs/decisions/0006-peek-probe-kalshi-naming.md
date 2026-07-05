# 0006 — Keep `cmd/peek` as an ongoing Kalshi catalog probe

**Status:** Accepted
**Date:** 2026-04-22
**Authors:** isobar team

## Context

On 2026-04-22 we re-ran `cmd/tape` after the W1 pivot to weather brackets
and discovered that our `config/config.example.yaml` listed series names
that Kalshi's REST API did not recognize. Discovery returned **48**
open markets (all `KXHIGH*`, HIGH temperature) but **0** from the four
`KXLOW*` series we had configured — not zero-activity, literally
`len(markets) == 0` with `status` unfiltered.

A diagnostic probe (`cmd/peek`) against `/series?category=Weather` and
per-candidate `/markets?series_ticker=…` calls showed that:

- Daily HIGH series are named `KXHIGH{CITY}` (confirmed working).
- Daily LOW series are named **`KXLOWT{CITY}`** — note the extra `T`
  and NY's is **`KXLOWTNYC`** (3-letter city code, not 2).
- Kalshi's category label for weather is **"Climate and Weather"**
  (not `"Weather"`). A filter of `category=Weather` returns 0 series.
- The `/series` endpoint response is >10 MiB unfiltered (8,998+ series
  parsed before the 10 MiB read cap in our client). Streaming JSON is
  required; a single `Unmarshal` fails mid-parse.

In other words: Kalshi's catalog naming is **heterogeneous, undocumented
outside the API itself, and evolves**. We will hit this same problem
again in W6 when we add hurricane-landfall series (names like
`KXHURMIA`, `HURMIA`, `KXHURPATHFLA`, `HURJACKFL` exist in the catalog
with no obvious pattern).

## Decision

We keep `cmd/peek` as a first-class ops tool (under `cmd/`, with a
`make run-peek` target, documented in `docs/ARCHITECTURE.md` and
`docs/GLOSSARY.md`) rather than a one-off throwaway. Any time we add
or change a configured series prefix we **run `peek` first to verify
the name against Kalshi's live catalog**, and only after that update
`config.example.yaml` and the recorder.

We also standardize on these facts:

- **Channels:** `orderbook_delta` + `trade`. `ticker_v2` is rejected by
  the current Kalshi WS (`code 8 "Unknown channel name"`) and
  `orderbook_delta` carries strictly more information. If a future
  strategy wants the ticker stream, verify the channel name with peek
  or the docs *before* configuring it.
- **Category label for weather:** `Climate and Weather` (if we ever
  need to filter the `/series` response).
- **Market status terminology:** the API accepts `status=open` as a
  filter but emits concrete values `active` / `closed` / `finalized`
  per market. These are not aliases you can predict from naming.

## Alternatives considered

- **Delete `cmd/peek` after the one-off fix.** Rejected: we will hit
  this exact problem again for hurricanes, and rebuilding the probe
  from scratch costs more than maintaining 110 lines.
- **Embed discovery in `cmd/tape` startup.** Rejected: recorder is
  production path, must be deterministic and dependency-light. Probe
  is exploratory and belongs out of band.
- **Hardcode series→ticker mappings from a checked-in fixture.**
  Rejected: Kalshi's catalog changes without a versioning story; a
  fixture goes stale silently. `peek` is the source of truth when it
  matters.
- **Rely on the Kalshi docs site.** Rejected: the official docs list
  example series but not an exhaustive catalog; the API is the ground
  truth and peek queries it directly.

## Consequences

- **Good:** Any future series-name surprise is 60 seconds of
  `make run-peek` away from a definitive answer. No more recording
  into a black hole.
- **Good:** `cmd/peek` uses `kalshi.Client.GetMarketsPage` for its
  probe. (An earlier `kalshi.Client.GetRaw` arbitrary-path wrapper was
  removed in the Phase-1 reduction sweep along with the rest of the
  unused trading/portfolio surface; recovery is git history.)
- **Bad:** One more binary to maintain and keep compiling. Trivial —
  peek has no runtime dependencies beyond what `cmd/tape` already uses.
- **Bad:** `peek` authenticates with the same Kalshi key as production.
  If the key is rotated, peek breaks until re-sourced. (This is already
  true of tape; no new exposure.)
- **Neutral:** Peek's probe list is hardcoded in its source. Adding a
  new city/series requires an edit + rebuild. For a tool that runs
  ~monthly, that's fine; a flag-driven version is cheap if the pattern
  shifts.

## Reversal cost

Low. `cmd/peek` is ~110 LOC, no tests, no packages depend on it.
Removing it is `git rm -r cmd/peek/` + one Makefile hunk + one
CHANGELOG line. The Kalshi naming facts captured above are
independently in `config/config.example.yaml` comments and the W1
changelog entry, so deleting peek does not lose knowledge.

The canary that says we got this wrong: if six months go by and we
never run `peek` for a real question, it's clutter and should go.

## References

- W1 discovery pass: commits `62649e4` (series fix), `ee34e3f` (peek).
- `cmd/peek/main.go` current scope.
- Kalshi catalog probe output archived in PR discussion (not
  committed — it's an 8,998-series dump).
- `docs/ROADMAP.md` W6: "Hurricane landfall" — the next expected
  naming-discovery event.
