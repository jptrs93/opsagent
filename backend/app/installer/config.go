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
	version: "2.0.5",
	url: func(arch string) string {
		v := "2.0.5"
		return "https://github.com/containerd/containerd/releases/download/v" + v +
			"/containerd-" + v + "-linux-" + arch + ".tar.gz"
	},
	sha256: map[string]string{
		"amd64": "88ab31f3e78e4d2fa12dcb933032122d11d441c83b79a89c6c8076f871e50df8",
		"arm64": "36eaf77dc65df4b60d6e06204631a4105b4e942dd2704d618758a2aa0eecc264",
	},
	// The tarball lays these out under bin/.
	binaries:  []string{"containerd", "containerd-shim-runc-v2", "ctr"},
	isTarball: true,
}

var runcDep = runtimeDep{
	name:    "runc",
	version: "1.2.6",
	url: func(arch string) string {
		return "https://github.com/opencontainers/runc/releases/download/v1.2.6/runc." + arch
	},
	sha256: map[string]string{
		"amd64": "0774f49d1b1eebb5849e644db5e4dc6f2b06cee05f13b3d17d5d6ba62d6f2ebc",
		"arm64": "12c612e2ebe6ca198de676ce75ed557e79fe6109032209bb8e25166c967fe170",
	},
	binaries:  []string{"runc"},
	isTarball: false,
}

var runtimeDeps = []runtimeDep{containerdDep, runcDep}
