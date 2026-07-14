# installer

`app/installer` provisions, upgrades, and removes an opendeploy deployment on
a host. It is built into the main `opendeploy` binary and invoked as the
`opendeploy install` / `opendeploy uninstall` subcommands.

This is the **only** supported install logic — it replaced the former
`deploy/ubuntu_server_{install,uninstall}.sh` shell scripts, which have been
removed. The repo-level `scripts/install_primary.sh`,
`scripts/install_secondary.sh`, and `scripts/uninstall.sh` files are thin
bootstrap wrappers that download the release binary and then invoke these
subcommands (see the repo `README.md` for one-liners).

## Bootstrap boundary

The installer owns the fresh-primary state transition. It calls
`app/primarybootstrap` directly to create `primary.db`, initialize the secrets
machine key, persist initial settings and the ULA prefix, import optional Web
TLS material, and create cluster TLS identity before systemd starts. Primary
runtime startup only opens and validates this state.

`ainit.init()` skips server directory and logging initialization for installer
subcommands. Installer paths remain explicit in `config.go`; bootstrap inputs
are passed as typed values rather than process environment variables.

## Use

```sh
# Fresh install (needs root) or in-place upgrade — auto-detected by whether
# /etc/systemd/system/opendeploy.service already exists.
sudo opendeploy install primary                 # latest release
sudo opendeploy install primary --version v0.1.0
sudo opendeploy install primary --web-listen '[2001:db8::12]:443' --cluster-listen '[2001:db8::12]:9443' --enrollment-listen '[2001:db8::12]:9444'
sudo opendeploy install secondary --cluster-addr primary.example.com:9443 --enrollment-addr primary.example.com:9444 --enrollment-fingerprint sha256:<hex>

# Upgrade can run as the opendeploy user (passwordless systemctl-restart sudoers).
sudo -u opendeploy opendeploy install primary --version v0.1.0

# Preview every action without touching the host.
sudo opendeploy install primary --dry-run

# Remove (keeps user data) / wipe everything.
sudo opendeploy uninstall
sudo opendeploy uninstall --purge
```

## Design notes (vs. the former shell scripts)

- **One typed source of truth** (`config.go`) for every path, pinned version,
  and checksum — no drift between installer and runtime layout.
- **Embedded assets** (`assets/`) instead of `curl`-ing unit files from
  raw.githubusercontent at install time.
- **`--dry-run`** prints every command, file write, chown, and symlink before
  committing — recovering the "read it before you run it as root"
  auditability that a compiled installer would otherwise lose versus a script.
- **Two-phase install**: phase 1 downloads + checksum-verifies *every* binary
  (agent + containerd + runc) into a temp dir; phase 2 only does local
  filesystem + systemctl work. A network or checksum failure aborts before the
  host is touched, instead of leaving a half-provisioned machine. (The shell
  installer downloads the runtime binaries late, after mutating the system.)
- Same semantics otherwise: versioned-dir + symlink for both the agent binary
  and the containerd/runc runtime, change-detected containerd restart, visudo
  validation, generated primary setup password, service start on fresh install,
  printed service log directory/current file, env-file preservation, and the
  preserve-vs-`--purge` split.
- **Direct primary bootstrap**: foundational database, secrets, network, and
  certificate state is complete before `opendeploy.service` starts. Reinstalls
  validate and preserve an existing database; startup never recreates one.

## Layout

| File | Responsibility |
|---|---|
| `installer.go` | `IsSubcommand` + `Run`: subcommand dispatch and flags |
| `config.go` | paths, pinned versions, checksums, runtime dep descriptors |
| `assets.go` / `assets/` | embedded units, env template, sudoers, config.toml renderer |
| `sys.go` | exec, download + checksum, file ops, systemd, users (all dry-run aware) |
| `install.go` | two-phase orchestration: `stageAll` (fetch+verify) then `runFreshInstall`/`runUpgrade` (apply) |
| `containerd.go` | `stageDep` (phase 1) + `applyRuntime` (phase 2): versioned-dir + symlink + change-detect restart |
| `uninstall.go` | uninstall + `--purge` |
