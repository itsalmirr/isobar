# audit/00-map.md — Phase 0: Map the territory

**Project**: `isobar` (Kalshi weather-bracket trading bot)
**Module**: `github.com/wh0o0pz/isobar` · Go 1.26
**Path**: `/Users/wh0o0pz/dev/isobar`
**main HEAD**: `4c2ede1` (15 commits ahead of `origin/main`)
**Mapped on**: 2026-05-13
**Scope**: every directory, dependency, entrypoint, data path, integration, env var, secret, listener, config, deploy unit, CI workflow, script, and doc.

---

## 0. Headline numbers

| Metric | Value |
|---|---|
| Go files | 65 (58 internal + 7 cmd) |
| Go LOC | 13,429 |
| Internal packages | 14 |
| Binaries built by `make build-all` | 7 |
| Direct deps | 5 |
| Transitive deps total | 30 |
| Markdown docs | 30 |
| Recorded tape on disk | 798 MB (kalshi 745 MB + openmeteo 8.7 MB + calibration 43 MB) |
| HTTP listeners | **0** — pure outbound client |
| Databases | **0** — append-only JSONL journals only |

---

## 1. File tree

```
isobar/
├── cmd/                   7 .go files, ~64 KB — one binary per tool
│   ├── tape/              recorder: Kalshi WS + Open-Meteo REST → JSONL journals (PROD)
│   ├── peek/              one-off Kalshi series-prefix probe (DEAD — used once)
│   ├── analyze/           reads journals, prints frame/gap/coverage sanity report
│   ├── edge-scan/         offline tape walker → per-trade naive/MOS/climate edge CSV
│   ├── calibrate-parse-cli/  parses NWS CLI bulletin zips → daily TMAX/TMIN CSV (truth)
│   ├── calibrate-fit/     pairs truth + GFS, fits MOS coefficients → JSON
│   └── calibrate-bayesian/  SCAFFOLD only — exits "not yet implemented" (ADR-0010)
│
├── internal/              58 .go files, ~512 KB
│   ├── edge/              12 files, 108 KB — central scan/edge engine [HOTSPOT]
│   ├── kalshi/             8 files,  80 KB — REST + WS v2, RSA-PSS auth [CRITICAL]
│   ├── calibration/
│   │   ├── mos/            3 files,  36 KB — MOS bias correction
│   │   ├── bayesian/       3 files,  28 KB — Bayesian posterior (ADR-0010 foundation)
│   │   ├── cliparse/       2 files,  76 KB + testdata/7 fixtures — NWS CLI parser
│   │   └── climate/        3 files,  24 KB — climatological prior
│   ├── replay/             7 files,  44 KB — JSONL reader + orderbook reconstruction
│   ├── journal/            3 files,  28 KB — append-only writer
│   ├── market/             5 files,  24 KB — bracket-ticker parser, station registry
│   ├── openmeteo/          2 files,  12 KB — ensemble-forecast HTTP client
│   ├── config/             2 files,  16 KB — YAML loader
│   ├── env/                2 files,  12 KB — .env loader
│   ├── obs/                2 files,   8 KB — slog setup
│   ├── clock/              2 files,   8 KB — wall+monotonic timestamps
│   └── retry/              2 files,   8 KB — ctx-aware exponential backoff
│
├── config/                config.yaml + config.example.yaml
├── docs/                 30 .md files, 336 KB — ARCHITECTURE/STRATEGY/RISK/CALIBRATION
│   ├── decisions/         10 ADRs + README + template
│   └── research/          9 pre-pivot essays + SYNTHESIS
├── audit/                 this directory
├── reports/               4 .md — phase 0.5 floor estimates + retracted v1
├── scripts/               5 .sh + README + loop-prompt.txt
├── data/                 593 files, 798 MB — recorded tape + calibration corpus
├── var/                   smoke.log only
├── bin/                  ⚠️ 7 prebuilt binaries (.gitignored but PRESENT, ~48 MB)
└── root                  go.mod, go.sum, Makefile, README, CHANGELOG, .golangci.yml, .env
```

