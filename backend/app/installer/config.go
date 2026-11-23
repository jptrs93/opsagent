package installer

// This file is the single source of truth for every path, version, and checksum
// the installer touches.

const (
	repo = "jptrs93/opsagent"

	// legacyUser and legacyGroup are the account the service ran as before it
	// ran as root. Nothing creates them; a purge removes them when present.
	legacyUser  = "opendeploy"
	legacyGroup = "opendeploy"

	// DataDir stays 0750 (db, TLS keys, logs private); siblings that a
	// container user must reach are 0755 and live outside it.
	dataDir       = "/var/lib/opendeploy"
	binPath       = "/var/lib/opendeploy/bin/opendeploy"
	releasesDir   = "/var/lib/opendeploy-releases"
	assetCacheDir = "/var/lib/opendeploy-assets"
	volumesDir    = "/var/lib/opendeploy-volumes"
	buildLogsDir  = "/var/lib/opendeploy-build-logs"
	runLogsDir    = "/var/lib/opendeploy-run-logs"
	logArchiveDir = "/var/lib/opendeploy-log-archive"
	metricsDir    = "/var/lib/opendeploy-metrics"
	nixStoresDir  = "/var/lib/opendeploy-nix"
	// siblingDirGlob matches every runtime root ainit derives as dataDir+"-<x>",
	// so a purge also catches siblings this binary does not list explicitly.
	siblingDirGlob = "/var/lib/opendeploy-*"
	configDir      = "/etc/opendeploy"
	envFile        = "/etc/opendeploy/env"
	envFileMode    = 0o600
	tlsDir         = dataDir + "/tls"

	serviceName     = "opendeploy.service"
	serviceUnitPath = "/etc/systemd/system/opendeploy.service"
	// legacySudoersFile was installed while the service ran unprivileged.
	// Nothing writes it; an uninstall removes it when present.
	legacySudoersFile = "/etc/sudoers.d/opendeploy"

	// Bundled, pinned container runtime: a dedicated containerd with its own
	// root/state/socket so it never collides with a distro/Docker install.
	// The binaries and their pinned versions live in lib/runtimebin.
	runtimeConfig    = "/var/lib/opendeploy/runtime/config.toml"
	containerdRoot   = "/var/lib/opendeploy-containerd"
	containerdSocket = "/run/opendeploy/containerd.sock"
	containerdState  = "/run/opendeploy-containerd"
	// runtimeStateDir is the containerd unit's RuntimeDirectory (socket dir).
	runtimeStateDir = "/run/opendeploy"
	// containerdNamespace and containerIDPrefix mirror ainit's CtrdNamespace and
	// the runner's opendeploy-<dep>-<version>-<instance>-<run> container ids;
	// a container's named netns under netnsRunDir carries the same id.
	containerdNamespace = "opendeploy"
	containerIDPrefix   = "opendeploy-"
	netnsRunDir         = "/run/netns"
	// containerCgroup is containerd's default cgroup parent for the namespace
	// (/<namespace>/<container id>), relative to the cgroup mount.
	containerCgroup    = "/" + containerdNamespace
	containerdService  = "opendeploy-containerd.service"
	containerdUnitPath = "/etc/systemd/system/opendeploy-containerd.service"
)
