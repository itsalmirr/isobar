# 0012 — Time-source discipline: Clock interface, now-func fields, and a custom lint analyzer

**Status:** Accepted
**Date:** 2026-05-13
**Authors:** isobar team
**Triggered by:** Phase 1 line audit + Bucket A4/A8

## Context

The Phase 1 line audit (see `audit/01-line-audit.md`) found that wall-time
access in `internal/*` was inconsistent and not test-controllable:

- `internal/clock` exposed `Now() Stamp` and `SinceStart()` but kept
  `processStart = time.Now()` as an unexposed package-level constant.
  There was no seam to swap the underlying time source — every
  downstream package that depended on "deterministic clock for tests"
  was making an aspirational claim.
- `internal/calibration/mos/mos.go` injected wall time into deterministic
  math via `FittedAt: time.Now().UTC()` on line 150. Two back-to-back
  fits over identical pairs produced different artifacts on disk —
  identical inputs → divergent outputs. Any byte-diff reproducibility
  check on calibration coefficients was broken by construction.
- `internal/kalshi/auth.go` already used a `now func() time.Time` field
  for its RSA-PSS timestamp generation — a known-good pattern. The rest
  of the kalshi package (`client.go` throttle accounting, ws.go) read
  `time.Now()` directly, with no way for tests to drive timing.
- `internal/openmeteo/client.go` measured per-attempt latency with
  `start := time.Now() / latency := time.Since(start)`. The latency
  shows up in structured logs; tests that wanted to assert "logs the
  right latency value" had no deterministic way to do it.