### Top 10 largest source files (LOC)

| LOC | Path |
|---:|---|
| 656 | [internal/edge/scan_test.go](internal/edge/scan_test.go) |
| 569 | [internal/edge/scan.go](internal/edge/scan.go) |
| 544 | [internal/kalshi/client_test.go](internal/kalshi/client_test.go) |
| 514 | [internal/kalshi/client.go](internal/kalshi/client.go) |
| 500 | [internal/calibration/mos/mos_test.go](internal/calibration/mos/mos_test.go) |
| 464 | [internal/calibration/bayesian/bayesian_test.go](internal/calibration/bayesian/bayesian_test.go) |
| 397 | [cmd/edge-scan/main.go](cmd/edge-scan/main.go) |
| 382 | [internal/calibration/mos/mos.go](internal/calibration/mos/mos.go) |
| 381 | [internal/journal/journal_test.go](internal/journal/journal_test.go) |
| 353 | [internal/calibration/cliparse/cliparse.go](internal/calibration/cliparse/cliparse.go) |

### Unusual artifacts

- **`bin/` is committed despite `.gitignore`** — 7 binaries totaling ~48 MB, including `tape-linux-amd64` (14 MB). Investigate.
- **`data/kalshi/2026-05-12/` is uncompressed** (118 raw `.jsonl`) while earlier dates are gzipped — rotation script may not have run on the most recent day.
- **`reports/phase0.5/2026-05-02-floor-estimate_RETRACTED.md`** — intentionally retained as historical record.
- **`var/smoke.log`** — lone smoke-test artifact.

---

## 2. Dependency catalog

### Direct dependencies (5)

| Module | Version | License | Maintenance | Imported in | Verdict |
|---|---|---|---|---|---|
| `github.com/bytedance/sonic` | v1.15.1 | Apache-2.0 | Active | 3 files · `internal/replay/{kalshi,reader,orderbook}.go` | **CRITICAL** (hot JSON on replay) |
| `github.com/coder/websocket` | v1.8.13 | ISC | Active (community fork of nhooyr/websocket) | 2 files · `internal/kalshi/{ws,ws_test}.go` | **CRITICAL** (live trade feed) |
| `github.com/stretchr/testify` | v1.11.1 | MIT | Active | 15 files (test-only) | OK |
| `golang.org/x/sync` | v0.20.0 | BSD-3-Clause | Go team | 1 file · `cmd/tape/main.go` (errgroup) | OK |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT/Apache-2.0 | Frozen since 2022 | 1 file · `internal/config/config.go` | STALE-OK (no CVEs against v3.0.1) |

### Transitive notable

- `golang.org/x/arch v0.0.0-20210923...` — **pseudo-version** pulled by sonic (2021 timestamp).
- `golang.org/x/telemetry v0.0.0-20260409...` — tooling chain, fine.
- `tool` directive: `goimports` + `gofumpt v0.9.2` — invoked from `make fmt`.

`go mod tidy -diff` is clean. No `// indirect` deps used directly in code. No `replace` directives.

---

## 3. Entrypoints

No HTTP, gRPC, message queue, or `exec.Command` anywhere. Only `cmd/tape` + the two `calibrate-*` cli's install signal handlers. All other binaries are pure one-shots.

| Binary | Purpose | External calls | Concurrency | Status |
|---|---|---|---|---|
| **[cmd/tape](cmd/tape/main.go)** | Market-data recorder: WS + REST → JSONL | Kalshi REST + WS, Open-Meteo REST | errgroup + 4–6 goroutines; SIGINT/SIGTERM via `signal.NotifyContext` | **PROD-RUNNING** (Vultr) |
| **[cmd/edge-scan](cmd/edge-scan/main.go)** | Offline backtest engine | None (reads disk) | Single-threaded | DEV-ONLY |
| **[cmd/calibrate-fit](cmd/calibrate-fit/main.go)** | Fit MOS coefficients | None | Single-threaded; ctx for early exit | DEV-ONLY |
| **[cmd/calibrate-parse-cli](cmd/calibrate-parse-cli/main.go)** | NWS CLI zip → CSV | None | Single-threaded | DEV-ONLY |
| **[cmd/calibrate-bayesian](cmd/calibrate-bayesian/main.go)** | Kernel-fit scaffold | None | None | ⚠️ **DEAD (scaffold only)** — exits "not yet implemented" |
| **[cmd/peek](cmd/peek/main.go)** | Series-prefix probe | Kalshi REST | Single-threaded | ⚠️ DEAD — series now hardcoded in config |
| **[cmd/analyze](cmd/analyze/main.go)** | Frame-count sanity report | None | Single-threaded | DEV-ONLY |

