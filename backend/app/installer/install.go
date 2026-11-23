package installer

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/app/primarybootstrap"
	"github.com/jptrs93/opsagent/backend/lib/runtimebin"
	"github.com/jptrs93/opsagent/backend/util/certu"
	buildversion "github.com/jptrs93/opsagent/backend/util/version"
)

type bootstrapCredentials struct {
	password string
	hash     string
}

// staged holds the verified temp paths produced by phase 1 (download + verify),
// consumed by phase 2 (apply).
type staged struct {
	agentBin string
	runtime  []runtimebin.Staged
}

type installOptions struct {
	role                  string
	httpOnly              *bool
	passwordLogin         *bool
	requirePasskey        *bool
	webListen             *string
	webTLSSelfSigned      *bool
	webTLSCertPEM         *string
	passkeyExtraOrigins   *string
	clusterListen         *string
	enrollmentListen      *string
	webHosts              *string
	clusterAddr           *string
	enrollmentAddr        *string
	enrollmentFingerprint *string
	underlayAddress       *string
	restore               *restoreOptions
}

func (o installOptions) hasEnvOverrides() bool {
	return o.passkeyExtraOrigins != nil || o.clusterAddr != nil || o.enrollmentAddr != nil || o.enrollmentFingerprint != nil || o.underlayAddress != nil
}

func (o installOptions) hasSystemConfigOverrides() bool {
	return o.httpOnly != nil || o.requirePasskey != nil || o.webListen != nil || o.webTLSSelfSigned != nil || o.webTLSCertPEM != nil || o.clusterListen != nil || o.enrollmentListen != nil || o.webHosts != nil
}

func doInstall(version string, opts installOptions) error {
	if pathExists(serviceUnitPath) {
		return fmt.Errorf("opendeploy is already installed (%s exists); run `%s upgrade` to upgrade it in place", serviceUnitPath, os.Args[0])
	}
	if !isRoot() && !dryRun {
		return fmt.Errorf("install must be run as root (try: sudo %s install %s)", os.Args[0], opts.role)
	}
	if err := requireRootOwned(dataDir); err != nil {
		return err
	}
	arch, err := runtimebin.HostArch()
	if err != nil {
		return err
	}
	version, selfInstall, err := resolveVersion(version)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "opendeploy-installer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	st, err := stageAll(version, arch, tmp, selfInstall)
	if err != nil {
		return err
	}
	return runFreshInstall(version, arch, st, opts)
}

func doUpgrade(version string) error {
	if !isRoot() && !dryRun {
		return fmt.Errorf("upgrade must be run as root (try: sudo %s upgrade)", os.Args[0])
	}
	role, err := installedRole()
	if err != nil {
		return err
	}
	if err := requireRootOwned(dataDir); err != nil {
		return err
	}
	if role == "primary" && !pathExists(filepath.Join(dataDir, "primary.db")) && !dryRun {
		return fmt.Errorf("refusing to upgrade primary because %s is missing", filepath.Join(dataDir, "primary.db"))
	}
	arch, err := runtimebin.HostArch()
	if err != nil {
		return err
	}
	version, selfInstall, err := resolveVersion(version)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "opendeploy-installer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	st, err := stageAll(version, arch, tmp, selfInstall)
	if err != nil {
		return err
	}
	return runUpgrade(version, arch, st, installOptions{role: role})
}

func requireRootOwned(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, not root: this node holds state from a release that ran the agent unprivileged; follow the per-node conversion in upgrade_log.md first", dir, st.Uid)
	}
	return nil
}

func resolveVersion(version string) (string, bool, error) {
	if version == "" {
		return buildversion.Version, true, nil
	}
	if version != "latest" {
		return version, false, nil
	}
	step("Resolving latest release")
	latest, err := resolveLatestTag()
	if err != nil {
		return "", false, err
	}
	info("latest is %s", latest)
	return latest, false, nil
}

func installedRole() (string, error) {
	content, err := os.ReadFile(serviceUnitPath)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("opendeploy is not installed (%s is missing); run `%s install primary` or `%s install secondary` first", serviceUnitPath, os.Args[0], os.Args[0])
	}
	if err != nil {
		return "", err
	}
	return roleFromUnit(content)
}

