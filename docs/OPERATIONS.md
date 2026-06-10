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
- `.env` (if present at repo root) must be mode `0o600` or stricter.
  `cmd/tape` calls [`env.LoadNearest`](../internal/env/env.go) which
  enforces this via `CheckMode` — `(mode & 0o077) != 0` is a fatal
  refusal to boot. See §1 and §7.

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
chmod 600 .env # REQUIRED — tape refuses to start with a group/world-readable .env

# Verify toolchain
make all      # runs lint + vet + race-tested tests + build
              # `make lint` includes the `clockcheck` analyzer (ADR-0012)
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
- [ ] First record in today's `data/kalshi/<date>/*.jsonl` and
      `data/openmeteo/<date>/*.jsonl` is a `kind:"boot"` frame with
      `src:"isobar-tape"` and `schema_version:"isobar-tape.boot/v1"`
      (see §3.1).
- [ ] If `data/alarms/<today>.jsonl` exists from a prior run, eyeball it
      before believing the new tape is clean (see §3.2).

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

| signal                                    | expectation                                 |
| ----------------------------------------- | ------------------------------------------- |
| `journal: drop-alarm fired` lines         | zero                                        |
| `data/alarms/<today>.jsonl`               | absent, or unchanged for the day's duration |
| new `.jsonl` files under `data/<source>/` | one per rotation boundary (hour by default) |
| disk growth                               | ~10–100 MiB/hour depending on activity      |
| CPU                                       | <10% on a modest VM                         |
| RSS                                       | <200 MiB                                    |

Quick live check:

```bash
# Drop-alarm events (should be empty file or absent file).
ls -l data/alarms/$(date -u +%Y-%m-%d).jsonl 2>/dev/null

# Cumulative drops in stderr log (60s heartbeat, not the alarm itself).
grep 'frames dropped\|drop-alarm fired' var/tape.log | tail -5

# Disk growth over last hour.
du -sh data/*/$(date -u +%Y-%m-%d)

# Most recent rotation.
ls -lt data/kalshi/$(date -u +%Y-%m-%d) | head -5
```

### 3.1 Boot frame (cutover marker)

Every `cmd/tape` start emits a single `kind:"boot"` frame as the first
record in **both** `data/kalshi/<date>/<file>.jsonl` and
`data/openmeteo/<date>/<file>.jsonl` for that run. Source: see
[`cmd/tape/main.go:140`](../cmd/tape/main.go) (`bootSchemaVersion`) +
[`writeBootFrame` at :157](../cmd/tape/main.go).

Payload shape (`schema_version:"isobar-tape.boot/v1"`):

```jsonc
{
  "v": "isobar.journal/v1",
  "t": "2026-05-13T17:04:02.123456789Z",
  "src": "isobar-tape",            // not "kalshi"/"openmeteo"
  "k": "boot",
  "p": {
    "config_sha256": "<lowercase hex>",  // SHA-256 of the config.yaml bytes
    "build_commit":  "<short-sha>",
    "build_version": "<git describe>",   // optional
    "build_time":    "<RFC 3339>",       // optional
    "stations": [ { "Code":"KNYC", … }, … ],  // market.AllStationsCanonical()
    "schema_version": "isobar-tape.boot/v1"
  }
}
```

Operator use — **partition-by-correctness across config-cutover events**.
Two boot frames with different `config_sha256` values demarcate a tape
segment recorded against a different config; downstream tooling
(edge-scan, analyze) can predicate on `src == "isobar-tape"` to find
the segment boundaries. The motivating case was the CHI/KMDW cutover
(see §8 and `audit/chi-blast-radius.md`): boot-frame partitioning lets
the analyzer cleanly drop the 21.1% poisoned pre-fix corpus and trust
everything after the first boot frame stamped with the corrected
config SHA.

Inspecting the most recent boot frames:

```bash
# Today's first record per source (should be the boot frame).
head -1 data/kalshi/$(date -u +%Y-%m-%d)/*.jsonl  | head -1 | jq .
head -1 data/openmeteo/$(date -u +%Y-%m-%d)/*.jsonl | head -1 | jq .

# All boot frames in today's kalshi tape (should be one per cmd/tape start).
grep -h '"k":"boot"' data/kalshi/$(date -u +%Y-%m-%d)/*.jsonl | jq -r '.p.config_sha256'
```

