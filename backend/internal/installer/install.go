package installer

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
)

type bootstrapCredentials struct {
	password string
	hash     string
}

// staged holds the verified temp paths produced by phase 1 (download + verify),
// consumed by phase 2 (apply). runtime is empty when the runtime isn't being
// provisioned (an unprivileged upgrade).
type staged struct {
	agentBin string
	runtime  []stagedDep
}

// doInstall is the entry point for `install`. It auto-detects fresh-install vs
// upgrade by whether the systemd unit already exists, then runs two phases:
//
//	Phase 1 — download + checksum every binary into a temp dir. No host changes.
//	Phase 2 — apply: create user/dirs, install the staged binaries, write units,
//	          enable + restart. Local filesystem + systemctl only, no network.
//
// A network or checksum failure in phase 1 aborts with the host untouched,
// rather than leaving a half-provisioned machine.
type installOptions struct {
	role             string
	useSelf          bool
	httpOnly         *bool
	webListen        *string
	clusterListen    *string
	enrollmentListen *string
	acmeHosts        *string
	clusterAddr      *string
	enrollmentAddr   *string
	primaryName      *string
	restore          *restoreOptions
}

func (o installOptions) hasEnvOverrides() bool {
	return o.httpOnly != nil || o.webListen != nil || o.clusterListen != nil || o.enrollmentListen != nil || o.acmeHosts != nil || o.clusterAddr != nil || o.enrollmentAddr != nil || o.primaryName != nil
}

func doInstall(version string, opts installOptions) error {
	upgrade := pathExists(serviceUnitPath)
	if opts.restore != nil && upgrade {
		return fmt.Errorf("backup restore is only supported for fresh primary installs")
	}

	arch, err := hostArch()
	if err != nil {
		return err
	}
	if err := preflight(upgrade); err != nil {
		return err
	}
	if opts.useSelf {
		version = "v0.0.0"
	} else if version == "" || version == "latest" {
		step("Resolving latest release")
		version, err = resolveLatestTag()
		if err != nil {
			return err
		}
		info("latest is %s", version)
	}

	tmp, err := os.MkdirTemp("", "opendeploy-installer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// Phase 1 — stage. Runtime binaries are provisioned root-only, so an
	// unprivileged upgrade skips staging them.
	withRuntime := isRoot() || dryRun
	st, err := stageAll(version, arch, tmp, withRuntime, opts.useSelf)
	if err != nil {
		return err
	}

	// Phase 2 — apply.
	if upgrade {
		return runUpgrade(version, arch, st, opts)
	}
	return runFreshInstall(version, arch, st, opts)
}

// stageAll (phase 1) downloads + verifies the agent binary and, when requested,
// every runtime dep, into tmp. Nothing on the host is touched.
func stageAll(version, arch, tmp string, withRuntime bool, useSelf bool) (*staged, error) {
	if useSelf {
		step("Phase 1/2 — staging current binary and verifying runtime deps (linux/%s)", arch)
	} else {
		step("Phase 1/2 — downloading and verifying binaries (linux/%s)", arch)
	}
	st := &staged{}

	var err error
	if useSelf {
		st.agentBin, err = stageSelfAgent(tmp)
	} else {
		st.agentBin, err = stageAgent(version, arch, tmp)
	}
	if err != nil {
		return nil, err
	}

	if withRuntime {
		for _, dep := range runtimeDeps {
			sd, err := stageDep(dep, arch, tmp)
			if err != nil {
				return nil, err
			}
			st.runtime = append(st.runtime, sd)
		}
	} else {
		info("skipping container runtime (needs root)")
	}
	return st, nil
}

func stageSelfAgent(tmp string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving current executable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("resolving current executable symlink: %w", err)
	}
	dst := filepath.Join(tmp, "opendeploy-self")
	if err := installBinary(self, dst, 0o755, noChown); err != nil {
		return "", fmt.Errorf("staging current executable %s: %w", self, err)
	}
	info("using current executable %s as v0.0.0", self)
	return dst, nil
}

