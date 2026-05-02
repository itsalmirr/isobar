# 0007 — Use NWS CLI bulletins as the calibration y-target

**Status:** Accepted
**Date:** 2026-04-29
**Authors:** isobar team

## Context

Phase 0.5 needs a paired calibration training set — model forecast `x`
against observed truth `y` over a fixed historical window — so the NGR/EMOS
fits in `weather-calibrated` (see ADR-0001) have something to regress
against. The y-target has to be **the same number Kalshi will use to
resolve the contract**, otherwise we are systematically biasing every fit.

Kalshi's daily-temperature contracts (`KXHIGH{CITY}`, `KXLOWT{CITY}` —
see ADR-0006 for the naming) resolve on the **NWS CLI daily climate
report**, a per-WFO AFOS text bulletin issued the morning after each
civil date. Two ways to get historical TMAX/TMIN:

1. **Aggregate hourly ASOS observations.** One curl against
   `mesonet.agron.iastate.edu/cgi-bin/request/asos.py` per station, ~21k
   rows per station for a 2y window, easy aggregation to local-time daily
   TMAX/TMIN.
2. **Parse the actual CLI bulletins.** AFOS archive zip per station via
   `cgi-bin/afos/retrieve.py?fmt=zip`, ~1500 text bulletins per station
   over 2y, requires a parser.

These produce different numbers. CLI uses **1-minute ASOS samples** under
the hood; our hourly fetch samples once at HH:51, so for a peak that
occurs at 14:23 we record the value at 13:51 or 14:51 — whichever was
warmer, but neither is the actual peak. CLI also rounds to whole degrees
and applies WFO QC (occasionally hand-adjusted). The structural bias of
hourly-derived TMAX/TMIN vs. CLI is **O(1°F)** typically, occasionally
larger.

For NGR specifically this matters. The fit minimises CRPS against y; if
y is biased by ~0.5–1°F structurally, every regression coefficient
absorbs that bias, every bracket-boundary probability shifts by a few
percent, and the calibrated edge we are trying to capture is exactly in
that band. The fee-aware edge floor in ADR-0001 is ~5¢; a 0.5°F shift
on a steep CDF can be that whole budget.

## Decision

The calibration data tree is split into two parallel subtrees with
different roles:

- **`data/calibration/cli/`** — raw NWS CLI bulletin zips, one per
  station, fetched by `scripts/calibration-fetch-cli.sh`. A Go parser
  (next step) extracts TMAX/TMIN per (station, valid_date) into
  `data/calibration/obs/K{STATION}.csv`. **This file is the canonical
  y-target for NGR.**