Only `tape` is in the default `make build` target. The other 6 build under `make build-all`.

---

## 4. Critical data-flow paths

### Path 1 — Live recording (`cmd/tape`, PROD)

```
cmd/tape main()                                          cmd/tape/main.go:43
 ├─ signal.NotifyContext(SIGINT, SIGTERM)               :67
 └─ errgroup.WithContext                                 :78
     │
     ├── g.Go(startKalshi)                               :81
     │     ├ kalshi.NewClient (REST signer)              internal/kalshi/client.go:33
     │     ├ rest.DiscoverOpenTickers                    REST /markets pagination
     │     ├ journal.Open(dir=data/kalshi)               spawns writer goroutine
     │     ├ kalshi.NewSubscriber
     │     ├ go runRediscovery (BARE go, NOT in eg)      ⚠️ cmd/tape/main.go:131
     │     └ sub.Run → retry.Do → runOnce                internal/kalshi/ws.go:130
     │         ├ websocket.Dial (auth headers)           wss Kalshi WS
     │         ├ go reloadCh watcher                     ws.go:143
     │         └ for { conn.Read → OnFrame(raw) }
     │            └ jrn.Write(Frame{ts, kalshi, ws, payload})
     │              (non-blocking; drops on full queue ⚠)
     │
     ├── g.Go(startOpenMeteo)                            :84
     │     ├ journal.Open(dir=data/openmeteo)
     │     └ poller.Run → ticker(30m) → http.Get → OnFetch → jrn.Write
     │
     └── Per-source journal writer goroutine             internal/journal/journal.go:161
         ├ buffered chan(16384) → bufio.Writer → file
         ├ flush every 250ms; rotate at 256 MiB or 1h
         └ on close: drain, fsync
            → data/<src>/YYYY-MM-DD/<src>-HHMMSS.NNNNNNNNN.jsonl(.gz)
```

### Path 2 — Offline backtest (`cmd/edge-scan`, single-threaded)

```
cmd/edge-scan main()                                     cmd/edge-scan/main.go:31
  build ScanOptions {Truth, MOS, Climate} → edge.Scan
    ├ loadPolls(data/openmeteo, [start,end))             builds per-station poll index
    ├ dailyFiles(data/kalshi, [start,end))               enumerates journals
    ├ collectSiblings(kalshiFiles)                       T-bracket sibling pre-pass
    └ replay.ForEachFrame(kalshiFiles, fn):              internal/replay/reader.go:106
        per trade frame:
          parse ticker → spec
          poll = idx.latest(station, ts) within MaxPollAge
          extremes per civil-day local tz
          resolveWithSiblings → naive p_fair
          MOS Predict → pFairMOS (with FitModelSet guard)
          Climate.RangeProb + ShrinkP → pFairClimate
          Truth.Get → realized PnL
          → EdgeRow

  → CSV edges-<start>_<end>.csv + summary-<start>_<end>.md
```

### Path 3 — MOS calibration (`cmd/calibrate-fit`, single-threaded)

```
cmd/calibrate-fit main()                                 cmd/calibrate-fit/main.go:35
  loadAllPairs: for each station:
    forecast CSV (gfs_tmax_f, gfs_tmin_f)
    obs CSV (tmax_f, tmin_f)
    pair-by-date → []mos.Pair
  mos.Fit(pairs, holdoutDays) → per (station,season,variable) {a, b, σ, SigmaSource}
  coeffs.FitModelSet = [ModelGFS]
  atomic rename → data/calibration/mos-coefficients.json
```