// preflight enforces the same permission rules as the shell installer: fresh
// install requires root; upgrade requires write access to the bin dir (and, if
// run unprivileged, a working passwordless systemctl restart via sudoers).
func preflight(upgrade bool) error {
	if !upgrade {
		if !isRoot() && !dryRun {
			return fmt.Errorf("first-time install must be run as root (try: sudo %s install)", os.Args[0])
		}
		return nil
	}
	if isRoot() || dryRun {
		return nil
	}
	binDir := filepath.Dir(binPath)
	if !isWritableDir(binDir) {
		return fmt.Errorf("upgrade requires write access to %s — run as root or as the opendeploy user", binDir)
	}
	if !probe("sudo", "-ln", "/usr/bin/systemctl", "restart", serviceName) {
		return fmt.Errorf("user is not permitted to restart %s without a password — run as root or the opendeploy user", serviceName)
	}
	return nil
}

// stageAgent (phase 1) fetches the release binary and verifies it against the
// release's sha256sums.txt, returning its temp path.
func stageAgent(version, arch, tmp string) (string, error) {
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, version)
	binFile := "opendeploy-linux-" + arch

	binDst := filepath.Join(tmp, binFile)
	if err := download(baseURL+"/"+binFile, binDst); err != nil {
		return "", err
	}

	sumsDst := filepath.Join(tmp, "sha256sums.txt")
	if err := download(baseURL+"/sha256sums.txt", sumsDst); err != nil {
		return "", err
	}
	want, err := checksumFor(sumsDst, binFile)
	if err != nil {
		return "", err
	}
	if err := verifySHA256(binDst, want); err != nil {
		return "", err
	}
	return binDst, nil
}

// checksumFor parses a `<hex>  <filename>` sums file for the given file.
func checksumFor(sumsFile, name string) (string, error) {
	data, err := os.ReadFile(sumsFile)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in %s", name, filepath.Base(sumsFile))
}

// releaseBinPath is where a downloaded agent binary lands (a versioned dir,
// symlinked from binPath — the pattern the runtime binaries mirror too).
func releaseBinPath(version, arch string) string {
	return filepath.Join(releasesDir, repo, version, "opendeploy-linux-"+arch)
}

// runUpgrade (phase 2) installs the staged binary into its versioned dir, flips
// the symlink, restarts the service, then applies the staged runtime (if any).
func runUpgrade(version, arch string, st *staged, opts installOptions) error {
	step("Phase 2/2 — upgrading opendeploy to %s (linux/%s)", version, arch)
	own := resolveOpenDeployOwner()
	rootOpenDeploy := owner{uid: 0, gid: own.gid}
	if !isRoot() && !dryRun && (!pathExists(buildLogsDir) || !pathExists(runLogsDir)) {
		return fmt.Errorf("upgrade requires root once to create %s and %s", buildLogsDir, runLogsDir)
	}
	if err := ensureLogDirs(own); err != nil {
		return err
	}

	dst := releaseBinPath(version, arch)
	if err := ensureReleaseArtifactDir(version, arch, own); err != nil {
		return err
	}
	if err := installBinary(st.agentBin, dst, 0o755, own); err != nil {
		return err
	}
	if err := atomicSymlink(dst, binPath); err != nil {
		return err
	}
	info("symlinked %s -> %s", binPath, dst)
	if opts.hasEnvOverrides() {
		step("Updating config")
		if err := updateEnvFile(opts, rootOpenDeploy); err != nil {
			return err
		}
	}
	step("Updating systemd unit")
	if err := updateServiceUnitForUpgrade(opts); err != nil {
		return err
	}

	step("Restarting %s", serviceName)
	if isRoot() || dryRun {
		if err := systemctl("restart", serviceName); err != nil {
			return err
		}
	} else {
		if err := run("sudo", "-n", "systemctl", "restart", serviceName); err != nil {
			return err
		}
	}

	// Root only — an opendeploy-user upgrade staged no runtime, so this is a no-op.
	if len(st.runtime) == 0 {
		info("skipping container runtime refresh (needs root)")
	} else if err := applyRuntime(st.runtime); err != nil {
		return err
	}

	step("Upgrade to %s complete", version)
	return nil
}

