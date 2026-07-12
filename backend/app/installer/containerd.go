package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// stagedDep is a runtimeDep whose binaries have been downloaded + checksum-
// verified into a temp staging dir during phase 1, ready to be installed in
// phase 2. verDir is the final version dir; files maps each binary name to its
// staged temp path.
type stagedDep struct {
	dep    runtimeDep
	verDir string
	files  map[string]string
}

// stageDep (phase 1) downloads dep@version, verifies its checksum, and extracts
// its binaries into a per-dep staging dir under tmp. No host state is touched.
// We always re-download — no idempotent skip — since the installer targets
// first-time installs and simplicity wins over avoiding a re-fetch.
func stageDep(dep runtimeDep, arch, tmp string) (stagedDep, error) {
	sd := stagedDep{
		dep:    dep,
		verDir: filepath.Join(runtimeVersions, dep.name+"-"+dep.version),
		files:  make(map[string]string, len(dep.binaries)),
	}
	want, ok := dep.sha256[arch]
	if !ok {
		return sd, fmt.Errorf("no %s checksum for arch %s", dep.name, arch)
	}

	dl := filepath.Join(tmp, dep.name+"-download")
	if err := download(dep.url(arch), dl); err != nil {
		return sd, err
	}
	if err := verifySHA256(dl, want); err != nil {
		return sd, err
	}

	if dep.isTarball {
		// staging dir lives under tmp (not host state), so create it directly —
		// it must exist even under --dry-run, where we still download + extract.
		stageDir := filepath.Join(tmp, "stage-"+dep.name)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			return sd, err
		}
		if err := extractTarGzMembers(dl, stageDir, dep.binaries); err != nil {
			return sd, err
		}
		for _, b := range dep.binaries {
			sd.files[b] = filepath.Join(stageDir, b)
		}
	} else {
		// Bare ELF: the download itself is the (only) binary.
		sd.files[dep.binaries[0]] = dl
	}
	return sd, nil
}

// applyRuntime (phase 2) installs the staged runtime binaries into their version
// dirs, points the active symlinks in runtimeBin at them, renders config.toml,
// installs/enables the unit, and restarts containerd only when the active
// version changed. No downloads. Empty deps (an unprivileged upgrade that
// skipped runtime staging) is a no-op.
func applyRuntime(deps []stagedDep) error {
	if len(deps) == 0 {
		return nil
	}
	step("Provisioning bundled container runtime")

	for _, d := range []string{runtimeDir, runtimeBin, runtimeVersions} {
		if err := ensureDir(d, 0o755, noChown); err != nil {
			return err
		}
	}

	// Install each staged binary into its version dir, then re-point the active
	// symlinks, tracking whether any active target changed.
	changed := false
	for _, sd := range deps {
		if err := ensureDir(sd.verDir, 0o755, noChown); err != nil {
			return err
		}
		for _, b := range sd.dep.binaries {
			if err := installBinary(sd.files[b], filepath.Join(sd.verDir, b), 0o755, noChown); err != nil {
				return err
			}
		}
		// Symlinks must live in runtimeBin (which the unit puts on PATH):
		// containerd execs the shim by PATH and the shim execs runc by PATH.
		for _, b := range sd.dep.binaries {
			target := filepath.Join(sd.verDir, b)
			link := filepath.Join(runtimeBin, b)
			if readlink(link) != target {
				changed = true
			}
			if err := atomicSymlink(target, link); err != nil {
				return err
			}
		}
	}

	// State dirs: containerd root private (0700); volumes dir opendeploy-owned and
	// world-traversable so the in-container user can reach its bind mount.
	if err := ensureDir(containerdRoot, 0o700, noChown); err != nil {
		return err
	}
	volOwner, err := lookupOwner()
	if err != nil {
		volOwner = noChown // dry-run before user creation
	}
	if volumesDirErr := ensureDir(volumesDir, 0o755, volOwner); volumesDirErr != nil {
		return volumesDirErr
	}

	// config.toml: gid scopes the gRPC socket to the opendeploy group.
	gid := 0
	if o, ownerErr := lookupOwner(); ownerErr == nil {
		gid = o.gid
	} else if !dryRun {
		return fmt.Errorf("resolve opendeploy gid for containerd socket: %w", ownerErr)
	}
	if _, configWriteErr := writeFile(runtimeConfig, []byte(renderContainerdConfig(gid)), 0o644, noChown, false); configWriteErr != nil {
		return configWriteErr
	}

	// Install + enable the dedicated unit (embedded — no fetch).
	if _, unitWriteErr := writeFile(containerdUnitPath, unitContainerd, 0o644, noChown, false); unitWriteErr != nil {
		return unitWriteErr
	}
	if reloadErr := daemonReload(); reloadErr != nil {
		return reloadErr
	}
	if enableErr := systemctl("enable", containerdService); enableErr != nil {
		return enableErr
	}

	// Start if down; if already running and the active binaries changed, restart
	// so the new version takes effect (the running daemon holds the old inode).
	switch {
	case !unitActive(containerdService):
		info("starting %s", containerdService)
		return systemctl("start", containerdService)
	case changed:
		info("restarting %s (runtime binaries updated)", containerdService)
		return systemctl("restart", containerdService)
	default:
		info("%s already current", containerdService)
		return nil
	}
}
