# isobar docs

This directory is the canonical source of truth for project direction,
strategy, architecture, and ops. Code is primary; these docs are the
contract that explains *why* the code is what it is.

## Index

| doc                                   | what it answers                               |
| ------------------------------------- | --------------------------------------------- |
| [ROADMAP.md](./ROADMAP.md)            | Where we're going, phase by phase, with exit criteria |
| [STRATEGY.md](./STRATEGY.md)          | The edge thesis + execution doctrine          |
| [ARCHITECTURE.md](./ARCHITECTURE.md)  | Package layout, invariants, runtime shape     |
| [TESTING.md](./TESTING.md)            | How we test, what we require, coverage floors |
| [OPERATIONS.md](./OPERATIONS.md)      | Runbook: start/stop, health signals, troubleshooting |
| [RISK.md](./RISK.md)                  | The risk-control hierarchy (L0–L4)            |
| [GLOSSARY.md](./GLOSSARY.md)          | Terms we use, defined once                    |

## Conventions

- **Docs are living.** A doc that no longer matches the code is a bug.
  PRs that change behavior update their doc in the same commit.
- **Every non-obvious decision** lives under `docs/decisions/<date>-<slug>.md`.
- **Every incident** produces a `docs/postmortems/<date>-<slug>.md` within
  48 hours.
- **Phase reports** (Phase 0.5 gate, Phase 2 paper summary, etc.) live
  under `docs/reports/<phase>/<date>/`.

## Reading order

If you're new to the project:

1. Top-level `README.md` for a 2-minute overview.
2. `STRATEGY.md` — what edge are we chasing and why.
3. `ARCHITECTURE.md` — how the code expresses that.
4. `ROADMAP.md` — where it's going next.
5. `OPERATIONS.md` + `RISK.md` — before touching anything live.
6. `TESTING.md` — before writing a PR.
7. `GLOSSARY.md` — whenever a term is unclear.
