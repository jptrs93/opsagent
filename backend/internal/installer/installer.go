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
	"strconv"
	"strings"
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
		httpOnlyRaw := fs.String("http-only", "", "set OPENDEPLOY_INITIAL_WEB_HTTP_ONLY (true or false)")
		webListenRaw := fs.String("web-listen", "", "set OPENDEPLOY_INITIAL_WEB_LISTEN (for example :8080)")
		acmeHostsRaw := fs.String("acme-hosts", "", "set OPENDEPLOY_INITIAL_ACME_HOSTS (comma-separated hostnames)")
		fs.BoolVar(&dryRun, "dry-run", false, "print the actions that would be taken without performing them")
		_ = fs.Parse(argv[2:])

		opts := installOptions{}
		var parseErr error
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "http-only":
				v, err := strconv.ParseBool(*httpOnlyRaw)
				if err != nil && parseErr == nil {
					parseErr = fmt.Errorf("invalid --http-only value %q: use true or false", *httpOnlyRaw)
					return
				}
				opts.httpOnly = &v
			case "web-listen":
				v := strings.TrimSpace(*webListenRaw)
				if err := validateEnvValue("--web-listen", v); err != nil && parseErr == nil {
					parseErr = err
					return
				}
				opts.webListen = &v
			case "acme-hosts":
				v := strings.TrimSpace(*acmeHostsRaw)
				if err := validateEnvValue("--acme-hosts", v); err != nil && parseErr == nil {
					parseErr = err
					return
				}
				opts.acmeHosts = &v
			}
		})
		if parseErr != nil {
			return parseErr
		}
		return doInstall(*version, opts)

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
  %[1]s install [--version vX.Y.Z] [--http-only true] [--web-listen :8080] [--acme-hosts host1,host2] [--dry-run]
  %[1]s uninstall [--purge] [--yes] [--dry-run]

Commands:
  install     Fresh install (needs root) or in-place upgrade (auto-detected).
  uninstall   Remove the service and binary; --purge also wipes all state.

Run install with --dry-run to print every action before committing.
`, prog)
}
