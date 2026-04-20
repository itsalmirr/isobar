# isobar docs

This directory is the canonical source of truth for project direction,
strategy, architecture, and ops. Code is primary; these docs are the
contract that explains *why* the code is what it is.

## Index

| doc                                     | what it answers                                                       |
| --------------------------------------- | --------------------------------------------------------------------- |
| [ROADMAP.md](./ROADMAP.md)              | Where we're going, phase by phase, with exit criteria                 |
| [STRATEGY.md](./STRATEGY.md)            | The edge thesis + execution doctrine                                  |
| [ARCHITECTURE.md](./ARCHITECTURE.md)    | Package layout, invariants, runtime shape                             |
| [TESTING.md](./TESTING.md)              | How we test, what we require, coverage floors                         |
| [OPERATIONS.md](./OPERATIONS.md)        | Runbook: start/stop, health signals, troubleshooting                  |
| [RISK.md](./RISK.md)                    | The risk-control hierarchy (L0–L4)                                    |
| [GLOSSARY.md](./GLOSSARY.md)            | Terms we use, defined once                                            |
| [RESEARCH_PIPELINE.md](./RESEARCH_PIPELINE.md) | The Python ↔ Go boundary contract (ADR-0006 implementation)    |
| [design/](./design/)                    | Pre-implementation design docs — lock APIs + test vectors before code |
| [research/](./research/)                | Research reports (8) + `SYNTHESIS.md` decision doc                    |
| [decisions/](./decisions/)              | Architecture Decision Records (ADRs) — why choices were made          |
| [../CHANGELOG.md](../CHANGELOG.md)      | Running log of every behavior change                                  |
| [../CONTRIBUTING.md](../CONTRIBUTING.md)| PR checklist + commit/ADR/CHANGELOG conventions                       |

## Conventions

- **Docs are living.** A doc that no longer matches the code is a bug.
  PRs that change behavior update their doc in the same commit.
- **Every non-obvious decision** becomes an ADR under
  `docs/decisions/NNNN-slug.md`. See `docs/decisions/README.md` for the
  template and philosophy.
- **Every behavior change** updates `CHANGELOG.md` under `[Unreleased]`
  in the same PR. Format: [Keep a Changelog 1.1.0](https://keepachangelog.com/).
- **Every incident** produces a `docs/postmortems/NNNN-slug.md` within
  48 hours.
- **Phase reports** (Phase 0.5 gate, Phase 2 paper summary, etc.) live
  under `docs/reports/<phase>/<date>/`.
- **Research agents' raw outputs** are preserved verbatim under
  `docs/research/`. Conclusions are consolidated in
  `docs/research/SYNTHESIS.md` — that's the decision doc.

## Reading order

If you're new to the project:

1. Top-level `README.md` for a 2-minute overview.
2. `docs/research/SYNTHESIS.md` — why the current strategy is what it is.
3. `STRATEGY.md` — what edge are we chasing and why.
4. `ARCHITECTURE.md` — how the code expresses that.
5. `ROADMAP.md` — where it's going next.
6. `CHANGELOG.md` at repo root — what changed recently.
7. `docs/decisions/` — any decision you find surprising, read its ADR.
8. `OPERATIONS.md` + `RISK.md` — before touching anything live.
9. `TESTING.md` + `CONTRIBUTING.md` — before writing a PR.
10. `GLOSSARY.md` — whenever a term is unclear.
