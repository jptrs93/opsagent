// Package installer provisions, upgrades, and removes an opendeploy deployment on
// a host. It is invoked as the `opendeploy install` / `opendeploy uninstall`
// subcommands of the main binary.
//
// It is deliberately self-contained: it does NOT import ainit, the server
// config, or any other backend package, and keeps its own copy of every path,
// pinned version, and checksum (see config.go). The only coupling to the rest
// of the binary is a guard in ainit.init() that skips server bootstrap when an
// installer subcommand is detected — the installer itself stays ignorant of it.
//
// The embedded systemd units, env template, and sudoers drop-in (assets/) make
// the binary self-installing — nothing is fetched from GitHub except the
// release binaries themselves.
package installer

import (
	"flag"
	"fmt"
	"os"
)

// IsSubcommand reports whether argv selects an installer subcommand. main()
// uses it to dispatch, and ainit uses the same set of names to skip server
// bootstrap. argv is the full os.Args.
func IsSubcommand(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	switch argv[1] {
	case "install", "uninstall":
		return true
	default:
		return false
	}
}

// Run executes the installer subcommand selected by argv (the full os.Args).
// It returns an error to the caller rather than exiting, so main() owns the
// process exit code. Callers should only invoke it when IsSubcommand(argv) is
// true.
func Run(argv []string) error {
	prog := "opendeploy"
	if len(argv) > 0 {
		prog = argv[0]
	}

	switch argv[1] {
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		version := fs.String("version", "latest", "release tag to install (default: latest)")
		fs.BoolVar(&dryRun, "dry-run", false, "print the actions that would be taken without performing them")
		_ = fs.Parse(argv[2:])
		return doInstall(*version)

	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
		purge := fs.Bool("purge", false, "also delete all state (data dir, releases, volumes, containerd root, config) and the opendeploy user")
		yes := fs.Bool("yes", false, "skip the --purge confirmation prompt")
		fs.BoolVar(&dryRun, "dry-run", false, "print the actions that would be taken without performing them")
		_ = fs.Parse(argv[2:])
		return doUninstall(*purge, *yes)

	default:
		usage(prog)
		return fmt.Errorf("unknown installer command: %s", argv[1])
	}
}

func usage(prog string) {
	fmt.Fprintf(os.Stderr, `%[1]s install / uninstall — provision, upgrade, and remove opendeploy

Usage:
  %[1]s install [--version vX.Y.Z] [--dry-run]
  %[1]s uninstall [--purge] [--yes] [--dry-run]

Commands:
  install     Fresh install (needs root) or in-place upgrade (auto-detected).
  uninstall   Remove the service and binary; --purge also wipes all state.

Run install with --dry-run to print every action before committing.
`, prog)
}
