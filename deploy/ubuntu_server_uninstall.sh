#!/usr/bin/env bash
#
# Ubuntu server uninstaller for opsagent.
#
# By default this is the reverse of ubuntu_server_install.sh *minus user data*:
# it stops/disables the service and removes the binary, systemd unit, and
# sudoers drop-in. The opsagent user, data dir (/var/lib/opsagent), env file
# (/etc/opsagent/env), and TLS dir (/etc/opsagent/tls) are left in place so
# that a subsequent reinstall picks up where it left off.
#
# Pass --purge to also delete all runtime state (data dir, env file, TLS dir,
# /etc/opsagent, and the opsagent system user). This is destructive and cannot
# be undone — all deployment history, secrets, and certs will be lost.
#
# Usage:
#   sudo ./ubuntu_server_uninstall.sh              # safe uninstall
#   sudo ./ubuntu_server_uninstall.sh --purge      # wipe everything (asks to confirm)
#   sudo ./ubuntu_server_uninstall.sh --purge -y   # wipe everything (no prompt)
#
set -euo pipefail

BIN_PATH="/var/lib/opsagent/bin/opsagent"
DATA_DIR="/var/lib/opsagent"
CONFIG_DIR="/etc/opsagent"
ENV_FILE="$CONFIG_DIR/env"
TLS_DIR="$CONFIG_DIR/tls"
SERVICE_NAME="opsagent.service"
SERVICE_UNIT_PATH="/etc/systemd/system/$SERVICE_NAME"
SUDOERS_FILE="/etc/sudoers.d/opsagent"

PURGE=0
ASSUME_YES=0
while [ $# -gt 0 ]; do
    case "$1" in
        --purge)   PURGE=1; shift;;
        -y|--yes)  ASSUME_YES=1; shift;;
        -h|--help) sed -n '2,20p' "$0"; exit 0;;
        *) echo "Unknown arg: $1" >&2; exit 1;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "This uninstaller must be run as root (try: sudo $0)" >&2
    exit 1
fi

if [ "$PURGE" -eq 1 ] && [ "$ASSUME_YES" -ne 1 ]; then
    echo "WARNING: --purge will permanently delete:"
    echo "  - $DATA_DIR (all deployment state and logs)"
    echo "  - $CONFIG_DIR (env file, TLS certs and keys)"
    echo "  - the opsagent system user"
    echo ""
    read -r -p "Type 'yes' to continue: " reply
    if [ "$reply" != "yes" ]; then
        echo "Aborted."
        exit 1
    fi
fi

# --- stop + disable service ---
if systemctl list-unit-files "$SERVICE_NAME" &>/dev/null && \
   systemctl cat "$SERVICE_NAME" &>/dev/null; then
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        echo "Stopping $SERVICE_NAME"
        systemctl stop "$SERVICE_NAME"
    fi
    if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
        echo "Disabling $SERVICE_NAME"
        systemctl disable "$SERVICE_NAME" >/dev/null
    fi
fi

# --- remove systemd unit ---
if [ -f "$SERVICE_UNIT_PATH" ]; then
    rm -f "$SERVICE_UNIT_PATH"
    systemctl daemon-reload
    echo "Removed $SERVICE_UNIT_PATH"
fi

# --- remove sudoers drop-in ---
if [ -f "$SUDOERS_FILE" ]; then
    rm -f "$SUDOERS_FILE"
    echo "Removed $SUDOERS_FILE"
fi

# --- remove binary ---
if [ -e "$BIN_PATH" ]; then
    rm -f "$BIN_PATH"
    echo "Removed $BIN_PATH"
fi

if [ "$PURGE" -ne 1 ]; then
    echo ""
    echo "Uninstall complete (user data preserved)."
    echo "Preserved:"
    echo "  - $DATA_DIR"
    echo "  - $ENV_FILE"
    echo "  - $TLS_DIR"
    echo "  - opsagent system user"
    echo "Re-run with --purge to delete these."
    exit 0
fi

# --- purge: wipe state dirs + user ---
if [ -d "$DATA_DIR" ]; then
    rm -rf "$DATA_DIR"
    echo "Removed $DATA_DIR"
fi

if [ -d "$CONFIG_DIR" ]; then
    rm -rf "$CONFIG_DIR"
    echo "Removed $CONFIG_DIR"
fi

if id -u opsagent &>/dev/null; then
    userdel opsagent 2>/dev/null || true
    if getent group opsagent >/dev/null; then
        groupdel opsagent 2>/dev/null || true
    fi
    echo "Removed opsagent system user"
fi

echo ""
echo "Purge complete. All opsagent state has been deleted."
