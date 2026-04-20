# Operations runbook

> How to run isobar in anger. Everything in this document should be
> executable by someone who did not write the code — paste-able commands,
> exact file paths, exact exit criteria.

## 0. Prereqs

- Go 1.26 (matches `go.mod`).
- `golangci-lint` v2.1+ (optional but enforced in CI).
- A Linux host with ≥ 20 GiB free on the data partition.
- An NTP-synced clock (`timedatectl status | grep "synchronized: yes"`).
- A Kalshi API key — key ID (UUID-ish) + RSA private key (PEM).

## 1. First-time setup

```bash
git clone git@github.com:nolmelabs/isobar.git
cd isobar

# Secrets
mkdir -p secrets && chmod 700 secrets
cp /path/to/kalshi.pem secrets/kalshi.pem
chmod 600 secrets/kalshi.pem

# Config
cp config/config.example.yaml config/config.yaml
cp .env.example .env
$EDITOR .env   # fill KALSHI_API_KEY_ID and (typically) set KALSHI_PRIVATE_KEY_PATH=./secrets/kalshi.pem

# Verify toolchain
make all      # runs lint + vet + race-tested tests + build
```

Expected: `bin/tape` exists and `make all` exits 0.

## 2. Starting the recorder (Phase 0)

```bash
set -a && source .env && set +a
./bin/tape -config config/config.yaml 2>&1 | tee -a var/tape.log
```

Checklist immediately after start (first 60s):

- [ ] `isobar tape starting` log line with version/commit populated.
- [ ] `kalshi: credentials loaded` with a redacted key prefix.
- [ ] `kalshi: discovered markets` with `n > 0`.
- [ ] `ws connected` for `kalshi`.
- [ ] `journal opened` appears for each enabled source.

If any of the above is missing after 60s, stop (`Ctrl-C`) and see §7.

## 2.1 Smoke-test your first run (1-hour gate)

First time running the tape? Let it run for **~60 minutes**, then
answer the question *"should I leave this going for 48 hours?"* with
the health checker — not with vibes:

```bash
scripts/tape-health.sh
# or with custom paths:
scripts/tape-health.sh --data /srv/isobar/data --log /var/log/tape.log
```

The script exits `0` if the tape is healthy and `1` if something is
off. It inspects:

- **Per-source file stats.** Lines, size, newest-file age.
  Freshness: Kalshi must touch a file within 5 min; Open-Meteo
  within 35 min (30-min poll cadence + slack).
- **Log patterns.** `error` / `warn` counts, dropped-frame events,
  reconnect storms, auth errors (`API error 401`, `decode PEM`),
  whether `ws connected` ever appeared.
- **Verdict.** Ranked PASS/FAIL/WARN plus a one-line recommendation.

### What a passing 1-hour run looks like

- Kalshi: thousands of frames under `data/kalshi/<today>/`. Rate
  varies with how many markets you subscribed to and the hour;
  10–30 frames/min per market is typical on an active hour.
- Open-Meteo: at least 1 file — maybe 2 — per configured location.
- Log: one `ws connected` line, zero reconnects, zero dropped
  frames, zero errors.
- File sizes: kalshi 5–50 MB total; openmeteo 50–500 KB per location.

### What a failing run looks like

- `newest file > 5 min ago` on Kalshi → WS died silently. Check log
  for `ws disconnected` and see §7.
- `auth errors detected (N)` → PEM / API-key-ID mismatch or clock
  drift. Run `timedatectl status` first, Kalshi dashboard second.
- `0 frames from kalshi` → subscription never landed. Check series
  prefixes in `config.yaml`; e.g. `KXHIGHNY` only resolves if there
  are open NYC-high markets for the date range.
- `log file missing` → you forgot to pipe to a file. Restart with
  `| tee -a var/tape.log`. Data is still being written to disk, but
  log-based checks will be disabled.

### Should I run 48h?

- Script exits `0` ⇒ **yes**, let it run overnight and re-check at
  24h and 48h.
- Script exits `1` ⇒ **no**. Fix the FAIL condition first. A 48-hour
  recording with a silent WS outage is worse than no recording.

Full 48h is what the ROADMAP wants for Phase 0.5. After that you can
either stop and pivot to calibration / analysis work, or leave the
tape running on a persistent host (cloud VM per ADR-0003 — deferred
to W6) while calibration happens in parallel.

## 3. Healthy-steady-state signals

Every 5 minutes a healthy recorder should show approximately:

| signal                                    | expectation                               |
| ----------------------------------------- | ----------------------------------------- |
| `frames dropped` warn lines               | zero, or transient single-digit bursts    |
| new `.jsonl` files under `data/<source>/` | one per rotation boundary (hour by default) |
| disk growth                               | ~10–100 MiB/hour depending on activity    |
| CPU                                       | <10% on a modest VM                       |
| RSS                                       | <200 MiB                                  |