---

## 5. Goroutine / lifecycle inventory (cmd/tape steady state)

| # | Spawn site | Owner | Cancellation | Leak risk |
|---|---|---|---|---|
| 1 | `errgroup.Go(startKalshi)` | errgroup | ctx | none |
| 2 | `errgroup.Go(startOpenMeteo)` | errgroup | ctx | none |
| 3 | `go j.run()` Kalshi journal writer | `defer jrn.Close()` drains via `j.closed` | indirect via ctx | none |
| 4 | `go j.run()` OpenMeteo journal writer | symmetric | symmetric | none |
| 5 | ⚠️ **`go runRediscovery`** at [cmd/tape/main.go:131](cmd/tape/main.go:131) — **BARE go, NOT in errgroup** | ctx-select only | ctx | **mild** — orphaned if blocking REST call ignores ctx; no `Wait` |
| 6 | `go reloadCh watcher` per WS connect | runCtx (defer cancel) | ctx OR reloadCh | none — defer covers |

**Steady state: 6 goroutines.** SIGINT cleanly cancels ctx → goroutines return → defers drain queues + fsync + close. **Backpressure: `journal.Write` is non-blocking** ([journal.go:124](internal/journal/journal.go:124)). Full queue → frame dropped silently → counter incremented, periodic warn log every 60s. **Risk**: data loss under sustained burst > 16k frames/250ms.

---

## 6. External integration surface

| Service | Protocol | Auth | Endpoint | Timeout | Retry | Rate limit |
|---|---|---|---|---|---|---|
| Kalshi Trade API v2 (REST) | HTTPS | RSA-PSS SHA-256 headers | `api.elections.kalshi.com/trade-api/v2` | 10s | Up to 5× on 429, `2^attempt` backoff | Self-throttle 1 req/s + adaptive on 429 |
| Kalshi Trade API v2 (WS) | WSS | Same RSA-PSS headers on upgrade | `wss://…/ws/v2` | No socket timeout; ctx-bound; 8 MiB msg cap | `retry.Do` 500ms→30s jittered | n/a (passive subscriber) |
| Open-Meteo Ensemble | HTTPS | none | `ensemble-api.open-meteo.com/v1/ensemble` | 30s | ⚠️ **None** — log warn + next tick | Self: poll_interval ≥ 1m |
| Open-Meteo Historical (calibration) | HTTPS | none | `historical-forecast-api.open-meteo.com/v1/forecast` | ⚠️ none set (`curl -sfS`) | none | none |
| IEM ASOS (calibration obs) | HTTPS | none | `mesonet.agron.iastate.edu/cgi-bin/request/asos.py` | `--max-time 300` | `--retry 3 --retry-delay 5` | `sleep 1` between stations |
| IEM NWS CLI bulletins | HTTPS | none | `mesonet.agron.iastate.edu/cgi-bin/afos/retrieve.py` | `--max-time 300` | `--retry 3 --retry-delay 5` | `sleep 1` between requests |

**No databases. No HTTP/gRPC listeners. No cron, no `time.AfterFunc`, no `exec.Command`.**

---

## 7. Environment variables

| Var | Type | Required? | Read in | Purpose |
|---|---|---|---|---|
| `ISOBAR_LOG_LEVEL` | string | no, default `info` | cmd/tape, cmd/calibrate-*, `internal/obs/log.go:16` | slog level |
| `ISOBAR_DATA_DIR` | path | no, default `./data` | `internal/config/config.go:153` | Override journal root |
| `ISOBAR_KALSHI_API_KEY_ID` / `KALSHI_API_KEY_ID` | UUID | yes (if `kalshi.enabled`) | `config.go:141` | Kalshi key ID |
| `ISOBAR_KALSHI_PRIVATE_KEY_PATH` / `KALSHI_PRIVATE_KEY_PATH` | path | yes (one of these) | `config.go:144` | PEM file path |
| `ISOBAR_KALSHI_PRIVATE_KEY` / `KALSHI_PRIVATE_KEY` / `KALSHI_API_KEY` | inline PEM | alt of above | `config.go:150` | PEM bytes |

