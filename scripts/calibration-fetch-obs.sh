#!/usr/bin/env bash
# Pulls 2y of hourly ASOS temperature for each station, writes CSV.
# Output: data/calibration/obs/<station>-hourly.csv
set -euo pipefail

OUT_DIR="data/calibration/obs"
mkdir -p "$OUT_DIR"

START_Y=2024; START_M=04; START_D=29
END_Y=2026;   END_M=04;   END_D=28

# Iowa Mesonet expects 3-letter ASOS IDs (no leading K).
# KNYC=NYC, KMIA=MIA, KLAX=LAX, KORD=ORD.
STATIONS=(NYC MIA LAX ORD)

for st in "${STATIONS[@]}"; do
  out="$OUT_DIR/K${st}-hourly.csv"
  echo "fetching K${st} -> $out"
  curl -fsSL --retry 3 --retry-delay 5 --get \
    --data-urlencode "station=${st}" \
    --data-urlencode "data=tmpf" \
    --data-urlencode "year1=${START_Y}" \
    --data-urlencode "month1=${START_M}" \
    --data-urlencode "day1=${START_D}" \
    --data-urlencode "year2=${END_Y}" \
    --data-urlencode "month2=${END_M}" \
    --data-urlencode "day2=${END_D}" \
    --data-urlencode "tz=Etc/UTC" \
    --data-urlencode "format=onlycomma" \
    --data-urlencode "latlon=no" \
    --data-urlencode "missing=M" \
    --data-urlencode "trace=T" \
    --data-urlencode "report_type=3" \
    --data-urlencode "report_type=4" \
    "https://mesonet.agron.iastate.edu/cgi-bin/request/asos.py" \
    -o "$out"
  rows=$(wc -l < "$out")
  echo "  ${rows} rows"
  if [ "$rows" -lt 1000 ]; then
    echo "  WARNING: suspiciously low row count for K${st}" >&2
  fi
done

echo "done. files in $OUT_DIR"
