#!/usr/bin/env bash
# calibration-fetch-obs.sh — fetch hourly ASOS temperature observations.
#
# Pulls hourly ASOS surface temperature from the Iowa Environmental Mesonet
# over a fixed window for the stations the recorder cares about. Output:
# one CSV per station plus MANIFEST.json (row counts, hashes, date spans)
# under <data>/obs/.
#
# This is supplementary feature data. The y-target for NGR is the NWS CLI
# daily report, fetched separately by calibration-fetch-cli.sh and parsed
# into <data>/obs/K<STATION>.csv.
#
# Stations and the window are intentionally hardcoded so the calibration
# training set is reproducible. Stations must stay in sync with
# config/config.example.yaml (openmeteo.locations); when that list changes,
# update this script in the same commit. Window can be overridden via
# --start/--end if you intentionally want a different range.
#
# Usage:
#   scripts/calibration-fetch-obs.sh
#   scripts/calibration-fetch-obs.sh --data /tmp/calib --start 2024-01-01 --end 2024-06-30
#
# Exit codes: 0 = all stations OK, 1 = one or more stations failed, 2 = usage error.

set -u
set -o pipefail

DATA_DIR="./data/calibration"
WINDOW_START="2024-04-29"
WINDOW_END="2026-04-28"

# 3-letter ASOS IDs, no leading K. Keep in sync with config/config.example.yaml.
# CHI uses MDW (Midway), not ORD (O'Hare): Kalshi's KXHIGHCHI/KXLOWTCHI
# settlement_sources both point at the NWS CLI report issuedby=MDW.
STATIONS=(NYC MIA LAX MDW)

while [ $# -gt 0 ]; do
  case "$1" in
    --data)  DATA_DIR="$2"; shift 2 ;;
    --start) WINDOW_START="$2"; shift 2 ;;
    --end)   WINDOW_END="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

case "$WINDOW_START" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
  *) echo "--start must be YYYY-MM-DD: got '$WINDOW_START'" >&2; exit 2 ;;
esac
case "$WINDOW_END" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
  *) echo "--end must be YYYY-MM-DD: got '$WINDOW_END'" >&2; exit 2 ;;
esac
if [ "$WINDOW_START" \> "$WINDOW_END" ]; then
  echo "--start ($WINDOW_START) must be <= --end ($WINDOW_END)" >&2
  exit 2
fi

IFS=- read -r START_Y START_M START_D <<< "$WINDOW_START"
IFS=- read -r END_Y END_M END_D <<< "$WINDOW_END"

OUT_DIR="${DATA_DIR}/obs"
mkdir -p "$OUT_DIR"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

failures=()

for st in "${STATIONS[@]}"; do
  out="${OUT_DIR}/K${st}-hourly.csv"
  tmp="${out}.tmp"

  echo "fetching K${st} -> $out"

  if ! curl -fsSL --retry 3 --retry-delay 5 --max-time 300 --get \
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
      -o "$tmp"; then
    echo "  ERROR: curl failed for K${st}" >&2
    failures+=("K${st}: curl failed")
    rm -f "$tmp"
    continue
  fi

  # Mesonet sometimes returns 200 OK with an HTML error page; verify the header.
  expected_header="station,valid,tmpf"
  actual_header=$(head -1 "$tmp" 2>/dev/null || true)
  if [ "$actual_header" != "$expected_header" ]; then
    echo "  ERROR: K${st} unexpected header: '$actual_header'" >&2
    failures+=("K${st}: bad header")
    rm -f "$tmp"
    continue
  fi

  rows=$(awk 'NR>1' "$tmp" | wc -l | tr -d ' ')
  if [ "$rows" -lt 100 ]; then
    echo "  ERROR: K${st} only $rows data rows" >&2
    failures+=("K${st}: ${rows} rows")
    rm -f "$tmp"
    continue
  fi

  mv "$tmp" "$out"
  first_dt=$(awk -F, 'NR==2 {print substr($2,1,10)}' "$out")
  last_dt=$(awk -F, 'END    {print substr($2,1,10)}' "$out")
  hash=$(sha256 "$out")
  echo "  ok: $rows rows, ${first_dt}..${last_dt}, sha256=${hash:0:16}..."

  # Iowa Mesonet is a free academic service — be courteous between requests.
  sleep 1
done

# --- Manifest: rebuilt from disk so it reflects current state, including
#     files preserved from earlier runs when a fetch failed this run.
fetched_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
manifest_path="${OUT_DIR}/MANIFEST.json"

manifest_files=""
first_entry=true
for f in "${OUT_DIR}"/K*-hourly.csv; do
  [ -f "$f" ] || continue
  name=$(basename "$f")
  rows=$(awk 'NR>1' "$f" | wc -l | tr -d ' ')
  first_dt=$(awk -F, 'NR==2 {print substr($2,1,10)}' "$f")
  last_dt=$(awk -F, 'END    {print substr($2,1,10)}' "$f")
  hash=$(sha256 "$f")
  entry="{\"name\":\"${name}\",\"rows\":${rows},\"first_date\":\"${first_dt}\",\"last_date\":\"${last_dt}\",\"sha256\":\"${hash}\"}"
  if $first_entry; then
    manifest_files="$entry"
    first_entry=false
  else
    manifest_files="${manifest_files},${entry}"
  fi
done

station_arr=""
first=true
for st in "${STATIONS[@]}"; do
  if $first; then station_arr="\"K${st}\""; first=false
  else            station_arr="${station_arr},\"K${st}\""
  fi
done

cat > "${manifest_path}.tmp" <<JSON
{
  "kind": "calibration-obs-hourly",
  "source": "Iowa Environmental Mesonet ASOS",
  "endpoint": "https://mesonet.agron.iastate.edu/cgi-bin/request/asos.py",
  "fetched_at": "${fetched_at}",
  "window_start": "${WINDOW_START}",
  "window_end": "${WINDOW_END}",
  "stations": [${station_arr}],
  "files": [${manifest_files}]
}
JSON
mv "${manifest_path}.tmp" "$manifest_path"

if [ ${#failures[@]} -eq 0 ]; then
  echo "done. all stations ok. manifest: $manifest_path"
  exit 0
else
  echo "done with errors:" >&2
  for f in "${failures[@]}"; do echo "  - $f" >&2; done
  echo "manifest (current disk state): $manifest_path" >&2
  exit 1
fi