`internal/env/env.go` parses `.env` and `os.Setenv` without overwrite. No `viper` / `envconfig` — stdlib `os.Getenv` only.

### Mismatches found

- ⚠️ `ISOBAR_LIVE_ALLOW` is referenced in `docs/research/04-dashboard-architecture.md` + `docs/decisions/0003-dashboard-stack.md` as a live-trade gate but **never read in any `.go` file**.
- ⚠️ `cloud-id=8c725749-...` line in `.env:31` is **never consulted** — looks like a stray artifact from onboarding.
- `ISOBAR_DATA_DIR` + `ISOBAR_KALSHI_*` variants are read but **not in `.env.example`**.

---

## 8. Secrets + filesystem paths

### Secrets convention

`.env` at repo root for env vars; PEM file at operator-chosen path (recommended `./secrets/kalshi.pem`). `.gitignore` covers `secrets/`, `*.pem`, `*.key`, `.env*` except `.env.example`. `git log --all -p -- .env` is empty (never committed).

### Findings

- **P1**: `/Users/wh0o0pz/dev/isobar/.env` contains a **real RSA-2048 private key in plaintext** with default mode `0o644`. Should be `0o600`. Not committed (verified). Treat as live credential.
- **P0 hardcoded credentials**: none. No `sk_`/`pk_`/base64 patterns in source files.
- Logging hygiene: [cmd/tape/main.go:293](cmd/tape/main.go:293) `redactKeyID` truncates to first 8 chars + `…`. PEM bytes never logged.

### Filesystem write paths

| Path pattern | Op | Used by | Perms |
|---|---|---|---|
| `<DataDir>/<src>/YYYY-MM-DD/<src>-HHMMSS.NNNNNNNNN.jsonl(.gz)` | append/rotate | tape journal | dir 0o750, file 0o600 |
| `<DataDir>/calibration/mos-coefficients.json` | atomic rename | calibrate-fit | umask |
| `<DataDir>/calibration/obs/K<STA>.csv` | create | calibrate-parse-cli | umask |
| `<DataDir>/calibration/edge-scan/edges-*.csv` + `summary-*.md` | create | edge-scan | umask |
| operator PEM file | read | tape, peek | operator-set |
| `./.env` (walked upward) | read | env.LoadNearest | read |
| `./config/config.yaml` | read | tape | read |

**No SQLite. No `/tmp` usage in Go code. No lock files. No log files** (slog → stderr).

---

## 9. Configuration matrix

| File | Loaded by | Top-level keys | Required when |
|---|---|---|---|
| `config/config.yaml` | tape (via `-config`), peek (indirect) | `data_dir`, `journal`, `kalshi`, `openmeteo` | `kalshi.enabled` → `ws_url`, `series`, `channels`; `openmeteo.enabled` → `base_url`, `locations`, `poll_interval ≥ 1m` |
| `config/config.example.yaml` | docs / cp template | same + `kalshi.rediscover_interval` (4h default) | n/a |
| `.env.example` | template | `KALSHI_API_KEY_ID`, `KALSHI_PRIVATE_KEY[_PATH]`, `ISOBAR_LOG_LEVEL` | n/a |
| `.golangci.yml` | `make lint`, CI | enables errcheck, govet, staticcheck, gosec, gofumpt | n/a |
| `Makefile` | dev + CI | 13 phony targets (see §11) | n/a |
| `go.mod` | go toolchain | 5 direct deps; tool block (gofumpt+goimports); no `replace` | n/a |

### YAML drift

- `config.example.yaml:51` documents `rediscover_interval: 4h`; `config.yaml` omits (falls back to 4h default at [config.go:102](internal/config/config.go:102)) — fine.
- ⚠️ **`config.example.yaml:70` uses CHI/Midway (KMDW) lat/lon `41.7861/-87.7522`; `config/config.yaml:64` uses ORD `41.9742/-87.9073`**. Kalshi resolves to Midway per the comment in the example file. **Active config is geographically wrong for KXHIGHCHI/KXLOWTCHI** — systematic prior bias on Chicago markets.

