# reduction/00-baseline.md — Phase 0 baseline

**Project**: `isobar` (Kalshi weather-bracket trading bot)
**HEAD**: `c78c9b1` (post-Bucket-A, post-ADR-0013, docs refreshed)
**Date**: 2026-05-13
**Scope**: code-minimization audit — Phase 0 measurement-only, **no code changes**
**Methodology**: 4 parallel Opus 4.7 Explore agents (read-only). darwin/arm64, Go 1.26.2.

Per the reduction-audit protocol: capture every metric that downstream phases will defend (LOC, binary size, performance, coverage, complexity, deps, exported-symbol count) so any Phase 1-6 change is evaluated against a fixed baseline. **Stop here for confirmation before Phase 1.**

---

## Headline numbers

| Metric | Baseline | Direction Phase 1-6 should move |
|---|---:|---|
| Production LOC (excl. tests) | **9,442** | ↓ |
| Test LOC | **8,682** | hold or ↑ |
| Production Go files | 26 | hold or ↓ |
| Test Go files | 33 | hold or ↑ |
| Binary count (`cmd/*`) | 8 (all build clean) | hold |
| Total stripped binary size | **33.1 MB** | ↓ or hold |
| Hot-path bench (`Journal.Write`) | 72.4 ns/op · 1 alloc | hold or ↓ |
| Overall test coverage | **66.7%** (statement-weighted) | hold or ↑ |
| Library-only coverage (internal/* + tools/) | 79.3% – 100% | hold or ↑ |
| Top-50 cyclomatic complexity (max) | **47** (`edge.Scan`) | should ↓ |
| Direct deps | **5** + 2 tool | ↓ or hold |
| Transitive deps (`go list -m all`) | 30 modules | ↓ or hold |
| Exported symbols (whole module) | ~227 | ↓ or hold |
| `make lint` | **0 issues** | hold |
| `staticcheck U1000` (unused) | **0 findings** | hold |

---

## §1 — LOC, file sizes, binary sizes, exported symbols

### Per-package LOC

Module totals: **9,442 prod / 8,682 test** Go LOC. Test-to-prod ratio: 0.92.

| Package | Prod LOC | Test LOC | Prod files | Notes |
|---|---:|---:|---:|---|
| `internal/edge` | **1,729** | 1,534 | 7 | Largest internal pkg; `scan.go` alone = 594 |
| `internal/kalshi` | **1,487** | 1,446 | 4 | `client.go` 725 = biggest single prod file |
| `cmd/tape` | 705 | 395 | 2 | Largest `cmd/*` (incl. `alarms.go`) |
| `internal/replay` | 657 | 777 | 3 | Test LOC > prod |
| `internal/journal` | 627 | 933 | 1 | Test-heavy (1.49×) post-A1+A2 |
| `internal/openmeteo` | 402 | 494 | 1 | |
| `cmd/edge-scan` | 397 | 0 | 1 | **No tests** |
| `internal/calibration/mos` | 396 | 644 | 1 | |
| `internal/calibration/cliparse` | 353 | 337 | 1 | |
| `internal/calibration/climate` | 348 | 400 | 1 | |
| `cmd/analyze` | 343 | 0 | 1 | **No tests** |
| `cmd/calibrate-fit` | 310 | 0 | 1 | **No tests** |
| `internal/config` | 269 | 358 | 1 | |
| `tools/clockcheck` | 256 | 71 | 1 | ADR-0012 analyzer |
| `internal/calibration/bayesian` | 252 | 464 | 2 | Test-heavy (1.84×) |
| `internal/env` | 147 | 233 | 1 | |
| `internal/market` | 144 | 181 | 1 | Post-A9 (ticker.go deleted) |
| `internal/clock` | 143 | 202 | 1 | |
| `cmd/peek` | 105 | 0 | 1 | **No tests** |
| `internal/retry` | 96 | 123 | 1 | |
| `cmd/calibrate-bayesian` | 80 | 0 | 1 | **No tests** (scaffold) |
| `cmd/calibrate-parse-cli` | 59 | 0 | 1 | **No tests** |
| `internal/obs` | 45 | 90 | 1 | Smallest internal pkg |
| `tools/clockcheck/cmd/clockcheck` | 15 | 0 | 1 | Tiny shim |

**10 largest production files** (Phase 1 reduction-target priority):

| LOC | File |
|---:|---|
| 725 | `internal/kalshi/client.go` |
| 627 | `internal/journal/journal.go` |
| 594 | `internal/edge/scan.go` |
| 465 | `cmd/tape/main.go` |
| 436 | `internal/replay/orderbook.go` |
| 402 | `internal/openmeteo/client.go` |
| 397 | `cmd/edge-scan/main.go` |
| 396 | `internal/calibration/mos/mos.go` |
| 359 | `internal/kalshi/ws.go` |
| 353 | `internal/calibration/cliparse/cliparse.go` |

### Binary sizes

All 8 `cmd/*` build cleanly on darwin/arm64.

| Binary | Stripped (bytes) | Stripped (MB) | Unstripped (MB) | Strip ratio |
|---|---:|---:|---:|---:|
| `tape` | 6,915,474 | 6.59 | 9.59 | 31.3% |
| `clockcheck` | 6,558,450 | 6.25 | 9.16 | 31.7% |
| `peek` | 6,508,514 | 6.21 | 9.03 | 31.3% |
| `edge-scan` | 3,289,762 | 3.14 | 4.52 | 30.6% |
| `analyze` | 3,171,682 | 3.02 | 4.37 | 30.8% |
| `calibrate-parse-cli` | 2,673,394 | 2.55 | 3.75 | 32.0% |
| `calibrate-fit` | 2,283,554 | 2.18 | 3.24 | 32.8% |
| `calibrate-bayesian` | 1,742,306 | 1.66 | 2.49 | 33.4% |
| **Total** | **33,142,136** | **31.61** | 46.16 | ~30% |

Observation: `peek` at 6.21 MB despite only 105 prod LOC suggests it pulls the full `internal/kalshi` REST+WS surface. Reduction-audit candidate (verify which symbols are actually reachable).

### Exported symbols per package

| Package | Total exports | Funcs | Methods | Types | Vars | Consts |
|---|---:|---:|---:|---:|---:|---:|
| `internal/kalshi` | **62** | 4 | 40 | 16 | 1 | 1 |
| `internal/edge` | 36 | 9 | 13 | 10 | 2 | 2 |
| `internal/replay` | 28 | 9 | 11 | 4 | 2 | 2 |
| `internal/journal` | 15 | 3 | 7 | 3 | 1 | 1 |
| `internal/calibration/mos` | 14 | 6 | 2 | 3 | 0 | 3 |
| `internal/clock` | 12 | 5 | 4 | 3 | 0 | 0 |
| `internal/calibration/bayesian` | 12 | 3 | 0 | 5 | 0 | 4 |
| `internal/config` | 9 | 2 | 1 | 6 | 0 | 0 |
| `internal/calibration/climate` | 8 | 2 | 3 | 1 | 0 | 2 |
| `internal/market`, `internal/openmeteo` | 7 each | | | | | |
| `internal/retry`, `internal/env` | 4, 3 | | | | | |
| `internal/calibration/cliparse` | 3 | | | | | |
| `internal/obs` | 1 | | | | | |

Top-5 by exports = top-5 by API-surface complexity. `internal/kalshi` is the elephant (62, 40 methods).
Bottom-5 are tight packages — `internal/obs` (1 export) is hard to reduce further.

Note: grouped declarations (`const ( X = ... )`) underreport `internal/kalshi` and `internal/replay` by ~5-10 each. Relative ordering holds.

---

## §2 — Performance baseline

### Existing benchmark results

`go test -bench=. -benchmem -count=5 -run='^$' -timeout=120s` across 6 bench files. All medians are middle-of-5; PollModelSet_NoCache had one GC spike but median is stable.

**`internal/journal/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `JournalWrite` | **72.4** | 243 | 1 |

**`internal/edge/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PollModelSet_NoCache` | 2,223 | 3,136 | 6 |
| `PollModelSet_Cached` | **0.301** | 0 | 0 |

A11 win: 7,400× speedup, 100% alloc elimination. Already proven in commit history.

**`internal/replay/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ApplySnapshot` | 1,886 | 2,255 | 32 |
| `ApplyFrameToBooks` | **743** | 650 | 8 |
| `ApplyDelta` | 335 | 323 | 4 |
| `BookMidYesCents` | 78.4 | 0 | 0 |

**`internal/calibration/mos/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Fit` | 86,775 | 211,106 | 1,114 |
| `Predict` | 36.1 | 0 | 0 |
| `ProbBelow` | 4.71 | 0 | 0 |

**`internal/calibration/climate/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BracketProb` | 2.88 | 0 | 0 |
| `ShrinkP` | 0.295 | 0 | 0 |

⚠️ Climate + MOS `Predict`/`ProbBelow` numbers are suspiciously low → compiler is hoisting these calls because the bench loops use invariant inputs and discard results. **Treat as smoke-test baselines, not load-planning inputs.** Phase 6 should fix by writing into a `b.B`-scoped `sink`.

**`internal/market/`**

| Bench | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `StationBySeries` | 1.87 | 0 | 0 |
| `StationByLocation` | 1.85 | 0 | 0 |

### Hot paths identified — covered vs missing

| Hot path | Iter count (representative) | Bench status |
|---|---:|---|
| `Journal.Write` | 28/s live (~2.4M/day) | ✅ covered |
| `Subscriber.runOnce` read pump | 28/s live | **❌ missing** |
| `Signer.AuthHeaders` (RSA-PSS) | ~10/min | **❌ missing** |
| `edge.Scan` main loop | ~14M frames/scan | partial (sub-paths only) |
| `replay.ForEachFrame` (sonic decode) | ~14M/scan | **❌ missing** |
| `replay.ApplyFrameToBooks` | ~14M/scan | ✅ covered |
| `Book.ApplyDelta` | ~13.9M/scan | ✅ covered |
| `Book.ApplySnapshot` | ~140k/scan | ✅ covered |
| `Poll.DailyExtremesFWithSkip` | ~10²/scan | **❌ missing** |
| `pollModelSet` cached | 181k/scan | ✅ covered (A11) |
| `mos.Predict`, `mos.ProbBelow` | ~181k/scan | ✅ covered (but hoisting suspicion) |
| `climate.BracketProb`, `climate.RangeProb` | ~181k/scan | ✅ partial (RangeProb missing) |
| `mos.Fit` (offline) | 1× per refit | ✅ covered |

### Missing benchmark inventory (proposal for Phase 0 follow-up)

Per protocol: if hot-path benches don't exist, ADD them before Phase 1 measures against them. The following are proposed but **not yet written**:

| Hot path | Proposed bench | Why it matters |
|---|---|---|
| `Subscriber.runOnce` dispatch | `BenchmarkWSReadPumpDispatch` | live recorder — per-frame overhead excluding syscall |
| `Signer.AuthHeaders` | `BenchmarkSignerAuthHeaders` | money-path latency (when cmd/bot lands) |
| `replay.ForEachFrame` decode | `BenchmarkForEachFrameDecode` | dominates `edge-scan` wall time; the "3-5× stdlib" claim re sonic |
| `Poll.DailyExtremesFWithSkip` | `BenchmarkDailyExtremesFWithSkip` | members × hours double loop |
| `climate.RangeProb` | `BenchmarkRangeProb` | parity check vs BracketProb |
| `edge.Scan` end-to-end | `BenchmarkScanSingleDay` | regression detection on aggregate |
| `Book.MidNoCents` | `BenchmarkBookMidNoCents` | symmetric to MidYesCents |

**Operator decision**: add these benches in Phase 0 follow-up before Phase 1 starts, or proceed with current bench coverage and accept blind spots for the missing hot paths?

---

## §3 — Complexity, coverage, lint signal

### Cyclomatic complexity (top-10 of 50 sampled)

`go run github.com/fzipp/gocyclo/cmd/gocyclo@latest -top 50 -ignore "_test.go" ./...`

| Complexity | Function | File:line |
|---:|---|---|
| **47** | `edge.Scan` | `internal/edge/scan.go:141` |
| **35** | `kalshi.Client.doRaw` | `internal/kalshi/client.go:565` |
| **32** | `journal.Journal.run` | `internal/journal/journal.go:349` |
| 24 | `cmd/edge-scan.writeSummary` | `cmd/edge-scan/main.go:222` |
| 22 | `edge.tradeMsg.priceDollars` | `internal/edge/poll_index.go:177` |
| 20 | `cmd/edge-scan.main` | `cmd/edge-scan/main.go:31` |
| 20 | `cliparse.Run` | `internal/calibration/cliparse/cliparse.go:69` |
| 16 | `config.Validate` | `internal/config/config.go:227` |
| 15 | `edge.loadTruthFile` | `internal/edge/truth.go:110` |
| 15 | `mos.Fit` | `internal/calibration/mos/mos.go:123` |

Top-50 distribution: 8 high (>15), 22 medium (10-15), 20 normal (5-9). No simple functions appear in top-50 by definition.

**`internal/edge` carries 11 of 50 top-complexity functions** (more than 2× any other package). Hot zone for any Phase 1-2 reduction work.

### Test coverage per package

Overall statement-weighted module coverage: **66.7%**.

| Tier | Packages | Coverage range |
|---|---|---|
| **0% (no tests)** | `cmd/{analyze, calibrate-bayesian, calibrate-fit, calibrate-parse-cli, edge-scan, peek}` + `tools/clockcheck/cmd/clockcheck` | 0.0% × 7 |
| **<50%** | `cmd/tape` | 37.2% |
| **79-89%** | `internal/edge`, `internal/journal`, `tools/clockcheck`, `internal/calibration/climate`, `internal/kalshi`, `internal/openmeteo` | 79.3% – 89.3% |
| **90-99%** | `internal/calibration/{cliparse, mos}`, `internal/replay`, `internal/env`, `internal/retry` | 90.1% – 97.0% |
| **100%** | `internal/calibration/bayesian`, `internal/clock`, `internal/config`, `internal/market`, `internal/obs` | 100.0% × 5 |

The 7 zero-coverage packages are all `main` (CLI glue + the analyzer entrypoint). Library packages are uniformly well-covered. Reduction in 100%-coverage packages must be statement-preserving or tests will fail loudly — useful constraint.

### Lint signal — staticcheck + make lint

`staticcheck -checks 'U1000,U1001,SA4000,SA4006,ST1000,ST1003' ./...`:

| Check | Count | Notes |
|---|---:|---|
| **U1000** (unused symbols) | **0** | No automated dead-code signal |
| U1001 (unused params) | 0 | |
| SA4000 (trivial cond) | 0 | |
| SA4006 (unused write) | 0 | |
| ST1000 (missing pkg doc) | 1 | `internal/market` |
| ST1003 (bad style name) | 2 | `TsMs` → `TSMs` in `internal/edge/poll_index.go:161`, `scan.go:61` |

`make lint`: **0 issues** (both `golangci-lint` and the custom `clockcheck` analyzer pass).

**Phase 1 implication**: with `staticcheck U1000 == 0`, Phase 1 cannot be driven by tooling alone. Manual call-graph walk is required. Consider adding `golang.org/x/tools/cmd/deadcode` (stricter than U1000) as a Phase 0 follow-up.

---

## §4 — Dependencies

### Direct + transitive deps

| Module | Version | License | Prod imports | Tool/test? | Purpose |
|---|---|---|---:|---|---|
| `bytedance/sonic` | v1.15.1 | Apache-2.0 | 3 (all in `internal/replay`) | prod | Faster JSON decode (offline tape replay only) |
| `coder/websocket` | v1.8.13 | ISC | 2 | prod | Kalshi WS client |
| `stretchr/testify` | v1.11.1 | MIT | **0** | **test-only** | Test assertions |
| `golang.org/x/sync` | v0.20.0 | BSD-3 | 2 | prod | `errgroup` |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT/Apache | 1 | prod | Config YAML decode |
| `golang.org/x/tools` (tool) | v0.44.0 | BSD-3 | — | tool | `goimports` |
| `mvdan.cc/gofumpt` (tool) | v0.9.2 | BSD-3 | — | tool | `gofumpt` |

**Total**: 5 direct + 2 tool. **30 total modules** in `go list -m all` (including main).

### `go mod why` flags

`stretchr/testify` is the only require-block dep whose graph traverses ONLY `.test` binaries — it ships in zero production binaries. Removal does not affect binary sizes; only flattens the test-time module graph.

### Custom helpers vs stdlib (survey for Phase 5)

| Custom helper | LOC | Stdlib equivalent | Recommendation |
|---|---:|---|---|
| `internal/retry/retry.go` | 96 | `time.Timer` + `context` + `math/rand/v2` per call site | **Keep** — ADR-0005 deliberate extraction; net LOC roughly equal vs inline-everywhere |
| `internal/clock/clock.go` | 143 | `time.Now()` + Go 1.25 `testing/synctest` | **Keep, flag** — ADR-0012 + clockcheck depend on this seam; `Stamp{Wall,Mono}` has no stdlib equivalent |
| `internal/env/env.go` | 147 | `os.ReadFile` + `bufio.Scanner` + `strings.Cut` | **Trim** — multi-line quoted-value branch (lines 130-143) handles a `.env` shape we don't use; `CheckMode` is real value. Could drop ~80 LOC. |
| `edge.matchTemperatureMember` + `isAllDigits` | ~50 | `regexp.MustCompile(...)` | **Keep** — hot path, alloc-free; regex would allocate per call |
| Custom JSON parsers | none found | — | n/a |
| Custom CSV parsers | none found | — | already `encoding/csv` |

### `bytedance/sonic` specifically

- **3 prod files** in 1 package: `internal/replay/{kalshi,orderbook,reader}.go`
- Same 3 files also import `encoding/json` (mixed pattern within one package)
- Pulls **7 indirect modules** transitively (sonic, sonic/loader, gopkg, base64x, cpuid/v2, golang-asm, x/arch)
- "3-5× stdlib" claim exists only in `internal/replay/reader.go:147-148` as a source comment. **Zero documentation** in any ADR, README, ARCHITECTURE.md, CHANGELOG.md.
- Live trading path (`internal/kalshi/ws.go`, `client.go`, `journal.go`) uses stdlib `encoding/json` — sonic accelerates only offline replay
- **Bench to validate the claim does not exist**. The reduction audit's Phase 5 (or Phase 0 follow-up) should write one before deciding to keep/swap.

Removal would collapse the indirect-dep graph from 14 → ~7 and `go list -m all` from 30 → ~21 modules.

---

## Cross-cutting observations

1. **`internal/edge` is the hot zone** for Phase 1+: 1,729 prod LOC (largest pkg), 11 of top-50 complexity functions, lowest library coverage at 79.3%, all post-A11 work concentrated here.

2. **`cmd/*` is an untested high-complexity surface**: 7 binaries with zero tests; `cmd/edge-scan` ships `writeSummary` (cyc 24) and `main` (cyc 20). The reduction audit overlaps with the proposed ADR-0019 (CLI test discipline) from audit/02-architecture.md — coordinate.

3. **Tooling deficit for dead-code detection**: zero `U1000` findings means staticcheck cannot drive Phase 1. The audit needs to install/run `golang.org/x/tools/cmd/deadcode` and/or do manual call-graph walks. Without that signal, deletion candidates must come from manual file-by-file scrutiny.

4. **Three "fat" binaries** (`tape`, `peek`, `clockcheck` at 6.2-6.6 MB) suggest dep-graph heaviness. `peek`'s 6.21 MB for 105 prod LOC is the most striking ratio.

5. **`bytedance/sonic` is the largest single dep-graph reduction lever** — 7 indirect modules removable, but needs a bench-validated decision first.

6. **Climate + MOS `Predict`/`ProbBelow` benches are likely compiler-hoisted** — fix the keep-alive pattern before relying on those numbers for any Phase 6 regression check.

7. **`internal/retry` and `internal/clock` are deliberate seams** (ADR-0005, ADR-0012). Phase 5 should NOT remove them.

---

## Phase 1 starting points (when authorized)

In rough priority order:

1. **Manual unused-symbol sweep on `internal/edge`** — 36 exports across 7 files; some likely unreachable post-Bucket-A consolidation.
2. **Add `golang.org/x/tools/cmd/deadcode`** to the lint chain — establishes a recurring tooling signal.
3. **Fix the two ST1003 hits** (`TsMs` → `TSMs`) — mechanical, ships any time.
4. **Verify `cmd/peek` reachable-symbol footprint** — it's 105 LOC but pulls a 6.21 MB binary. Mid-audit candidate for understanding which `internal/kalshi` symbols are actually needed.
5. **Add a `docs/decisions/0014-sonic-vs-stdlib.md`** (claims slot 0014 in the non-Phase-2-architecture-ADR space) documenting the sonic decision + a bench. This is the load-bearing precondition for any "swap to stdlib" reduction in Phase 5.

---

## Phase 0 follow-up — operator decision points

Before Phase 1 starts, two questions:

1. **Add missing hot-path benches** (§2 inventory: 7 proposed)?
   - Pro: establishes "performance hold" gate for Phase 1-6.
   - Con: adds ~7 new bench files (~200-300 LOC of test code); not a "reduction" itself.
2. **Add `deadcode` to the toolchain**?
   - Pro: replaces manual call-graph walking with automated signal.
   - Con: another indirect dep (`golang.org/x/tools` is already a tool dep though).

If skipped, Phase 1 proceeds with current bench coverage and manual sweep methodology.

---

## End of Phase 0

**Per protocol: stopping here. Confirm baseline before proceeding to Phase 1.**

Next phase (when authorized): **dead-code sweep** — `staticcheck U1000`, `unparam`, `deadcode`, `go mod why` per direct dep, exported-symbol cross-grep. Deletion only, no abstraction. Output: `reduction/01-deadcode.md` with `file:line` per removal + LOC delta per file + per-commit attribution to the tool that flagged it.