### 3.2 Drop-alarm file (`data/alarms/<date>.jsonl`)

`cmd/tape` writes a structured alarm record to
`data/alarms/<YYYY-MM-DD>.jsonl` every time the journal drop-rate
alarm fires. Source: [`cmd/tape/alarms.go`](../cmd/tape/alarms.go);
mechanism documented in ADR-0011.

Record shape (one JSON object per line, UTC date rollover, `0o600` perms):

```jsonc
{
  "ts":                  "2026-05-13T17:04:02.123456789Z",
  "src":                 "kalshi",     // or "openmeteo"
  "dropped":             <delta-since-last-fire>,
  "queue_depth":         <int>,        // point-in-time at fire (len)
  "since_last_drop_ms":  <int64>       // ms since the first drop in this breach
}
```

A drop-alarm fires from the journal writer goroutine on the
**crossing edge** when sustained drop rate ≥ threshold (ADR-0011
defaults: ≥1 drop/sec for ≥60s; debounced — re-fires only after the
condition clears and re-crosses). The hook in cmd/tape sinks to three
places: this file, an `slog.Error` log line (`journal: drop-alarm
fired`), and the process-wide `JournalDropsTotal` atomic counter.

Triage playbook:

1. **Get the rate, not just the cumulative.** `since_last_drop_ms`
   localizes the burst window to ~1 second; `dropped` is the count
   surfaced *this firing* (delta since last alarm, not lifetime).
2. **Correlate with `Journal.Dropped()`** — the `Dropped()` accessor
   (preserved per ADR-0011 §5) returns the journal's lifetime drop
   counter. If the alarm file shows three breaches but the journal
   counter has incremented far past their sum, more drops happened
   *between* alarm crossings (under threshold or sustain-window) —
   chronic slow leak, not a single burst.
3. **Disk first.** Drops nearly always indicate fsync stall (the
   journal queue is sized for sustained 50 frames/s with a 16384-slot
   buffer; only multi-second fsync tails fill it). Run `iostat -x 1`
   for 30s, look for `%util` > 80 or `await` > 50ms.
4. **Then look at the rotation log line.** `journal: rotated` lines
   stamp the gzip-on-rotate event; rotation gzip can take 1-3s on
   large files and is the most common burst cause. If alarms always
   fire within 5s of a rotation log line, raise `journal.max_file_bytes`
   or rotate more frequently to shrink the gzip pause.

**Retention policy.** Operator decision today. Recommendation: keep
indefinitely (`data/alarms/` grows at ~bytes-per-event so it is not a
disk-pressure concern relative to `data/kalshi/`); rotate manually only
if `data/` total approaches disk budget per §5. The alarm file is the
*paper trail* — moving these files off-host before deleting is the
right shape if you need the space.

**Default behavior if no alarm has ever fired.** The file does not
exist — `alarmSink.rotateLocked` is lazy. An empty `data/alarms/`
directory is also valid (a previous run created it but no alarm
fired); the lazy-open path will reuse it on next breach.

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

### `fatal: load .env: .env at … has insecure mode 0o644 …`

`internal/env.CheckMode` refuses to load a `.env` whose mode is
group- or world-accessible. The error message includes the exact
chmod command. Recovery:

```bash
chmod 600 .env
```

The check is `(mode & 0o077) != 0` — any non-owner read/write/execute
bit disqualifies. Symlinks are followed (the target's mode is what
matters). This guard exists because `.env` typically contains the
inline RSA PEM (`KALSHI_PRIVATE_KEY`) or path to the PEM, and a
group-readable file is a credential leak.

### `config: openmeteo.locations[CHI] coords drift from canonical KMDW …`

`internal/config.validateStationCoords` refuses to boot if any
`openmeteo.locations` entry's lat/lon drifts more than 0.001° from
the canonical entries in
[`internal/market/stations.go`](../internal/market/stations.go).
This is the guard against the CHI/KORD-vs-KMDW class of bug
(see §8, audit/chi-blast-radius.md).