Quick live check:

```bash
# Dropped frames (should stay 0 or be an old count).
grep 'frames dropped' var/tape.log | tail -5

# Disk growth over last hour.
du -sh data/*/$(date -u +%Y-%m-%d)

# Most recent rotation.
ls -lt data/kalshi/$(date -u +%Y-%m-%d) | head -5
```

## 4. Graceful shutdown

```bash
# From the launcher shell:
Ctrl-C        # sends SIGINT

# Or from another shell:
pkill -INT tape
```

The recorder will:
1. Cancel its context (stops reads).
2. Drain each journal's queue.
3. Flush + fsync each open file.
4. Gzip rotated files if configured.
5. Log `tape exited cleanly` and exit 0.

If it exits non-0, inspect the last `err=` field and check §7.

## 5. Disk management

The recorder **never rotates out old data on its own**. Operator
responsibility:

```bash
# How big is the pile?
du -sh data/

# Archive > 7-day-old data elsewhere.
find data -type f -mtime +7 -name '*.jsonl.gz' -print

# After verifying the list, copy offsite then delete.
# (Always verify the copy before deleting.)
```

A reasonable budget for Phase 0 is ≤ 10 GiB/week uncompressed, or
≤ 2 GiB/week compressed.

## 6. Upgrading

```bash
git fetch origin
git diff origin/main --stat          # scan the changes
git checkout origin/main             # or a tagged vPHASE.X.Y
make all                             # must be green before rolling
# stop old tape gracefully (see §4) before starting new one
```

## 7. Troubleshooting playbook

### `config: KALSHI_API_KEY_ID required`

You didn't source `.env`, or it sets the key under a different name.
Run `env | grep KALSHI` to verify what's in scope.

### `kalshi: failed to decode PEM block`

The file at `KALSHI_PRIVATE_KEY_PATH` isn't a PEM. Check with
`head -1 secrets/kalshi.pem` — first line should begin
`-----BEGIN RSA PRIVATE KEY-----` or `-----BEGIN PRIVATE KEY-----`.

### `kalshi: API error 401`

Key ID ↔ private key mismatch, or clock drift. Verify with
`timedatectl status` and re-verify the key-ID on the Kalshi dashboard.

### `kalshi: API error 429` recurring

Rate limit tripped. The client adaptively backs off (see
`client.go: doRaw`), but frequent 429s mean series enumeration is too
aggressive — narrow the `series` list or reduce parallel discoveries.

### `ws disconnected; reconnecting`

Normal on overnight maintenance windows. Persistent (every few seconds
for > 10 min) reconnects indicate the WS URL is wrong, auth is failing,
or the network is unstable. Check with `curl -I https://api.elections.kalshi.com`.

### `frames dropped (queue full)`

The journal queue is saturated. Either a disk stall, or the incoming
rate exceeds `flush_every` + `queue_size`. Check `iostat -x 1` for disk
pressure; tune `queue_size` up or `flush_every` down. If disk is fine,
this is usually a symptom — root-cause before masking.

### Binary exits immediately with `config: …`

Validation failure; the error message names the offending key. See
`internal/config/config.go: Validate`.

## 8. Security posture

- Secrets live under `./secrets/` (chmod 700) or env vars. **Never** in
  `config.yaml` or a committed `.env`.
- RSA key is `chmod 600`. The binary reads it once at startup; do not
  copy it elsewhere.
- Logs go to stderr in JSON. They include no secret material (the key ID
  is redacted in `main.go`).
- Data files (`data/`) are NDJSON of market feeds — not secrets, but
  contain Kalshi's proprietary market data. Don't publish publicly.
- `.env` is gitignored; `.env.example` is the only committed env file.

## 9. On-call behavior (Phase 3+, placeholder)

Phase 3 adds webhook-based alerting on `ERROR` slog lines. Until then:

- Phase 0 is not on-call critical. A tape outage loses data, not money.
- If the recorder dies overnight, restart in the morning and note the
  gap in `docs/decisions/`. Phase 0.5 analyzer handles gaps gracefully.

## 10. Checklists

**End-of-day Phase 0.**
- [ ] `tail -50 var/tape.log` has no error-level entries.
- [ ] Dropped frame count unchanged since morning.
- [ ] Disk usage growth within budget.
- [ ] At least one file per source under today's date folder.

**Pre-restart.**
- [ ] Pull latest `main`, `make all` green.
- [ ] `git log -n 3` reviewed.
- [ ] Config diff vs. running version checked.
- [ ] Graceful shutdown of previous instance completed (saw
      `tape exited cleanly`).
