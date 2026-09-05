# Setup notes

# Install

Installs from GitHub releases on Ubuntu (amd64 or arm64). The `opendeploy` binary
is its own installer; the shell wrappers only detect the host architecture,
download and checksum the release binary, then invoke `opendeploy install` /
`opendeploy uninstall`. With no `--version`, the binary installs itself using
its embedded version; an explicit `--version` requests a release download.
Idempotent — re-run to upgrade.

```bash
# Install or upgrade a primary.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s --

# Install or upgrade a secondary.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_secondary.sh | bash -s -- \
  --cluster-addr primary.example.com:9443 \
  --enrollment-addr primary.example.com:9444 \
  --enrollment-fingerprint sha256:<hex>
```

Options are passed through to the underlying installer. To pin a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- --version v0.0.1
```

Example primary install bound to a public IPv6 address with ACME TLS:

```bash
VERSION=v0.0.139
IPV6=2001:db8:203:a17e::12
DOMAIN=opendeploy.example.com

curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --version "$VERSION" \
  --web-listen "[$IPV6]:443" \
  --cluster-listen "[$IPV6]:9443" \
  --enrollment-listen "[$IPV6]:9444" \
  --acme-hosts "$DOMAIN"
```

Use the IPv6 address without the `/128` prefix length in listen addresses, and point the domain's `AAAA` record at that address.

## Local or evaluation install

Single-machine installs for trying OpenDeploy out. The installer prints the **master password** once.
`--password-login true` lets that password sign in directly with any username. Without it, the master
password only registers a passkey under *First time setup*. Passkeys need `http://localhost` or HTTPS
with a trusted certificate, so pick the flow that matches how you will reach the machine.

**1. Plain HTTP, master-password login.** Open `http://localhost:8080` (tunnel with
`ssh -L 8080:localhost:8080 <vm>` if remote) and sign in with a name and the master password.

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --http-only true \
  --web-listen 127.0.0.1:8080 \
  --web-hosts localhost \
  --password-login true
```

Use `--web-listen :8080` instead to reach it as `http://<machine-address>:8080` over a trusted LAN;
credentials then cross the network in clear text.

**1b. Plain HTTP, passkeys.** Open `http://localhost:8080`, click *First time setup*, register a
passkey. Only works when the address bar says `localhost`.

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --http-only true \
  --web-listen 127.0.0.1:8080 \
  --web-hosts localhost
```

**2. HTTPS with a local CA, master-password login.** The installer generates a local CA and a
certificate for the listed hosts. Open `https://localhost:8443`, continue through the browser
warning, sign in with a name and the master password. Trusting the CA (below) is optional here.

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --web-tls-self-managed true \
  --web-listen 127.0.0.1:8443 \
  --web-hosts localhost \
  --password-login true
```

**3. HTTPS with a local CA, passkeys.** Same certificate setup; passkeys need the CA trusted first.
Trust it using the steps the installer prints (also behind *Trust the CA* on the login page), restart the browser, open `https://mybox.local:8443`, use *First time setup*.
The host name must resolve to the machine, for example via `/etc/hosts`.

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --web-tls-self-managed true \
  --web-listen :8443 \
  --web-hosts mybox.local
```

CA certificate: `/var/lib/opendeploy/web-ca.crt` on the server, or `https://<host>:8443/v1/tls/ca.crt`.

| Browser machine | Trust command |
|---|---|
| macOS | `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt` |
| Linux system store | `sudo cp opendeploy-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates` |
| Linux Chrome/Chromium | additionally `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" -i opendeploy-ca.crt` |
| Windows | `certutil -addstore -f ROOT opendeploy-ca.crt` |
| Firefox | `security.enterprise_roots.enabled` = true in `about:config`, or import under Settings › Certificates |

Master-password login and hostnames can be changed later under **Settings**.

Preview every action without touching the host with:

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- --dry-run
```

The installer:

- creates the `opendeploy` system user and ownership-sensitive runtime roots, including `/var/lib/opendeploy/`, `/var/lib/opendeploy-assets/`, releases, volumes, and log directories; agent startup creates the fixed children below the data directory before primary or secondary services run
- downloads and checksum-verifies the release binary into `/var/lib/opendeploy-releases/jptrs93/opsagent/<version>/opendeploy-linux-<arch>` and symlinks `/var/lib/opendeploy/bin/opendeploy` to it
- provisions a bundled, pinned containerd + runc runtime and the `opendeploy-containerd.service` unit
- creates `primary.db`, the secrets machine key, the cluster ULA prefix, and primary cluster mTLS material before the first service start
- stores the generated temporary setup password hash in `primary.db` and prints the password once; bootstrap values are not written to `/etc/opendeploy/env`
- installs sudoers + the `opendeploy.service` systemd unit
- on upgrade: atomically swaps the binary and restarts the service
- on first install: starts `opendeploy.service` and prints the Web UI address plus the current service log directory/file

To remove:

```bash
# Keeps data.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/uninstall.sh | bash -s --