Recovery: update `config/config.yaml` to the canonical lat/lon, **or**
update the canonical table — but never silently both. Changes to the
canonical table require an ADR amendment because every recorded tape
file is implicitly station-tagged via the boot frame's
`stations[]` payload (§3.1) and the canonical table is the cross-
reference. Unknown locations (not in `AllStationsCanonical`) skip the
check and pass through.

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

### `ws frame starvation — forcing reconnect` repeating every ~90–120s

The watchdog sees no inbound frames for `MaxIdleBetweenFrames` (60s),
kills the connection, and the reconnect also produces no frames. The
warn line carries `tickers` (count) and `tickers_sample` — **read the
dates embedded in the sampled tickers first** (`KXHIGHCHI-26MAY27-…`
means the subscription is for May 27 brackets). If those dates are in
the past, the recorder is subscribed to settled markets: Kalshi acks
the subscribe and sends one stub `orderbook_snapshot` per market
(`market_id: ""`, no book fields), then nothing, ever.

Since the 2026-06-09 fix the subscriber re-resolves the open-market
set via `RefreshTickers` on **every** reconnect, so a stale in-memory
list self-heals on the next watchdog kill. If starvation still repeats
after reconnects, REST discovery itself is returning settled markets.
Verify from the affected host what the subscription actually contains
— dates and stub-ness are both visible in the journaled snapshots:

```bash
# What did the last connection subscribe to? (settled markets answer
# with market_id:"" stubs and no book fields)
jq -r 'select(.k=="ws" and .p.type=="orderbook_snapshot")
       | .p.msg.market_ticker + " " + (if (.p.msg.market_id // "") == "" then "STUB" else "ok" end)' \
  data/kalshi/$(date -u +%F)/*.jsonl | tail -20
```

Beware: `make run-peek` prints per-status market **counts** — a
plausible-looking count (e.g. 96) of zombie markets is exactly how the
2026-05 outage hid. Counts don't establish freshness; ticker dates do.
See §15 for the postmortem.

### `journal: drop-alarm fired` (slog.Error)

The journal drop-rate alarm crossed its threshold. See §3.2 for the
triage playbook and `data/alarms/<date>.jsonl` for the structured
record. The alarm is **edge-triggered** — one log line per crossing,
not per dropped frame. Default thresholds (ADR-0011): ≥1 drop/sec
sustained for ≥60s.

Most common root cause is disk fsync stall during gzip-on-rotate;
second is downstream consumer (analyze, edge-scan) holding the
directory inode open during a high-frame-rate burst. Tape itself
never blocks the WS reader on drops — see ADR-0011 for the
non-blocking-producer rationale.

### Binary exits immediately with `config: …`

Validation failure; the error message names the offending key. See
[`internal/config/config.go`](../internal/config/config.go) `Validate`.

### `make lint` fails with `clockcheck` violations

Per ADR-0012, all `time.Now()` / `time.Since()` / `time.Until()`
calls inside `internal/` must flow through one of three shapes:

1. A `clock.Clock` interface threaded into the constructor (preferred
   for new code; production wires `clock.Wall()` at the top of
   `cmd/tape/main.go`).
2. A `now func() time.Time` field on the struct, defaulting to
   `time.Now` (the existing `kalshi.Signer.now` pattern).
3. `internal/clock.Now()` for envelope timestamps (the existing
   journal envelope convention).

Allowlist exceptions: `internal/clock` itself, `cmd/*/main.go`,
`*_test.go`, and `//nolint:clockcheck` lines (must include a
justification). To fix a flagged line, pick one of (a) inject a
`Clock`, (b) annotate with `//nolint:clockcheck` + reason, (c)
move the call site to an allowed location. The analyzer lives at
[`tools/clockcheck/`](../tools/clockcheck/) and is run by
`make clockcheck` (a prerequisite of `make lint`).

## 8. Security posture

- Secrets live under `./secrets/` (chmod 700) or env vars. **Never** in
  `config.yaml` or a committed `.env`.
- `.env` must be `chmod 600` (enforced — see §0/§7). The binary
  refuses to start otherwise. This also applies whenever the operator
  edits `.env` and forgets to re-chmod (some editors recreate the file
  with the umask default).
- RSA key is `chmod 600`. The binary reads it once at startup; do not
  copy it elsewhere.
- Logs go to stderr in JSON. They include no secret material (the key ID
  is redacted in `main.go`).
