# Reports Index

Decision-gate evaluations and other formal review artifacts. Reports are
immutable once published; corrections happen via supersession (a new
report with a `_RETRACTED.md` archive of the original), not edits.

## Phase 0.5 — calibration vs market on weather brackets

The Phase 0.5 evaluation went through three iterations as methodology
matured. Read in chronological order to follow the reasoning chain;
read only the **current** entry if you want the latest verdict.

| Date | Report | Status | Verdict |
|---|---|---|---|
| 2026-05-05 | [`floor-estimate-v2.md`](./phase0.5/2026-05-05-floor-estimate-v2.md) | **Current** | Phase 0.5 gate **NOT MET** by current MOS calibration. MOS loses to market in tradeable [35,65)¢ zone with statistically significant Brier delta. |
| 2026-05-02 | [`pipeline-validation.md`](./phase0.5/2026-05-02-pipeline-validation.md) | Superseded | Interim status. Pipeline build + validation; floor verdict deferred. |
| 2026-05-02 | [`floor-estimate_RETRACTED.md`](./phase0.5/2026-05-02-floor-estimate_RETRACTED.md) | **Retracted** | Original first-pass report. Headline "MOS Brier-skill +53.5% vs market" was driven by selection bias toward OTM brackets; preserved with retraction header for audit trail. |

### What changed between iterations

- **v0 → retraction**: original report aggregated across all market-mid
  buckets without stratification. The "+53.5%" was real for the OTM
  population (mid <15¢) but not representative of the tradeable zone.
  After user pushback the report was retracted same-day.
- **v0-retracted → interim status**: interim doc stated what was
  verified (pipeline correctness, MOS shrinks naive overconfidence) and
  what was gated on (n=200-300 with proper stratification + climate
  baseline). No verdict claimed.
- **interim → v2**: with 5.6× more data (n=299 verifiable), a
  climate-shrunk-naive baseline added per Pass 3 §4.b critique, and
  bootstrap 95% CIs on Brier deltas. Verdict became actionable: MOS
  doesn't beat market in the tradeable zone, and doesn't beat shrunk
  there either. ADR-0008's canary fired; ADR-0009 escalates the
  calibration approach.

### Where v3 will live (when it lands)

A v3 report will be produced after either or both of:

- **Track A — ECMWF IFS blend** (per ADR-0009): adds 51-member ECMWF
  ENS to the calibration training set. Free since Oct 2025.
- **Track B — T−3h Bayesian update** (per ADR-0009): collapses forecast
  σ at near-settlement using observed-trajectory ASOS data; targets the
  lead bucket where the market currently wins.

If the v3 report shows the gate is met, the project proceeds to
Phase 1 scope review. If not, ADR-0010 either escalates to Track C
(NOAA GEFS GRIB ensemble historical) or pivots the project framing to
"forecasting research artifact with documented failed trading gate."

## Other report families (placeholder)

None yet. Future report types (postmortems, calibration drift summaries,
microstructure fill audits) will land here with their own subdirectory
and update this index.

## Conventions

- One subdirectory per report family (`phase0.5/`, `postmortems/`, etc.)
- File naming: `YYYY-MM-DD-short-slug.md`
- Retracted reports keep their original filename + `_RETRACTED.md`
  suffix and prepend a retraction header explaining what changed
- Cross-references between reports are explicit links, not "see prior
  report"; future readers should not have to guess which is current
- Each report includes a "Reproducibility" section with concrete
  commands so a fresh reader can re-derive the numbers

## Anti-conventions (what we don't do)

- We do not edit a published report in place. Only supersede.
- We do not delete retracted reports. They are evidence of how the
  reasoning evolved.
- We do not let a report become "current" by implicit recency. The
  current verdict is whichever report this index links as **Current**;
  if no entry is marked Current, no verdict is in force.
