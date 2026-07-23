# 0013 — Retire the PR#9 paper-trader/scoring lineage during Bucket A consolidation

- **Date**: 2026-05-13
- **Status**: Accepted
- **Decider**: Solo operator + audit
- **Audit context**: Bucket A closeout (`audit/01-line-audit.md` appendix)

## Context

Origin `main` carried 11 commits of cmd/bot-precursor capability work
merged before the audit began, via PRs #7–#9. The retired surface was:

- `cmd/paper-trader` — live edge engine + JSONL bet ledger
- `cmd/fillability` — analyzes which trades would have filled
- `cmd/score` + `cmd/score/score_test.go` — backtest scoring
- `cmd/settle` — daily Kalshi-resolution reconciler
- `cmd/backfill-score` — historical Kalshi-resolved P&L over fillability candidates
- `internal/papertrade` — paper trading state machine + JSONL bet ledger
- `internal/edge/edge.go` — top-level orchestration helpers used by the above
- `deploy/systemd/isobar-paper-trader.service`, `isobar-settle.service`, `isobar-settle.timer`
- `deploy/README.md`, `deploy/install.sh`

The 11 commits, in chronological order:

| SHA       | Subject                                                                |
|-----------|------------------------------------------------------------------------|
| `89555f9` | feat(paper-trader): live edge engine + JSONL bet ledger                |
| `956bb24` | fix(paper-trader): race-safe scalar snapshots + boot-time dedup recovery |
| `ed4bbbf` | feat(settle): daily reconciler — Kalshi resolution → bet outcomes      |
| `9310702` | feat: corner filter + cmd/score + cmd/fillability + deploy artifacts    |
| `274f2cb` | feat(backfill-score): historical Kalshi-resolved P&L over fillability candidates |
| `392f48c` | fix(edge,scripts): poll-pair sort, ticker error gating, fetch threshold, parse flags |
| `7ac4c65` | fix(edge,scripts,build): five Copilot review-comment follow-ups on PR #7 |
| `18cbd97` | chore(edge): cleanup pass — bug fixes, dead code removal, doc/test sharpening |
| `60788f3` | Merge pull request #8 from nolmelabs/claude/code-review-discussion-EpsqP |
| `bb4743a` | Merge branch 'main' into claude/cool-newton-953683                     |
| `6a6530a` | Merge pull request #9 from nolmelabs/claude/cool-newton-953683          |

These represent capability work — paper trading engine, fillability
analysis, scoring, daily reconciliation, backfill scoring — that the
audit explicitly placed out of scope per the **"no new capability"**
framing of the Phase 1 response. Local `main` had been deliberately
diverged from this lineage for many turns of conscious audit work
before the merge attempt.

During Bucket A, two interrelated deletions broke API compatibility
between the audit lineage and the PR#9 lineage:

- **A9** deleted `internal/market/{ticker.go, ticker_test.go}` — the
  dead `market.ParseTicker` / `market.Ticker` / `market.BracketType` /
  `market.BracketAbove` / `market.BracketBelow` / `market.TempVar` /
  `market.RoundTripFeeCents` symbols. The PR#9 binaries called these
  directly. After A9, no consumer outside the deleted file itself
  referenced them; `internal/edge`'s `ParseTicker` / `Spec` /
  `BracketBetween` / `BracketThreshold` is the canonical parser, with
  different (corrected) semantics.
- The earlier consolidation refactor (`aa176e4 refactor(edge):
  consolidate fee/scanner/ensemble into internal/edge`) had already
  removed `openmeteo.NumMembers` in favor of `Poll.MemberCount()` /
  the dynamic `Poll.Members` map.

These deletions were both well-motivated correctness fixes. The
PR#9 binaries, written against the pre-consolidation API, no longer
compiled in the post-Bucket-A tree (verified: 11 `undefined:` errors
in `internal/edge/edge.go` alone before the merge attempt was aborted).

Three real paths to "combine into one branch — `main`" were considered
at force-push time:

