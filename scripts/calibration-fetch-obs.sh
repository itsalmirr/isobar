#!/usr/bin/env bash
# calibration-fetch-obs.sh — pull 2y of hourly ASOS temperature obs for the
# four Kalshi weather-bracket stations (NY/MIA/LAX/CHI). Used as ground truth
# for NGR/EMOS calibration in Phase 0.5.
#
# Source: Iowa Mesonet ASOS archive (free, unauthenticated, well-maintained).
# Output: data/calibration/obs/K<STATION>-hourly.csv (UTC timestamps, °F).
#
# Usage:
#   scripts/calibration-fetch-obs.sh                  # 2y back from today, 4 stations
#   scripts/calibration-fetch-obs.sh --out /some/dir  # override output dir
#   scripts/calibration-fetch-obs.sh --start 2024-01-01 --end 2026-04-28
#
# Exit code: 0 = all stations fetched, 1 = at least one fetch failed.

set -u
set -o pipefail

OUT_DIR="data/calibration/obs"
# Default window: ending yesterday (UTC), 2y wide.
END_DATE="$(date -u -v-1d +%F 2>/dev/null || date -u -d 'yesterday' +%F)"
START_DATE="$(date -u -v-1d -v-2y +%F 2>/dev/null || date -u -d "$END_DATE - 2 years" +%F)"

STATIONS=(NYC MIA LAX ORD)   # ASOS IDs: drop the leading K (NYC=KNYC, ORD=KORD…)

while [ $# -gt 0 ]; do
  case "$1" in
    --out)         OUT_DIR="$2"; shift 2 ;;
    --start)       START_DATE="$2"; shift 2 ;;
    --end)         END_DATE="$2"; shift 2 ;;
    --stations)    IFS=',' read -r -a STATIONS <<< "$2"; shift 2 ;;
    -h|--help)
      sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

mkdir -p "$OUT_DIR"

split_date() {
  local d="$1"
  echo "${d%%-*}" "$(echo "$d" | cut -d- -f2 | sed 's/^0//')" "$(echo "$d" | cut -d- -f3 | sed 's/^0//')"
}

read -r SY SM SD <<< "$(split_date "$START_DATE")"
read -r EY EM ED <<< "$(split_date "$END_DATE")"

echo "calibration-fetch-obs"
echo "  window  : $START_DATE → $END_DATE"
echo "  stations: ${STATIONS[*]}"
echo "  out dir : $OUT_DIR"
echo

failures=0
for st in "${STATIONS[@]}"; do
  out="$OUT_DIR/K${st}-hourly.csv"
  printf "  K%-3s -> %s ... " "$st" "$out"

  # report_type=3,4 = METAR + SPECI hourly observations
  # missing=M, trace=T preserves the source's null/trace conventions
  http_code=$(
    curl -fsSL --get \
      --write-out '%{http_code}' \
      --output "$out.tmp" \
      --data-urlencode "station=${st}" \
      --data-urlencode "data=tmpf" \
      --data-urlencode "year1=${SY}"  --data-urlencode "month1=${SM}"  --data-urlencode "day1=${SD}" \
      --data-urlencode "year2=${EY}"  --data-urlencode "month2=${EM}"  --data-urlencode "day2=${ED}" \
      --data-urlencode "tz=Etc/UTC" \
      --data-urlencode "format=onlycomma" \
      --data-urlencode "latlon=no" \
      --data-urlencode "missing=M" \
      --data-urlencode "trace=T" \
      --data-urlencode "report_type=3" \
      --data-urlencode "report_type=4" \
      "https://mesonet.agron.iastate.edu/cgi-bin/request/asos.py" \
    || echo "curl-fail"
  )

  if [ "$http_code" != "200" ]; then
    echo "FAIL (http=$http_code)"
    rm -f "$out.tmp"
    failures=$((failures+1))
    continue
  fi

  rows=$(wc -l < "$out.tmp" | tr -d ' ')
  if [ "$rows" -lt 100 ]; then
    echo "FAIL (only $rows rows — likely empty response)"
    rm -f "$out.tmp"
    failures=$((failures+1))
    continue
  fi

  mv "$out.tmp" "$out"
  echo "OK ($rows rows)"
done

echo
if [ "$failures" -eq 0 ]; then
  echo "all stations fetched. next: aggregate hourly → daily TMAX/TMIN per local civil day."
  exit 0
else
  echo "$failures station(s) failed; rerun with --stations <list> to retry just those." >&2
  exit 1
fi
