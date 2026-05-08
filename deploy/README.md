# Deploying isobar to Vultr (or any linux/amd64 host)

This is the production-deploy bundle for the paper-trading stack:

- **`tape`** — recorder (Kalshi WS + Open-Meteo polls → JSONL journals)
- **`paper-trader`** — live edge engine, appends "would-have-bet" decisions to a JSONL ledger
- **`settle`** — daily reconciler, reads the ledger and writes outcome rows after Kalshi settles each bracket

`paper-trader` and `settle` share `/opt/isobar/data/papertrade/` and the same Kalshi credentials. `tape` is independent — keep it running if you want a continuous recorded tape; paper-trader does not require tape (it opens its own connections).

## One-shot deploy from your laptop

```bash
# 1. Build linux/amd64 binaries
make build-linux

# 2. Stage the deploy bundle on the host
HOST=your-vultr-host
scp -r bin/linux-amd64 deploy config data/calibration "$HOST:/tmp/isobar-deploy/"
scp .env "$HOST:/tmp/isobar-deploy/.env"

# 3. Install
ssh "$HOST" 'sudo bash /tmp/isobar-deploy/deploy/install.sh /tmp/isobar-deploy'
```

The installer is idempotent — re-running it upgrades binaries and unit files in place while preserving `/opt/isobar/.env` and `/opt/isobar/data/`.

## What the installer does

1. Creates a system user `isobar` (no shell, no home).
2. Lays out `/opt/isobar/{bin,config,data,var}/` owned by `isobar:isobar`.
3. Installs `tape`, `paper-trader`, `settle` to `/opt/isobar/bin/`.
4. Installs config to `/opt/isobar/config/config.yaml` (only on first install — operator-edited copies are preserved).
5. Syncs `data/calibration/obs/` (NOAA CLI history used by climate-shrunk model).
6. Installs `.env` at mode 0600.
7. Installs three systemd units.
8. Enables + starts `isobar-paper-trader.service` and `isobar-settle.timer`.

## Layout after install

```
/opt/isobar/
├── bin/
│   ├── tape
│   ├── paper-trader
│   └── settle
├── config/
│   ├── config.yaml         (preserved across upgrades)
│   └── config.example.yaml
├── data/
│   ├── kalshi/             (recorder output, if tape running)
│   ├── openmeteo/
│   ├── papertrade/         (paper-trader bets + settle outcomes)
│   │   └── YYYY-MM-DD/
│   │       ├── papertrade-*.jsonl(.gz)   # bets
│   │       └── outcomes-*.jsonl          # settle output
│   └── calibration/obs/    (NOAA CLI history, read-only)
└── .env                    (mode 0600, KALSHI_*)

/etc/systemd/system/
├── isobar-paper-trader.service
├── isobar-settle.service
└── isobar-settle.timer
```

## Operations

```bash
# Tail the live trader
journalctl -u isobar-paper-trader -f

# Tail recent activity (last 1h)
journalctl -u isobar-paper-trader --since '1 hour ago'

# Stop / restart trader (keeps recorded bets)
systemctl stop isobar-paper-trader
systemctl restart isobar-paper-trader

# Run settle manually (dry-run first to check what would land)
sudo -u isobar /opt/isobar/bin/settle \
  -config /opt/isobar/config/config.yaml -dry-run -lookback 24h

# Force-fire the settle timer now (e.g. after a deploy)
systemctl start isobar-settle.service

# Check timer schedule
systemctl list-timers isobar-settle.timer

# Score the ledger (run from your laptop, with data synced back)
rsync -a "$HOST:/opt/isobar/data/papertrade/" data/papertrade/
./bin/score -config config/config.yaml
```

## Tuning the trader (live, no restart needed for most flags)

Override via systemd drop-in:

```bash
sudo systemctl edit isobar-paper-trader
```

Add a stanza like:

```ini
[Service]
ExecStart=
ExecStart=/opt/isobar/bin/paper-trader \
  -config /opt/isobar/config/config.yaml \
  -climate /opt/isobar/data/calibration/obs \
  -min-edge 5 \
  -max-spread 3 \
  -min-mid 10 \
  -max-mid 90 \
  -model climate_shrunk
```

Then `systemctl restart isobar-paper-trader`. The empty first `ExecStart=` line is required — it resets the unit's default before adding ours.

## Troubleshooting

- **`paper-trader` won't start, "no PEM source configured"**
  → `/opt/isobar/.env` is missing or unreadable by user `isobar`. Check `ls -la /opt/isobar/.env` (mode should be 0600, owner `isobar:isobar`).

- **`paper-trader` spammy reconnects**
  → Likely Kalshi rate-limiting on initial market discovery. Tune `kalshi.reconnect.{initial_backoff,max_backoff}` in `config.yaml` and restart.

- **`settle` reports `kalshi_pending` for all bets**
  → Brackets haven't actually settled yet on Kalshi's side. Re-run `settle` later or wait for the next timer fire.

- **Disk fills up under `/opt/isobar/data/`**
  → `tape` writes ~150-300 MB/day per source; the recorder rotates + gzips files but doesn't auto-delete. Add a cron to prune `data/{kalshi,openmeteo}/` files older than your retention window.

## Pre-flight before risking real money

This deploy bundle does NOT enable real-money trading. `paper-trader` only logs decisions; the Kalshi REST client's order-placing methods are unused.

Before flipping to real money, you need at minimum:

1. ≥ 200 scored paper bets (run `score` after a few weeks).
2. Per-bet Sharpe > 0 with reasonable significance.
3. Realized-vs-predicted edge slope ≥ 0.5 (model isn't anti-predictive).
4. Fillability rate consistent with `cmd/fillability` measurements on recorded data.
5. A separate `cmd/place-order` (not in this repo) with explicit per-trade cap and kill switch.

The current code path has zero "place real order" surface. Don't change that without a separate review.
