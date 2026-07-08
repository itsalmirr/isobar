#!/usr/bin/env bash
# calibration-fetch-cli.sh — fetch raw NWS CLI daily climate report bulletins.
#
# Pulls all CLI text bulletins for the configured stations from the Iowa
# Environmental Mesonet AFOS archive over a fixed window. Output: one ZIP
# per station (raw bulletins) plus MANIFEST.json under <data>/cli/. A
# separate Go parser (next step) reads these zips and emits canonical daily
# TMAX/TMIN to <data>/obs/K<STATION>.csv.
#
# Why CLI: Kalshi's daily-temperature contracts resolve on the NWS CLI
# report. ASOS-derived TMAX/TMIN diverges from CLI by O(1°F); for NGR
# fitting we want CLI as the y-target.
#
# Stations and the window are intentionally hardcoded for training-set
# reproducibility. Stations must stay in sync with config/config.example.yaml
# and with calibration-fetch-obs.sh; window can be overridden via
# --start/--end if you intentionally want a different range.
#
# Usage:
#   scripts/calibration-fetch-cli.sh
#   scripts/calibration-fetch-cli.sh --data /tmp/calib --start 2024-01-01 --end 2024-06-30
#
# Exit codes: 0 = all stations OK, 1 = one or more stations failed, 2 = usage error.

set -u
set -o pipefail

DATA_DIR="./data/calibration"
WINDOW_START="2024-04-29"
WINDOW_END="2026-04-28"

# CLI bulletins are AFOS products with PIL = "CLI" + 3-letter station ID.
# Each station ID must resolve to the *same physical station Kalshi uses for
# resolution* — verified via Kalshi's `settlement_sources` API, not assumed.
# Chicago: MDW (Midway), not ORD — both KXHIGHCHI and KXLOWTCHI point at
# https://forecast.weather.gov/product.php?site=LOT&product=CLI&issuedby=MDW.
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

# CLI bulletin for date D is issued on D+1 morning UTC. To capture the
# bulletin for WINDOW_END, the AFOS query window has to extend one day past it.
if [ "$(uname -s)" = "Darwin" ]; then
  edate_day=$(date -v+1d -j -f "%Y-%m-%d" "$WINDOW_END" "+%Y-%m-%d")
  start_epoch=$(date -j -f "%Y-%m-%d" "$WINDOW_START" "+%s")
  end_epoch=$(date   -j -f "%Y-%m-%d" "$WINDOW_END"   "+%s")
else
  edate_day=$(date -d "$WINDOW_END + 1 day" "+%Y-%m-%d")
  start_epoch=$(date -d "$WINDOW_START" "+%s")
  end_epoch=$(date   -d "$WINDOW_END"   "+%s")
fi
sdate="${WINDOW_START}T00:00Z"
edate="${edate_day}T23:59Z"

# Sanity threshold: ≥1 bulletin per civil day. (CLI is normally issued ~2×/day,
# so this floor accepts the "preliminary only, no afternoon correction" days
# while still catching obvious failure modes like an empty/HTML response.)
window_days=$(( (end_epoch - start_epoch) / 86400 + 1 ))
[ "$window_days" -lt 1 ] && window_days=1
MIN_BULLETINS=$window_days

OUT_DIR="${DATA_DIR}/cli"
mkdir -p "$OUT_DIR"

if ! command -v unzip >/dev/null 2>&1; then
  echo "unzip is required but not installed" >&2
  exit 2
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

failures=()

for st in "${STATIONS[@]}"; do
  pil="CLI${st}"
  out="${OUT_DIR}/K${st}.zip"
  tmp="${out}.tmp"

  echo "fetching $pil [$sdate .. $edate] -> $out"

  if ! curl -fsSL --retry 3 --retry-delay 5 --max-time 300 --get \
      --data-urlencode "pil=${pil}" \
      --data-urlencode "sdate=${sdate}" \
      --data-urlencode "edate=${edate}" \
      --data-urlencode "fmt=zip" \
      --data-urlencode "limit=9999" \
      "https://mesonet.agron.iastate.edu/cgi-bin/afos/retrieve.py" \
      -o "$tmp"; then
    echo "  ERROR: curl failed for $pil" >&2
    failures+=("$pil: curl failed")
    rm -f "$tmp"
    continue
  fi

  if ! unzip -tq "$tmp" >/dev/null 2>&1; then
    echo "  ERROR: $pil response is not a valid zip (got $(wc -c <"$tmp") bytes)" >&2
    failures+=("$pil: not a zip")
    rm -f "$tmp"
    continue
  fi

  bulletin_count=$(unzip -l "$tmp" 2>/dev/null | awk '/\.txt$/' | wc -l | tr -d ' ')
  if [ "$bulletin_count" -lt "$MIN_BULLETINS" ]; then
    echo "  ERROR: $pil zip has $bulletin_count bulletins (expected ≥ $MIN_BULLETINS for $window_days-day window)" >&2
    failures+=("$pil: ${bulletin_count} bulletins")
    rm -f "$tmp"
    continue
  fi

  mv "$tmp" "$out"
  hash=$(sha256 "$out")
  size=$(wc -c < "$out" | tr -d ' ')
  echo "  ok: $bulletin_count bulletins, $size bytes, sha256=${hash:0:16}..."

  sleep 1
done

# --- Manifest from disk state ----------------------------------------------
fetched_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
manifest_path="${OUT_DIR}/MANIFEST.json"

manifest_files=""
first_entry=true
for f in "${OUT_DIR}"/K*.zip; do
  [ -f "$f" ] || continue
  name=$(basename "$f")
  bulletins=$(unzip -l "$f" 2>/dev/null | awk '/\.txt$/' | wc -l | tr -d ' ')
  size=$(wc -c < "$f" | tr -d ' ')
  hash=$(sha256 "$f")
  # Recover the PIL from the filename: KNYC.zip -> CLINYC.
  base="${name%.zip}"
  pil="CLI${base#K}"
  entry="{\"name\":\"${name}\",\"pil\":\"${pil}\",\"bulletins\":${bulletins},\"bytes\":${size},\"sha256\":\"${hash}\"}"
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
  "kind": "calibration-cli-bulletins",
  "source": "Iowa Environmental Mesonet AFOS archive",
  "endpoint": "https://mesonet.agron.iastate.edu/cgi-bin/afos/retrieve.py",
  "fetched_at": "${fetched_at}",
  "window_start": "${WINDOW_START}",
  "window_end": "${WINDOW_END}",
  "afos_sdate": "${sdate}",
  "afos_edate": "${edate}",
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
