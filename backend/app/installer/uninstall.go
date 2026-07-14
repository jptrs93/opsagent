package installer

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// doUninstall reverses the install. By default it stops/disables the services
// and removes the binary, units, and sudoers drop-in, but preserves user data
// (data dir, env, TLS, the opendeploy user) so a reinstall resumes. --purge wipes
// everything.
func doUninstall(purge, assumeYes bool) error {
	if !isRoot() && !dryRun {
		return fmt.Errorf("uninstall must be run as root (try: sudo %s uninstall)", os.Args[0])
	}

	if purge && !assumeYes && !dryRun {
		if !confirmPurge() {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// stop + disable both services
	for _, unit := range []string{serviceName, containerdService} {
		if !unitInstalled(unit) {
			continue
		}
		if unitActive(unit) {
			info("stopping %s", unit)
			if err := systemctl("stop", unit); err != nil {
				return err
			}
		}
		if unitEnabled(unit) {
			info("disabling %s", unit)
			if err := systemctl("disable", unit); err != nil {
				return err
			}
		}
	}

	// remove unit files
	for _, path := range []string{serviceUnitPath, containerdUnitPath} {
		if pathExists(path) {
			if err := removeFile(path); err != nil {
				return err
			}
			info("removed %s", path)
		}
	}
	if err := daemonReload(); err != nil {
		return err
	}

	// sudoers + binary
	if pathExists(sudoersFile) {
		if err := removeFile(sudoersFile); err != nil {
			return err
		}
		info("removed %s", sudoersFile)
	}
	if pathExists(binPath) {
		if err := removeFile(binPath); err != nil {
			return err
		}
		info("removed %s", binPath)
	}

	if !purge {
		fmt.Printf(`
Uninstall complete (user data preserved).
Preserved:
  - %s
  - %s
  - %s
  - %s
  - %s
  - %s
  - opendeploy system user
Re-run with --purge to delete these.
`, dataDir, assetCacheDir, buildLogsDir, runLogsDir, envFile, tlsDir)
		return nil
	}

	// purge: wipe state dirs + user
	step("Purging all state")
	for _, dir := range []string{dataDir, assetCacheDir, releasesDir, volumesDir, buildLogsDir, runLogsDir, containerdRoot, configDir} {
		if pathExists(dir) {
			if err := removeAll(dir); err != nil {
				return err
			}
			info("removed %s", dir)
		}
	}
	if userExists(osUser) {
		_ = run("userdel", osUser)
		info("removed opendeploy system user")
	}

	fmt.Println("\nPurge complete. All opendeploy state has been deleted.")
	return nil
}

func confirmPurge() bool {
	fmt.Printf(`WARNING: --purge will permanently delete:
  - %s (all deployment state and logs)
  - %s (materialized deployment asset cache)
  - %s (downloaded deployment release artifacts)
  - %s (container data volumes)
  - %s (prepare/build logs)
  - %s (run logs)
  - %s (container images and snapshots)
  - %s (env file, TLS certs and keys)
  - the opendeploy system user

Type 'yes' to continue: `, dataDir, assetCacheDir, releasesDir, volumesDir, buildLogsDir, runLogsDir, containerdRoot, configDir)

	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(reply) == "yes"
}