- Data files (`data/`) are NDJSON of market feeds — not secrets, but
  contain Kalshi's proprietary market data. Don't publish publicly.
- `data/alarms/<date>.jsonl` files are mode `0o600`; the directory is
  `0o750`. Records contain no payload bytes, only counters/timestamps.
- `.env` is gitignored; `.env.example` is the only committed env file.

## 9. On-call behavior (Phase 3+, placeholder)

Phase 3 adds webhook-based alerting on `ERROR` slog lines. Until then:

- Phase 0 is not on-call critical. A tape outage loses data, not money.
- If the recorder dies overnight, restart in the morning and note the
  gap in `docs/decisions/`. Phase 0.5 analyzer handles gaps gracefully.

## 10. Checklists

**End-of-day Phase 0.**
- [ ] `tail -50 var/tape.log` has no error-level entries.
- [ ] `data/alarms/<today>.jsonl` absent or unchanged since morning.
- [ ] Dropped frame count (`Journal.Dropped()` via the 60s heartbeat
      log line) unchanged since morning.
- [ ] Disk usage growth within budget.
- [ ] At least one file per source under today's date folder, and the
      first record of each is a `kind:"boot"` frame.

**Pre-restart.**
- [ ] Pull latest `main`, `make all` green (`make lint` includes
      `clockcheck`, per ADR-0012).
- [ ] `git log -n 3` reviewed.
- [ ] Config diff vs. running version checked. If `openmeteo.locations`
      lat/lon changed, expect a new boot frame with a different
      `config_sha256` on the next start.
- [ ] `.env` mode is still `0o600` (umask drift after edits).
- [ ] Graceful shutdown of previous instance completed (saw
      `tape exited cleanly`).

## 11. Force-push precedent (`origin/main`)