---

## 10. Deployment topology

**No `deploy/` directory exists in the repo.** No systemd unit files, no Dockerfile, no Procfile. Deploy is captured only in:

- `~/.claude/projects/.../memory/project_isobar_prod_deploy.md` (user memory) — says `/root/isobar/...` on Vultr, Tailscale `100.108.80.29`, systemd `isobar-tape.service` with `RuntimeMaxSec=30h`.
- `scripts/loop-prompt.txt:3-5` references **`/opt/isobar/...`** and a separate `isobar` user. ⚠️ **Contradicts memory.** One is stale.

### Deploy flow (memory-only)

1. Cross-compile: `GOOS=linux GOARCH=amd64 make build`
2. `scp` binary + config + `.env` to `root@isobar-prod`
3. `systemctl restart isobar-tape`
4. Data extraction: `scp -r root@isobar-prod:/root/isobar/data/...`

**Risk:** unit file, install script, user, and target paths are documented only in operator memory + one inconsistent text file. **No deploy artifact in the repo.**

---

## 11. CI/CD

Single workflow: `.github/workflows/ci.yml`. No `CODEOWNERS`, no `PULL_REQUEST_TEMPLATE.md`.

| Trigger | Jobs | Steps | Gates |
|---|---|---|---|
| push to main, PR to main | test + lint (parallel) | test: checkout → setup-go 1.26 → `go mod tidy && git diff --exit-code go.{mod,sum}` → `go vet` → `go test -race -count=1 -timeout=120s ./...` → `make build-all` | lint: golangci-lint-action@v6 | Concurrency group cancels in-progress; `permissions: contents: read`. **Branch protection / required-checks not declared in repo** — verify via `gh api repos/.../branches/main/protection` |

---

## 12. Scripts + Makefile

### `scripts/`

| Script | Purpose | Used by | Idempotent |
|---|---|---|---|
| `tape-health.sh` | PASS/FAIL smoke of running tape | human, memo CI hooks | yes |
| `calibration-fetch-obs.sh` | IEM ASOS hourly → CSV + MANIFEST | human | yes |
| `calibration-fetch-cli.sh` | IEM AFOS CLI bulletins → ZIP + MANIFEST | run-backtest.sh + human | yes |
| `calibration-fetch-forecast.sh` | Open-Meteo historical GFS → CSV | human | yes |
| `run-backtest.sh` | End-to-end realized-PnL chain | `make backtest`, human | yes |
| `loop-prompt.txt` | Claude `/loop` prompt template (not a shell script) | human | n/a |

All scripts use `set -u -o pipefail` but **none use `set -e`** — intentional per `scripts/README.md` (graceful per-station failure tolerance).

### Makefile targets (13 phony)

`all` (lint+vet+test+build), `build`, `build-all`, `run-tape`/`run-peek`/`run-analyze`/`run-edge-scan`, `backtest` (requires TAPE/START/END env), `test`/`test-short`/`bench`/`coverage`, `vet`/`lint`/`fmt`/`tidy`/`clean`.

---

## 13. Documentation inventory

`docs/` — 10 top-level docs + `decisions/` (10 ADRs + README + template) + `research/` (9 essays).

| Doc | mtime | Status |
|---|---|---|
| docs/ARCHITECTURE.md | 2026-05-13 | fresh |
| docs/BACKTEST.md | 2026-05-13 | fresh |
| docs/CALIBRATION.md | 2026-05-13 | fresh |
| docs/TESTING.md | 2026-05-13 | fresh |
| docs/ROADMAP.md | 2026-05-05 | fresh |
| docs/OPERATIONS.md | 2026-04-22 | ⚠️ **predates prod-deploy 2026-05-12** — likely missing RuntimeMaxSec, rediscover_interval, Tailscale DNS notes, CHI station bug |
| docs/RISK.md | 2026-04-18 | ⚠️ predates ADR-0009/0010 |
| docs/STRATEGY.md | 2026-04-18 | ⚠️ predates ADR-0009/0010 |
| docs/GLOSSARY.md | 2026-04-22 | fresh |
| docs/README.md | 2026-04-18 | ⚠️ **stale** — missing 5 newer docs + 4 newer ADRs |
| docs/decisions/0001-0006 | 2026-04-18 / 04-22 | immutable by policy |
| docs/decisions/0007-0010 | 2026-04-29 → 2026-05-13 | fresh; 0010 still "Proposed" |
| docs/research/01-08 + SYNTHESIS | 2026-04-18 | historical by design |

