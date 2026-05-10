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

set -u
set -o pipefail

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

# CLI bulletins for date D are issued on D+1 morning. We need bulletins
# up to END+1 to cover trades for END's bracket; fetch a 1-day buffer.
case "$(uname -s)" in
  Darwin) BUFFER_END=$(date -j -v+1d -f "%Y-%m-%d" "$END" "+%Y-%m-%d") ;;
  *)      BUFFER_END=$(date -d "$END + 1 day" "+%Y-%m-%d") ;;
esac

CALIB_DIR="$TAPE_DIR/calibration"
CLI_DIR="$CALIB_DIR/cli"
OBS_DIR="$CALIB_DIR/obs"
mkdir -p "$CLI_DIR" "$OBS_DIR"

# --- 1. Fetch CLI bulletins -----------------------------------------------
if [ "$SKIP_FETCH" -eq 0 ]; then
  echo "===> 1/3 fetching NWS CLI bulletins ($START..$BUFFER_END) ..."
  ./scripts/calibration-fetch-cli.sh \
    --data "$CALIB_DIR" \
    --start "$START" \
    --end   "$BUFFER_END"
else
  echo "===> 1/3 skipping CLI fetch (--skip-fetch); using existing $CLI_DIR"
fi

# --- 2. Parse bulletins into KSTATION.csv ---------------------------------
echo
echo "===> 2/3 parsing CLI bulletins to canonical TMAX/TMIN CSVs ..."
if [ ! -x ./bin/calibrate-parse-cli ]; then
  echo "  building cmd/calibrate-parse-cli ..."
  go build -o ./bin/calibrate-parse-cli ./cmd/calibrate-parse-cli
fi
./bin/calibrate-parse-cli \
  --in  "$CLI_DIR" \
  --out "$OBS_DIR"

# --- 3. Run edge-scan with truth -----------------------------------------
echo
echo "===> 3/3 running edge-scan with realized-PnL backtest ..."
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
