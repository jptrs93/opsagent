package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/lib/runtimebin"
)

func stageDep(dep runtimebin.Component, arch, tmp string) (runtimebin.Staged, error) {
	dir := filepath.Join(tmp, "runtime-"+dep.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return runtimebin.Staged{}, err
	}
	return runtimebin.Fetch(context.Background(), dep, arch, dir, info)
}

// applyRuntime (phase 2) installs the staged runtime binaries into their version
// dirs, points the active symlinks in the runtime bin dir at them, renders
// config.toml, installs/enables the unit, and restarts containerd only when the
// active version changed. No downloads. Empty deps (an unprivileged upgrade that
// skipped runtime staging) is a no-op.
func applyRuntime(deps []runtimebin.Staged) error {
	if len(deps) == 0 {
		return nil
	}
	step("Provisioning bundled container runtime")

	changed := false
	for _, sd := range deps {
		if dryRun {
			for _, b := range sd.Component.Binaries {
				target := filepath.Join(sd.Component.VersionDir(), b)
				planned("install -m 755 %s -> %s", sd.Files[b], target)
				link := filepath.Join(runtimebin.BinDir, b)
				if readlink(link) != target {
					changed = true
					planned("symlink %s -> %s", link, target)
				}
			}
			continue
		}
		ch, _, err := runtimebin.Install(sd)
		if err != nil {
			return err
		}
		changed = changed || ch
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
	if err := ensureDir(volumesDir, 0o755, volOwner); err != nil {
		return err
	}

	gid := 0
	if o, err := lookupOwner(); err == nil {
		gid = o.gid
	} else if !dryRun {
		return fmt.Errorf("resolve opendeploy gid for containerd socket: %w", err)
	}
	if _, err := writeFile(runtimeConfig, []byte(renderContainerdConfig(gid)), 0o644, noChown, false); err != nil {
		return err
	}

	if _, err := writeFile(containerdUnitPath, unitContainerd, 0o644, noChown, false); err != nil {
		return err
	}
	if err := daemonReload(); err != nil {
		return err
	}
	if err := systemctl("enable", containerdService); err != nil {
		return err
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
