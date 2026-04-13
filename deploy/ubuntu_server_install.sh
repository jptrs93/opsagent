#!/usr/bin/env bash
#
# Ubuntu server installer + upgrader for opsagent.
#
# Two modes, auto-detected by whether /etc/systemd/system/opsagent.service
# already exists:
#
#   Fresh install (unit file absent):
#     Full setup — creates the opsagent user, data dir, env file, sudoers,
#     and systemd unit; downloads and installs the binary; enables the unit.
#     *Must be run as root* (the installer errors out immediately otherwise).
#
#   Upgrade (unit file present):
#     Downloads the release binary, verifies its checksum, atomically swaps
#     /var/lib/opsagent/bin/opsagent, and restarts the service. Nothing else
#     is touched — env file, sudoers, unit file, TLS dir, and user are all
#     left alone. *Does not require root*: can be run as the opsagent user
#     (which has NOPASSWD sudoers for `systemctl restart opsagent.service`).
#
# Usage:
#   # Fresh install (needs sudo)
#   curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/deploy/ubuntu_server_install.sh | sudo bash
#   sudo ./ubuntu_server_install.sh --version v0.1.0
#
#   # Upgrade (as opsagent user, no sudo needed)
#   sudo -u opsagent ./ubuntu_server_install.sh --version v0.1.0
#
set -euo pipefail

REPO="jptrs93/opsagent"
BIN_PATH="/var/lib/opsagent/bin/opsagent"
DATA_DIR="/var/lib/opsagent"
ENV_FILE="/etc/opsagent/env"
SERVICE_NAME="opsagent.service"
SERVICE_UNIT_PATH="/etc/systemd/system/$SERVICE_NAME"
SUDOERS_FILE="/etc/sudoers.d/opsagent"

VERSION="latest"
while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2;;
        -h|--help) sed -n '2,29p' "$0"; exit 0;;
        *) echo "Unknown arg: $1" >&2; exit 1;;
    esac
done

# --- detect mode ---
if [ -f "$SERVICE_UNIT_PATH" ]; then
    MODE="upgrade"
else
    MODE="install"
fi

# --- preflight: permission checks before we download or touch anything ---
if [ "$MODE" = "install" ]; then
    if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: first-time install must be run as root (try: sudo $0)" >&2
        exit 1
    fi
else
    BIN_DIR="$(dirname "$BIN_PATH")"
    if [ ! -w "$BIN_DIR" ]; then
        echo "ERROR: upgrade requires write access to $BIN_DIR" >&2
        echo "Run as root, or as the opsagent user (e.g. sudo -u opsagent $0)." >&2
        exit 1
    fi
    if [ "$(id -u)" -ne 0 ]; then
        if ! sudo -ln /usr/bin/systemctl restart "$SERVICE_NAME" &>/dev/null; then
            echo "ERROR: user '$(id -un)' is not permitted to restart $SERVICE_NAME without a password." >&2
            echo "Run the upgrade as root or as the opsagent user." >&2
            exit 1
        fi
    fi
fi

case "$(uname -m)" in
    x86_64)        ARCH="amd64";;
    aarch64|arm64) ARCH="arm64";;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1;;
esac

if [ "$VERSION" = "latest" ]; then
    echo "Resolving latest release..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | grep -m1 '"tag_name":' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        echo "Failed to resolve latest release tag" >&2
        exit 1
    fi
fi

if [ "$MODE" = "install" ]; then
    echo "Installing opsagent $VERSION (linux/$ARCH)"
else
    echo "Upgrading opsagent to $VERSION (linux/$ARCH)"
fi

BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
RAW_URL="https://raw.githubusercontent.com/$REPO/$VERSION/deploy"
BIN_FILE="opsagent-linux-$ARCH"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $BIN_FILE..."
curl -fsSL -o "$TMP/$BIN_FILE" "$BASE_URL/$BIN_FILE"
curl -fsSL -o "$TMP/sha256sums.txt" "$BASE_URL/sha256sums.txt"

echo "Verifying checksum..."
(cd "$TMP" && grep " $BIN_FILE\$" sha256sums.txt | sha256sum -c -)

# ============================================================================
# UPGRADE PATH — download to releases dir, symlink, restart.
# ============================================================================
if [ "$MODE" = "upgrade" ]; then
    RELEASE_DIR="$DATA_DIR/releases/$REPO/$VERSION"
    mkdir -p "$RELEASE_DIR"
    install -m 755 "$TMP/$BIN_FILE" "$RELEASE_DIR/$BIN_FILE"
    ln -sfn "$RELEASE_DIR/$BIN_FILE" "$BIN_PATH"
    echo "Installed $RELEASE_DIR/$BIN_FILE"
    echo "Symlinked $BIN_PATH -> $(readlink "$BIN_PATH")"

    echo "Restarting $SERVICE_NAME"
    if [ "$(id -u)" -eq 0 ]; then
        systemctl restart "$SERVICE_NAME"
    else
        sudo -n systemctl restart "$SERVICE_NAME"
    fi
    systemctl --no-pager status "$SERVICE_NAME" | head -n 5 || true
    echo "Upgrade to $VERSION complete."
    exit 0
