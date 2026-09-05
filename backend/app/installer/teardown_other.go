//go:build !linux

package installer

// The uninstaller only ever runs on Linux hosts; these stubs keep the package
// building (and --dry-run runnable) elsewhere.

func stopUnitProcesses(unit string) error {
	info("skipping leftover process cleanup for %s (linux only)", unit)
	return nil
}

func stopContainers() error {
	info("skipping container teardown (linux only)")
	return nil
}

func killContainerProcesses() error { return nil }

func killStrayProcesses(roots []string) (int, error) { return 0, nil }

func teardownNetworking() {
	info("skipping virtual network teardown (linux only)")
}

func unmountUnder(root string) error { return nil }
