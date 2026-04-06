

# Setup notes

- Configure ipv6 addresses

```/etc/netplan/99-local.yaml
network:
  version: 2
  ethernets:
    enp66s0f0:
      addresses:
        - "2607:5300:203:a17e::/64"
        - "2607:5300:203:a17e::10/128"
        - "2607:5300:203:a17e::11/128"
        - "2607:5300:203:a17e::12/128"
      routes:
        - to: "default"
          via: "2607:5300:203:a1ff:ff:ff:ff:ff"
          on-link: true
```
NOTE: specific addresses above must be adjusted based on machines assigned ipv6 block
```bash
sudo chmod 600 /etc/netplan/99-local.yaml
sudo chown root:root /etc/netplan/99-local.yaml
sudo netplan generate
sudo netplan try
```

- Set AAAA DNS record to ops.d.flippingcopilot.com

# Install

Installs from GitHub releases on Ubuntu (amd64 or arm64). Idempotent — re-run to upgrade.

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/deploy/ubuntu_server_install.sh | sudo bash
```

To pin a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/v0.0.1/deploy/ubuntu_server_install.sh \
  | sudo bash -s -- --version v0.0.1
```

The installer:

- creates the `opsagent` system user and `/var/lib/opsagent/` data dir
- downloads and checksum-verifies the release binary into `/var/lib/opsagent/bin/opsagent`
- writes `/etc/opsagent/env` with placeholder secrets (first install only)
- installs sudoers + the `opsagent.service` systemd unit
- on upgrade: atomically swaps the binary and restarts the service
- on first install: leaves the service stopped until you populate `/etc/opsagent/env`

## First-run configuration

After the first install, edit `/etc/opsagent/env`:

1. **`OPSAGENT_MASTER_PASSWORD_HASH`** — generate with `cd backend && go run ./cmd/genhash` (requires Go; run on any machine, copy the hash over).
2. **Primary vs. worker node** — leave `OPSAGENT_PRIMARY_ADDR` unset on the primary; set it to `host:9443` on each worker.
3. **Cluster mTLS** — generate certs with `deploy/tls/generate_certs.sh <machine-names>` and copy `ca.crt` + the node's `node.crt` / `node.key` into `/etc/opsagent/tls/`. For single-node setups, comment out the `OPSAGENT_CLUSTER_*` lines instead.
4. **ACME / TLS** — set `OPSAGENT_ACME_HOSTS` and `OPSAGENT_ACME_EMAIL` to your public hostname and contact email.

Then start the service:

```bash
sudo systemctl start opsagent
sudo journalctl -u opsagent -f
```

# Development

Local dev uses a Nix flake as the source of truth for Go, Node, and pnpm versions. See `CLAUDE.md` for the full set of dev commands.

