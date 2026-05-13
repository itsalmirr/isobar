# Decision records (ADRs)

Every non-obvious decision on this project is recorded as an
Architecture Decision Record (ADR) in this directory. The format is
[Michael Nygard's](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions)
with minor adaptations: short, dated, actionable, and **immutable once
accepted** — if we reverse course, we write a new ADR that supersedes
the old one rather than editing history.

## Why this discipline

- **Traceability.** A reader returning in 6 months should be able to
  answer "why did we do X?" in under 2 minutes of reading.
- **Reversibility check.** Writing down the alternatives forces us to
  examine the reversal cost before committing.
- **Not silent.** Strategy and risk-level changes especially must be
  timestamped against the state of knowledge at the time.

## Index

| ADR  | Date       | Title                                            | Status     |
| ---- | ---------- | ------------------------------------------------ | ---------- |
| [0001](./0001-research-driven-pivot.md) | 2026-04-18 | Pivot strategy after 8-agent research wave | Accepted |
| [0002](./0002-kill-btc-hourly.md)       | 2026-04-18 | Kill BTC-hourly, delete `internal/coinbase` | Accepted |
| [0003](./0003-dashboard-stack.md)       | 2026-04-18 | Dashboard = stdlib Go + htmx + SSE + Lightweight Charts + SQLite WAL | Accepted |
| [0004](./0004-two-signal-scope.md)      | 2026-04-18 | Scope to two signals (weather-calibrated + hurricane-landfall); defer tail-MM | Accepted |
| [0005](./0005-extract-retry.md)         | 2026-04-18 | Extract `internal/retry`; fix kalshi throttle; promote DiscoverOpenTickers | Accepted |
| [0006](./0006-peek-probe-kalshi-naming.md) | 2026-04-22 | Keep `cmd/peek` as an ongoing Kalshi catalog probe | Accepted |
| [0007](./0007-calibration-ground-truth-cli.md) | 2026-04-29 | Use NWS CLI bulletins as the calibration y-target | Accepted |
| [0008](./0008-deterministic-historical-forecast.md) | 2026-05-02 | Use deterministic GFS historical forecasts for first-pass NGR/MOS | Accepted (canary fired — see 0009) |
| [0009](./0009-mos-canary-fired-escalate-calibration.md) | 2026-05-05 | MOS canary fired; escalate calibration via ECMWF blend or T−3h Bayesian | Accepted |
| [0010](./0010-t-minus-3h-bayesian-update.md) | 2026-05-13 | T−3h Bayesian update of MOS prior via intraday observations | Proposed |
| [0011](./0011-journal-backpressure.md) | 2026-05-13 | Journal backpressure: drop with counter, sampled debug log, alarm hook | Accepted |
| [0012](./0012-time-source-discipline.md) | 2026-05-13 | Time-source discipline: Clock interface, now-func fields, custom lint analyzer | Accepted |

## Template

```markdown
# NNNN — <Short imperative title>

**Status:** Proposed | Accepted | Superseded by ADR-XXXX | Deprecated
**Date:** YYYY-MM-DD
**Authors:** <name>
**Supersedes:** ADR-XXXX (optional)

## Context

What forces are in play? What did we know before? What data / research
motivates revisiting this?

## Decision

One-paragraph description of the decision. Present tense, active voice.
"We will X. We will not Y."

## Alternatives considered

- **Option A** — what it would be, why we didn't pick it, what we'd
  lose.
- **Option B** — same.

## Consequences

- Good (expected benefits, what becomes easier).
- Bad (what we're giving up, what risks we're now carrying).
- Neutral (side effects worth noting).

## Reversal cost

If this turns out to be wrong, how hard is it to back out? What's
the canary that would tell us we're wrong?

## References

- Linked research docs / papers / issues / PRs.
```

## How to add one

1. Copy the template above into `docs/decisions/NNNN-slug.md` (next
   integer, kebab-case slug).
2. Set status to `Proposed`.
3. Link it in the Index table above.
4. Open a PR. Reviewers focus on Context and Consequences, not
   wording.
5. On merge, flip status to `Accepted` in a follow-up commit (or
   the same PR, at the author's discretion).
6. Never delete an ADR. Supersede it with a new one and cross-link.
