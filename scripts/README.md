# Scripts

Operational one-liners. Anything that grows beyond ~100 lines of bash
should be promoted to a proper Go subcommand under `cmd/`.

| script                | purpose                                                |
| --------------------- | ------------------------------------------------------ |
| [tape-health.sh](./tape-health.sh) | Smoke-check the tape after the first ~1h run; prints a PASS/FAIL verdict answering "is it worth 48 hours?" |

## Conventions

- POSIX-friendly bash. Works on macOS and Linux.
- Exit code `0` = happy, `1` = problem found, `2` = usage error.
- `--help` on every script. `-h` also accepted.
- Never write inside the repo. Everything operates on `./data/`,
  `var/*.log`, or explicit paths passed as flags.