# Wipes everything.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/uninstall.sh | bash -s -- --purge
```

Uninstall always stops the services, kills and deletes every container (and its shim) in the bundled containerd, removes the virtual network state (container netns entries, veths, the WireGuard link, routes, and nftables tables), unmounts and removes runtime state under `/run`, and removes the units, sudoers drop-in, and binary. Without `--purge` all data directories (`/var/lib/opendeploy` and every `/var/lib/opendeploy-*` sibling: assets, releases, volumes, build logs, run logs, log archive, metrics, containerd images) and `/etc/opendeploy` stay in place so a reinstall resumes. `--purge` deletes them and the `opendeploy` user after listing them for confirmation (`--yes` skips the prompt). Add `--dry-run` to print every action first.

## First-run configuration

Fresh primary configuration is persisted by the installer. Later changes use the Settings UI/API; `/etc/opendeploy/env` is reserved for process-level settings such as passkey origins and worker connection addresses.

1. **Initial setup password** — fresh primary installs print a high-entropy password like `opendeploy-...`. This is the master password. Use it under *First time setup* to create the first operator account and register a passkey or, with `--password-login true`, sign in with it directly. It remains available for recovery and additional user enrollment until rotated in Settings.
2. **Primary vs. worker node** — install primaries with `opendeploy install primary`; install workers from the Cluster page command, or with `opendeploy install secondary --cluster-addr host:9443 --enrollment-addr host:9444 --enrollment-fingerprint sha256:<hex>`.
3. **Cluster mTLS** — primary cluster (`:9443`) and HTTPS enrollment (`:9444`) listeners start by default. Set `--cluster-listen` and `--enrollment-listen` during primary install to bind them to specific addresses. Primary CA/server key material is generated by the installer and stored encrypted in the primary secrets store. Workers use `OPENDEPLOY_PRIMARY_CLUSTER_ADDR` for mTLS traffic, `OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR` for bootstrap enrollment, and `OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT` to pin the enrollment TLS certificate SPKI before sending a CSR. During enrollment the worker generates its private key locally, sends a CSR, and caches the received `ca.crt` / `node.crt` plus its local `node.key` under `/var/lib/opendeploy/tls/`.
4. **Web UI listeners / TLS** — HTTPS is enabled by default on `:443`. Use `--http-only`, `--web-listen`, `--web-hosts` (alias `--acme-hosts`), and the self-managed TLS flags during installation; use Settings afterward. If initial self-managed TLS has no supplied PEM bundle, the installer generates a local CA and a certificate under it, exports the CA to `/var/lib/opendeploy/web-ca.crt`, and prints trust instructions.
5. **Login methods** — passkeys are always available. `--password-login true` (or *Settings → Authentication*) additionally lets the master password sign in directly; see the local install section above for when that is needed.

For diagnostics, inspect the printed OpenDeploy service log file first. It is under:

```bash
/var/lib/opendeploy-run-logs/0/YYYYMMDD_HHMM_0_1.logbin
```

Use `sudo journalctl -u opendeploy -f` as a fallback when the service does not start far enough to create runtime logs.

# Networking

- Virtual-mode deployments use a stable IPv6 instance address `S`, derived from the cluster ULA prefix, space ID, deployment ID, and instance ordinal. It remains unchanged across restarts and upgrades.
- A rollover candidate also gets a temporary run-scoped IPv6 address `R`, derived from the same prefix, space ID, and deployment ID plus its run number.
- `S` and `R` share the deployment's first 108 address bits. `S` uses address kind `0` with the instance ordinal as its final 16-bit field; `R` uses kind `2` with the run number as that field.
- During warmup, the old container receives traffic for `S`. The candidate has preferred address `R` plus `S` configured as deprecated, so its outbound warmup traffic normally uses `R`.
- Promotion replaces the host route for `S` so it points to the candidate, updates host-port forwarding, publishes the candidate as `RUNNING`, and then stops the old container.
- Promotion does not remove either candidate address. The promoted container retains preferred address `R` and deprecated address `S`; inbound traffic uses `S`, while outbound traffic can continue to prefer `R`.
- Virtual-mode IPv4 addresses are machine-local and used only for egress and IPv4 host-port forwarding. They are not stable workload identities.
- Host-network deployments do not use `S`, `R`, a network namespace, or route-flip promotion. Their rollover candidates share the host network and must cooperate around conflicting ports.

See [`docs/engineering/networking.md`](docs/engineering/networking.md) for the address layout, runtime wiring, DNS, ingress, and rollover details.

# Development

Local dev uses a Nix flake as the source of truth for Go, Node, and pnpm versions. See `CLAUDE.md` for the full set of dev commands.

## Quint setup (macOS)

Quint is a typed TLA+-family spec language, of interest alongside the FizzBee specs in `fizzbee/`. To set it up on a Mac:

- Install the CLI (needs Node): `npm install -g @informalsystems/quint`
- Verify with `quint --version`; try the REPL with `quint`
- Install a JVM for the model checker backend: `brew install openjdk@21` — `quint verify` downloads and manages Apalache itself but requires Java 17+ (point `JAVA_HOME` at it if it is not the default JDK)
- Editor support: in VS Code install the `informal.quint-vscode` extension; in Cursor the extension is not on Open VSX, so download the `.vsix` from the VS Code Marketplace page and use `Extensions: Install from VSIX...`; for JetBrains IDEs install the community `Quint` plugin from the Marketplace (runs `quint typecheck` for diagnostics, so the CLI must be installed)
- Quick check: `quint run <spec>.qnt` simulates, `quint test` runs spec unit tests, `quint verify` model checks via Apalache