// runFreshInstall (phase 2) does the full first-time setup from staged binaries.
func runFreshInstall(version, arch string, st *staged, opts installOptions) error {
	step("Phase 2/2 — installing opendeploy %s (linux/%s)", version, arch)

	// system user (must exist before we can resolve ownership)
	step("Creating system user and directories")
	if err := ensureSystemUser(); err != nil {
		return err
	}
	own := resolveOpenDeployOwner()
	rootOpenDeploy := owner{uid: 0, gid: own.gid}

	if err := ensureDir(dataDir, 0o750, own); err != nil {
		return err
	}
	if opts.restore != nil {
		step("Restoring primary database")
		if err := restorePrimaryBackup(*opts.restore, own); err != nil {
			return err
		}
	}
	// Sibling dirs outside the private data dir. Release artifacts are reachable by
	// a different runAs user (0755); logs stay private to opendeploy (0750).
	if err := ensureDir(releasesDir, 0o755, own); err != nil {
		return err
	}
	if err := ensureLogDirs(own); err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(binPath), 0o755, own); err != nil {
		return err
	}

	// install staged binary into its versioned dir + symlink
	step("Installing binary")
	dst := releaseBinPath(version, arch)
	if err := ensureReleaseArtifactDir(version, arch, own); err != nil {
		return err
	}
	if err := installBinary(st.agentBin, dst, 0o755, own); err != nil {
		return err
	}
	if err := atomicSymlink(dst, binPath); err != nil {
		return err
	}
	info("symlinked %s -> %s", binPath, dst)

	// env file — never clobber an existing operator-edited file
	step("Writing config")
	bootstrap, err := generateBootstrapCredentials(opts)
	if err != nil {
		return err
	}
	wrote, err := writeFile(envFile, renderEnvTemplate(opts, bootstrap), 0o640, rootOpenDeploy, true)
	if err != nil {
		return err
	}
	if !wrote && opts.hasEnvOverrides() {
		if err := updateEnvFile(opts, rootOpenDeploy); err != nil {
			return err
		}
	}
	if wrote {
		info("created %s (edit before starting)", envFile)
	} else {
		info("kept existing %s", envFile)
	}

	// TLS dir
	if err := ensureDir(tlsDir, 0o750, own); err != nil {
		return err
	}

	// sudoers — validate with visudo before moving into place
	step("Installing sudoers drop-in")
	if err := installSudoers(); err != nil {
		return err
	}

	// systemd unit (embedded)
	step("Installing systemd unit")
	if _, err := writeFile(serviceUnitPath, renderOpenDeployUnit(opts), 0o644, noChown, false); err != nil {
		return err
	}
	if err := daemonReload(); err != nil {
		return err
	}
	if err := systemctl("enable", serviceName); err != nil {
		return err
	}

	// bundled container runtime (staged in phase 1)
	if err := applyRuntime(st.runtime); err != nil {
		return err
	}

	step("Starting service")
	if err := systemctl("start", serviceName); err != nil {
		return err
	}

	printInstallComplete(version, opts, bootstrap, wrote)
	return nil
}

func generateBootstrapCredentials(opts installOptions) (*bootstrapCredentials, error) {
	if opts.role != "primary" {
		return nil, nil
	}
	if opts.restore != nil {
		return nil, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return nil, fmt.Errorf("generating setup password: %w", err)
	}
	password := fmt.Sprintf("opendeploy%04d", n.Int64())
	hash, err := authu.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hashing setup password: %w", err)
	}
	return &bootstrapCredentials{password: password, hash: hash}, nil
}

func ensureLogDirs(own owner) error {
	for _, dir := range []string{buildLogsDir, runLogsDir} {
		if err := ensureDir(dir, 0o750, own); err != nil {
			return err
		}
	}
	return nil
}

func ensureReleaseArtifactDir(version, arch string, own owner) error {
	dir := filepath.Dir(releaseBinPath(version, arch))
	if err := ensureDir(releasesDir, 0o755, own); err != nil {
		return err
	}
	rel, err := filepath.Rel(releasesDir, dir)
	if err != nil {
		return err
	}
	current := releasesDir
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if err := ensureDir(current, 0o755, own); err != nil {
			return err
		}
	}
	return nil
}

// installSudoers writes the drop-in to a .new file, validates it with visudo,
// and only then renames it into place — never leaving an invalid sudoers file.
func installSudoers() error {
	tmpFile := sudoersFile + ".new"
	if _, err := writeFile(tmpFile, sudoersTemplate, 0o440, noChown, false); err != nil {
		return err
	}
	if err := run("visudo", "-cf", tmpFile); err != nil {
		_ = removeFile(tmpFile)
		return fmt.Errorf("sudoers validation failed: %w", err)
	}
	return renameFile(tmpFile, sudoersFile)
}

func renameFile(from, to string) error {
	if dryRun {
		planned("rename %s -> %s", from, to)
		return nil
	}
	return os.Rename(from, to)
}

