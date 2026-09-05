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

## Local or evaluation install (plain HTTP, password login)

For a single machine you just want to try OpenDeploy on, such as a throwaway VM or a Linux
dev box, install with plain HTTP bound to loopback and password login enabled:

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --http-only true \
  --web-listen 127.0.0.1:8080 \
  --password-login true
```

If the machine is remote, forward the port over SSH from your laptop:

```bash
ssh -L 8080:localhost:8080 <vm>
```

Then open `http://localhost:8080`, click **First time setup**, enter a username and the
temporary setup password the installer printed, and choose a password. From then on sign in with
that username and password. Because the UI is reached as `localhost`, passkeys also work here if you
prefer them.

Why this shape:

- Browsers only allow passkeys in a secure context: HTTPS with a certificate the browser trusts, or
  plain HTTP on `localhost`. Plain HTTP on any other address refuses WebAuthn before the server is
  ever contacted, and HTTPS behind a certificate the browser does not trust is not a reliably
  supported passkey configuration across browsers. Password login is what makes a remote VM usable
  without certificate setup.
- Password login is off by default and is a cluster setting (**Settings → Authentication**), so a
  production install that never turns it on exposes no password endpoint.
- The loopback bind plus SSH tunnel keeps the setup password, your password, and the session token
  off the network. Nothing is encrypted by the server itself in HTTP mode.

**Reaching it directly over the network instead.** Drop `--web-listen` to bind every interface
(`:8080`) and open `http://<machine-address>:8080`. This is fine on a trusted LAN, but every
credential and all UI traffic then crosses the network in clear text, and passkeys will not work at
that address. The installer prints a warning for this combination.

Change your own password later under **Sessions → Personal sessions**.

### Same thing over HTTPS with a self-signed certificate

If you would rather not send the password in clear text, keep password login and let the installer
generate a self-signed certificate instead of disabling HTTPS:

```bash
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s -- \
  --web-tls-self-managed true \
  --password-login true
```

Open `https://<machine-address>` (port 443), accept the browser's certificate warning once, and
sign in with a username and password exactly as above. The connection is encrypted; the warning
only says the browser cannot vouch for who is on the other end.

Do not count on passkeys in this setup. Chrome refuses WebAuthn outright on a page whose
certificate it does not trust, even after the warning has been accepted, and behaviour after a
bypass varies across other browsers. Password login is what makes the self-signed certificate
usable. To use passkeys reliably here you would have to trust the generated certificate in the
browser or operating system, which is a separate step this section does not cover.

Pass `--web-listen <address>:8443` to move it off port 443, and `--acme-hosts <hostname>` to put
a name of your choosing in the certificate. The installer's summary prints the placeholder
`https://opendeploy.example.com` when no hostname was given; use the machine's real address.

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

## First-run configuration

Fresh primary configuration is persisted by the installer. Later changes use the Settings UI/API; `/etc/opendeploy/env` is reserved for process-level settings such as passkey origins and worker connection addresses.

1. **Initial setup password** — fresh primary installs print a high-entropy password like `opendeploy-...`. Use it under *First time setup* to create the first operator account and register a passkey or, with `--password-login true`, set a password. It remains available for recovery and additional user enrollment until rotated in Settings.
2. **Primary vs. worker node** — install primaries with `opendeploy install primary`; install workers from the Cluster page command, or with `opendeploy install secondary --cluster-addr host:9443 --enrollment-addr host:9444 --enrollment-fingerprint sha256:<hex>`.
3. **Cluster mTLS** — primary cluster (`:9443`) and HTTPS enrollment (`:9444`) listeners start by default. Set `--cluster-listen` and `--enrollment-listen` during primary install to bind them to specific addresses. Primary CA/server key material is generated by the installer and stored encrypted in the primary secrets store. Workers use `OPENDEPLOY_PRIMARY_CLUSTER_ADDR` for mTLS traffic, `OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR` for bootstrap enrollment, and `OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT` to pin the enrollment TLS certificate SPKI before sending a CSR. During enrollment the worker generates its private key locally, sends a CSR, and caches the received `ca.crt` / `node.crt` plus its local `node.key` under `/var/lib/opendeploy/tls/`.
4. **Web UI listeners / TLS** — HTTPS is enabled by default on `:443`. Use `--http-only`, `--web-listen`, `--acme-hosts`, and the self-managed TLS flags during installation; use Settings afterward. If initial self-managed TLS has no supplied PEM bundle, the installer generates and stores a self-signed certificate.
5. **Login methods** — passkeys are always available. `--password-login true` (or *Settings → Authentication*) additionally allows username/password login; see the local install section above for when that is needed.

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
