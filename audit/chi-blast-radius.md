# CHI Station Mismatch — Blast-Radius Memo

**Defect.** `config/config.yaml:64` lists ORD (`41.9742, -87.9073`) for the CHI Open-Meteo poll location, but Kalshi resolves `KXHIGHCHI` / `KXLOWTCHI` to **Midway (KMDW)**, per `config/config.example.yaml:70` and the existing `internal/edge/poll_index.go:107-108` mapping (`rest:CHI → KMDW`). Every CHI frame ever recorded has been paired with the wrong station's forecast.

Authored 2026-05-13. Status: P1 data integrity.

---

## 1. Canonical KMDW coordinates

**Primary source.** NWS station endpoint, fetched 2026-05-13:
`https://api.weather.gov/stations/KMDW`

```json
{
  "geometry": { "coordinates": [-87.75528, 41.78417] },
  "properties": {
    "stationIdentifier": "KMDW",
    "name": "Chicago, Chicago Midway Airport",
    "timeZone": "America/Chicago",
    "elevation": { "value": 188.0616, "unitCode": "wmoUnit:m" }
  }
}
```

**Cross-check.** Aviation Weather Center, fetched 2026-05-13:
`https://aviationweather.gov/api/data/stationinfo?ids=KMDW&format=json`

```json
{ "icao_id": "KMDW", "name": "Chicago/Midway Intl",
  "latitude": 41.78412, "longitude": -87.75515, "elevation_m": 186 }
```

Sources agree within 0.0001° lat / 0.0002° lon and 2 m elevation — well inside the requested 0.001° tolerance.

**Canonical values (use these in config):**

| Field | Value |
|---|---|
| Latitude | `41.7842` |
| Longitude | `-87.7553` |
| Elevation | `188 m` (NWS) / `186 m` (AWC) |
| Name | Chicago Midway International (KMDW) |
| Time zone | America/Chicago |

Existing `config/config.example.yaml:70` used `41.7861, -87.7522` — within 0.003° of the NWS canonical, so the *example* is fine but slightly looser than canonical; consider tightening on the same commit.

---

## 2. Tape-day blast radius (2026-04-18 → 2026-05-12)

All 11 recorded days under `data/kalshi/<date>/*.jsonl[.gz]`. Counts come from `jq 'select(.p.type=="trade")'` on the envelope shape from `internal/journal/journal.go:48-55` (keys `t`,`src`,`k`,`p`). Timestamps are envelope wall-clock (`.t`) in UTC because the early-tape trade payloads carry `ts` (sec) and the new ones carry `ts_ms` — `.t` is universal.

| Date | Total trade frames | CHI trade frames | Distinct CHI tickers | First→Last CHI ts (UTC) | % CHI |
|---|---:|---:|---:|---|---:|
| 2026-04-18 | 773 | 8 | 7 | 17:04:02 → 17:06:48 | 1.0% |
| 2026-04-22 | 20 | 4 | 3 | 02:57:48 → 05:18:26 | 20.0% |
| 2026-04-29 | 28,695 | 5,335 | 24 | 07:23:00 → 04-30 00:25:39 | 18.6% |
| 2026-04-30 | 27,423 | 5,594 | 23 | 00:32:09 → 05-01 00:27:45 | 20.4% |
| 2026-05-01 | 692 | 86 | 11 | 00:34:19 → 05:56:32 | 12.4% |
| 2026-05-02 | 18,435 | 3,283 | 24 | 15:31:13 → 05-03 00:31:32 | 17.8% |
| 2026-05-03 | 35,362 | 9,905 | 35 | 00:35:20 → 05-04 00:38:09 | 28.0% |
| 2026-05-04 | 34,280 | 7,689 | 36 | 00:38:38 → 05-05 00:40:38 | 22.4% |
| 2026-05-05 | 33,756 | 6,033 | 34 | 00:45:00 → 05-06 00:49:53 | 17.9% |
| 2026-05-06 | 2,436 | 420 | 19 | 00:50:42 → 03:28:06 | 17.2% |
| 2026-05-12 | 589 | 125 | 10 | 03:47:39 → 04:36:03 | 21.2% |
| **TOTAL** | **182,461** | **38,482** | — | — | **21.1%** |

**Grand total: 38,482 CHI trade frames poisoned** (21.1% of all recorded trade frames). The full-coverage continuous days (Apr 29 – May 5) account for 37,839 of those — that's the high-value backtest corpus.

---

## 3. ORD ↔ KMDW temperature delta

Open-Meteo Historical Forecast API, fetched 2026-05-13:
- ORD: `https://historical-forecast-api.open-meteo.com/v1/forecast?latitude=41.9742&longitude=-87.9073&start_date=2026-04-18&end_date=2026-05-12&daily=temperature_2m_max,temperature_2m_min&temperature_unit=fahrenheit&timezone=America/Chicago`
- KMDW: same endpoint with `latitude=41.7842&longitude=-87.7553`

25-day window (`2026-04-18` … `2026-05-12`), delta = ORD − KMDW:

| Metric | TMAX | TMIN |
|---|---:|---:|
| Mean Δ (°F) | +0.34 | −0.45 |
| Median Δ (°F) | +0.10 | −0.60 |
| Max \|Δ\| (°F) | +3.1 (04-24) | −3.2 (04-27) |
| Days with \|Δ\| > 1°F | **10 / 25** | **11 / 25** |

Combined: **16 of 25 days (64%)** had at least one bracket (high or low) where |ORD−KMDW| > 1°F. Kalshi weather brackets are 1-degree (B*.5) buckets, so a 1°F delta is exactly the resolution at which the bracket can flip. The Apr 24, Apr 27, May 03, May 10, and May 12 outliers (|Δ| ≥ 2°F) would have shifted the model's bracket-probability mass across two or three buckets — material for any KX*CHI signal.

**Interpretation.** The two stations are 14 mi apart with KMDW further from the lake. The mean bias is small (≈0.4°F), so a long-run base-rate model would look OK in aggregate. But over the 25 calendar days that map to our tape, two-thirds of them had a bracket-relevant divergence and five days exceeded 2°F. Any per-day edge calculation built on the ORD pull was wrong on the days that mattered most.

---

## 4. Recommendation

**Retrofit, do not poison.** The Open-Meteo polls in the tape are timestamped (`Frame.Stamp.Wall`), station-keyed by `Frame.Kind` (`rest:CHI`), and the downstream `internal/edge/poll_index.go:107-108` already declares `rest:CHI → KMDW`. The bug is one config row; the data plumbing is correct. Path forward:

1. Fix `config/config.yaml:64` to `41.7842, -87.7553` (match canonical NWS; example file is close but loose — tighten in the same commit).
2. Re-poll Open-Meteo for the tape window with the correct coordinates and write a parallel `data/openmeteo-kmdw/` set of frames stamped at the original poll wall times. Open-Meteo's historical-forecast endpoint is deterministic for a given (lat, lon, date), so this is reproducible.
3. In `edge-scan`, key CHI features off the new KMDW poll stream. Old ORD frames stay on disk for audit but are excluded by `Frame.Kind` filtering.
4. Re-run any backtest that ingested CHI before this commit. The 21.1% poisoned-trade-frame share is large enough that prior CHI Sharpe / hit-rate numbers are not trustworthy and should be quarantined until the rerun lands.

Citations: `config/config.yaml:64`, `config/config.example.yaml:70`, `internal/edge/poll_index.go:107-108`, `internal/journal/journal.go:48-55`.
