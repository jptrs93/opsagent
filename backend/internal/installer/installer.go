// Package installer provisions, upgrades, and removes an opendeploy deployment on
// a host. It is invoked as the `opendeploy install` / `opendeploy uninstall`
// subcommands of the main binary.
//
// It is deliberately self-contained: it does NOT import ainit, the server
// config, and keeps its own copy of every path, pinned version, and checksum
// (see config.go). Restore unlock reuses the storage and secrets packages so it
// can rewrite the local machine key before first boot. The runtime coupling is a
// guard in ainit.init() that skips server bootstrap when an installer subcommand
// is detected.
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
		version, opts, err := parseInstall(argv[2:])
		if err != nil {
			return err
		}
		return doInstall(version, opts)

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

func parseInstall(args []string) (string, installOptions, error) {
	if len(args) == 0 {
		return "", installOptions{}, fmt.Errorf("install requires a role: primary or secondary")
	}
	switch args[0] {
	case "primary":
		return parseInstallPrimary(args[1:])
	case "secondary":
		return parseInstallSecondary(args[1:])
	default:
		return "", installOptions{}, fmt.Errorf("unknown install role %q: use primary or secondary", args[0])
	}
}

func parseInstallPrimary(args []string) (string, installOptions, error) {
	fs := flag.NewFlagSet("install primary", flag.ExitOnError)
	version := fs.String("version", "latest", "release tag to install (default: latest)")
	useSelf := fs.Bool("use-self", false, "install this executable as v0.0.0 instead of downloading opendeploy")
	httpOnlyRaw := fs.String("http-only", "", "enable HTTP web UI and disable HTTPS web UI (true or false)")
	webListenRaw := fs.String("web-listen", "", "set initial web UI listen address (for example :8080)")
	clusterListenRaw := fs.String("cluster-listen", "", "set OPENDEPLOY_INITIAL_CLUSTER_LISTEN (for example :9443)")
	enrollmentListenRaw := fs.String("enrollment-listen", "", "set OPENDEPLOY_INITIAL_ENROLLMENT_LISTEN (for example :9444)")
	acmeHostsRaw := fs.String("acme-hosts", "", "set OPENDEPLOY_INITIAL_ACME_HOSTS (comma-separated hostnames)")
	primaryNameRaw := fs.String("primary-name", "", "set OPENDEPLOY_PRIMARY_NAME for the primary certificate/machine name")
	restoreBackupRaw := fs.String("restore-backup", "", "restore primary.db from S3 before first boot (true or false)")
	restoreS3AccessKeyIDRaw := fs.String("restore-s3-access-key-id", "", "S3 access key id for backup restore")
	restoreS3SecretAccessKeyRaw := fs.String("restore-s3-secret-access-key", "", "S3 secret access key for backup restore")
	restoreS3BucketRaw := fs.String("restore-s3-bucket", "", "S3 bucket for backup restore")
	restoreS3PathRaw := fs.String("restore-s3-path", "", "S3 path/prefix for backup restore")
	restoreS3RegionRaw := fs.String("restore-s3-region", "", "S3 region for backup restore")
	restoreS3EndpointRaw := fs.String("restore-s3-endpoint", "", "optional S3 endpoint for backup restore")
	recoveryCodeRaw := fs.String("recovery-code", "", "secrets recovery code used to unlock the restored database")
	fs.BoolVar(&dryRun, "dry-run", false, "print the actions that would be taken without performing them")
	_ = fs.Parse(args)

	opts := installOptions{role: "primary", useSelf: *useSelf}
	var parseErr error
	restoreBackupSet := false
	restoreBackup := false
	restoreValuesSet := map[string]bool{}
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
		case "cluster-listen":
			v := strings.TrimSpace(*clusterListenRaw)
			if err := validateInstallStringFlag("--cluster-listen", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.clusterListen = &v
		case "enrollment-listen":
			v := strings.TrimSpace(*enrollmentListenRaw)
			if err := validateInstallStringFlag("--enrollment-listen", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.enrollmentListen = &v
		case "acme-hosts":
			v := strings.TrimSpace(*acmeHostsRaw)
			if err := validateEnvValue("--acme-hosts", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.acmeHosts = &v
		case "primary-name":
			v := strings.TrimSpace(*primaryNameRaw)
			if err := validateInstallStringFlag("--primary-name", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.primaryName = &v
		case "restore-backup":
			restoreBackupSet = true
			v, err := strconv.ParseBool(*restoreBackupRaw)
			if err != nil && parseErr == nil {
				parseErr = fmt.Errorf("invalid --restore-backup value %q: use true or false", *restoreBackupRaw)
				return
			}
			restoreBackup = v
		case "restore-s3-access-key-id", "restore-s3-secret-access-key", "restore-s3-bucket", "restore-s3-path", "restore-s3-region", "restore-s3-endpoint", "recovery-code":
			restoreValuesSet[f.Name] = true
		}
	})
	if parseErr != nil {
		return "", installOptions{}, parseErr
	}
	if restoreBackup || len(restoreValuesSet) > 0 {
		if !restoreBackupSet || !restoreBackup {
			return "", installOptions{}, fmt.Errorf("restore options require --restore-backup true")
		}
		restore := &restoreOptions{
			AccessKeyID:     strings.TrimSpace(*restoreS3AccessKeyIDRaw),
			SecretAccessKey: strings.TrimSpace(*restoreS3SecretAccessKeyRaw),
			Bucket:          strings.TrimSpace(*restoreS3BucketRaw),
			Path:            strings.TrimSpace(*restoreS3PathRaw),
			Region:          strings.TrimSpace(*restoreS3RegionRaw),
			Endpoint:        strings.TrimSpace(*restoreS3EndpointRaw),
			RecoveryCode:    strings.TrimSpace(*recoveryCodeRaw),
		}
		if err := restore.validate(); err != nil {
			return "", installOptions{}, err
		}
		opts.restore = restore
	}
	return *version, opts, nil
}

func parseInstallSecondary(args []string) (string, installOptions, error) {
	fs := flag.NewFlagSet("install secondary", flag.ExitOnError)
	version := fs.String("version", "latest", "release tag to install (default: latest)")
	useSelf := fs.Bool("use-self", false, "install this executable as v0.0.0 instead of downloading opendeploy")
	clusterAddrRaw := fs.String("cluster-addr", "", "set OPENDEPLOY_PRIMARY_CLUSTER_ADDR for the primary mTLS cluster address")
	enrollmentAddrRaw := fs.String("enrollment-addr", "", "set OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR for the primary enrollment address")
	primaryNameRaw := fs.String("primary-name", "", "set OPENDEPLOY_PRIMARY_NAME for primary TLS verification (default runtime value: primary)")
	fs.BoolVar(&dryRun, "dry-run", false, "print the actions that would be taken without performing them")
	_ = fs.Parse(args)

	opts := installOptions{role: "secondary", useSelf: *useSelf}
	var parseErr error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "cluster-addr":
			v := strings.TrimSpace(*clusterAddrRaw)
			if err := validateInstallStringFlag("--cluster-addr", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.clusterAddr = &v
		case "enrollment-addr":
			v := strings.TrimSpace(*enrollmentAddrRaw)
			if err := validateInstallStringFlag("--enrollment-addr", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.enrollmentAddr = &v
		case "primary-name":
			v := strings.TrimSpace(*primaryNameRaw)
			if err := validateInstallStringFlag("--primary-name", v); err != nil && parseErr == nil {
				parseErr = err
				return
			}
			opts.primaryName = &v
		}
	})
	if parseErr != nil {
		return "", installOptions{}, parseErr
	}
	if opts.clusterAddr == nil {
		return "", installOptions{}, fmt.Errorf("install secondary requires --cluster-addr")
	}
	if opts.enrollmentAddr == nil {
		return "", installOptions{}, fmt.Errorf("install secondary requires --enrollment-addr")
	}
	return *version, opts, nil
}

func validateInstallStringFlag(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return validateEnvValue(name, value)
}

func usage(prog string) {
	fmt.Fprintf(os.Stderr, `%[1]s install / uninstall — provision, upgrade, and remove opendeploy

Usage:
  %[1]s install primary [--version vX.Y.Z] [--use-self] [--http-only true] [--web-listen :8080] [--cluster-listen :9443] [--enrollment-listen :9444] [--acme-hosts host1,host2] [--primary-name primary] [--restore-backup true --restore-s3-access-key-id ... --restore-s3-secret-access-key ... --restore-s3-bucket ... --restore-s3-path ... --restore-s3-region ... --recovery-code ...] [--dry-run]
  %[1]s install secondary --cluster-addr host:9443 --enrollment-addr host:9444 [--version vX.Y.Z] [--use-self] [--primary-name primary] [--dry-run]
  %[1]s uninstall [--purge] [--yes] [--dry-run]

Commands:
  install     Fresh install (needs root) or in-place upgrade (auto-detected).
  uninstall   Remove the service and binary; --purge also wipes all state.

Run install with --dry-run to print every action before committing.
`, prog)
}
