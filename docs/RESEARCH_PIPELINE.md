# Research pipeline — the Python ↔ Go boundary

> How fitted statistical models cross from `research/` (Python) into
> `internal/calibration/` (Go) at runtime. The boundary is cold
> (offline fit → JSON artifact → online predict) and verified by
> cross-language golden-fixture tests in CI.
>
> This document is the contract. Changes here require an ADR.
>
> Rationale: `docs/decisions/0006-dual-language-architecture.md`.

## Directory layout

```
isobar/
├── cmd/                       Go only
├── internal/                  Go only
├── research/                  Python only — never runs in production
│   ├── pyproject.toml         uv-managed, pinned deps
│   ├── uv.lock                committed; single source of truth
│   ├── README.md              how to set up + run
│   ├── calibration/
│   │   ├── fit_ngr.py         NGR/EMOS fitting script
│   │   ├── fit_conformal.py
│   │   ├── fit_evt.py
│   │   └── testdata/          fixtures for Go golden tests
│   ├── notebooks/             Jupyter, for exploration only
│   └── archive/               cached archive CSV/Parquet (gitignored)
├── models/                    Committed JSON model artifacts
│   └── ngr/
│       └── KNYC-spring-24h.json
└── testdata/                  Golden fixtures shared between
    └── calibration/           Python fit and Go predict
```

### Why `models/` in git

Fitted models are **source** — the project's Go code is incomplete
without them, the same way a binary is incomplete without its
configuration file. They are small (few KB per station/season/lead
bin), human-readable, and their provenance matters.

When models grow beyond 10 MB total we revisit (object store + git
LFS or a separate artefact repo). Until then, git is the simplest
tool that works.

## Python environment

