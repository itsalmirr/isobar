#!/usr/bin/env bash
# tape-health.sh — smoke-check a running or recently-stopped isobar tape.
#
# Answers one question: "is it worth running this for 48h?"
# Run any time after ~1 hour of tape uptime.
#
# Usage:
#   scripts/tape-health.sh                    # defaults: --data ./data --log var/tape.log
#   scripts/tape-health.sh --data /srv/data --log /var/log/tape.log
#
# Exit code: 0 = PASS, 1 = FAIL (print the verdict for CI hooks).

set -u
set -o pipefail

DATA_DIR="./data"
LOG_FILE="var/tape.log"
MIN_UPTIME_MIN=30   # don't judge before this

while [ $# -gt 0 ]; do
  case "$1" in
    --data)       DATA_DIR="$2"; shift 2 ;;
    --log)        LOG_FILE="$2"; shift 2 ;;
    --min-uptime) MIN_UPTIME_MIN="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

bold()   { printf "\033[1m%s\033[0m" "$1"; }
green()  { printf "\033[32m%s\033[0m" "$1"; }
red()    { printf "\033[31m%s\033[0m" "$1"; }
yellow() { printf "\033[33m%s\033[0m" "$1"; }

checks_pass=0
checks_fail=0
verdict_lines=()

pass() { verdict_lines+=("  $(green '[PASS]') $1"); checks_pass=$((checks_pass+1)); }
fail() { verdict_lines+=("  $(red   '[FAIL]') $1"); checks_fail=$((checks_fail+1)); }
warn() { verdict_lines+=("  $(yellow '[WARN]') $1"); }

# --- 0. Preconditions ------------------------------------------------------
if [ ! -d "$DATA_DIR" ]; then
  echo "$(red '[FAIL]') data directory not found: $DATA_DIR"
  echo "Has the tape been started? See docs/OPERATIONS.md §2."
  exit 1
fi

echo "$(bold 'isobar tape health check')"
echo "time     : $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
echo "data dir : $DATA_DIR"
echo "log file : $LOG_FILE ($( [ -f "$LOG_FILE" ] && echo present || echo missing ))"
echo

# --- 1. Per-source stats ---------------------------------------------------
echo "$(bold 'per-source stats')"
total_bytes=0
total_lines=0
sources_seen=0

