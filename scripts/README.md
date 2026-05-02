# Scripts

Operational one-liners. Anything that grows beyond ~100 lines of bash
should be promoted to a proper Go subcommand under `cmd/`.

| script                | purpose                                                |
| --------------------- | ------------------------------------------------------ |
| [tape-health.sh](./tape-health.sh) | Smoke-check the tape after the first ~1h run; prints a PASS/FAIL verdict answering "is it worth 48 hours?" |
| [calibration-fetch-obs.sh](./calibration-fetch-obs.sh) | Pull hourly ASOS surface temperature from Iowa Mesonet for the calibration window. Output: `data/calibration/obs/K<STATION>-hourly.csv` + `MANIFEST.json`. |
| [calibration-fetch-cli.sh](./calibration-fetch-cli.sh) | Pull NWS CLI daily climate report bulletins from the IEM AFOS archive (canonical y-target per ADR-0007). Output: `data/calibration/cli/K<STATION>.zip` + `MANIFEST.json`. |

## Conventions

- POSIX-friendly bash. Works on macOS and Linux.
- Exit code `0` = happy, `1` = problem found, `2` = usage error.
- `--help` on every script. `-h` also accepted.
- Never write inside the repo. Everything operates on `./data/`,
  `var/*.log`, or explicit paths passed as flags.
