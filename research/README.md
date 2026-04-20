# Research / calibration pipeline (Python)

This directory is the Python half of isobar's dual-language architecture
(see ADR-0006 and `docs/RESEARCH_PIPELINE.md`). It holds **offline
model-fitting code and Jupyter exploration** — nothing here runs at
trade time.

## Status

Stubbed. Contents will grow in W2 of the 12-week plan:

- `pyproject.toml` — `uv`-managed dependency spec.
- `uv.lock` — committed lockfile.
- `.python-version` — pins Python 3.12.
- `calibration/fit_ngr.py` — NGR/EMOS fitting script.
- `calibration/generate_fixtures.py` — emits `testdata/` fixtures the
  Go predict code tests against.
- `notebooks/` — Jupyter exploration (reliability diagrams, EDA).

## Setup (once this has content)

Install [`uv`](https://docs.astral.sh/uv/) first. Then:

```bash
cd research
uv sync               # installs everything from uv.lock
uv run python -V      # → Python 3.12.x
uv run jupyter lab    # for exploration
```

## Boundary contract

**Output: JSON artifacts in `models/`** (repo root). Every artifact
carries a `schema` field of the form `isobar.<name>/vN` and full
provenance (git SHA of the fitter, archive SHA-256, package versions).

**Verification: golden fixtures in `testdata/`** (repo root). Python
generates them; Go's `internal/calibration/*_test.go` consumes them
and asserts per-output equality to `1e-9`. If Python and Go disagree,
the build fails.

Full contract in `docs/RESEARCH_PIPELINE.md`.

## Non-goals

- **No production runtime code.** Python never runs on the trading VM.
  If you find yourself writing a Flask endpoint or a gRPC service
  here, stop and reread ADR-0006.
- **No ad-hoc scripts outside `uv run`.** Everything uses the pinned
  lockfile.
- **No data checked in.** The archive (`research/archive/`) is
  gitignored; it's regenerated from the public APIs by
  `cmd/fetch-archive` (Go).
