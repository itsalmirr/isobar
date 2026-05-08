#!/usr/bin/env bash
# install.sh — set up isobar paper-trader + settle on a fresh Linux host.
#
# Idempotent: re-running upgrades binaries and unit files in place,
# preserves /opt/isobar/.env and /opt/isobar/data/.
#
# Usage:
#   1. Build linux/amd64 binaries on your laptop:
#        make build-linux
#   2. scp the artifact bundle to the host:
#        scp -r bin/linux-amd64 deploy config data/calibration host:/tmp/isobar-deploy/
#        scp .env host:/tmp/isobar-deploy/.env       # creds (DO NOT commit)
#   3. ssh in and run as root:
#        sudo bash /tmp/isobar-deploy/deploy/install.sh /tmp/isobar-deploy
#
# Layout after install:
#   /opt/isobar/
#     bin/{tape,paper-trader,settle}
#     config/config.yaml
#     data/{kalshi,openmeteo,papertrade,calibration}
#     .env                          (chmod 600, owned by isobar:isobar)
#   /etc/systemd/system/isobar-*.{service,timer}

set -euo pipefail

DEPLOY_DIR="${1:-}"
if [[ -z "$DEPLOY_DIR" ]]; then
  echo "usage: install.sh <deploy-bundle-dir>" >&2
  echo "       expects bin/linux-amd64/, deploy/, config/, data/calibration/, .env inside" >&2
  exit 2
fi

if [[ "$EUID" -ne 0 ]]; then
  echo "must run as root (sudo)" >&2
  exit 2
fi

INSTALL_DIR=/opt/isobar
SYSTEMD_DIR=/etc/systemd/system
SVC_USER=isobar

# 1. Create system user if missing.
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  echo "==> creating user $SVC_USER"
  useradd --system --home-dir "$INSTALL_DIR" --shell /usr/sbin/nologin "$SVC_USER"
fi

# 2. Lay out dirs (idempotent).
echo "==> ensuring $INSTALL_DIR layout"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/bin"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/config"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/data"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/data/papertrade"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/data/calibration"
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$INSTALL_DIR/var"

# 3. Install binaries (replaces in place).
echo "==> installing binaries"
for bin in tape paper-trader settle; do
  src="$DEPLOY_DIR/bin/linux-amd64/$bin"
  if [[ ! -f "$src" ]]; then
    echo "missing $src — did you run 'make build-linux'?" >&2
    exit 1
  fi
  install -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$src" "$INSTALL_DIR/bin/$bin"
done

# 4. Install config (do NOT clobber an existing config.yaml — operator
# may have local edits). Always sync the example as a reference.
if [[ ! -f "$INSTALL_DIR/config/config.yaml" ]]; then
  echo "==> writing initial config/config.yaml"
  install -m 0644 -o "$SVC_USER" -g "$SVC_USER" \
    "$DEPLOY_DIR/config/config.example.yaml" "$INSTALL_DIR/config/config.yaml"
fi
install -m 0644 -o "$SVC_USER" -g "$SVC_USER" \
  "$DEPLOY_DIR/config/config.example.yaml" "$INSTALL_DIR/config/config.example.yaml"

# 5. Install climate obs (read-only reference data — overwrite).
if [[ -d "$DEPLOY_DIR/data/calibration/obs" ]]; then
  echo "==> syncing data/calibration/obs"
  rsync -a --delete --chown="$SVC_USER:$SVC_USER" \
    "$DEPLOY_DIR/data/calibration/obs/" "$INSTALL_DIR/data/calibration/obs/"
fi

# 6. Install .env (creds). Refuse if not provided — better to fail than
# leave a stale credential in place silently.
ENV_SRC="$DEPLOY_DIR/.env"
if [[ ! -f "$ENV_SRC" ]]; then
  echo "WARN: $ENV_SRC missing; leaving existing $INSTALL_DIR/.env unchanged" >&2
else
  install -m 0600 -o "$SVC_USER" -g "$SVC_USER" "$ENV_SRC" "$INSTALL_DIR/.env"
  echo "==> .env installed (mode 600)"
fi

# 7. Install systemd units.
echo "==> installing systemd units"
install -m 0644 "$DEPLOY_DIR/deploy/systemd/isobar-paper-trader.service" "$SYSTEMD_DIR/"
install -m 0644 "$DEPLOY_DIR/deploy/systemd/isobar-settle.service"       "$SYSTEMD_DIR/"
install -m 0644 "$DEPLOY_DIR/deploy/systemd/isobar-settle.timer"          "$SYSTEMD_DIR/"

systemctl daemon-reload

# 8. Enable + start (operator can disable individually if desired).
echo "==> enabling + starting paper-trader"
systemctl enable --now isobar-paper-trader.service

echo "==> enabling settle daily timer"
systemctl enable --now isobar-settle.timer

echo
echo "==> done. Status:"
systemctl --no-pager status isobar-paper-trader.service | head -10 || true
echo "---"
systemctl --no-pager list-timers isobar-settle.timer || true
echo
echo "Useful next commands:"
echo "  journalctl -u isobar-paper-trader -f      # tail live trader"
echo "  systemctl start isobar-settle             # one-off settle run"
echo "  /opt/isobar/bin/settle -config /opt/isobar/config/config.yaml -dry-run -lookback 24h"
