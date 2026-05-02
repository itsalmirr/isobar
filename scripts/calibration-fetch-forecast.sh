#!/usr/bin/env bash
# calibration-fetch-forecast.sh — fetch historical deterministic GFS forecasts.
#
# Pulls daily TMAX/TMIN forecast values from Open-Meteo's
# historical-forecast-api for our 4 stations × 2y window. The deterministic
# (control) GFS run is the only thing freely available historically — full
# ensemble member archive requires NOAA GEFS GRIB processing, deferred.
#
# Output: data/calibration/forecast/K{STA}.csv with columns:
#   valid_date,gfs_tmax_f,gfs_tmin_f
#
# Pairs with data/calibration/obs/K{STA}.csv (CLI ground truth) for NGR
# residual analysis. See docs/decisions/0008 for rationale.
#
# Usage:
#   scripts/calibration-fetch-forecast.sh                # defaults
#   scripts/calibration-fetch-forecast.sh --start 2024-04-29 --end 2026-04-28
#
# Conventions match calibration-fetch-cli.sh (--data, --start, --end, atomic
# writes, MANIFEST.json with sha256, exit 0/1/2).

set -u
set -o pipefail

DATA_DIR="./data"
WIN_START="2024-04-29"
WIN_END="2026-04-28"
ENDPOINT="https://historical-forecast-api.open-meteo.com/v1/forecast"

# Stations × (lat, lon, timezone). Match config/config.example.yaml exactly.
# Parallel arrays — keep in lockstep. (bash 3.2 on macOS lacks assoc arrays.)
STATIONS=(KNYC KMIA KLAX KMDW)
LATS=(40.7794 25.7617 33.9416 41.7861)
LONS=(-73.9692 -80.1918 -118.4085 -87.7522)
TZS=(America/New_York America/New_York America/Los_Angeles America/Chicago)

while [ $# -gt 0 ]; do
  case "$1" in
    --data)  DATA_DIR="$2"; shift 2 ;;
    --start) WIN_START="$2"; shift 2 ;;
    --end)   WIN_END="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

OUT_DIR="$DATA_DIR/calibration/forecast"
mkdir -p "$OUT_DIR"

bold()   { printf "\033[1m%s\033[0m" "$1"; }
green()  { printf "\033[32m%s\033[0m" "$1"; }
red()    { printf "\033[31m%s\033[0m" "$1"; }

echo "$(bold 'isobar calibration: deterministic GFS historical forecasts')"
echo "  endpoint: $ENDPOINT"
echo "  window:   $WIN_START → $WIN_END"
echo "  out:      $OUT_DIR"
echo

FILE_ENTRIES=()
errs=0

for i in "${!STATIONS[@]}"; do
  sta="${STATIONS[$i]}"
  lat="${LATS[$i]}"
  lon="${LONS[$i]}"
  tz="${TZS[$i]}"
  out="$OUT_DIR/$sta.csv"
  tmp="$out.tmp"

  echo -n "  $sta ($tz) ... "

  url="$ENDPOINT?latitude=$lat&longitude=$lon&start_date=$WIN_START&end_date=$WIN_END&daily=temperature_2m_max,temperature_2m_min&models=gfs_seamless&timezone=$tz&temperature_unit=fahrenheit"

  # Fetch JSON, extract daily arrays via python (always available on this
  # project; jq isn't a hard dep).
  if ! curl -sfS -o "$tmp.json" "$url"; then
    echo "$(red 'FAIL') (curl)"
    errs=$((errs+1))
    continue
  fi

  python3 - "$tmp.json" "$tmp" <<'PYEOF'
import json, sys, csv
in_path, out_path = sys.argv[1], sys.argv[2]
with open(in_path) as f:
    d = json.load(f)
if 'error' in d and d['error']:
    print(f"API error: {d.get('reason', d)}", file=sys.stderr)
    sys.exit(1)
daily = d.get('daily', {})
times = daily.get('time', [])
tmax = daily.get('temperature_2m_max', [])
tmin = daily.get('temperature_2m_min', [])
with open(out_path, 'w') as f:
    w = csv.writer(f)
    w.writerow(['valid_date','gfs_tmax_f','gfs_tmin_f'])
    rows = 0
    for t, hi, lo in zip(times, tmax, tmin):
        if hi is None or lo is None:
            continue
        w.writerow([t, f'{hi:.1f}', f'{lo:.1f}'])
        rows += 1
print(f'  rows: {rows}')
PYEOF

  if [ $? -ne 0 ]; then
    rm -f "$tmp" "$tmp.json"
    echo "$(red 'FAIL') (parse)"
    errs=$((errs+1))
    continue
  fi

  rm -f "$tmp.json"
  mv "$tmp" "$out"

  rows=$(($(wc -l < "$out") - 1))
  bytes=$(wc -c < "$out")
  sha=$(shasum -a 256 "$out" | awk '{print $1}')
  echo "$(green 'OK') ($rows rows, $(numfmt --to=iec 2>/dev/null <<< "$bytes" || echo "${bytes}B"))"

  FILE_ENTRIES+=("{\"name\":\"$sta.csv\",\"rows\":$rows,\"bytes\":$bytes,\"sha256\":\"$sha\"}")
done

# MANIFEST.json
manifest="$OUT_DIR/MANIFEST.json"
{
  echo "{"
  echo "  \"kind\": \"calibration-forecast-deterministic\","
  echo "  \"source\": \"Open-Meteo historical-forecast-api\","
  echo "  \"endpoint\": \"$ENDPOINT\","
  echo "  \"model\": \"gfs_seamless (deterministic / control run only)\","
  echo "  \"note\": \"Ensemble member archive requires NOAA GEFS GRIB processing — see ADR-0008.\","
  echo "  \"fetched_at\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
  echo "  \"window_start\": \"$WIN_START\","
  echo "  \"window_end\": \"$WIN_END\","
  echo "  \"stations\": [\"KNYC\",\"KMIA\",\"KLAX\",\"KMDW\"],"
  echo -n "  \"files\": ["
  printf '%s,' "${FILE_ENTRIES[@]}" | sed 's/,$//'
  echo "]"
  echo "}"
} > "$manifest"

echo
if [ "$errs" -eq 0 ]; then
  echo "$(green '→') All stations fetched. Manifest at $manifest"
  exit 0
else
  echo "$(red '→') $errs station(s) failed."
  exit 1
fi