fi

# ============================================================================
# FRESH INSTALL PATH — full setup.
# ============================================================================

# --- system user ---
if ! id -u opsagent &>/dev/null; then
    useradd --system --shell /usr/sbin/nologin --home-dir "$DATA_DIR" --create-home opsagent
    echo "Created system user: opsagent"
fi

# --- data directory ---
mkdir -p "$DATA_DIR"
chown opsagent:opsagent "$DATA_DIR"
chmod 750 "$DATA_DIR"

# --- binary directory ---
mkdir -p "$(dirname "$BIN_PATH")"
chown opsagent:opsagent "$(dirname "$BIN_PATH")"

# --- install binary (download to releases dir, symlink) ---
RELEASE_DIR="$DATA_DIR/releases/$REPO/$VERSION"
mkdir -p "$RELEASE_DIR"
chown -R opsagent:opsagent "$DATA_DIR/releases"
install -o opsagent -g opsagent -m 755 "$TMP/$BIN_FILE" "$RELEASE_DIR/$BIN_FILE"
ln -sfn "$RELEASE_DIR/$BIN_FILE" "$BIN_PATH"
echo "Installed $RELEASE_DIR/$BIN_FILE"
echo "Symlinked $BIN_PATH -> $(readlink "$BIN_PATH")"

# --- env file ---
mkdir -p "$(dirname "$ENV_FILE")"
if [ ! -f "$ENV_FILE" ]; then
    cat > "$ENV_FILE" <<'ENVEOF'
# Generate a hash with: cd backend && go run ./cmd/genhash
OPSAGENT_MASTER_PASSWORD_HASH=
OPSAGENT_GITHUB_TOKEN=
OPSAGENT_DATA_DIR=/var/lib/opsagent
OPSAGENT_ACME_HOSTS=opsagent.example.com
OPSAGENT_ACME_EMAIL=

# Cluster mTLS — generate certs with: deploy/tls/generate_certs.sh <machine-names>
# Copy ca.crt + this node's cert/key to /etc/opsagent/tls/
OPSAGENT_CLUSTER_CA=/etc/opsagent/tls/ca.crt
OPSAGENT_CLUSTER_CERT=/etc/opsagent/tls/node.crt
OPSAGENT_CLUSTER_KEY=/etc/opsagent/tls/node.key
OPSAGENT_CLUSTER_LISTEN=:9443
# For worker nodes only — set to the primary's address:
# OPSAGENT_PRIMARY_ADDR=10.0.0.1:9443
ENVEOF
    chmod 640 "$ENV_FILE"
    chown root:opsagent "$ENV_FILE"
    echo "Created env file: $ENV_FILE (edit before starting)"
fi

# --- TLS directory ---
mkdir -p /etc/opsagent/tls
chown root:opsagent /etc/opsagent/tls
chmod 750 /etc/opsagent/tls

# --- sudoers ---
cat > "$SUDOERS_FILE.new" <<SUDOEOF
# Allow the opsagent user to manage its own service without a password.
# systemctl is used for stop/start; systemd-run is used for restart so
# that self-restarts don't get caught in the cgroup teardown.
opsagent ALL=(root) NOPASSWD: /usr/bin/systemctl restart $SERVICE_NAME
opsagent ALL=(root) NOPASSWD: /usr/bin/systemctl stop $SERVICE_NAME
opsagent ALL=(root) NOPASSWD: /usr/bin/systemctl start $SERVICE_NAME
opsagent ALL=(root) NOPASSWD: /usr/bin/systemd-run --no-block /usr/bin/systemctl restart $SERVICE_NAME
SUDOEOF
chmod 440 "$SUDOERS_FILE.new"
visudo -cf "$SUDOERS_FILE.new" >/dev/null
mv -f "$SUDOERS_FILE.new" "$SUDOERS_FILE"

# --- systemd unit (fetched from the same tag) ---
echo "Downloading systemd unit..."
curl -fsSL -o "$TMP/$SERVICE_NAME" "$RAW_URL/$SERVICE_NAME"
install -m 644 "$TMP/$SERVICE_NAME" "$SERVICE_UNIT_PATH"
systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null

echo ""
echo "Install complete. $SERVICE_NAME is enabled but not started."
echo "Next steps:"
echo "  1. Edit $ENV_FILE:"
echo "     - On the primary: set OPSAGENT_MASTER_PASSWORD_HASH"
echo "       (generate with: cd backend && go run ./cmd/genhash)"
echo "     - On a worker: set OPSAGENT_PRIMARY_ADDR to the primary's host:port"
echo "  2. Copy cluster mTLS certs to /etc/opsagent/tls/"
echo "     (see deploy/tls/generate_certs.sh)"
echo "  3. Start: sudo systemctl start $SERVICE_NAME"
