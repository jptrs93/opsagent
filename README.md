# Setup notes

# Install

Installs from GitHub releases on Ubuntu (amd64 or arm64). The `opendeploy` binary
is its own installer — download it and run `opendeploy install primary` or
`opendeploy install secondary`. Idempotent —
re-run to upgrade.

```bash
# Detect arch, fetch the latest release binary, and install.
ARCH=$(uname -m); case "$ARCH" in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; esac
curl -fsSL "https://github.com/jptrs93/opsagent/releases/latest/download/opendeploy-linux-$ARCH" -o opendeploy
chmod +x opendeploy
sudo ./opendeploy install primary
```

To pin a specific version (the installer fetches that release itself):

```bash
sudo ./opendeploy install primary --version v0.0.1
```

Example primary install bound to a public IPv6 address with ACME TLS:

```bash
ARCH=$(uname -m); case "$ARCH" in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; esac
VERSION=v0.0.139
IPV6=2001:db8:203:a17e::12
DOMAIN=opendeploy.example.com

curl -fsSL "https://github.com/jptrs93/opsagent/releases/download/$VERSION/opendeploy-linux-$ARCH" -o opendeploy
chmod +x opendeploy
sudo ./opendeploy install primary \
  --version "$VERSION" \
  --web-listen "[$IPV6]:443" \
  --acme-hosts "$DOMAIN"
```

Use the IPv6 address without the `/128` prefix length in `--web-listen`, and point the domain's `AAAA` record at that address.

Preview every action without touching the host with `sudo ./opendeploy install primary --dry-run`.

The installer:

- creates the `opendeploy` system user, `/var/lib/opendeploy/` data dir, `/var/lib/opendeploy-releases/` release dir, and `/var/lib/opendeploy-volumes/` volume dir
- downloads and checksum-verifies the release binary into `/var/lib/opendeploy-releases/jptrs93/opsagent/<version>/opendeploy-linux-<arch>` and symlinks `/var/lib/opendeploy/bin/opendeploy` to it
- provisions a bundled, pinned containerd + runc runtime and the `opendeploy-containerd.service` unit
- generates primary cluster mTLS material on first startup and stores it encrypted in the primary secrets store
- writes `/etc/opendeploy/env` with a generated temporary setup password hash (first primary install only) and prints the password once
- installs sudoers + the `opendeploy.service` systemd unit
- on upgrade: atomically swaps the binary and restarts the service
- on first install: starts `opendeploy.service` and prints the Web UI address

To remove: `sudo ./opendeploy uninstall` (keeps data) or `sudo ./opendeploy uninstall --purge` (wipes everything).

## First-run configuration

After the first primary install, edit `/etc/opendeploy/env`:

1. **Initial setup password** — fresh primary installs print a temporary password like `opendeploy1234`. Use it only to register the first passkey.
2. **Primary vs. worker node** — install primaries with `opendeploy install primary`; install workers with `opendeploy install secondary --cluster-addr host:9443 --enrollment-addr host:9444`.
3. **Cluster mTLS** — primary cluster (`:9443`) and HTTPS enrollment (`:9444`) listeners start by default. Primary CA/server key material is generated automatically and stored encrypted in the primary secrets store. Workers use `OPENDEPLOY_PRIMARY_CLUSTER_ADDR` for mTLS traffic and `OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR` for bootstrap enrollment. During enrollment the worker generates its private key locally, sends a CSR, and caches the received `ca.crt` / `node.crt` plus its local `node.key` under `/var/lib/opendeploy/tls/`.
4. **ACME / TLS** — set `OPENDEPLOY_INITIAL_ACME_HOSTS` and `OPENDEPLOY_INITIAL_ACME_EMAIL` to your public hostname and contact email.

Then start the service:

```bash
sudo systemctl start opendeploy
sudo journalctl -u opendeploy -f
```

# Development

Local dev uses a Nix flake as the source of truth for Go, Node, and pnpm versions. See `CLAUDE.md` for the full set of dev commands.
