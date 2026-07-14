package installer

// This file is the single source of truth for every path, version, and checksum
// the installer touches. In the shell installer these were scattered across
// top-of-file constants and inline strings; here they live in one typed place
// that the daemon could eventually import directly (keeping installer and
// runtime layout from drifting).

const (
	repo = "jptrs93/opsagent"

	// System user the daemon runs as. Created on fresh install.
	osUser  = "opendeploy"
	osGroup = "opendeploy"

	// Core paths. DataDir stays 0750 (db, TLS keys, logs private); siblings that
	// must be reachable by a different runAs user are 0755 and live outside it.
	dataDir                  = "/var/lib/opendeploy"
	binPath                  = "/var/lib/opendeploy/bin/opendeploy"
	releasesDir              = "/var/lib/opendeploy-releases"
	assetCacheDir            = "/var/lib/opendeploy-assets"
	volumesDir               = "/var/lib/opendeploy-volumes"
	buildLogsDir             = "/var/lib/opendeploy-build-logs"
	runLogsDir               = "/var/lib/opendeploy-run-logs"
	configDir                = "/etc/opendeploy"
	envFile                  = "/etc/opendeploy/env"
	tlsDir                   = dataDir + "/tls"

	serviceName     = "opendeploy.service"
	serviceUnitPath = "/etc/systemd/system/opendeploy.service"
	sudoersFile     = "/etc/sudoers.d/opendeploy"

	// Bundled, pinned container runtime: a dedicated containerd with its own
	// root/state/socket so it never collides with a distro/Docker install.
	runtimeDir         = "/var/lib/opendeploy/runtime"
	runtimeBin         = "/var/lib/opendeploy/runtime/bin"
	runtimeVersions    = "/var/lib/opendeploy/runtime/versions"
	runtimeConfig      = "/var/lib/opendeploy/runtime/config.toml"
	containerdRoot     = "/var/lib/opendeploy-containerd"
	containerdSocket   = "/run/opendeploy/containerd.sock"
	containerdState    = "/run/opendeploy-containerd"
	containerdService  = "opendeploy-containerd.service"
	containerdUnitPath = "/etc/systemd/system/opendeploy-containerd.service"
)

// runtimeDep describes one pinned runtime binary set fetched from upstream and
// verified against per-arch checksums. The shell installer hard-coded these as
// CONTAINERD_VERSION / CONTAINERD_SHA256_amd64 / ... ; here they're data.
type runtimeDep struct {
	name    string // logical name, e.g. "containerd"
	version string
	// url builds the download URL for the given arch (amd64/arm64).
	url func(arch string) string
	// sha256 maps arch -> expected hex digest of the downloaded artifact.
	sha256 map[string]string
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