Five packages (`clock`, `kalshi`, `openmeteo`, `calibration/mos`,
`journal`) were each making their own decision about how to read time,
and four of them were either non-injectable or only partially so. The
reviewer-discipline option ("just don't write `time.Now` in business
logic") was already failing: the audit found five existing violations.

### What "wall-time access" means in this codebase

Three classes of time read appear in production code:

1. **Wall-clock reads** — `time.Now()`, `time.Since(t)`, `time.Until(t)`.
   These ask the OS what civil time it is right now. They are the only
   reads that affect behavior across process boundaries (envelopes,
   timestamps stamped into artifacts, rate-limit accounting). They are
   the ones tests need to control.
2. **Monotonic-anchored scheduling** — `time.NewTicker(d)`,
   `time.After(d)`, `time.AfterFunc(d, fn)`, `time.Sleep(d)`. These
   produce a relative delay; the host OS resolves them against its
   monotonic clock. They are not portable across processes, but they
   are not "wall time" in any meaningful sense and they are not
   stamped into artifacts.
3. **Envelope stamping** — `clock.Now() Stamp` returning paired
   `(wall, mono)` for journal envelopes. Already injection-aware via
   the package's internal `processStart` anchor; the only gap is that
   the anchor itself was unswappable.

This ADR addresses class 1 directly and class 3 indirectly (by adding
an `InitForTest` seam to `internal/clock`). Class 2 is left alone.

## Decision

All wall-time access in `internal/*` packages flows through one of:

1. **`clock.Clock` interface accepted in a constructor** — the preferred
   shape for new code. Production constructs `clock.Wall()` at the top
   of the binary (`cmd/<name>/main.go`) and threads it downward.
   Implemented in this commit via:

   ```go
   package clock

   type Clock interface { Now() time.Time }
   func Wall() Clock                       // production singleton
   func Fake(initial time.Time) *FakeClock // deterministic test clock
   ```

   `FakeClock` exposes `Now()`, `Advance(d time.Duration)`, and
   `Set(t time.Time)`. `Advance` panics on negative durations — tests
   that need to regress the clock construct a fresh `Fake`.

2. **`now func() time.Time` field on a struct, defaulting to `time.Now`**
   — acceptable for existing patterns. This matches what
   `internal/kalshi/auth.go` already did (`Signer.now`). Tests override
   with a `SetNowForTest` helper. Extended to `kalshi.Client` (throttle
   accounting) and `openmeteo.Poller` (per-attempt latency) in this
   commit.

3. **`internal/clock.Now()` for envelope timestamps** — the existing
   convention is preserved. The new `InitForTest(c Clock)` helper lets
   tests reanchor both the wall source and the monotonic origin
   deterministically. Production never calls `InitForTest`.

**Allowlist exceptions** (out of policy, in the lint analyzer's bypass):

- `internal/clock` itself — the package providing the seam can't depend
  on itself for its own implementation.
- `cmd/<name>/main.go` entry points — `time.Now()` and friends are how
  flags like `-date` default to "today", and how `clock.Wall()` is
  constructed at the top of the binary.
- `*_test.go` files — tests freely use `time.Now()` for elapsed-time
  assertions and tape envelope construction; injection is for the code
  under test, not the test harness.
- Lines with `//nolint:clockcheck` and a justification comment. Use
  sparingly; reviewers should reject undocumented exemptions.
- `internal/journal/*` — temporarily exempted while parallel
  A1+A2 work modifies that package. The migration to the now-func field
  pattern is tracked as a follow-up PR; remove this branch from
  `pkgIsExempt` in `tools/clockcheck/clockcheck.go` when it lands.

### Lint enforcement (`tools/clockcheck`)

A real `golang.org/x/tools/go/analysis` analyzer at `tools/clockcheck`
flags any direct `time.Now` / `time.Since` / `time.Until` outside the
allowlist. It type-resolves the receiver against `package time` to
avoid false positives when a local variable shadows the name `time`,
and honors `//nolint:clockcheck` directives (both inline-trailing and
the line immediately preceding the call, per golangci-lint convention).

The analyzer ships with a runnable binary at
`tools/clockcheck/cmd/clockcheck/main.go` (using
`singlechecker.Main`), wired into `make lint` as a prerequisite. CI
runs `make lint`; failing the build is the enforcement mechanism. The
analyzer's own behavior is covered by `analysistest`-based tests with
two fixture packages: one passing (every allowed shape) and one
failing (each banned identifier on its own line).

### Why now (A4 plus A8)

A8 (MOS determinism) and A4 (clock-injection mechanism) share the
same dependency — a real injection seam. Shipping them together was
the product-lead decision (pure-hardening mode); the alternative was
either landing A8 alone (rebuilding the seam locally inside `mos`
for a single field) or A4 alone (no immediate value-delivering
caller proving the seam works). One PR keeps the cross-cutting
change reviewable and ensures the lint analyzer's policy and at
least one in-tree non-trivial caller of the new seam ship together.

## Alternatives considered

- **Global `var now = time.Now` package vars in each package.**
  Rejected. We'd need to remember to swap `now` in every package the
  test touches; missed swaps would silently fall through to wall time
  with no compile-time signal. Footgun-prone.

- **Just write the rule down (review-time discipline).** Rejected. The
  Phase 1 audit found five packages already violating an unwritten
  version of this rule. Reviewers missed them. A custom analyzer
  costs ~200 lines once and catches every future regression.

- **A `Clock` interface threaded into every package, no now-func field
  allowed.** Rejected as too churny for the existing code. The
  `internal/kalshi/auth.go` now-func pattern is fine — it's already
  there, it's tested, and forcing it to a `Clock` interface would
  trip a wider blast radius (signature change to a callsite that
  every other Kalshi REST/WS test depends on). New code should
  prefer the `Clock` interface; existing patterns may stay as
  now-func fields.

- **Use one of the off-the-shelf clock libraries
  (`github.com/benbjohnson/clock`, `tilinna/clock`, etc.).**
  Rejected. None is in the standard library; adding a dependency for
  ~30 lines of code is poor cost/benefit. The standard-library shape
  is also what reviewers expect; a custom interface called `Clock` is
  semantically clearer than importing a third-party `clock.Clock`.

- **Replace `time.Now` everywhere with `clock.Now` (always go through
  the Stamp-producing seam).** Rejected. `clock.Now()` returns a
  `Stamp{Wall, Mono}` — useful for journal envelopes, overkill for
  rate-limit accounting (we only need a `time.Time`). Different
  abstraction levels: `Clock.Now()` is the seam, `clock.Now()` is the
  envelope helper that uses the seam.

## Consequences

### Good

- Five packages get deterministic wall-time injection (`clock`,
  `calibration/mos`, `kalshi`, `openmeteo`; `journal` is the follow-up).
- MOS calibration becomes reproducible: identical pairs → identical
  Coefficients JSON byte-for-byte (new `TestFit_DeterministicAcrossCalls`
  asserts this).
- Future Phase 1 / Phase 6 packages get a documented pattern to follow
  on first commit (no rule to discover after the fact).
- `cmd/bot` (when it lands) inherits the seam: its top-of-binary
  `clock.Wall()` flows downward through the same constructor params
  that the existing tests already exercise with `clock.Fake`.
- One lint rule, one ADR, one place to point at when the next
  reviewer asks "why not just use time.Now here?".

### Bad

- One more constructor parameter on a handful of packages (~5 sites).
- The `Fit(pairs, holdoutDays, clock.Clock)` signature is a hard break
  for any future external caller. Project is solo; blast radius is
  the one production caller (`cmd/calibrate-fit`) and the test file,
  both updated atomically in this PR.
- Contributors must learn the analyzer's policy; first-time
  experience is a build failure with a pointer to this ADR. Mitigated
  by the analyzer's diagnostic text naming both this ADR and the
  three acceptable shapes inline.

### Neutral

- No on-disk format change. The journal envelope shape is unchanged;
  the `clock.Stamp` JSON tag layout is unchanged. Existing tapes
  replay identically.
- No new dependency. The analyzer reuses `golang.org/x/tools` which
  was already in `go.mod` (indirect via tool dependencies).

## Test plan

- **Clock interface tests** (`internal/clock/clock_test.go`): cover
  Wall, Fake initial / Advance / Advance(0) / Advance(negative)-panic /
  Set / concurrent access, plus the InitForTest swap-and-restore for
  the Stamp-producing `Now()` and `SinceStart()` entry points.
- **MOS regression** (`internal/calibration/mos/mos_test.go`):
  `TestFit_DeterministicAcrossCalls` marshals two Coefficients produced
  from the same `pairs` with the same `clock.Fake` and asserts
  `bytes.Equal` on the JSON output. `encoding/json` sorts map keys, so
  the `Bins` map cannot perturb the comparison. Plus
  `TestFit_PanicsOnNilClock` documents the explicit-parameter rule.
- **Kalshi / Open-Meteo** (`internal/kalshi/client_test.go`,
  `internal/openmeteo/client_retry_test.go`): existing tests pass under
  the new field plumbing; no behavior change. Two `Client{...}`
  literals in tests gained a `now: time.Now` field.
- **Analyzer self-tests** (`tools/clockcheck/clockcheck_test.go`):
  uses `analysistest.TestData()` with two fixture packages:
  - `testdata/src/passing` — every allowed shape (now-func field,
    `//nolint:clockcheck` suppression, `time.NewTicker` / `After` /
    `Sleep`), expected zero diagnostics.
  - `testdata/src/failing` — direct `time.Now`, `time.Since`,
    `time.Until` each on their own line, expected one diagnostic each.
  - Plus `commentMatchesNolint` and `isCmdMain` unit tests covering
    the lookup logic directly.
- **Lint gate**: `make lint` (which now depends on `make clockcheck`)
  reports zero issues across `./internal/...` after the migration. CI
  enforces this; failing the build is the enforcement.

## Reversal cost

Low to moderate.

- The `clock.Clock` interface and `clock.Wall()` / `clock.Fake()`
  helpers are additive; removing them is a delete.
- The MOS `Fit(..., clock.Clock)` signature is a hard break; reversing
  it would require updating `cmd/calibrate-fit` and the test file,
  ~10 lines. Acceptable.
- The `kalshi.Client.now` and `openmeteo.Poller.now` fields are
  additive; removing them and defaulting to `time.Now` is a delete.
- The lint analyzer is in `tools/`, independent of the production
  build; removing it is a directory delete plus a one-line Makefile
  edit. The ADR text would need a status update to "Superseded by ...".

If a future change wants to relax the policy (e.g. allow
`time.Since(t)` outside the allowlist on the argument that it's
already passing a relative duration), the analyzer's `bannedNames`
map is the single point of edit.

## References

- `internal/clock/clock.go` — Clock interface, Wall/Fake, InitForTest
- `internal/clock/clock_test.go` — interface + Fake + InitForTest tests
- `internal/calibration/mos/mos.go` — Fit signature change (A8)
- `internal/calibration/mos/mos_test.go` — determinism regression test
- `cmd/calibrate-fit/main.go` — production caller wiring `clock.Wall()`
- `internal/kalshi/client.go` — now-func field on Client
- `internal/openmeteo/client.go` — now-func field on Poller
- `tools/clockcheck/` — the analyzer, runner, fixtures, and tests
- `Makefile` — `lint: clockcheck` dependency
- `audit/01-line-audit.md` — Phase 1 finding that triggered this work
- ADR-0011 — journal-backpressure work that is in flight and that
  this ADR's analyzer temporarily exempts; the journal migration is
  the follow-up that removes that exemption.
