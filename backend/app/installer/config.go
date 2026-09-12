package installer

// This file is the single source of truth for every path, version, and checksum
// the installer touches.

const (
	repo = "jptrs93/opsagent"

	osUser  = "opendeploy"
	osGroup = "opendeploy"

	// DataDir stays 0750 (db, TLS keys, logs private); siblings that must be
	// reachable by a different runAs user are 0755 and live outside it.
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
	tlsDir         = dataDir + "/tls"

	serviceName     = "opendeploy.service"
	serviceUnitPath = "/etc/systemd/system/opendeploy.service"
	sudoersFile     = "/etc/sudoers.d/opendeploy"

	// Bundled, pinned container runtime: a dedicated containerd with its own
	// root/state/socket so it never collides with a distro/Docker install.
	runtimeDir       = "/var/lib/opendeploy/runtime"
	runtimeBin       = "/var/lib/opendeploy/runtime/bin"
	runtimeVersions  = "/var/lib/opendeploy/runtime/versions"
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

// runtimeDep describes one pinned runtime binary set fetched from upstream and
// verified against per-arch checksums.
type runtimeDep struct {
	name    string
	version string
	url     func(arch string) string
	sha256  map[string]string
	// binaries are the executables this dep contributes to runtimeBin. They are
	// resolved relative to extractDir after download (tarball members for
	// containerd, the single downloaded file for runc).
	binaries []string
	// isTarball is true when the artifact is a .tar.gz to be extracted; false
	// when the download is the binary itself (runc ships a bare ELF).
	isTarball bool
}

var containerdDep = runtimeDep{
	name:    "containerd",
	version: "2.3.5",
	url: func(arch string) string {
		v := "2.3.5"
		return "https://github.com/containerd/containerd/releases/download/v" + v +
			"/containerd-" + v + "-linux-" + arch + ".tar.gz"
	},
	sha256: map[string]string{
		"amd64": "2f0a095a71e3262d0d91ff0e50e2e4ae73c3866c4d1ff6a15f341097fba3dd44",
		"arm64": "06f46cbc073872c5ad1fbc922a53543e9a26b798d62ff106d44639dcb9947942",
	},
	// The tarball lays these out under bin/.
	binaries:  []string{"containerd", "containerd-shim-runc-v2", "ctr"},
	isTarball: true,
}

var runcDep = runtimeDep{
	name:    "runc",
	version: "1.5.1",
	url: func(arch string) string {
		return "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc." + arch
	},
	sha256: map[string]string{
		"amd64": "177df879d50c913eb205e898d5c1c05a18f574053c0ce5524c471208eaf06f6f",
		"arm64": "ca70e7dbd6616ca782a59b5d3ac86909123fdaa9fa3f89dcf29051c70eee7ce9",
	},
	binaries:  []string{"runc"},
	isTarball: false,
}

var runtimeDeps = []runtimeDep{containerdDep, runcDep}