- **B**: Update PR#9 binaries in place to the new APIs. Rejected —
  violates the no-new-features framing; ~200 LOC of speculative
  migration for binaries with no current consumer.
- **C**: Revert A9 (restore `internal/market/ticker.go`). Rejected —
  undoes an approved Bucket A correctness fix; brings back the
  duplicated-`ParseTicker` ambiguity A9 explicitly closed.
- **D**: Open a PR on GitHub from `audit/2026-05-13-post-bucket-a`
  to `main` and resolve via the GitHub UI. Valid but slower; the audit
  was at a clean breaking point.

## Decision

**Retire the PR#9 lineage from `main`'s direct lineage** at the
force-push point on 2026-05-13:

```
+ 6a6530a...bfa7f4b main -> main (forced update)
```

The PR#9 capability work is **out of scope for the audit** and is
**explicitly deferred** until the post-audit decision to unblock
`cmd/bot` and the live-money path. When that decision is made, the
work returns via cherry-pick + migration to the new `internal/edge`
API (recovery paths below).

The audit lineage — `cmd/edge-scan`, `cmd/calibrate-{parse-cli,fit,
bayesian}`, `cmd/tape`, `cmd/analyze`, `cmd/peek`, plus the
`internal/{edge,calibration/*,journal,kalshi,replay,...}` packages —
is canonical on `origin/main` post-force-push.

## Recovery paths

Nothing is permanently lost. The PR#9 lineage is reachable from
multiple sources, in order of preference:

1. **GitHub PR refs** (kept indefinitely):
   - `refs/pull/9/head` — full PR#9 head
   - `refs/pull/8/head` — full PR#8 head
   - `refs/pull/7/head` — full PR#7 head
   Each is fetchable via `git fetch origin pull/N/head:<branch>`.

2. **Git commit SHAs** (kept ~30 days in origin's reflog; longer in
   any local clone that fetched `origin/main` before 2026-05-13):
   the 11 SHAs in the Context table above.

3. **The two audit backup branches on origin**:
   - `audit/2026-05-13` (sha `205de5f`) — pre-Bucket-A snapshot. Note:
     this snapshot does NOT itself contain PR#9 work; local `main`
     was already diverged from PR#9 when the snapshot was taken.
     Useful only as the pre-Bucket-A audit state.
   - `audit/2026-05-13-post-bucket-a` (sha `bfa7f4b`) — identical
     to current `main` post-force-push. The "after" snapshot.

4. **Any developer's local clone** that has `origin/main` fetched
   before 2026-05-13 retains the full 11-commit lineage in their
   reflog.

## Consequences

### When `cmd/bot` is unblocked

A future ADR (post-Phase-2, post-canary-cleared gate, when the
no-new-features framing is lifted) will unblock the live-money path.
At that point, the PR#9 work returns via:

- `git cherry-pick refs/pull/9/head` or equivalent
- **Mandatory migration** off the deleted symbols to current APIs:
  - `market.ParseTicker` / `market.Ticker` → `edge.ParseTicker` /
    `edge.Spec` (note: semantics differ; the dead `market` versions
    had `BracketAbove`/`BracketBelow` keyed as one-sided inequalities,
    while `edge` uses `BracketBetween [Lo, Hi)` + `BracketThreshold`)
  - `market.BracketAbove` / `BracketBelow` → `edge.BracketThreshold`
    (or `edge.BracketBetween` for two-sided)
  - `market.TempVar` → derive from `edge.Spec.IsHigh()`
  - `market.BracketType` → `edge.BracketKind`
  - `market.RoundTripFeeCents` → `edge.FeePerContractCents` (note:
    A9's audit found the round-trip variant was unused; double-check
    whether the migration needs a 2× variant restored)
  - `openmeteo.NumMembers` (constant) → `Poll.MemberCount()` (dynamic)

- **Schema-versioning policy from ADR-0014+** (forthcoming Phase 2)
  applies to any persisted state from `internal/papertrade`. The
  journal envelope `"v": "isobar.journal/v1"` field added by A1+A2/S4
  is already in place; paper-trader's JSONL bet ledger needs its own
  versioning decision under the Phase 2 schema-migration policy.

