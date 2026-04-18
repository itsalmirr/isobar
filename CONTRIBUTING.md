# Contributing to isobar

This repo enforces a small set of disciplines that make returning to
the codebase six months later painless. None of them cost much at the
time of writing; all of them pay back later.

## Quick checklist for a PR

- [ ] `make all` green (lint + vet + race-tested tests + build).
- [ ] `CHANGELOG.md` updated under `[Unreleased]` if the change is
      user-visible or behavior-affecting. Pure typo/whitespace PRs
      are exempt.
- [ ] New ADR under `docs/decisions/` if the change picks between
      genuine alternatives (added dependency, architectural split,
      strategy/risk parameter change).
- [ ] Docs updated in the same commit as behavior — never in a
      follow-up "fixup docs" PR.
- [ ] Property test or regression test for any bug fix.

## Commit message conventions

[Conventional Commits](https://www.conventionalcommits.org/) flavor,
short and functional:

```
<type>(<scope>): <imperative summary under 70 chars>

<body: why, not what. reference ADR/research doc numbers where
relevant. wrap at 72 chars.>
```

Types we use:

| type       | when                                                       |
| ---------- | ---------------------------------------------------------- |
| `feat`     | new capability visible to a user or operator               |
| `fix`      | bug fix (include regression test reference in body)        |
| `refactor` | internal restructuring, no behavior change                 |
| `perf`     | behavior-identical speedup                                 |
| `docs`     | documentation only                                         |
| `test`     | test-only changes                                          |
| `chore`    | build / CI / tooling                                       |
| `research` | research-doc additions (`docs/research/`)                  |

Examples from the project history:

- `W1: complete research pivot — kill BTC, calibrate weather, add hurricane`
- `refactor: extract internal/retry; fix throttle lock; reuse Kalshi discovery`
- `docs(research): alt markets + dashboard architecture (4/8)`

Phase-gated tags: `vPHASE.X.Y` at phase boundaries (see
`docs/ROADMAP.md`).

## CHANGELOG discipline

`CHANGELOG.md` at repo root follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).
Update the `[Unreleased]` section in the same PR that makes the
change. Release cuts move `[Unreleased]` contents into a new
`[X.Y.Z] — YYYY-MM-DD` section and add a new empty `[Unreleased]`.

Allowed section headers (in order):

`Added` · `Changed` · `Fixed` · `Removed` · `Deprecated` ·
`Security` · `Docs` · `Research`

Bullet style: imperative, past-tense, ≤2 lines per bullet. Link
commits only when not obvious.

## ADR (Architecture Decision Record) discipline

Put any **non-obvious** decision under `docs/decisions/NNNN-slug.md`.
See `docs/decisions/README.md` for the template and why we do it.

Rule of thumb: if a new contributor would ask "why did they do X
instead of Y?" and the answer takes more than one sentence, that
question needs an ADR.

ADRs are **immutable once accepted**. To reverse course, write a
new ADR that supersedes the old, and cross-link both.

## Test discipline

See `docs/TESTING.md` for the full contract. The short version:

- `go test -race -count=1` is the canonical runner. Flaky tests are
  bugs — file, bisect, fix.
- No test reaches the real network. Mock with `httptest` +
  `coder/websocket.Accept`.
- No `time.Sleep(X)` as synchronization. Use channels or
  `Wait(ctx)`.
- Timeouts on every test.
- Fresh RSA keys in-test (`rsa.GenerateKey(rand.Reader, 2048)`) —
  no checked-in PEMs.
- Coverage floors in `docs/TESTING.md` are **floors**, not ceilings.

## Risk / strategy discipline

Per `docs/RISK.md` §"Risk-change procedure", any change to:

- A cap (per-ticker / per-series / portfolio / max-order).
- A precondition (edge floor, Lyapunov horizon, staleness cap).
- A disabling of a control.

requires:

1. A new ADR under `docs/decisions/`.
2. Updated `docs/RISK.md` in the same commit.
3. A property test that exercises the new boundary.
4. 24-hour cooling-off period before merge (even solo).

## Architecture discipline

- **`internal/` only** until cross-binary import is demonstrably
  needed. Compiler enforces the boundary.
- **Stdlib first.** Every third-party import is an audit burden.
  The current allowlist is: `coder/websocket`, `yaml.v3`,
  `golang.org/x/sync`, `testify` (test-only), `modernc.org/sqlite`
  (W11+). New deps require an ADR.
- **Errors wrap with `%w`** (`fmt.Errorf("ctx: %w", err)`).
  Enforced by `errorlint`.
- **`log/slog`** is the only logger. No `fmt.Println` in production
  paths.
- **Every long-lived goroutine** either lives in an `errgroup.Group`
  or is owned by a struct with a documented `Close()`.

## What skips the checklist

These bypass CHANGELOG / ADR / tests:

- Pure typo fixes in comments or docs.
- Formatting-only changes from `go tool gofumpt`.
- CHANGELOG / ADR doc updates *about* existing accepted changes.

When in doubt, err on the side of documenting. The cost is minutes;
the reward is months later.

## Help

- `/help` — Claude Code help (if you're using the CLI for work).
- Report issues at https://github.com/anthropics/claude-code/issues
  (Claude Code tooling only).
- isobar-specific questions: open an issue on this repo, or add a
  `?` entry to the CHANGELOG `[Unreleased]` section under "Research"
  for items that need a research agent to answer.
