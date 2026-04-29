# CLI parser test corpus

Six real NWS Climate Report bulletins extracted from the calibration
archive at `data/calibration/cli/` (gitignored). Together they cover
every format variation the parser has been observed to encounter
across our 4 stations × 2y window. NWS products are public-domain US
government works (17 USC § 105), so committing them is fine.

Dates are deliberately spread across the calibration window
(Feb 2025 — Apr 2026) so the corpus catches drift in NWS bulletin
formatting over time, not just one snapshot.

| File | Station | Valid date | What it tests |
| ---- | ------- | ---------- | ------------- |
| [CLINYC_202502220629.txt](./CLINYC_202502220629.txt) | KNYC | 2025-02-21 | Full `MM`: TMAX value and time both `MM` (sensor outage) — emit empty fields, log gap |
| [CLIMIA_202505092030.txt](./CLIMIA_202505092030.txt) | KMIA | 2025-05-09 | Partial `MM`: TMAX value=86, time=`MM` (preliminary intraday) — emit value, time empty |
| [CLILAX_202604020141.txt](./CLILAX_202604020141.txt) | KLAX | 2026-04-01 | `TODAY` preliminary header (afternoon issuance) — same valid date as the next two |
| [CLILAX_202604020834.txt](./CLILAX_202604020834.txt) | KLAX | 2026-04-01 | `YESTERDAY` overnight — first final issuance for valid date 2026-04-01 |
| [CLILAX_202604020841.txt](./CLILAX_202604020841.txt) | KLAX | 2026-04-01 | `YESTERDAY` overnight — corrected reissue 7 min later; **dedup winner** by latest filename |
| [CLIORD_202601010635.txt](./CLIORD_202601010635.txt) | KORD | 2025-12-31 | Standard `YESTERDAY` overnight — baseline normal case |

## Parser behavior the corpus pins down

- **Header date wins.** Valid date comes from the
  `...THE <station name> CLIMATE SUMMARY FOR <MONTH DD YYYY>...` line,
  not from the `TODAY`/`YESTERDAY` label and not from the issuance
  timestamp in the AFOS header.
- **Latest issuance wins.** When multiple bulletins report on the same
  valid date (the three `CLILAX_202604020*` files), keep the one with
  the lexicographically latest filename (i.e. latest issuance time).
- **`MM` value → empty CSV field.** Parser emits `tmax_f=` (empty
  string) for `MAXIMUM MM`. Same for TMIN. The row is still written,
  with the missing column blank, so downstream code sees an explicit
  gap rather than silently re-using the previous day's value.
- **`MM` time + numeric value is fine.** `MAXIMUM 86 MM` means TMAX=86
  with no logged observation time. `tmax_f=86` in the CSV; the time is
  not part of the output schema, so this case is identical to a
  bulletin with a real time.
- **Out-of-window valid dates are dropped.** Every fetched zip includes
  a few bulletins whose header date falls outside `[--start, --end]`
  (the morning issuance of `--start` reports on the previous day).
  Parser filters by header date, not issuance time.

## Output schema reminder

`data/calibration/obs/K<STATION>.csv` per ADR-0007:

```
valid_date,tmax_f,tmin_f,issued_at,bulletin_id
```

- `valid_date`: `YYYY-MM-DD`, station-local civil date taken from the
  bulletin header.
- `tmax_f`, `tmin_f`: integer °F (CLI rounds to whole degrees), or empty.
- `issued_at`: ISO-8601 UTC timestamp of the kept issuance, parsed from
  the AFOS header (`CDUSXX KXXX DDHHMM`) or the filename suffix.
- `bulletin_id`: filename inside the zip, for traceability back to the
  source bulletin.
