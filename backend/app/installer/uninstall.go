package installer

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"strings"
)

// doUninstall reverses the install. It always stops both services and the
// workloads they leave behind (containers and their shims, build/backup/log
// helper processes), tears down the virtual network state (container netns
// entries, veths, the WireGuard link, routes, nftables tables), unmounts and
// removes runtime state under /run, and removes the binary, units, and sudoers
// drop-in. Without --purge it preserves user data (the data dir and its
// siblings, the env file, the opendeploy user) so a reinstall resumes; --purge
// wipes everything.
func doUninstall(purge, assumeYes bool) error {
	if !isRoot() && !dryRun {
		return fmt.Errorf("uninstall must be run as root (try: sudo %s uninstall)", os.Args[0])
	}
	targets := purgeTargets()
	if purge && !assumeYes && !dryRun {
		if !confirmPurge(targets) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	step("Stopping %s", serviceName)
	if err := stopUnit(serviceName); err != nil {
		return err
	}
	if err := stopUnitProcesses(serviceName); err != nil {
		return err
	}

	// Containers first, through containerd, so it unmounts the rootfs snapshots
	// and drops the task cgroups itself; then the daemon, then whatever is left.
	step("Stopping containers and %s", containerdService)
	if err := stopContainers(); err != nil {
		warn("%v; falling back to a cgroup and process sweep", err)
	}
	if err := stopUnit(containerdService); err != nil {
		return err
	}
	if err := killContainerProcesses(); err != nil {
		return err
	}
	if err := stopUnitProcesses(containerdService); err != nil {
		return err
	}
	if _, err := killStrayProcesses(strayProcessRoots()); err != nil {
		return err
	}

	step("Removing virtual network state")
	teardownNetworking()

	step("Removing runtime state")
	for _, root := range []string{containerdState, containerdRoot} {
		if err := unmountUnder(root); err != nil {
			return err
		}
	}
	for _, dir := range []string{containerdState, runtimeStateDir} {
		if err := removePath(dir, true); err != nil {
			return err
		}
	}

	step("Removing units, sudoers drop-in, and binary")
	for _, path := range []string{serviceUnitPath, containerdUnitPath} {
		if err := removePath(path, false); err != nil {
			return err
		}
	}
	if err := daemonReload(); err != nil {
		return err
	}
	for _, path := range []string{sudoersFile, sudoersFile + ".new", binPath} {
		if err := removePath(path, false); err != nil {
			return err
		}
	}

	if !purge {
		fmt.Print("\nUninstall complete (user data preserved).\nPreserved:\n")
		for _, dir := range targets {
			fmt.Printf("  - %s\n", dir)
		}
		fmt.Print("  - opendeploy system user\nRe-run with --purge to delete these.\n")
		printSysctlNote()
		return nil
	}

	step("Purging all state")
	for _, dir := range targets {
		if err := removePath(dir, true); err != nil {
			return err
		}
	}
	if userExists(osUser) {
		if err := run("userdel", osUser); err != nil {
			warn("removing the %s user: %v", osUser, err)
		} else {
			info("removed %s system user", osUser)
		}
	}
	if groupExists(osGroup) {
		// userdel only drops a user-private group; remove it explicitly otherwise.
		if err := run("groupdel", osGroup); err == nil {
			info("removed %s group", osGroup)
		}
	}

	fmt.Println("\nPurge complete. All opendeploy state has been deleted.")
	printSysctlNote()
	return nil
}

// stopUnit stops and disables a unit if it is installed. With KillMode=process
// this only ends the main process; stopUnitProcesses handles the rest.
func stopUnit(unit string) error {
	if !unitInstalled(unit) {
		return nil
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
	return nil
}

// removePath deletes path if it exists (recursively when recursive is set)
// and reports it; in dry-run the planned action is printed instead.
func removePath(path string, recursive bool) error {
	if !pathExists(path) {
		return nil
	}
	remove := removeFile
	if recursive {
		remove = removeAll
	}
	if err := remove(path); err != nil {
		return err
	}
	if !dryRun {
		info("removed %s", path)
	}
	return nil
}

func groupExists(name string) bool {
	_, err := user.LookupGroup(name)
	return err == nil
}

func printSysctlNote() {
	fmt.Print("\nNote: the agent enabled net.ipv4.ip_forward and net.ipv6.conf.all.forwarding at runtime; they are left as-is and revert on reboot unless persisted elsewhere.\n")
}

func confirmPurge(targets []string) bool {
	fmt.Print("WARNING: --purge will permanently delete:\n")
	for _, dir := range targets {
		fmt.Printf("  - %s%s\n", dir, purgeTargetHint(dir))
	}
	fmt.Print("  - the opendeploy system user\n\nType 'yes' to continue: ")

	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(reply) == "yes"
}

func purgeTargetHint(dir string) string {
	hints := map[string]string{
		dataDir:        "database, secrets, TLS keys, git cache, large assets",
		assetCacheDir:  "materialized deployment asset cache",
		releasesDir:    "downloaded release binaries",
		volumesDir:     "container data volumes",
		buildLogsDir:   "prepare/build logs",
		runLogsDir:     "run logs",
		logArchiveDir:  "compacted log archive",
		metricsDir:     "container metrics",
		containerdRoot: "container images and snapshots",
		configDir:      "env file",
	}
	if hint, ok := hints[dir]; ok {
		return " (" + hint + ")"
	}
	return ""
}