---

## 14. Issues surfaced during mapping

Each item is a concrete defect, not an opinion. File:line included where applicable. To be confirmed / categorized in Phase 1.

### P1 — correctness / runtime

1. **CHI lat/lon points to ORD, not KMDW** — [config/config.yaml:64](config/config.yaml:64) (`41.9742/-87.9073` = ORD). Kalshi resolves CHI markets to **Midway (KMDW)** per the comment in `config.example.yaml:70` (`41.7861/-87.7522`). Systematic prior bias on `KXHIGHCHI`/`KXLOWTCHI`. Real bug.
2. **`runRediscovery` not in errgroup** — [cmd/tape/main.go:131](cmd/tape/main.go:131). Bare `go func()`. No `Wait`. If REST `DiscoverOpenTickers` ignores ctx cancellation (it doesn't today, but future change risks it), goroutine leaks past shutdown.
3. **Open-Meteo has no retry, no circuit breaker** — failed fetch is logged at warn and skipped to next 30m tick. A multi-hour outage = silent multi-hour data gap.
4. **Journal `Write` silently drops on full queue** ([journal.go:124](internal/journal/journal.go:124)). Counter increments + 60s warn log, but the dropped frames are gone. Bursts > 16k frames in 250ms = data loss.

### P2 — supply chain / repo hygiene

5. **`bin/` is committed despite `.gitignore`** — 7 binaries, ~48 MB. Strip from history or fix the gitignore pattern.
6. **`.env` plaintext PEM with default `0o644`** — `chmod 600 .env` recommended; or migrate PEM to a separate `secrets/kalshi.pem` with explicit perms.
7. **`data/kalshi/2026-05-12/` uncompressed** — rotation script may not have run; 118 raw `.jsonl`.

### P3 — drift / docs

8. **`ISOBAR_LIVE_ALLOW`** documented as a live-trade gate but **never read in any `.go` file**. Either implement (W4 work per docs) or remove the references.
9. **`cloud-id=...` stray entry in `.env:31`** — unused, looks like onboarding artifact.
10. **`docs/README.md`** at 2026-04-18 is missing references to CALIBRATION.md, BACKTEST.md, ROADMAP.md, the four new ADRs (0007-0010), and a few research docs.
11. **`docs/OPERATIONS.md`** predates the live prod deploy — likely missing RuntimeMaxSec, the Tailscale DNS pitfall, CHI station mismatch, post-refactor file paths.
12. **`docs/STRATEGY.md` + `docs/RISK.md`** predate the MOS canary firing (ADR-0009) and the T-3h Bayesian plan (ADR-0010).
13. **Deploy is in operator memory only** — no `deploy/` dir in repo. `scripts/loop-prompt.txt` references `/opt/isobar/...` while memory says `/root/isobar/...`. One is wrong.
14. **Branch protection is not declared in repo** — verify via `gh api repos/<owner>/<repo>/branches/main/protection`.

### Dead code surfaced

15. **`cmd/calibrate-bayesian`** — exits "not yet implemented" ([main.go:74](cmd/calibrate-bayesian/main.go:74)). Foundation only per ADR-0010; expected to remain scaffold until Phase 1 implementation.
16. **`cmd/peek`** — one-shot Kalshi series-prefix probe; series now hardcoded in config. No recent invocations.

---

## End of Phase 0

Per the cadence rule: **stop and confirm this map before proceeding to Phase 1 (line-by-line audit).** Phase 1 will inspect every file for dead code, error handling, magic numbers, concurrency safety, and untrusted input.