func roleFromUnit(content []byte) (string, error) {
	for _, line := range strings.Split(string(content), "\n") {
		command, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		fields := strings.Fields(command)
		if len(fields) == 2 && fields[0] == binPath && (fields[1] == "primary" || fields[1] == "secondary") {
			return fields[1], nil
		}
		return "", fmt.Errorf("cannot determine the installed role from %s: unexpected ExecStart %q", serviceUnitPath, command)
	}
	return "", fmt.Errorf("cannot determine the installed role from %s: no ExecStart line", serviceUnitPath)
}

// stageAll (phase 1) downloads + verifies the agent binary and every runtime
// dep into tmp. Nothing on the host is touched.
func stageAll(version, arch, tmp string, selfInstall bool) (*staged, error) {
	if selfInstall {
		step("Phase 1/2 — staging current binary and verifying runtime deps (linux/%s)", arch)
	} else {
		step("Phase 1/2 — downloading and verifying binaries (linux/%s)", arch)
	}
	st := &staged{}

	var err error
	if selfInstall {
		st.agentBin, err = stageSelfAgent(tmp)
	} else {
		st.agentBin, err = stageAgent(version, arch, tmp)
	}
	if err != nil {
		return nil, err
	}

	for _, dep := range runtimebin.Components() {
		sd, err := stageDep(dep, arch, tmp)
		if err != nil {
			return nil, err
		}
		st.runtime = append(st.runtime, sd)
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
	// Stage for real even under --dry-run: tmp is scratch, and phase 2 compares
	// the staged bytes against the installed binary to decide whether to restart.
	data, err := os.ReadFile(self)
	if err != nil {
		return "", fmt.Errorf("reading current executable %s: %w", self, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return "", fmt.Errorf("staging current executable %s: %w", self, err)
	}
	info("using current executable %s", self)
	return dst, nil
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
// the symlink, refreshes the env file and unit, restarts the service when any
// of them changed, then applies the staged runtime.
func runUpgrade(version, arch string, st *staged, opts installOptions) error {
	step("Phase 2/2 — upgrading opendeploy to %s (linux/%s)", version, arch)
	if err := ensureLogDirs(); err != nil {
		return err
	}
	if err := ensureDir(assetCacheDir, 0o755); err != nil {
		return err
	}
	if opts.role == "primary" && !dryRun {
		if err := (primarybootstrap.Service{DataDir: dataDir}).Validate(context.Background()); err != nil {
			return fmt.Errorf("validating primary before upgrade: %w", err)
		}
	}

	dst := releaseBinPath(version, arch)
	binaryChanged := !fileBytesEqual(binPath, st.agentBin)
	if err := ensureReleaseArtifactDir(version, arch); err != nil {
		return err
	}
	if fileBytesEqual(dst, st.agentBin) {
		info("%s already in place", dst)
	} else if err := installBinary(st.agentBin, dst, 0o755); err != nil {
		return err
	}
	if err := atomicSymlink(dst, binPath); err != nil {
		return err
	}
	info("symlinked %s -> %s", binPath, dst)
	envChanged := false
	if opts.hasEnvOverrides() || opts.role == "primary" {
		step("Updating config")
		var err error
		if envChanged, err = updateEnvFile(opts); err != nil {
			return err
		}
	}
	step("Updating systemd unit")
	unitChanged, err := updateServiceUnitForUpgrade(opts)
	if err != nil {
		return err
	}

	if !binaryChanged && !envChanged && !unitChanged {
		info("binary, config, and unit are unchanged; not restarting %s", serviceName)
	} else {
		step("Restarting %s", serviceName)
		if err := systemctl("restart", serviceName); err != nil {
			return err
		}
	}

	if err := applyRuntime(st.runtime); err != nil {
		return err
	}

	step("Upgrade to %s complete", version)
	return nil
}

// runFreshInstall (phase 2) does the full first-time setup from staged binaries.
func runFreshInstall(version, arch string, st *staged, opts installOptions) error {
	step("Phase 2/2 — installing opendeploy %s (linux/%s)", version, arch)

	step("Creating directories")
	if err := ensureDir(dataDir, 0o750); err != nil {
		return err
	}
	if err := ensureDir(assetCacheDir, 0o755); err != nil {
		return err
	}
	if opts.restore != nil {
		step("Restoring primary database")
		if err := restorePrimaryBackup(*opts.restore, opts); err != nil {
			return err
		}
	}
	// Sibling dirs outside the private data dir. Release artifacts are
	// world-readable (0755); logs stay private to root (0750).
	if err := ensureDir(releasesDir, 0o755); err != nil {
		return err
	}
	if err := ensureLogDirs(); err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(binPath), 0o755); err != nil {
		return err
	}

	step("Installing binary")
	dst := releaseBinPath(version, arch)
	if err := ensureReleaseArtifactDir(version, arch); err != nil {
		return err
	}
	if err := installBinary(st.agentBin, dst, 0o755); err != nil {
		return err
	}
	if err := atomicSymlink(dst, binPath); err != nil {
		return err
	}
	info("symlinked %s -> %s", binPath, dst)

	bootstrap, err := initializePrimary(opts)
	if err != nil {
		return err
	}

	// env file — never clobber an existing operator-edited file
	step("Writing config")
	wrote, err := writeFile(envFile, renderEnvTemplate(opts), envFileMode, true)
	if err != nil {
		return err
	}
	if !wrote && (opts.hasEnvOverrides() || opts.role == "primary") {
		if _, err := updateEnvFile(opts); err != nil {
			return err
		}
	}
	if wrote {
		info("created %s (edit before starting)", envFile)
	} else {
		info("kept existing %s", envFile)
	}

	step("Installing systemd unit")
	if _, err := writeFile(serviceUnitPath, renderOpenDeployUnit(opts), 0o644, false); err != nil {
		return err
	}
	if err := daemonReload(); err != nil {
		return err
	}
	if err := systemctl("enable", serviceName); err != nil {
		return err
	}

	if err := applyRuntime(st.runtime); err != nil {
		return err
	}

	step("Starting service")
	if err := systemctl("start", serviceName); err != nil {
		return err
	}

	printInstallComplete(opts, bootstrap)
	return nil
}

func initializePrimary(opts installOptions) (*bootstrapCredentials, error) {
	if opts.role != "primary" {
		return nil, nil
	}
	service := primarybootstrap.Service{DataDir: dataDir}
	if opts.restore != nil || pathExists(filepath.Join(dataDir, "primary.db")) {
		if dryRun {
			planned("validate existing primary bootstrap state")
			return nil, nil
		}
		if opts.restore == nil && opts.hasSystemConfigOverrides() {
			return nil, fmt.Errorf("initial primary config flags cannot be applied when preserving an existing database")
		}
		if err := service.Validate(context.Background()); err != nil {
			return nil, fmt.Errorf("validating existing primary bootstrap state: %w", err)
		}
		if err := securePrimaryBootstrapArtifacts(); err != nil {
			return nil, err
		}
		info("preserved existing primary bootstrap state")
		return nil, nil
	}
	if dryRun {
		planned("initialize primary database, secrets, network prefix, and TLS identity")
		return nil, nil
	}
	bootstrap, err := generateBootstrapCredentials(opts)
	if err != nil {
		return nil, err
	}
	initial := systemconfig.DefaultInitial()
	initial.MasterPasswordHash = bootstrap.hash
	if opts.httpOnly != nil {
		initial.WebHTTPEnabled = *opts.httpOnly
		initial.WebHTTPSEnabled = !*opts.httpOnly
	}
	if opts.requirePasskey != nil {
		initial.RequirePasskey = *opts.requirePasskey
	}
	if opts.webListen != nil {
		initial.WebHTTPListen = *opts.webListen
		initial.WebHTTPSListen = *opts.webListen
	}
	if opts.selfManagedTLS() {
		initial.WebTLSSelfManaged = true
	}
	if opts.clusterListen != nil {
		initial.ClusterListen = *opts.clusterListen
	}
	if opts.enrollmentListen != nil {
		initial.EnrollmentListen = *opts.enrollmentListen
	}
	initial.AcmeHosts = webHosts(opts)
	var webTLSCertPEM []byte
	if opts.webTLSCertPEM != nil {
		webTLSCertPEM = []byte(*opts.webTLSCertPEM)
	}
	underlayAddress := ""
	if opts.underlayAddress != nil {
		underlayAddress = *opts.underlayAddress
	}
	if _, err := service.Initialize(context.Background(), primarybootstrap.Options{
		Initial:         initial,
		WebTLSCertPEM:   webTLSCertPEM,
		UnderlayAddress: underlayAddress,
	}); err != nil {
		return nil, fmt.Errorf("initializing primary: %w", err)
	}
	printSetupPassword(bootstrap.password)
	if err := securePrimaryBootstrapArtifacts(); err != nil {
		return nil, err
	}
	info("initialized primary database, secrets, network prefix, and TLS identity")
	return bootstrap, nil
}

func printSetupPassword(password string) {
	fmt.Printf("\nMaster password: %s\n", password)
	fmt.Print("Use this password under \"Enrol or recover\" on the web UI sign-in page to create the first administrator. It signs nobody in by itself; keep it for recovery, or replace it under Settings.\n")
}

func securePrimaryBootstrapArtifacts() error {
	dbPath := filepath.Join(dataDir, "primary.db")
	paths := append(sqliteArtifactPaths(dbPath), filepath.Join(dataDir, "machine.key"))
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func generateBootstrapCredentials(opts installOptions) (*bootstrapCredentials, error) {
	if opts.role != "primary" {
		return nil, nil
	}
	if opts.restore != nil {
		return nil, nil
	}
	token, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating setup password: %w", err)
	}
	password := "opendeploy-" + token
	hash, err := authu.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hashing setup password: %w", err)
	}
	return &bootstrapCredentials{password: password, hash: hash}, nil
}

func ensureLogDirs() error {
	for _, dir := range []string{buildLogsDir, runLogsDir} {
		if err := ensureDir(dir, 0o750); err != nil {
			return err
		}
	}
	return nil
}

func ensureReleaseArtifactDir(version, arch string) error {
	dir := filepath.Dir(releaseBinPath(version, arch))
	if err := ensureDir(releasesDir, 0o755); err != nil {
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
		if err := ensureDir(current, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func updateServiceUnitForUpgrade(opts installOptions) (bool, error) {
	rendered := renderOpenDeployUnit(opts)
	content, err := os.ReadFile(serviceUnitPath)
	if err != nil {
		return false, err
	}
	if bytes.Equal(content, rendered) {
		info("kept existing %s", serviceUnitPath)
		return false, nil
	}
	if _, err := writeFile(serviceUnitPath, rendered, 0o644, false); err != nil {
		return false, err
	}
	return true, daemonReload()
}

func printInstallComplete(opts installOptions, bootstrap *bootstrapCredentials) {
	logDir, logFile := serviceLogPaths(time.Now().UTC())
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
	if bootstrap != nil {
		fmt.Print("\nThe master password was printed above. Use it under \"Enrol or recover\" on the sign-in page to create the first administrator.\n")
	} else {
		fmt.Print("\nPreserved the existing primary setup credentials.\n")
	}
	if opts.passwordLogin != nil {
		fmt.Print("\n--password-login is ignored: every user signs in with their own password now. The flag will be removed in a later release.\n")
	}
	if opts.httpOnly != nil && *opts.httpOnly {
		fmt.Print("\nThe web UI is plain HTTP, so passwords cross the network unencrypted. Use this only on a trusted network or over an SSH tunnel.\n")
	}
	if localCAInUse(opts) {
		addrs := webUIAddrs(opts)
		fmt.Print("\n" + caTrustInstructions(filepath.Join(dataDir, certu.WebUILocalCAFileName), addrs[0]+"/v1/tls/ca.crt"))
	}
	printServiceLogDetails(logDir, logFile)
}

func printServiceLogDetails(logDir, logFile string) {
	fmt.Print("\nOpenDeploy service logs:\n")
	fmt.Printf("  Directory: %s\n", logDir)
	fmt.Printf("  Current file: %s\n", logFile)
	fmt.Printf("  Journal fallback: sudo journalctl -u %s -f\n", serviceName)
}

func serviceLogPaths(now time.Time) (string, string) {
	logDir := filepath.Join(runLogsDir, "0")
	bucket := serviceLogBucket(now.UTC())
	return logDir, filepath.Join(logDir, bucket.Format("20060102_1504")+".wal")
}

func serviceLogBucket(t time.Time) time.Time {
	t = t.UTC()
	minute := 0
	if t.Minute() >= 30 {
		minute = 30
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
}

func webUIAddrs(opts installOptions) []string {
	scheme := "https"
	defaultPort := "443"
	if opts.httpOnly != nil && *opts.httpOnly {
		scheme = "http"
		defaultPort = "80"
	}
	listen := ":443"
	if opts.httpOnly != nil && *opts.httpOnly {
		listen = ":8080"
	}
	if opts.webListen != nil && strings.TrimSpace(*opts.webListen) != "" {
		listen = strings.TrimSpace(*opts.webListen)
	}
	port := listenPort(listen, defaultPort)

	var addrs []string
	for _, host := range webHosts(opts) {
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

// webHosts are the hostnames the web UI will be reached by. Without an
// explicit list, a local-style install (HTTP only, or self-managed TLS) is
// assumed to be reached as localhost; an ACME install keeps the placeholder
// the operator is expected to replace.
func webHosts(opts installOptions) []string {
	if opts.webHosts == nil {
		if localStyleInstall(opts) {
			return []string{"localhost"}
		}
		return []string{"opendeploy.example.com"}
	}
	var hosts []string
	for _, host := range strings.Split(*opts.webHosts, ",") {
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// selfManagedTLS reports whether the install supplies its own Web UI
// certificate, either a bundle from the operator or one issued by the local
// CA, rather than requesting one from Let's Encrypt.
func (o installOptions) selfManagedTLS() bool {
	return o.webTLSCertPEM != nil || (o.webTLSSelfSigned != nil && *o.webTLSSelfSigned)
}

// validateWebTLS rejects flag combinations that ask for two ways of serving
// the Web UI at once.
func (o installOptions) validateWebTLS() error {
	selfSigned := o.webTLSSelfSigned != nil && *o.webTLSSelfSigned
	httpOnly := o.httpOnly != nil && *o.httpOnly
	if selfSigned && o.webTLSCertPEM != nil {
		return fmt.Errorf("--web-tls-self-signed true and --web-tls-cert-pem-file cannot be combined: choose one")
	}
	if httpOnly && (selfSigned || o.webTLSCertPEM != nil) {
		return fmt.Errorf("--http-only true cannot be combined with a Web UI certificate option")
	}
	return nil
}

func localStyleInstall(opts installOptions) bool {
	return (opts.httpOnly != nil && *opts.httpOnly) || opts.selfManagedTLS()
}

func localCAInUse(opts installOptions) bool {
	return opts.webTLSSelfSigned != nil && *opts.webTLSSelfSigned && opts.webTLSCertPEM == nil && !(opts.httpOnly != nil && *opts.httpOnly)
}

// caTrustInstructions is printed after a self-managed TLS install and mirrors
// the expandable help on the login page.
func caTrustInstructions(caPath, caURL string) string {
	return fmt.Sprintf(`The web UI uses a certificate issued by a locally generated CA. Browsers will
warn until that CA is trusted. Password login works through the warning;
passkeys need the CA trusted first.

  CA certificate: %s
  Also served at: %s

Trust it on the machine running the browser:
  macOS:    sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt
  Linux:    sudo cp opendeploy-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates
            (Chrome/Chromium on Linux also need: certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" -i opendeploy-ca.crt)
  Windows:  certutil -addstore -f ROOT opendeploy-ca.crt
  Firefox:  set security.enterprise_roots.enabled to true in about:config to use the OS store, or import it under Settings > Certificates.
Then restart the browser and reload the page.
`, caPath, caURL)
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