Per [ADR-0013](decisions/0013-retire-pr9-paper-trader-lineage.md),
on 2026-05-13 `origin/main` was force-pushed
(`+ 6a6530a...93733e4 main -> main (forced update)`) to retire the
PR#9 paper-trader / fillability / score / settle / backfill-score
lineage during Bucket A consolidation. Recovery paths for that
lineage are documented in ADR-0013 §"Recovery paths"
(`refs/pull/{7,8,9}/head` on GitHub, plus the
`audit/2026-05-13` and `audit/2026-05-13-post-bucket-a` backup
branches; the 11 SHAs are listed in ADR-0013's Context table).

**Operator rule going forward.** Future force-pushes to `main`
require an **explicit "force-push authorized" line in the operator's
message**. The classifier denies inferred force-pushes — Claude will
refuse a force-push request that is implied rather than stated. This
guard exists because the 2026-05-13 force-push was the first and
should be the rare case; the default for `main` is fast-forward only.

Verify the current `origin/main` state:

```bash
git reflog show origin/main      # local — shows the 6a6530a→93733e4 transition for ~30d
git log --oneline -n 5 origin/main
```

## 12. Canonical station registry + CHI fix (2026-05-13)

Authoritative lat/lon, station codes, series prefixes, and CLI PIL
strings live in
[`internal/market/stations.go`](../internal/market/stations.go)
(`allStations`). Boot-time validation in
`internal/config.validateStationCoords` rejects any
`openmeteo.locations` entry that drifts > 0.001° from this registry
(see §7).

| Location | Station | Lat / Lon (canonical) | Series |
|---|---|---|---|
| NY  | KNYC | 40.7794 / -73.9692 | KXHIGHNY, KXLOWTNYC |
| MIA | KMIA | 25.7959 / -80.2870 | KXHIGHMIA, KXLOWTMIA |
| LAX | KLAX | 33.9425 / -118.4081 | KXHIGHLAX, KXLOWTLAX |
| CHI | **KMDW** | **41.7842 / -87.7553** | KXHIGHCHI, KXLOWTCHI |

**CHI/KMDW fix.** `config/config.yaml:64` was patched during Bucket A
from ORD (`41.9742, -87.9073`) to the canonical KMDW coordinates
(`41.7842, -87.7553`). Pre-fix, the CHI Open-Meteo poll stream was
sampling ORD ~14 mi away — close enough to look right in aggregate
but bracket-divergent on 16/25 days (64%) of the recorded window. The
full post-mortem is in
[`audit/chi-blast-radius.md`](../audit/chi-blast-radius.md):
**38,482 of 182,461 trade frames (21.1%)** in the pre-fix corpus
were paired with the wrong-station forecast.

**Recovery for poisoned tape** (already-recorded CHI frames before
2026-05-13): re-pair the Kalshi trade frames with a *fresh* KMDW poll
stream via offline scan. Open-Meteo's historical-forecast endpoint is
deterministic for `(lat, lon, date)`, so the parallel poll stream
under e.g. `data/openmeteo-kmdw/` is reproducible. `internal/edge`
keys CHI features off `Frame.Kind == "rest:CHI"`; the new KMDW frames
should re-use that Kind so the existing edge code resolves them
without modification. Old ORD frames stay on disk for audit but are
predicate-filtered by partitioning on the boot frame
(`config_sha256` before vs after the fix — see §3.1). Step-by-step
recipe is in `audit/chi-blast-radius.md` §"Recommendation".

## 13. Rotation status

`cmd/tape` rotates each per-source journal on the smaller of
`MaxFileBytes` (256 MiB default) or `MaxFileAge` (1h default), then
gzips the rotated file in-place if `CompressOnRotate` is set.
Per-day directories are layouts of multiple `*.jsonl.gz` files.

**Known anomaly: `data/kalshi/2026-05-12/`** was uncompressed at
audit time (118 raw `.jsonl` files) and was manually gzipped during
Bucket A. This was a one-off — every subsequent recording day is
gzipped automatically by the rotation logic; no operator action
needed under normal operation.

Heuristic for "rotation has stopped" (no alarm fires for this yet —
this is on the operator):

```bash
# Should return 0 (or a single in-progress file) past 24h on a
# previous day. Anything else is anomalous.
find data/kalshi/$(date -u -d 'yesterday' +%Y-%m-%d) -name '*.jsonl' -type f | wc -l
```

A non-zero count past 24h on a closed day means the rotate-and-gzip
step failed somewhere; check the `var/tape.log` for `journal: rotate
failed` lines and inspect for partial `.jsonl.gz` next to the same
basename `.jsonl` (the `gzipFile + Remove` pair is not atomic per the
audit notes).

## 14. Deploy ground truth (pending)

`systemctl cat isobar-tape.service` paste is **awaited from the
operator**. The canonical unit file, the install paths
(`/root/isobar/...` vs `/opt/isobar/...` is currently inconsistent
between operator memory and `scripts/loop-prompt.txt`), the user
account, the `RuntimeMaxSec` value (memory says 30h), and the
restart/cron schedule will land here once received. Until then this
section is intentionally a stub — do not paste speculative systemd
units; this runbook is operator-authoritative and the prod host is
the source of truth.

The known-out-of-repo deploy facts (memory only):

- Vultr HF host on Tailscale `100.108.80.29`.
- Cross-compile via `GOOS=linux GOARCH=amd64 make build`.
- Data extracted with `scp -r root@isobar-prod:/root/isobar/data/...`.

Once the unit file paste lands, the install/scp/restart sequence
moves here verbatim.

## 15. Postmortem: WS frame starvation outage (2026-05-21 → 2026-06-01)

**Status**: root-caused and fixed locally 2026-06-09; prod redeploy +
prod-side log forensics still open (see "Open items").

### Impact

- Kalshi tape collapsed from ~22 MB/day to <200 KB/day on 05-21..23,
  missing entirely on 05-24/25, stub-only through 06-01.
- 3 of 5 forward-validation days unusable (`n=15` windows instead of
  ~194); week-1 verdict (`reports/phase0.5/2026-05-26-forward-week1-verdict.md`)
  had to caveat its own n-counts.
- ~6,090 reconnect attempts logged by 05-26; every one paid the full
  30s backoff cap.

### Root cause

The subscriber's in-memory ticker list went fully stale — every
subscribed market had settled. Kalshi **acks** a subscribe for settled
markets and sends one stub `orderbook_snapshot` per ticker
(`market_id: ""`, no book fields), then nothing further: no deltas, no
trades, no error. The liveness watchdog (added 2026-05-13, c0c59f5)
correctly detected the silence at 60s and forced a reconnect — but the
reconnect path reused the same stale list, so the loop could never
converge. Observed cycle: connect → ack + N stubs in <2s → silence →
starvation kill at ~90s → ~30s capped backoff → repeat (~122s/cycle,
measured from the 05-26 tape).

Three compounding defects:

1. **Reconnects never re-resolved tickers.** Only the 4h rediscovery
   loop could refresh the list; if it stalled or its host slept
   through ticks (local Mac) — or discovery returned zombies (prod
   hypothesis, unconfirmed) — the WS layer had no second chance.
2. **`retry.Do` backoff never reset.** The attempt counter and backoff
   were monotonic over the process lifetime, so even a connection that
   had streamed healthily for hours reconnected at the 30s cap.
3. **The starvation warn was blind.** It logged frame age only; the
   subscribed tickers (whose names embed bracket dates) were not
   logged anywhere, so "subscribed to May 27 markets on May 29" was
   invisible without tape archaeology.

### What the data showed (how to re-derive)

All from the local `./data` archive (mix of scp'd prod days and local
runs), with `jq`/`awk` over the journal frames:

- Sick connections: `subscribed` acks + exactly one stub snapshot per
  ticker, **zero** `orderbook_delta`/`trade`, life <2s of frames.
  06-01 dead connections held 60 tickers dated 26MAY26/26MAY27 (all
  STUB); the first healthy connection at ~14:50Z held 48 tickers dated
  26JUN01 (all real) and streamed 45k+ deltas.
- Healthy-day gap analysis (05-20, 394k frames): max intra-connection
  inter-frame gap was **40s**; zero gaps >45s. The 60s
  `MaxIdleBetweenFrames` was correctly sized — "watchdog too
  aggressive on quiet evenings" is **refuted**.
- 05-21..23 prod days are hourly-rotated 23-byte gzips: zero frames
  per connection — not even acks. Signature differs from 05-26+
  (stubs); cause unconfirmed without prod logs (see Open items).
- Suspects ruled out: WS auth (acks arrived; fresh local boot on
  06-09 worked immediately), Kalshi subscribe-protocol change (same
  code captured 1.2 MB in 90s on 06-09 with a fresh list).

### Fix (2026-06-09)

- `internal/kalshi/ws.go`: `SubscriberOptions.RefreshTickers` — called
  at the start of **every** connection attempt with a bounded context
  (`RefreshTimeout`, default 30s); error/empty falls back to the
  last-known list. `cmd/tape` wires it to `DiscoverOpenTickers`, so a
  watchdog kill now self-heals on the very next connect. The 4h
  rediscovery loop is kept for adding next-day markets to long-lived
  healthy connections.
- `internal/retry`: `Config.ResetAfter` — an attempt that ran ≥
  ResetAfter before failing resets backoff and the attempt counter.
  The subscriber sets it to `2×MaxIdleBetweenFrames`, so post-outage
  reconnects start at `InitialBackoff` (500ms), not the 30s cap.
- Starvation warn now logs `tickers` + `tickers_sample` — stale
  bracket dates are visible in a single log line.
- Tests: `TestSubscriber_RefreshTickersOnReconnect`,
  `TestSubscriber_RefreshTickersFailureKeepsLastKnownList`,
  `TestDoResetAfterLongAttempt`,
  `TestDoResetAfterShortAttemptsStillBounded`.

### Open items (prod)

1. `journalctl -u isobar-tape --since 2026-05-20 --until 2026-05-24`
   on `isobar-prod`: the zero-frame days should name the actual error
   (dial failure vs silent server vs no-ack). This distinguishes a
   Kalshi-side blip from host-side (DNS pitfall — see deploy notes).
2. On the prod host, confirm discovery freshness at boot: ticker
   *dates* in the first post-restart subscription (§7 starvation
   entry has the one-liner). If prod's REST view returns settled
   markets as `open`, refresh-on-reconnect cannot help and the DNS
   path needs the fix.
3. Cross-compile + redeploy the fixed binary
   (`GOOS=linux GOARCH=amd64 make build`), restart the unit, then run
   the §2.1 1-hour smoke gate and confirm ≥80% expected daily frames
   for 3 consecutive days (the week-1 verdict's tape-health
   prerequisite) before resuming forward validation.