// resolveOpenDeployOwner resolves the opendeploy uid/gid, tolerating dry-run runs
// where the user may not exist yet.
func resolveOpenDeployOwner() owner {
	o, err := lookupOwner()
	if err != nil {
		return noChown
	}
	return o
}

// isWritableDir reports whether the current user can create files in dir.
func isWritableDir(dir string) bool {
	probeFile := filepath.Join(dir, ".opendeploy-write-probe")
	f, err := os.OpenFile(probeFile, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	_ = os.Remove(probeFile)
	return true
}

func updateServiceUnitForUpgrade(opts installOptions) error {
	if isRoot() || dryRun {
		if _, err := writeFile(serviceUnitPath, renderOpenDeployUnit(opts), 0o644, noChown, false); err != nil {
			return err
		}
		return daemonReload()
	}
	content, err := os.ReadFile(serviceUnitPath)
	if err != nil {
		return err
	}
	if !strings.Contains(string(content), "ExecStart=/var/lib/opendeploy/bin/opendeploy "+opts.role) {
		return fmt.Errorf("changing opendeploy.service role to %s requires root", opts.role)
	}
	info("kept existing %s", serviceUnitPath)
	return nil
}

func printInstallComplete(version string, opts installOptions, bootstrap *bootstrapCredentials, wroteEnv bool) {
	logDir, logFile := serviceLogPaths(version, time.Now().UTC())
	if opts.role == "secondary" {
		fmt.Print("\nInstall complete. opendeploy.service is enabled and started.\n")
		printServiceLogDetails(logDir, logFile)
		return
	}
	fmt.Print("\nInstall complete. opendeploy.service is enabled and started.\n")
	fmt.Print("\nWeb UI:\n")
	for _, addr := range webUIAddrs(opts) {
		fmt.Printf("  %s\n", addr)
	}
	if bootstrap != nil && wroteEnv {
		fmt.Printf("\nTemporary setup password: %s\n", bootstrap.password)
		fmt.Print("Use this password to register the first passkey. Replace the master password after bootstrap if needed.\n")
	} else {
		fmt.Printf("\nKept existing %s. Use its configured setup password/hash to bootstrap.\n", envFile)
	}
	printServiceLogDetails(logDir, logFile)
}

func printServiceLogDetails(logDir, logFile string) {
	fmt.Print("\nOpenDeploy service logs:\n")
	fmt.Printf("  Directory: %s\n", logDir)
	fmt.Printf("  Current file: %s\n", logFile)
	fmt.Printf("  Journal fallback: sudo journalctl -u %s -f\n", serviceName)
}

func serviceLogPaths(version string, now time.Time) (string, string) {
	logDir := filepath.Join(runLogsDir, "0", version, "opendeploy")
	return logDir, filepath.Join(logDir, now.UTC().Format("20060102_15")+".logbin")
}

func webUIAddrs(opts installOptions) []string {
	scheme := "https"
	defaultPort := "443"
	if opts.httpOnly != nil && *opts.httpOnly {
		scheme = "http"
		defaultPort = "80"
	}
	listen := ":443"
	if opts.webListen != nil && strings.TrimSpace(*opts.webListen) != "" {
		listen = strings.TrimSpace(*opts.webListen)
	}
	port := listenPort(listen, defaultPort)

	var addrs []string
	for _, host := range acmeHosts(opts) {
		addrs = append(addrs, formatURL(scheme, host, port, defaultPort))
	}
	if len(addrs) == 0 {
		host := listenHost(listen)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		addrs = append(addrs, formatURL(scheme, host, port, defaultPort))
	}
	addrs = append(addrs, "listening on "+listen)
	return addrs
}

func acmeHosts(opts installOptions) []string {
	if opts.acmeHosts == nil {
		return []string{"opendeploy.example.com"}
	}
	var hosts []string
	for _, host := range strings.Split(*opts.acmeHosts, ",") {
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func listenHost(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return ""
}

func listenPort(listen string, fallback string) string {
	_, port, err := net.SplitHostPort(listen)
	if err == nil && port != "" {
		return port
	}
	idx := strings.LastIndex(listen, ":")
	if idx >= 0 && idx+1 < len(listen) {
		return strings.Trim(listen[idx+1:], "[]")
	}
	return fallback
}

func formatURL(scheme, host, port, defaultPort string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if port == "" || port == defaultPort {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}
