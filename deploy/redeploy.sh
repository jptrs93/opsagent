#!/usr/bin/env bash
#
# Redeploy opsagent by symlinking a new nix build output into the service's
# binary path and restarting the systemd service.
#
# Usage: ./redeploy.sh /nix/store/...-opsagent/bin/opsagent
#
# This script is designed to be run by the opsagent user (which has sudoers
# permission to restart its own service). It can also be run as root.
#
set -euo pipefail

BIN_PATH="/var/lib/opsagent/bin/opsagent"
SERVICE_NAME="opsagent.service"

if [ $# -lt 1 ]; then
    echo "Usage: $0 <path-to-new-binary>" >&2
    exit 1
fi

NEW_BIN="$1"

if [ ! -x "$NEW_BIN" ]; then
    echo "Error: $NEW_BIN does not exist or is not executable" >&2
    exit 1
fi

echo "Symlinking $BIN_PATH -> $NEW_BIN"
ln -sf "$NEW_BIN" "$BIN_PATH"

echo "Restarting $SERVICE_NAME"
sudo systemctl restart "$SERVICE_NAME"

echo "Done. Service status:"
systemctl --no-pager status "$SERVICE_NAME" || true