for src_dir in "$DATA_DIR"/*/; do
  [ -d "$src_dir" ] || continue
  src="$(basename "$src_dir")"
  sources_seen=$((sources_seen+1))

  plain_count=$(find "$src_dir" -type f -name '*.jsonl' 2>/dev/null | wc -l | tr -d ' ')
  gz_count=$(find "$src_dir"    -type f -name '*.jsonl.gz' 2>/dev/null | wc -l | tr -d ' ')
  file_count=$((plain_count + gz_count))

  bytes=$(find "$src_dir" -type f \( -name '*.jsonl' -o -name '*.jsonl.gz' \) -exec wc -c {} + 2>/dev/null | tail -1 | awk '{print $1}')
  bytes=${bytes:-0}
  total_bytes=$((total_bytes + bytes))

  # Line count across plain JSONL files (gz excluded for speed; they're rotated
  # and represent past data anyway — current-hour activity is the signal).
  if [ "$plain_count" -gt 0 ]; then
    lines=$(find "$src_dir" -type f -name '*.jsonl' -exec wc -l {} + 2>/dev/null | tail -1 | awk '{print $1}')
    lines=${lines:-0}
  else
    lines=0
  fi
  total_lines=$((total_lines + lines))

  # Most recent file modification time → freshness. Portable across
  # macOS BSD find and GNU find: list paths first, then sort by mtime
  # via stat (BSD `-f %m`, GNU `-c %Y`), pick the highest.
  newest_file=""
  newest_mtime=0
  while IFS= read -r path; do
    [ -z "$path" ] && continue
    mtime=$(stat -f %m "$path" 2>/dev/null || stat -c %Y "$path" 2>/dev/null || echo 0)
    if [ "$mtime" -gt "$newest_mtime" ]; then
      newest_mtime=$mtime
      newest_file=$path
    fi
  done < <(find "$src_dir" -type f \( -name '*.jsonl' -o -name '*.jsonl.gz' \) 2>/dev/null)
  if [ -n "$newest_file" ]; then
    age_sec=$(( $(date +%s) - newest_mtime ))
  else
    age_sec=99999
  fi

  human_bytes=$(numfmt --to=iec 2>/dev/null <<< "$bytes" || echo "${bytes}B")
  printf "  %-10s files=%-3d  lines=%-8d  size=%-8s  newest=%ds ago\n" \
    "$src" "$file_count" "$lines" "$human_bytes" "$age_sec"

  # Freshness check: most recent file should have been touched within 5 min
  # on any active source. (Open-Meteo polls every 30m so has its own rule.)
  case "$src" in
    openmeteo)
      if [ "$age_sec" -gt 2100 ]; then   # > 35 min
        fail "$src: newest file older than 35 min — poller stalled?"
      else
        pass "$src: recent activity within poll window"
      fi
      ;;
    *)
      if [ "$age_sec" -gt 300 ]; then
        fail "$src: newest file older than 5 min — WS disconnected?"
      else
        pass "$src: recent activity within 5 min"
      fi
      ;;
  esac

  # Frame-rate check for WS sources (skip openmeteo; it's a poller).
  if [ "$src" != "openmeteo" ] && [ "$lines" -lt 50 ]; then
    warn "$src: only $lines frames — is the WS getting data?"
  fi
done

if [ "$sources_seen" -eq 0 ]; then
  fail "no per-source subdirectories under $DATA_DIR"
fi

human_total=$(numfmt --to=iec 2>/dev/null <<< "$total_bytes" || echo "${total_bytes}B")
echo "  $(bold 'total')       size=$human_total  lines=$total_lines  sources=$sources_seen"
echo

# --- 2. Log analysis -------------------------------------------------------
if [ -f "$LOG_FILE" ]; then
  echo "$(bold 'log analysis') ($(wc -l < "$LOG_FILE" | tr -d ' ') lines)"

  # slog JSON: "level":"ERROR" / "WARN". Also handle text-format fallback.
  errs=$(grep -c -E '"level":"ERROR"|level=ERROR' "$LOG_FILE" 2>/dev/null || echo 0)
  warns=$(grep -c -E '"level":"WARN"|level=WARN'  "$LOG_FILE" 2>/dev/null || echo 0)
  drops=$(grep -c 'frames dropped' "$LOG_FILE" 2>/dev/null || echo 0)
  reconnects=$(grep -c 'ws disconnected\|reconnecting' "$LOG_FILE" 2>/dev/null || echo 0)
  ws_connected=$(grep -c 'ws connected' "$LOG_FILE" 2>/dev/null || echo 0)
  auth_err=$(grep -c -E 'API error 401|401 Unauthorized|decode PEM' "$LOG_FILE" 2>/dev/null || echo 0)

  echo "  errors       : $errs"
  echo "  warnings     : $warns"
  echo "  drop events  : $drops"
  echo "  reconnects   : $reconnects"
  echo "  ws connected : $ws_connected"

  if [ "$auth_err" -gt 0 ]; then
    fail "auth errors detected ($auth_err) — PEM/key ID mismatch or clock drift"
  else
    pass "no auth errors"
  fi
  if [ "$drops" -gt 0 ]; then
    warn "dropped frames observed ($drops) — queue may be undersized"
  else
    pass "no dropped frames"
  fi
  if [ "$reconnects" -gt 5 ]; then
    warn "$reconnects reconnect events — check network stability"
  fi
  if [ "$ws_connected" -ge 1 ]; then
    pass "ws subscriber reached connected state at least once"
  else
    fail "no 'ws connected' log line seen yet — subscription not established"
  fi
else
  warn "log file $LOG_FILE not found — run with '2>&1 | tee -a $LOG_FILE' to enable log checks"
fi
echo

# --- 3. Verdict ------------------------------------------------------------
echo "$(bold 'verdict')"
for line in "${verdict_lines[@]}"; do
  echo -e "$line"
done
echo

if [ "$checks_fail" -eq 0 ]; then
  echo "$(green '→') Run the tape for 48h. Data flowing, no auth issues, freshness good."
  exit 0
else
  echo "$(red '→') Fix $checks_fail issue(s) before committing 48h of uptime."
  echo "  See docs/OPERATIONS.md §7 for the troubleshooting playbook."
  exit 1
fi
