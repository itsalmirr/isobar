#!/usr/bin/env bash
# run-backtest.sh — end-to-end realized-PnL backtest over a recorded tape.
#
# Pipeline:
#   1. Fetch NWS CLI bulletins covering the recording window (+1 day buffer
#      for late-issuance corrections).
#   2. Parse them into canonical KSTATION.csv (TMAX/TMIN per civil day) via
#      cmd/calibrate-parse-cli.
#   3. Run cmd/edge-scan with --truth set, which:
#        - Skips trades whose valid_date isn't covered by truth
#          (the "no imaginary bets" rule).
#        - For each remaining trade, computes naive p_fair from the
#          most-recent Open-Meteo poll, decides whether our hypothetical
#          strategy would enter, and scores realized PnL against the
#          actual TMAX/TMIN.
#
# Usage:
#   scripts/run-backtest.sh --tape ~/isobar-data --start 2026-04-29 --end 2026-04-30
#   scripts/run-backtest.sh --help
#
# Exit codes: 0 = backtest produced output, 1 = a stage failed, 2 = usage error.

set -euo pipefail

TAPE_DIR=""
START=""
END=""
EDGE_THRESHOLD=5
SKIP_FETCH=0

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --tape)            TAPE_DIR="$2"; shift 2 ;;
    --start)           START="$2"; shift 2 ;;
    --end)             END="$2"; shift 2 ;;
    --edge-threshold)  EDGE_THRESHOLD="$2"; shift 2 ;;
    --skip-fetch)      SKIP_FETCH=1; shift ;;
    -h|--help)         usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

if [ -z "$TAPE_DIR" ] || [ -z "$START" ] || [ -z "$END" ]; then
  echo "ERROR: --tape, --start, --end are all required" >&2
  usage
  exit 2
fi

if [ ! -d "$TAPE_DIR/kalshi" ] || [ ! -d "$TAPE_DIR/openmeteo" ]; then
  echo "ERROR: --tape '$TAPE_DIR' must contain kalshi/ and openmeteo/ subdirs" >&2
  exit 2
fi

# calibration-fetch-cli.sh internally extends the AFOS query by +1 day to
# capture the morning-after CLI bulletin for WINDOW_END. We pass the user's
# END as-is and let the fetcher do the +1 — adding our own +1 here would
# double-buffer and inflate the bulletin-count threshold by an extra day.
CALIB_DIR="$TAPE_DIR/calibration"
CLI_DIR="$CALIB_DIR/cli"
OBS_DIR="$CALIB_DIR/obs"
mkdir -p "$CLI_DIR" "$OBS_DIR"

# --- 1. Fetch CLI bulletins -----------------------------------------------
# We tolerate exit 1 from the fetch script: short windows can fail its
# bulletin-count threshold even when the zips are perfectly fine, and on a
# windowed re-run we only need new zips for the dates we don't already have.
if [ "$SKIP_FETCH" -eq 0 ]; then
  echo "===> 1/4 fetching NWS CLI bulletins ($START..$END +1d AFOS buffer applied internally) ..."
  ./scripts/calibration-fetch-cli.sh \
    --data  "$CALIB_DIR" \
    --start "$START" \
    --end   "$END" || echo "  (fetch script returned non-zero; continuing if zips exist)"
else
  echo "===> 1/4 skipping CLI fetch (--skip-fetch); using existing $CLI_DIR"
fi

# --- 2. Verify zips exist before parsing ---------------------------------
echo
echo "===> 2/4 verifying CLI zips ..."
missing=()
for st in NYC MIA LAX MDW; do
  z="$CLI_DIR/K${st}.zip"
  if [ ! -f "$z" ]; then
    missing+=("$z")
else
  if ! unzip -tq "$z" >/dev/null 2>&1; then
    missing+=("$z (corrupt zip)")
    continue
  fi
  bulletins=$(unzip -l "$z" | awk '/\.txt$/' | wc -l | tr -d ' ')
  echo "  K${st}.zip: ${bulletins} bulletins"
fi
done
if [ ${#missing[@]} -gt 0 ]; then
  echo "ERROR: missing CLI zips:" >&2
  for m in "${missing[@]}"; do echo "  - $m" >&2; done
  exit 1
fi

# --- 3. Parse each station's zip into KSTATION.csv -----------------------
echo
echo "===> 3/4 parsing CLI bulletins to canonical TMAX/TMIN CSVs ..."
if [ ! -x ./bin/calibrate-parse-cli ]; then
  echo "  building cmd/calibrate-parse-cli ..."
  go build -o ./bin/calibrate-parse-cli ./cmd/calibrate-parse-cli
fi
parse_failed=0
for st in NYC MIA LAX MDW; do
  if ! ./bin/calibrate-parse-cli \
      -zip   "$CLI_DIR/K${st}.zip" \
      -start "$START" \
      -end   "$END" \
      -out   "$OBS_DIR/K${st}.csv"; then
    parse_failed=$((parse_failed + 1))
  fi
done
if [ "$parse_failed" -gt 0 ]; then
  echo "ERROR: $parse_failed station(s) failed to parse" >&2
  exit 1
fi
echo "  truth files written:"
ls -la "$OBS_DIR"/K{NYC,MIA,LAX,MDW}.csv 2>&1

# --- 3. Run edge-scan with truth -----------------------------------------
echo
echo "===> 4/4 running edge-scan with realized-PnL backtest ..."
if [ ! -x ./bin/edge-scan ]; then
  echo "  building cmd/edge-scan ..."
  go build -o ./bin/edge-scan ./cmd/edge-scan
fi

./bin/edge-scan \
  --data  "$TAPE_DIR" \
  --start "$START" \
  --end   "$END" \
  --truth "$OBS_DIR" \
  --edge-threshold-cents "$EDGE_THRESHOLD"

echo
echo "==> done. detailed report in $TAPE_DIR/calibration/edge-scan/"