- **`data/calibration/obs/K{STATION}-hourly.csv`** — supplementary
  hourly ASOS, fetched by `scripts/calibration-fetch-obs.sh`. Used for:
  (a) the intraday Bayesian update at T−3h that ADR-0001 specifies (we
  need the recent diurnal trajectory, which CLI doesn't provide), (b)
  cross-checking the CLI parser against an independent derivation, (c)
  any feature engineering that wants the diurnal curve.

Both fetchers share conventions (`--help`, `--data`, `--start`/`--end`,
exit 0/1/2, atomic writes, per-station error isolation, MANIFEST.json).
The window is hardcoded at 2024-04-29 → 2026-04-28 (2y ending the day
before today) for training-set reproducibility; overrides exist but we
do not change defaults without a follow-up ADR.

## Alternatives considered

- **ASOS-derived TMAX/TMIN as the y-target, skip CLI.** Rejected. The
  whole point of the `weather-calibrated` signal is to remove the
  systematic bias of the ensemble. Introducing a fresh systematic bias
  in y, on the order of the edge we are trying to capture, would
  undermine the entire calibration project. If we are going to fit NGR
  we need the right y.
- **Use IEM `/api/1/daily.{fmt}` daily summary.** Rejected. The IEM
  daily summary is convenient (CSV out, no parser needed) but its
  derivation is not documented as matching CLI exactly; it appears to
  be ASOS-derived itself, in which case we have solved nothing. Audit
  trail is also worse — we would not be able to point at the exact
  bulletin Kalshi resolved on.
- **Defer CLI to a Phase 0.6 task; ship NGR on ASOS now.** Rejected.
  Cheaper to do CLI now (~half-day for the parser) than to run
  miscalibrated production for weeks, accumulate misattributed P&L, and
  then reframe historical results once we fix it. Also: the `obs/` API
  surface (one CSV per station, daily TMAX/TMIN) does not change
  between ASOS-derived and CLI-derived, so writing it to take CLI now
  costs nothing structurally.
- **Fetch raw CLI text via the live NWS API.** Rejected. The live NWS
  endpoints serve current bulletins only; archive lives at IEM. We are
  already on IEM for ASOS, so one more endpoint at the same host is no
  new dependency.

## Consequences

Good:

- y-target matches Kalshi resolution exactly. NGR has no structural
  bias from the wrong target.
- Hourly ASOS still pays for itself: intraday update path needs it,
  and cross-derivation gives us a free sanity check on the CLI parser.
- Two fetchers are independent — a Mesonet outage on one endpoint does
  not block the other. Both emit `MANIFEST.json` with sha256 +
  date-span, so downstream code can verify input integrity.
- AFOS archive is stable historical infrastructure. Re-fetching the
  same window gives byte-identical data (modulo issuance corrections),
  so a calibration retrain is reproducible.

Bad:

- ~Half-day to write a CLI text parser (it is regular but not trivial
  — multi-issuance-per-day, occasional preliminary-vs-corrected
  reissues, station-name-in-header rather than in PIL).
- ~30 MB on disk per 2y window (4 zips × ~7 MB each + hourly CSVs).
  Trivial.
- Maintainer must remember that `data/calibration/obs/K*.csv`
  (canonical) and `data/calibration/obs/K*-hourly.csv` (supplementary)
  are different things. The README in `data/calibration/` (TBD) will
  say so explicitly.

Neutral:

- CLI bulletins are issued ~2× per day per station (preliminary
  morning + corrected late afternoon). Parser will use the **latest
  issuance** for each valid date, since corrections supersede.
- Stations and the window are fixed by `STATIONS=(NYC MIA LAX ORD)` in
  both scripts. The C2 question (whether `KXLOWTNYC` resolves on KNYC
  or KLGA, and whether `KXHIGHCHI` resolves on KORD or KMDW) is
  open and tracked separately; if either answer is "the other one",
  we add the station to both fetchers in the same commit.

## Reversal cost

Low. If a future audit shows the parser-derived CLI TMAX/TMIN is
indistinguishable from ASOS-derived TMAX/TMIN at our resolution (a
single byte-comparable case is enough), we delete the CLI zips and
drop `calibration-fetch-cli.sh`. The NGR fit code reads from
`data/calibration/obs/K{STATION}.csv`, which can be produced by either
path — no downstream change.

Canary: when the parser is in place we run a one-off compare of CLI
TMAX/TMIN vs ASOS-derived TMAX/TMIN over the full window. If
mean-absolute-difference < 0.5°F **and** there is no day with
difference > 2°F, this ADR was overcautious and we may simplify. If
either bound is breached, the decision is confirmed.

## References

- `scripts/calibration-fetch-obs.sh`, `scripts/calibration-fetch-cli.sh`
  — implementation, commit `ae8d7a0`.
- ADR-0001 — `weather-calibrated` requires NGR, requires paired y.
- ADR-0004 — two-signal scope; calibration is the keystone of one of
  the two.
- ADR-0006 — Kalshi series naming (KXHIGH*, KXLOWT*).
- Iowa Mesonet AFOS retrieve API:
  `https://mesonet.agron.iastate.edu/cgi-bin/afos/retrieve.py`.
- NWS CLI product family — issued per-WFO, PIL `CLI{STATION}`,
  resolves Kalshi temperature contracts.