### During the gap (now → cmd/bot unblock)

- Audit-track tools (`cmd/edge-scan`, `cmd/calibrate-*`, `cmd/tape`,
  `cmd/analyze`) remain in main and continue to ship.
- The paper trading + scoring + settlement capabilities are
  unavailable from `origin/main` directly. Anyone using `origin/main`
  expecting these will see them gone.
- Mitigations: the audit branches preserve the prior state; PR refs
  on GitHub indefinitely; this ADR documents the recovery paths.

### Documentation and cross-references

- `audit/01-line-audit.md` — full Bucket A status (appendix)
- `audit/01b-prior-review.md` — prior-art review; cross-linked to
  this ADR
- A9 commit `169da3b` — `chore(market): delete dead ParseTicker/Ticker`
- Force-push artifact: `git reflog show origin/main` will reveal the
  `6a6530a → bfa7f4b` transition for ~30 days

## Alternatives considered

### B — Update PR#9 binaries to new APIs in place

Migrate `paper-trader`, `fillability`, `score`, `settle`,
`backfill-score`, `internal/papertrade`, `internal/edge/edge.go` to
use the new `internal/edge` API. ~200 LOC of changes across 7+
files. **Rejected**:

- Violates the no-new-features framing — these binaries were
  explicitly out of scope.
- Speculative work — no consumer requested the migration during the
  audit; "fix something you don't currently exercise" is anti-pattern.
- Would block the Bucket A close on indefinite migration effort.

### C — Revert A9 (restore `internal/market/ticker.go`)

Bring back the deleted dead code so PR#9 compiles. **Rejected**:

- Undoes an approved Bucket A correctness fix.
- Restores the duplicate-`ParseTicker` ambiguity (one in `market`
  with inverted semantics, one in `edge`) that A9 explicitly closed.
- Future contributors hitting two `ParseTicker` implementations would
  have to re-litigate which is canonical — exactly the institutional-
  memory failure mode A9 fixed.

### D — Open PR on GitHub, resolve via UI

Push to `audit/2026-05-13-post-bucket-a` (already done) + open a PR
to `main`. Resolve all conflicts via GitHub's UI. **Valid but
slower**:

- The audit was at a clean breaking point; carrying a 69-vs-11
  diverged state on a feature branch for an indefinite review period
  is its own kind of risk.
- All the same migration questions (B vs C) would re-emerge in the
  PR review; this ADR documents the decision either way.

Option D remains the right move if the operator later wants to
revisit and re-merge the PR#9 work selectively. The PR refs make
that mechanically straightforward.

### A — Force-push local main to origin/main (this ADR)

The chosen path. Force-push precedent from the Phase 2 response:
*"Force-push to `main` requires an explicit 'force-push authorized'
line in my prior message."* That precedent applies going forward;
this ADR records the one instance where it was authorized, and why.

## Reversal cost

- **Low** for the next ~30 days while origin's reflog still holds the
  retired commits and developer clones still have them in their own
  reflogs.
- **Medium** at 30 days–6 months: PR refs and the audit branches
  remain, but recovery is one `git fetch refs/pull/9/head:rescue-pr9`
  + cherry-pick + migration.
- **Higher** at 6+ months: same recovery path, but the migration
  surface grows as `internal/edge` continues to evolve in Phase 2 and
  beyond. Pair this ADR with the eventual `cmd/bot` ADR to lock the
  migration path before it goes stale.

## References

- Phase 1 response framing: "no new capability ships"
- A9 commit: `169da3b chore(market): delete dead ParseTicker/Ticker`
- Force-push event: `+ 6a6530a...bfa7f4b main -> main (forced update)`
  on 2026-05-13
- `audit/01-line-audit.md` Bucket A appendix
- `audit/01b-prior-review.md` (origin-unknown prior review, cross-
  linked to this ADR)
