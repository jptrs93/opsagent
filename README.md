# Setup notes

# Install

Installs from GitHub releases on Ubuntu (amd64 or arm64). The `opendeploy` binary
is its own installer — download it and run `opendeploy install`. Idempotent —
re-run to upgrade.

```bash
# Detect arch, fetch the latest release binary, and install.
ARCH=$(uname -m); case "$ARCH" in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; esac
curl -fsSL "https://github.com/jptrs93/opsagent/releases/latest/download/opendeploy-linux-$ARCH" -o opendeploy
chmod +x opendeploy
sudo ./opendeploy install
```

To pin a specific version (the installer fetches that release itself):

```bash
sudo ./opendeploy install --version v0.0.1
```

Preview every action without touching the host with `sudo ./opendeploy install --dry-run`.

The installer:

- creates the `opendeploy` system user, `/var/lib/opendeploy/` data dir, `/var/lib/opendeploy-releases/` release dir, and `/var/lib/opendeploy-volumes/` volume dir
- downloads and checksum-verifies the release binary into `/var/lib/opendeploy-releases/jptrs93/opsagent/<version>/opendeploy-linux-<arch>` and symlinks `/var/lib/opendeploy/bin/opendeploy` to it
- provisions a bundled, pinned containerd + runc runtime and the `opendeploy-containerd.service` unit
- generates primary cluster mTLS material on first startup and stores it encrypted in the primary secrets store
- writes `/etc/opendeploy/env` with placeholder secrets (first install only)
- installs sudoers + the `opendeploy.service` systemd unit
- on upgrade: atomically swaps the binary and restarts the service
- on first install: leaves the service stopped until you populate `/etc/opendeploy/env`

To remove: `sudo ./opendeploy uninstall` (keeps data) or `sudo ./opendeploy uninstall --purge` (wipes everything).

## First-run configuration

After the first install, edit `/etc/opendeploy/env`:

1. **`OPENDEPLOY_MASTER_PASSWORD_HASH`** — generate with `cd backend && go run ./cmd/genhash` (requires Go; run on any machine, copy the hash over).
2. **Primary vs. worker node** — the installed systemd unit runs `opendeploy primary`. Worker nodes should run `opendeploy secondary` instead and set `OPENDEPLOY_PRIMARY_ADDR=host:9443`.
3. **Cluster mTLS** — primary cluster (`:9443`) and enrollment (`:9444`) listeners start by default. Primary CA/server key material is generated automatically and stored encrypted in the primary secrets store. Workers run `opendeploy secondary`, set `OPENDEPLOY_PRIMARY_ADDR`, enroll with the primary, and then cache their received `ca.crt`, `node.crt`, and `node.key` under `/var/lib/opendeploy/tls/`.
4. **ACME / TLS** — set `OPENDEPLOY_ACME_HOSTS` and `OPENDEPLOY_ACME_EMAIL` to your public hostname and contact email.

Then start the service:

```bash
sudo systemctl start opendeploy
sudo journalctl -u opendeploy -f
```

# Development

Local dev uses a Nix flake as the source of truth for Go, Node, and pnpm versions. See `CLAUDE.md` for the full set of dev commands.
