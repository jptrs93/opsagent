# installer

`internal/installer` provisions, upgrades, and removes an opendeploy deployment on
a host. It is built into the main `opendeploy` binary and invoked as the
`opendeploy install` / `opendeploy uninstall` subcommands.

This is the **only** supported install logic — it replaced the former
`deploy/ubuntu_server_{install,uninstall}.sh` shell scripts, which have been
removed. The repo-level `scripts/install_primary.sh`,
`scripts/install_secondary.sh`, and `scripts/uninstall.sh` files are thin
bootstrap wrappers that download the release binary and then invoke these
subcommands (see the repo `README.md` for one-liners).

## Independence

This package is deliberately self-contained:

- It does **not** import `ainit`, the server config, or any other backend
  package. It keeps its own copy of every path, pinned version, and checksum in
  `config.go`.
- The only coupling to the rest of the binary is a guard in `ainit.init()` that
  skips server bootstrap (data-dir creation, file logging) when an installer
  subcommand is detected. Go runs every imported package's `init()` before
  `main()`, so that skip has to live in `ainit`; the installer itself stays
  ignorant of it. `IsSubcommand` / `Run` are the only exported symbols.

## Use

```sh
# Fresh install (needs root) or in-place upgrade — auto-detected by whether
# /etc/systemd/system/opendeploy.service already exists.
sudo opendeploy install primary                 # latest release
sudo opendeploy install primary --version v0.1.0
sudo opendeploy install secondary --cluster-addr primary.example.com:9443 --enrollment-addr primary.example.com:9444

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
