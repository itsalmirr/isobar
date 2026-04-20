# 0006 — Go runtime + Python research pipeline

**Status:** Accepted
**Date:** 2026-04-20
**Authors:** isobar team

## Context

Through the 12-week plan we need to implement a stack of well-studied
statistical procedures:

- **NGR / EMOS** (Scheuerer & Büermann 2014, `arXiv:1407.0058`).
- **Conformal prediction** (Vovk, Gammerman, Shafer).
- **EVT Peaks-Over-Threshold** (Pickands 1975; Coles 2001).
- **Bayesian T−3h update** and reliability-diagram analysis.

These are not research frontiers — they are 20-to-50-year-old techniques
with **reference implementations** in the Python / R scientific
ecosystem:

| Technique | Reference implementation | Notes |
|---|---|---|
| NGR / EMOS | [`crch`](https://cran.r-project.org/package=crch) (R); portable to Python via `statsmodels.regression` with custom likelihood | The Gneiting / Scheuerer lab publishes in R first |
| CRPS evaluation | [`properscoring`](https://github.com/properscoring/properscoring) (Python) | 300+ stars, stable API, ~100 LOC to use |
| Reliability diagrams, Brier skill | [`climpred`](https://climpred.readthedocs.io) (Python) | Xarray-native, the standard tool in the climate-forecasting community |
| GPD tail fit | `scipy.stats.genpareto` | Stdlib-ish, battle-tested |
| CDF / probability integrals | `scipy.stats.norm`, `scipy.special` | Numerically stable, no reinventing |
| Jupyter / EDA | `jupyterlab`, `pandas`, `matplotlib`, `seaborn` | Standard iteration loop |

Implementing any of these from scratch in Go is:

1. **2–3× more code** (no vectorised numerical libraries at the same
   level; `gonum` is capable but the ergonomics are a tax).
2. **2–3× slower iteration** on the model itself — the question "what
   if I condition NGR on ensemble spread instead of variance?" is a
   two-line edit + kernel-restart in Python; in Go it's a package edit
   + recompile + test cycle.
3. **Re-deriving unit-tested wheels.** `properscoring` has ~300 tests
   for CRPS / Brier. We'd either trust those indirectly or write our
   own.

The binding constraint on Phase 0.5 is **how fast we can reach the
Brier-skill gate** (calibrated predictor beating market-implied on
held-out data). Calibration iteration speed dominates everything else.

On the other side: we also have firm reasons to keep the **live trading
runtime** in Go:

- Single static binary (`FROM scratch` deployable). Python's deployment
  story involves an interpreter, dependencies, and supply-chain
  surface.
- Compile-time type safety at the audit boundary where real money
  goes through.
- Integration with everything already in `internal/` (journal,
  kalshi, retry, config).
- Resource model: goroutines + channels is the right primitive for
  WS + risk + exec; asyncio + GIL is the wrong primitive for the
  same job.

The two needs are not in conflict — they live on different sides of
a **cold boundary** (model-fitting happens offline, the fitted model
is consumed at runtime).

## Decision

The project is **dual-language, with a deliberate cold boundary**:

- **Runtime is Go.** `cmd/tape`, `cmd/bot`, `cmd/analyze`, `cmd/dashboard`,
  every `internal/*` package that runs at trade time. No Python
  interpreter ever touches a live trading process.
- **Research pipeline is Python.** Under `research/` in the repo:
  Jupyter notebooks for exploration, `.py` scripts for reproducible
  fitting, `pyproject.toml` + `uv.lock` for dependency pinning.
  Output: **immutable JSON artifacts** checked into `models/` (or a
  future object store) containing fitted coefficients + validation
  metadata.
- **Cross the boundary as JSON.** Python fits → serialises coefficients
  + provenance into a versioned JSON schema → Go loads at startup
  and performs the fast, audited *predict* step.
- **Cross-language golden tests.** Every JSON schema ships with a
  Python-produced `testdata/*-fixture.json` that includes both the
  fitted coefficients AND a set of `(input, expected_output)` pairs.
  The Go consumer's test re-runs the predict step and asserts
  per-output equality to `1e-9`. This is our guarantee against
  implementation drift.

Full concrete layout + schema in `docs/RESEARCH_PIPELINE.md`.

## Alternatives considered

- **Go-only.** Rejected. Reimplementing NGR, CRPS, reliability
  diagrams, and GPD fitting in Go is a ~4-week detour from the
  12-week plan. If we find NGR calibration doesn't help, we want to
  discover that in week 3 with Python, not week 8 with Go.
- **Python-only (runtime + research).** Rejected. We've already
  built the Go runtime (tape, kalshi, journal, retry). The interpreter
  + dependency surface is non-trivial to deploy on a $0/mo VM, and
  the compile-time safety we get in Go at the audit boundary is a
  feature, not a burden. Also: live trading from a GIL-bound process
  under adverse scheduler pressure is a known footgun.
- **Embed Python in Go via `cgo` / `go-python3`.** Rejected. Adds
  a CGO dependency, breaks `FROM scratch`, gives us the worst of
  both worlds.
- **Rust for hot paths.** Rejected. We have no hot paths — Kalshi
  rate-limits us to ~1 REST/s; WS is sub-thousand-frames/s at peak.
  Go's runtime is two orders of magnitude faster than we need.
- **R for statistics (where the `crch` package lives natively).**
  Rejected. R's deployment + packaging ecosystem is weaker than
  Python's; we can port `crch`'s logic to Python using
  `statsmodels.regression` with a custom likelihood in ~100 LOC.
- **Polyglot microservices (Python model server, Go trader, RPC
  between).** Rejected. Adds an RPC surface, network hop, and a
  second deployment artifact to what's supposed to be one VM + one
  binary. Model inference at our frequency (bracket re-evaluation
  every few minutes at most) doesn't need a server.

## Consequences

Good:
- Calibration iteration loop compresses from ~30 min (Go edit →
  recompile → test) to ~5 sec (Jupyter cell re-run).
- Access to mature references: `properscoring`, `climpred`,
  `scipy.stats`, `statsmodels`. When a reviewer asks "does your
  CRPS match the reference implementation?" we can answer "yes,
  here's the fixture".
- Go runtime stays pure and deployable as a static binary.
- Python supply chain is confined to a directory that never
  runs in production — even if a research dependency is
  compromised, it can't touch live trading.
- Clear ownership: research code iterates fast, production code
  changes deliberately.

Bad:
- Two toolchains for contributors (Python + Go). Mitigated: pinned
  via `uv.lock`; Python setup is a single `uv sync` after the repo
  is cloned. For contributors who only touch Go, they never need
  Python.
- Two CI jobs (Go tests, Python tests + golden-fixture generation).
- One more artifact format (JSON) to version. Mitigated: explicit
  `schema: "isobar.calibration.<name>/vN"` field + Go consumer must
  reject unknown versions.
- Risk of the fitted model and the Go predict implementation
  silently drifting. **Mitigated by the cross-language golden
  fixture test that runs in CI.** This is non-negotiable.

Neutral:
- Some research code will be thrown away (exploratory notebooks).
  That's fine — notebooks are not production artifacts.
- `research/` adds a directory, not a dependency for Go builds.

## Reversal cost

Medium. If we decide Python is net-negative after W5:

1. The **fitted models themselves are just JSON coefficients** —
   they survive language changes. Replacing the fitter is a Python
   → Go port of the fit step (2–3 weeks) without touching the
   predict layer.
2. The Go predict layer is language-native already; reversal doesn't
   touch it.
3. We'd lose the Jupyter exploration capability. That's the real
   cost of reversing.

Canary: if Python supply-chain or deployment pain exceeds 1 day/week
of maintenance overhead measured over a month, open a new ADR.

## Proof / verification plan

Design is only valid if we can prove it works. The W5 Phase-0.5-lite
gate that validates this whole ADR:

1. **Install + run within 15 minutes** on a clean macOS or Linux
   box: `git clone && cd research && uv sync && jupyter lab`.
2. **Fit an NGR model** on synthetic data with known ground-truth
   coefficients; recover coefficients to within 5% relative error.
3. **Export the fitted model** to `models/ngr-synthetic-v1.json`.
4. **Go consumer test** loads the JSON, runs predict on 1000
   randomly-generated inputs, and matches the Python predict output
   for the same inputs to `<= 1e-9`. This is the golden-fixture
   test.
5. **Run on real GEFS + KNYC data.** Fitted model must produce
   CRPS < raw ensemble CRPS on a held-out 90-day window by
   ≥ 10% relative improvement. If not, NGR isn't helping and we
   investigate; if it is, we commit to the full 5-station × 4-season
   × 3-lead fit.

If step 4 fails at any point through Phase 0.5, this ADR is
superseded — the boundary is broken.

## References

- `docs/research/SYNTHESIS.md` — why calibration is on the critical
  path.
- `docs/research/05-literature-review.md` — NGR / EMOS citations.
- `docs/research/07-atmospheric-physics.md` — conformal + CRPS
  citations.
- `docs/RESEARCH_PIPELINE.md` — the concrete implementation of
  this boundary (directory layout, JSON schema, golden tests).
- [`uv` docs](https://docs.astral.sh/uv/) — the Python environment
  tool we'll pin to.
- [`properscoring` GitHub](https://github.com/properscoring/properscoring).
- [`climpred` docs](https://climpred.readthedocs.io).
- [`crch` R package](https://cran.r-project.org/package=crch) —
  the NGR reference we'll port from.