**Tool:** [`uv`](https://docs.astral.sh/uv/). Fast, reproducible,
`pyproject.toml` + `uv.lock`, no `virtualenv` + `pip-tools` dance.

**Setup (one-time, per contributor):**

```bash
cd research
uv sync               # installs everything from uv.lock
uv run jupyter lab    # or: uv run python calibration/fit_ngr.py
```

**Adding a dependency:**

```bash
cd research
uv add scipy          # writes to pyproject.toml + uv.lock
git add pyproject.toml uv.lock
```

**Python version:** pinned to 3.12 via `.python-version`.

### Dependency allowlist

As with Go, third-party Python deps are an audit burden. Current
planned set (actual versions pinned in `uv.lock`):

| package | purpose | why this one |
|---|---|---|
| `numpy` | array math | foundational; every other library depends on it |
| `scipy` | `stats.norm`, `stats.genpareto`, `special.erf` | stdlib-ish for scientific Python |
| `pandas` | archive data loading | convenient for CSV/Parquet I/O |
| `statsmodels` | NGR regression likelihood | `regression.linear_model` + custom likelihood for NGR |
| `properscoring` | CRPS, Brier, reliability | the reference implementation |
| `climpred` | forecast verification | Xarray-native; industry-standard |
| `scikit-learn` | conformal wrappers | stable, well-tested |
| `pyarrow` | Parquet reads if archive grows | fast, columnar |
| `jupyterlab` | notebooks | research-only |
| `matplotlib` | plotting reliability diagrams | research-only |
| `pytest` | Python unit tests | standard |
| `pydantic` | JSON schema validation on output | prevents malformed artifacts |

New deps require a note in the PR body explaining why a narrower
existing dep can't cover the need.

## The JSON artifact schema

**Versioned.** Every artifact carries `"schema": "isobar.<name>/vN"`.
Go consumer rejects unknown versions (fails loudly at startup).

### Example: NGR v1

```json
{
  "schema": "isobar.calibration.ngr/v1",
  "provenance": {
    "fitted_at": "2026-04-20T14:30:00Z",
    "fitter": "research/calibration/fit_ngr.py",
    "fitter_git_sha": "018497c",
    "source_archive_sha256": "abc123def456...",
    "python_version": "3.12.3",
    "packages": {
      "statsmodels": "0.14.4",
      "properscoring": "0.2"
    }
  },
  "bin": {
    "station": "KNYC",
    "season": "spring",
    "lead_hours": 24,
    "variable": "temperature_2m_max_celsius"
  },
  "coefficients": {
    "mean": {"a": 0.42, "b": 0.98},
    "variance": {"c": 0.15, "d": 0.85}
  },
  "validation": {
    "sample_size_fit": 1826,
    "sample_size_held_out": 365,
    "crps_raw_ensemble": 1.82,
    "crps_ngr_fit": 1.42,
    "crps_ngr_held_out": 1.48,
    "crps_improvement_vs_raw": 0.187,
    "reliability_rmse": 0.018,
    "brier_skill_vs_raw": 0.12
  },
  "predict_spec": {
    "formula_mean": "a + b * ensemble_mean",
    "formula_variance": "c + d * ensemble_spread_squared",
    "input_units": "celsius",
    "output_units": "celsius"
  }
}
```

### Design rules for schemas

1. **Machine-readable + human-readable.** No binary, no Protobuf.
   JSON only. We look at these files by hand during debugging.
2. **Self-describing.** Include the formula as text in `predict_spec`.
   A reader who knows nothing about the code should be able to
   re-implement predict from the artifact alone.
3. **Provenance is mandatory.** `fitter_git_sha`,
   `source_archive_sha256`, package versions. A fitted model whose
   provenance we can't reproduce is garbage.
4. **Validation metrics live with the artifact.** You shouldn't have
   to go hunting for "was this model any good?" — it's right there.
5. **Bin keys are fixed vocabulary.** `station` ∈ {KNYC, KORD, KMIA,
   KLAX, KDEN}; `season` ∈ {winter, spring, summer, fall};
   `lead_hours` ∈ {24, 48, 72}. Changes to the vocabulary require an
   ADR.
6. **One bin per file.** `models/ngr/KNYC-spring-24h.json` — file
   path IS the key. Prevents the "which row in the giant table?"
   problem.

## Cross-language golden-fixture testing

This is the non-negotiable guarantee. Without it the two sides
silently drift apart and we ship a miscalibrated bot.

### Fixture format

`testdata/calibration/ngr/fixture-v1.json`:

```json
{
  "schema": "isobar.calibration.ngr/v1",
  "model": { ...coefficients as above... },
  "test_cases": [
    {
      "name": "basic warm-day NYC",
      "input": {
        "ensemble_members": [22.1, 22.3, 22.0, 21.9, 22.4, 22.2]
      },
      "expected_output": {
        "posterior_mean_celsius": 22.128,
        "posterior_sigma_celsius": 0.642,
        "bracket_20_22_probability": 0.4221,
        "bracket_22_24_probability": 0.4673
      }
    },
    ...
  ]
}
```

### Generation (Python side)

`research/calibration/generate_fixtures.py`:

1. Fit NGR on a small synthetic dataset with known ground-truth
   coefficients.
2. For N=1000 randomly-sampled inputs, run predict in Python,
   serialise inputs + outputs into `testdata/calibration/ngr/fixture-v1.json`.
3. Commit the fixture.

### Consumption (Go side)

`internal/calibration/ngr_test.go`:

```go
func TestNGRPredictMatchesPythonFixture(t *testing.T) {
    data, err := os.ReadFile("../../testdata/calibration/ngr/fixture-v1.json")
    require.NoError(t, err)

    var fix Fixture
    require.NoError(t, json.Unmarshal(data, &fix))

    model, err := LoadNGR(fix.Model)
    require.NoError(t, err)

    for _, tc := range fix.TestCases {
        got := model.Predict(tc.Input.EnsembleMembers)
        require.InDelta(t, tc.ExpectedOutput.PosteriorMean, got.Mean, 1e-9, tc.Name)
        require.InDelta(t, tc.ExpectedOutput.PosteriorSigma, got.Sigma, 1e-9, tc.Name)
        // ...
    }
}
```

Tolerance is `1e-9`, not `0`, to allow for floating-point accumulation
order differences. If we see drift at `1e-9` that's a real bug.

### CI wiring (W5+)

Two jobs:

1. `go-test` (existing) — runs `make all`. Loads committed fixtures.
2. `python-test` (new in W2) — runs `uv run pytest` in `research/`,
   then regenerates fixtures and checks `git diff --exit-code
   testdata/`. If the Python-regenerated fixture differs from the
   committed fixture, either:
   - The Python fit changed (legitimate — commit the new fixture).
   - The fit is nondeterministic (bug — add a seed and re-fit).

Both jobs must pass for a merge.

## How models flow through the Go runtime

```
Startup (cmd/bot or cmd/analyze):
  1. Read config.models_dir (default ./models)
  2. For each (station, season, lead) expected by the active signal:
     - Load models/ngr/<station>-<season>-<lead>h.json
     - Validate schema version
     - Extract coefficients
     - Hold in memory (few KB per bin)
  3. If any required model is missing: fail loudly, do not start.

Per-tick (internal/signal/weather):
  1. Receive ensemble members from internal/openmeteo.
  2. Compute ens_mean, ens_spread.
  3. Call internal/calibration.NGR.Predict(mean, spread)
     → returns (μ, σ) of calibrated Gaussian.
  4. Use normal CDF for bracket probabilities.
  5. Fee-aware edge check (internal/fees).
  6. Emit Quote or skip.
```

No Python in the critical path. If Python is uninstalled on the
production VM, the bot doesn't care.

## Python test discipline (summary)

Short version — mirrors Go conventions where sensible:

- `pytest` with `pytest --strict-markers`.
- Fixtures under `research/calibration/testdata/` mirror Go layout.
- No network in tests — fetch from archive CSV.
- Reproducibility: every fit seeded explicitly; ADR-0006 regression
  test re-fits and checks coefficients stable to `1e-10`.
- Coverage target: 85% of `research/calibration/*.py`.

## Risk: "Python is another supply chain"

True. Mitigations:

- **`uv.lock` is committed.** Every contributor gets bit-identical
  deps.
- **No runtime interpreter.** A compromised Python dep can poison
  the fit but not the trader. The fit's output is a small JSON file
  that a human or a linter can inspect before it's committed to
  `models/`.
- **`models/` PRs are reviewed.** Any change to a `models/*.json`
  file is a behavior change and requires a CHANGELOG entry plus
  validation metrics that haven't regressed.
- **`properscoring`, `scipy`, `statsmodels` are mature** with large
  maintainer bases. We avoid niche abandoned packages.

## What this pipeline does NOT do (non-goals)

- **No live Python inference.** If inference needs to happen on a
  per-tick basis (milliseconds), it's in Go. We don't keep a Python
  model server warm.
- **No cross-language IPC.** The boundary is files (JSON) checked
  into git, not sockets or RPC.
- **No ML training in Python we then port to Go.** NGR's predict
  step is three lines of arithmetic. If a future technique needs
  a neural network at predict time, that's a design-doc
  conversation — probably `onnxruntime-go` + an ONNX artifact, but
  we don't need that for NGR / conformal / EVT.

## How to verify this pipeline works end-to-end today

Before writing any fit code, we can **dry-run** the pipeline:

1. Create `testdata/calibration/ngr/fixture-v0-handcrafted.json` by
   hand — coefficients `(a=0, b=1, c=0, d=1)` (identity model) plus
   a handful of inputs + outputs computed with pencil-and-paper.
2. Write `internal/calibration/ngr.go` that loads + predicts.
3. Run the test against the handcrafted fixture — it should pass
   before Python is even installed.

This proves the *shape* of the contract is right even before we
wire up the real fitter.

## References

- ADR-0006 — decision to adopt this pipeline.
- [`uv` documentation](https://docs.astral.sh/uv/).
- [`properscoring`](https://github.com/properscoring/properscoring).
- [`climpred`](https://climpred.readthedocs.io).
- [Scheuerer & Büermann 2014](https://arxiv.org/abs/1407.0058) — NGR.
- [Pangu-Weather reference JSON model format](https://github.com/198808xc/Pangu-Weather)
  — inspiration for the `schema + coefficients + provenance` pattern.
