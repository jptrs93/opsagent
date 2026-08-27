package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const ipv4EgressListenerPort = 18080

const mockUpgradeVersion = "v1.0.0"

var mockAdditionalReleaseVersions = []string{"v1.0.1", "v1.0.2", "v1.0.3"}

var tlsIngressHosts = []string{
	"one.ingress.opendeploy.test",
	"two.ingress.opendeploy.test",
	"three.ingress.opendeploy.test",
	"web.ingress.opendeploy.test",
	"api.ingress.opendeploy.test",
	"stream.ingress.opendeploy.test",
	"streamh1.ingress.opendeploy.test",
	"ws.ingress.opendeploy.test",
	"rollover.ingress.opendeploy.test",
	"netpol.ingress.opendeploy.test",
}

type config struct {
	ScriptDir string
	RepoRoot  string
	Timings   []*timingEntry
	StepStack []*timingEntry
	Log       *runLogger

	StateDir        string
	MockArtifactDir string
	ResultsDir      string
	ReportDir       string
	E2EEnvFile      string
	CertDir         string
	CACert          string
	CAKey           string
	ServerCert      string
	ServerKey       string
	ServerBundle    string

	PrimaryName    string
	SecondaryName  string
	Secondary2Name string
	RepoMirrorName string
	NetworkName    string
	VMType         string
	WebHost        string
	WebBaseURL     string

	ReleaseRepo          string
	InstallVersion       string
	UpgradeVersion       string
	SelfVersion          string
	BackupRestore        bool
	RemoteMode           string
	MockOpenDeploySource string

	NodeCPUs         string
	NodeMemory       string
	NodeDisk         string
	RepoMirrorCPUs   string
	RepoMirrorMemory string
	RepoMirrorDisk   string

	RepoMirrorReleases             string
	RepoMirrorLatest               string
	RepoRegistryHost               string
	RepoRegistryPort               string
	PostgresImage                  string
	DeclarativePostgresSourceImage string
	DeclarativePostgresImage       string
	MinioImage                     string
	RepoMirrorOCI                  string
	ContainerdVersion              string
	RuncVersion                    string

	Goarch   string
	LimaArch string

	SelfBin                   string
	RepoMirrorBin             string
	MockReleaseBinDir         string
	RepoMirrorRefresh         bool
	PrepareArtifacts          bool
	RepoProxyUnknown          string
	NixCacheMode              string
	LimaArm64URL              string
	LimaAmd64URL              string
	LimaArm64Image            string
	LimaAmd64Image            string
	PlaywrightDockerImage     string
	PlaywrightBaseURL         string
	PlaywrightBaseURLSet      bool
	PlaywrightHostPort        string
	PlaywrightSecondaryPorts  string
	PlaywrightFilteredPorts   string
	PlaywrightTLSIngressPort  string
	PlaywrightHTTPIngressPort string
	PlaywrightTunnelCmds      []*exec.Cmd

	OpenDeployGitHubToken string
}

type timingEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Seconds     float64        `json:"seconds"`
	Started     string         `json:"started"`
	Finished    string         `json:"finished"`
	Children    []*timingEntry `json:"children,omitempty"`
}

type runLogger struct {
	mu           sync.Mutex
	file         *os.File
	path         string
	finalPath    string
	currentStage string
}

var activeLogger *runLogger

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runMain() error {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	c, err := loadConfig(needsReleaseVersion(cmd))
	if err != nil {
		return err
	}
	switch cmd {
	case "run":
		return c.run()
	case "run-playwright":
		return c.runPlaywrightFlows()
	case "repo-mirror-up":
		return c.repoMirrorUp()
	case "repo-mirror-down":
		return c.repoMirrorDown()
	case "repo-mirror-status":
		return c.repoMirrorStatus()
	case "prepare-mock-artifacts":
		return c.prepareMockArtifacts()
	case "cleanup":
		return c.cleanup()
	case "backup-restore":
		return c.backupRestore()
	case "help", "-h", "--help":
		fmt.Println("usage: test-orchestrator [run|run-playwright|repo-mirror-up|repo-mirror-down|repo-mirror-status|prepare-mock-artifacts|cleanup|backup-restore]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func needsReleaseVersion(cmd string) bool {
	switch cmd {
	case "run", "run-playwright", "repo-mirror-up", "prepare-mock-artifacts", "backup-restore":
		return true
	default:
		return false
	}
}

func loadConfig(resolveLatestRelease bool) (*config, error) {
	scriptDir, err := findScriptDir()
	if err != nil {
		return nil, err
	}
	repoRoot := filepath.Dir(scriptDir)
	c := &config{ScriptDir: scriptDir, RepoRoot: repoRoot}

	c.StateDir = env("OPD_VM_STATE_DIR", filepath.Join(scriptDir, ".tmp"))
	c.MockArtifactDir = env("OPD_MOCK_ARTIFACT_DIR", filepath.Join(scriptDir, ".mock-artifacts"))
	c.ResultsDir = env("OPD_VM_RESULTS_DIR", filepath.Join(scriptDir, "test-results"))
	c.ReportDir = env("OPD_VM_REPORT_DIR", filepath.Join(scriptDir, "playwright-report"))
	c.E2EEnvFile = env("OPD_VM_E2E_ENV_FILE", filepath.Join(c.StateDir, "e2e.env"))
	// Keep the trusted test CA separate from disposable per-run state.
	c.CertDir = env("OPD_VM_CERT_DIR", filepath.Join(scriptDir, ".test-ca"))
	c.CACert = filepath.Join(c.CertDir, "ca.crt")
	c.CAKey = filepath.Join(c.CertDir, "ca.key")
	c.ServerCert = filepath.Join(c.CertDir, "server.crt")
	c.ServerKey = filepath.Join(c.CertDir, "server.key")
	c.ServerBundle = filepath.Join(c.CertDir, "server-bundle.pem")

	c.PrimaryName = env("OPD_VM_PRIMARY", "opendeploy-primary")
	c.SecondaryName = env("OPD_VM_SECONDARY", "opendeploy-secondary")
	c.Secondary2Name = env("OPD_VM_SECONDARY_2", "opendeploy-secondary-2")
	c.RepoMirrorName = env("OPD_VM_REPO_MIRROR", "opendeploy-repo-mirror")
	c.NetworkName = env("OPD_VM_NETWORK", "user-v2")
	c.VMType = env("OPD_VM_TYPE", "vz")
	c.WebHost = env("OPD_WEB_HOST", "primary.opendeploy.test")
	c.WebBaseURL = env("OPD_BASE_URL", "https://"+c.WebHost)

	c.ReleaseRepo = env("RELEASE_REPO", "jptrs93/opsagent")
	c.InstallVersion = strings.TrimSpace(os.Getenv("OPD_INSTALL_VERSION"))
	c.UpgradeVersion = strings.TrimSpace(os.Getenv("OPD_UPGRADE_VERSION"))
	c.SelfVersion = env("OPD_SELF_VERSION", "v0.0.0")
	c.BackupRestore = envBool("OPD_BACKUP_RESTORE", false)
	c.RemoteMode = env("OPD_REMOTE", "mock")
	c.MockOpenDeploySource = env("OPD_MOCK_OPENDEPLOY_SOURCE", "local")
	if c.usesSelfBootstrap() {
		if c.InstallVersion != "" {
			return nil, fmt.Errorf("OPD_INSTALL_VERSION is not supported in local mock mode: bootstrap always uses the local executable")
		}
		if c.UpgradeVersion != "" && c.UpgradeVersion != mockUpgradeVersion {
			return nil, fmt.Errorf("OPD_UPGRADE_VERSION is not supported in local mock mode: the upgrade fixture is always %s", mockUpgradeVersion)
		}
		c.InstallVersion = c.SelfVersion
		c.UpgradeVersion = mockUpgradeVersion
	} else if resolveLatestRelease && (c.InstallVersion == "" || c.UpgradeVersion == "") {
		latest, err := latestReleaseTag(c.ReleaseRepo, os.Getenv("OPENDEPLOY_GITHUB_TOKEN"))
		if err != nil {
			return nil, err
		}
		if c.InstallVersion == "" {
			c.InstallVersion = latest
		}
		if c.UpgradeVersion == "" {
			c.UpgradeVersion = latest
		}
	}

	c.NodeCPUs = env("OPD_VM_NODE_CPUS", "4")
	c.NodeMemory = env("OPD_VM_NODE_MEMORY", "6GiB")
	c.NodeDisk = env("OPD_VM_NODE_DISK", "80GiB")
	c.RepoMirrorCPUs = env("OPD_VM_REPO_MIRROR_CPUS", "4")
	c.RepoMirrorMemory = env("OPD_VM_REPO_MIRROR_MEMORY", "6GiB")
	c.RepoMirrorDisk = env("OPD_VM_REPO_MIRROR_DISK", "80GiB")

	if c.usesSelfBootstrap() {
		c.RepoMirrorReleases = env("OPD_REPO_MIRROR_RELEASES", strings.Join(append([]string{c.SelfVersion, mockUpgradeVersion}, mockAdditionalReleaseVersions...), " "))
		c.RepoMirrorLatest = env("OPD_REPO_MIRROR_LATEST", mockUpgradeVersion)
	} else {
		c.RepoMirrorReleases = env("OPD_REPO_MIRROR_RELEASES", c.InstallVersion+" "+c.UpgradeVersion)
		c.RepoMirrorLatest = env("OPD_REPO_MIRROR_LATEST", c.UpgradeVersion)
	}
	c.RepoRegistryHost = env("OPD_REPO_REGISTRY_HOST", c.RepoMirrorName)
	c.RepoRegistryPort = env("OPD_REPO_REGISTRY_PORT", "5000")
	if c.RemoteMode == "real" {
		c.PostgresImage = env("OPD_POSTGRES_IMAGE", "docker.io/library/postgres:18")
		c.DeclarativePostgresSourceImage = env("OPD_DECLARATIVE_POSTGRES_IMAGE", "ghcr.io/jptrs93/declarative-postgres-backrest:18.4_2.58.0_v13")
		c.DeclarativePostgresImage = c.DeclarativePostgresSourceImage
		c.MinioImage = env("OPD_MINIO_IMAGE", "docker.io/bitnamilegacy/minio:latest")
	} else {
		c.PostgresImage = env("OPD_POSTGRES_IMAGE", c.RepoRegistryHost+":"+c.RepoRegistryPort+"/library/postgres:18")
		c.DeclarativePostgresSourceImage = env("OPD_DECLARATIVE_POSTGRES_IMAGE", "ghcr.io/jptrs93/declarative-postgres-backrest:18.4_2.58.0_v13")
		c.DeclarativePostgresImage = env("OPD_DECLARATIVE_POSTGRES_MIRROR_IMAGE", c.RepoRegistryHost+":"+c.RepoRegistryPort+"/jptrs93/declarative-postgres-backrest:18.4_2.58.0_v13")
		c.MinioImage = env("OPD_MINIO_IMAGE", c.RepoRegistryHost+":"+c.RepoRegistryPort+"/bitnamilegacy/minio:latest")
	}
	c.RepoMirrorOCI = env("OPD_REPO_MIRROR_OCI_IMAGES", "docker.io/library/postgres:18="+c.PostgresImage+" "+c.DeclarativePostgresSourceImage+"="+c.DeclarativePostgresImage+" docker.io/bitnamilegacy/minio:latest="+c.MinioImage)
	c.ContainerdVersion = env("CONTAINERD_VERSION", "2.0.5")
	c.RuncVersion = env("RUNC_VERSION", "1.2.6")

	switch runtime.GOARCH {
	case "arm64":
		c.Goarch = "arm64"
		c.LimaArch = "aarch64"
	case "amd64":
		c.Goarch = "amd64"
		c.LimaArch = "x86_64"
	default:
		return nil, fmt.Errorf("unsupported host architecture: %s", runtime.GOARCH)
	}

	c.SelfBin = filepath.Join(c.StateDir, "opendeploy-linux-"+c.Goarch)
	c.RepoMirrorBin = filepath.Join(c.StateDir, "repo-mirror-linux-"+c.Goarch)
	c.MockReleaseBinDir = filepath.Join(c.StateDir, "mock-releases")
	c.RepoMirrorRefresh = envBool("OPD_REPO_MIRROR_REFRESH", false)
	c.PrepareArtifacts = envBool("OPD_PREPARE_MOCK_ARTIFACTS", false)
	c.RepoProxyUnknown = env("OPD_REPO_MIRROR_PROXY_UNKNOWN", "nixpkgs")
	c.NixCacheMode = env("OPD_NIX_CACHE_MODE", "proxy-cache")
	c.LimaArm64URL = env("OPD_LIMA_IMAGE_ARM64_URL", "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img")
	c.LimaAmd64URL = env("OPD_LIMA_IMAGE_AMD64_URL", "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img")
	c.LimaArm64Image = env("OPD_LIMA_IMAGE_ARM64", filepath.Join(c.MockArtifactDir, "lima", "ubuntu-24.04-server-cloudimg-arm64.img"))
	c.LimaAmd64Image = env("OPD_LIMA_IMAGE_AMD64", filepath.Join(c.MockArtifactDir, "lima", "ubuntu-24.04-server-cloudimg-amd64.img"))
	c.PlaywrightDockerImage = env("OPD_PLAYWRIGHT_DOCKER_IMAGE", "mcr.microsoft.com/playwright:v1.57.0-noble")
	c.PlaywrightHostPort = env("OPD_PLAYWRIGHT_HOST_PORT", "8443")
	// 18184 publishes a network-policy workload in a non-global space, so the
	// suite can check that externally DNATed traffic still reaches it.
	c.PlaywrightSecondaryPorts = env("OPD_PLAYWRIGHT_SECONDARY_PORTS", "18181 18182 18184")
	// Ports whose DNAT rules carry a source allow list. Tunneled via the
	// primary VM so probes reach the secondary over the Lima network and
	// traverse its prerouting chain with the primary's source address; the
	// direct secondary tunnel dials locally and would bypass the filter
	// through the unconditional output-chain rule.
	c.PlaywrightFilteredPorts = env("OPD_PLAYWRIGHT_FILTERED_PORTS", "18183")
	c.PlaywrightTLSIngressPort = env("OPD_PLAYWRIGHT_TLS_INGRESS_PORT", "18443")
	// Not 18080: the ipv4 egress listener binds that port inside worker-2 and
	// Lima auto-forwards guest listeners to the same host port.
	c.PlaywrightHTTPIngressPort = env("OPD_PLAYWRIGHT_HTTP_INGRESS_PORT", "18980")
	c.PlaywrightBaseURLSet = os.Getenv("OPD_PLAYWRIGHT_BASE_URL") != ""
	c.PlaywrightBaseURL = env("OPD_PLAYWRIGHT_BASE_URL", c.WebBaseURL)
	c.OpenDeployGitHubToken = os.Getenv("OPENDEPLOY_GITHUB_TOKEN")

	if c.repoMirrorEnabled() && !wordListContains(c.RepoMirrorReleases, c.UpgradeVersion) {
		c.RepoMirrorReleases = strings.TrimSpace(c.RepoMirrorReleases + " " + c.UpgradeVersion)
	}
	return c, nil
}

func latestReleaseTag(repo, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("creating latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opendeploy-e2e-harness")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release for %s: %w", repo, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching latest release for %s: GitHub returned %s", repo, res.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding latest release for %s: %w", repo, err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", fmt.Errorf("latest release for %s has no tag name", repo)
	}
	return strings.TrimSpace(release.TagName), nil
}

func findScriptDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "testing-vms" {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, "testing-vms", "e2e")); err == nil {
			return filepath.Join(dir, "testing-vms"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("could not locate testing-vms directory from %s", wd)
}

func (c *config) run() error {
	totalStart := time.Now()
	if err := c.requireHostTools(); err != nil {
		return err
	}
	for _, dir := range []string{c.StateDir, c.ResultsDir, c.ReportDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := c.initRunLog(); err != nil {
		return err
	}
	defer c.closeRunLog()
	defer c.writeTimings()
	if envBool("RESET", true) {
		if err := c.step("reset", "deleting cluster VMs and old run state", func() error {
			c.deleteClusterVMs()
			c.resetClusterState()
			if err := removeDirContentsExcept(c.ResultsDir, "run.log"); err != nil {
				return err
			}
			_ = os.RemoveAll(c.ReportDir)
			if err := os.MkdirAll(c.ReportDir, 0o755); err != nil {
				return err
			}
			matches, _ := filepath.Glob(filepath.Join(c.ScriptDir, "bootstrap-*-chromium"))
			for _, m := range matches {
				_ = os.RemoveAll(m)
			}
			_ = os.Remove(filepath.Join(c.ScriptDir, ".last-run.json"))
			return nil
		}); err != nil {
			return err
		}
	}
	steps := []struct {
		name        string
		description string
		fn          func() error
	}{
		{"build", "preparing OpenDeploy test binaries", c.buildSelfOpenDeploy},
		{"certificates", "creating or reusing test CA and server certificate", c.ensureTestCerts},
		{"repo mirror", "verifying mock repository services", c.verifyRepoMirrorForRun},
		{"vms", "starting primary and secondary Lima VMs", c.startAllVMs},
		{"hosts", "syncing VM /etc/hosts entries", c.syncHostsAll},
		{"ca", "installing test CA into cluster VMs", c.installTestCAAll},
		{"cluster", "installing primary and secondary services", c.installCluster},
		{"IPv4 egress listener", "starting receiver on the second worker", c.startIPv4EgressListener},
	}
	for _, step := range steps {
		if err := c.step(step.name, step.description, step.fn); err != nil {
			return err
		}
	}
	if err := c.runPlaywrightFlows(); err != nil {
		logf("VM e2e run failed; results copied to %s", c.ResultsDir)
		return err
	}
	// After the flows: these assertions read kernel state directly, and the
	// last two of them deliberately break it.
	if err := c.step("network policy kernel checks", "anti-spoofing, IPv4 close, drop counters, netaudit", c.networkPolicyKernelChecks); err != nil {
		return err
	}
	if c.BackupRestore {
		if err := c.step("backup restore", "restoring primary from replicated backup", c.backupRestore); err != nil {
			return err
		}
	}
	c.recordTiming("total", "", totalStart, time.Now())
	c.Log.finishLine(0, "total", "total", totalStart, time.Now())
	logf("VM e2e run complete")
	return nil
}

func (c *config) timeStep(name string, fn func() error) error {
	return c.step(name, "", fn)
}

func (c *config) step(name, description string, fn func() error) error {
	return c.withStep(name, description, fn)
}

func (c *config) substep(name, description string, fn func() error) error {
	return c.withStep(name, description, fn)
}

func (c *config) withStep(name, description string, fn func() error) error {
	start := time.Now()
	level := len(c.StepStack)
	path := c.stepPath(name)
	if c.Log != nil {
		c.Log.startLine(level, path, name, description)
	}
	entry := c.beginTiming(name, description, start)
	err := fn()
	finish := time.Now()
	c.finishTiming(entry, start, finish)
	if err != nil {
		if c.Log != nil {
			c.Log.failedLine(level, path, name, start, finish)
		}
		return err
	}
	if c.Log != nil {
		c.Log.finishLine(level, path, name, start, finish)
	}
	return nil
}

func (c *config) stepPath(name string) string {
	parts := make([]string, 0, len(c.StepStack)+1)
	for _, entry := range c.StepStack {
		parts = append(parts, stepKey(entry.Name))
	}
	parts = append(parts, stepKey(name))
	return strings.Join(parts, ".")
}

func (c *config) recordTiming(name, description string, start, finish time.Time) {
	entry := &timingEntry{
		Name:        name,
		Description: description,
		Seconds:     finish.Sub(start).Seconds(),
		Started:     start.Format(time.RFC3339),
		Finished:    finish.Format(time.RFC3339),
	}
	if len(c.StepStack) == 0 {
		c.Timings = append(c.Timings, entry)
		return
	}
	parent := c.StepStack[len(c.StepStack)-1]
	parent.Children = append(parent.Children, entry)
}

func (c *config) initRunLog() error {
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.ResultsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(c.StateDir, "run.log")
	finalPath := filepath.Join(c.ResultsDir, "run.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	c.Log = &runLogger{file: file, path: path, finalPath: finalPath}
	activeLogger = c.Log
	return nil
}

func (c *config) closeRunLog() {
	if c.Log != nil {
		_ = c.Log.file.Close()
		_ = copyFile(c.Log.path, c.Log.finalPath)
	}
	if activeLogger == c.Log {
		activeLogger = nil
	}
}

func (l *runLogger) startLine(level int, path, name, description string) {
	l.printStageFor(name, level)
	line := fmt.Sprintf("%sstep=%s starting", stepIndent(level), path)
	if description != "" {
		line += " - " + description
	}
	fmt.Println(line)
	l.detail("%s", strings.TrimLeft(line, " "))
}

func (l *runLogger) finishLine(level int, path, name string, start, finish time.Time) {
	l.printStageFor(name, level)
	line := fmt.Sprintf("%sstep=%s finished - took: %s", stepIndent(level), path, formatDuration(finish.Sub(start)))
	fmt.Println(line)
	l.detail("%s", strings.TrimLeft(line, " "))
}

func (l *runLogger) failedLine(level int, path, name string, start, finish time.Time) {
	l.printStageFor(name, level)
	line := fmt.Sprintf("%sstep=%s failed - took: %s", stepIndent(level), path, formatDuration(finish.Sub(start)))
	fmt.Println(line)
	l.detail("%s", strings.TrimLeft(line, " "))
}

func (l *runLogger) printStageFor(name string, level int) {
	if level != 0 {
		return
	}
	stage := stageForStep(name)
	if stage == l.currentStage {
		return
	}
	l.currentStage = stage
	fmt.Printf("stage=%s\n", stage)
	l.detail("stage=%s", stage)
}

func stageForStep(name string) string {
	switch name {
	case "flows":
		return "RunningTests"
	case "backup restore":
		return "BackupRestore"
	case "total":
		return "Summary"
	default:
		return "Preperation"
	}
}

func stepIndent(level int) string {
	return strings.Repeat("  ", level+1)
}

func stepKey(name string) string {
	fields := strings.Fields(strings.ToLower(name))
	return strings.Join(fields, "-")
}

func (l *runLogger) detail(format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.file, "%s ", time.Now().Format(time.RFC3339))
	fmt.Fprintf(l.file, format, args...)
	fmt.Fprintln(l.file)
}

type lockedLogWriter struct {
	logger *runLogger
}

func (w lockedLogWriter) Write(p []byte) (int, error) {
	if w.logger == nil || w.logger.file == nil {
		return len(p), nil
	}
	w.logger.mu.Lock()
	defer w.logger.mu.Unlock()
	return w.logger.file.Write(p)
}

func (l *runLogger) writer() io.Writer {
	return lockedLogWriter{logger: l}
}

func (l *runLogger) commandFailed(err error, tail string) {
	if l == nil {
		return
	}
	fmt.Printf("  command failed: %v\n", err)
	fmt.Printf("  full log: %s\n", l.path)
	if tail = strings.TrimSpace(tail); tail != "" {
		fmt.Println("  last command output:")
		for _, line := range strings.Split(tail, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
}

func formatDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

func (c *config) pushTiming(entry *timingEntry) {
	c.StepStack = append(c.StepStack, entry)
}

func (c *config) popTiming() {
	c.StepStack = c.StepStack[:len(c.StepStack)-1]
}

func (c *config) beginTiming(name, description string, start time.Time) *timingEntry {
	entry := &timingEntry{
		Name:        name,
		Description: description,
		Started:     start.Format(time.RFC3339),
	}
	if len(c.StepStack) == 0 {
		c.Timings = append(c.Timings, entry)
	} else {
		parent := c.StepStack[len(c.StepStack)-1]
		parent.Children = append(parent.Children, entry)
	}
	c.pushTiming(entry)
	return entry
}

func (c *config) finishTiming(entry *timingEntry, start, finish time.Time) {
	entry.Seconds = finish.Sub(start).Seconds()
	entry.Finished = finish.Format(time.RFC3339)
	c.popTiming()
}

func removeDirContentsExcept(dir, keepBase string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.Name() == keepBase {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if mkdirErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (c *config) writeTimings() {
	if len(c.Timings) == 0 {
		return
	}
	data, err := json.MarshalIndent(c.Timings, "", "  ")
	if err != nil {
		logf("Could not marshal timings: %v", err)
		return
	}
	path := filepath.Join(c.ResultsDir, "timings.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		logf("Could not write timings to %s: %v", path, err)
		return
	}
	logf("Timing summary:")
	for _, timing := range c.Timings {
		logf("  %s: %s", timing.Name, time.Duration(timing.Seconds*float64(time.Second)).Round(time.Second))
	}
}

func (c *config) resetClusterState() {
	paths := []string{c.E2EEnvFile}
	for _, name := range c.clusterVMNames() {
		paths = append(paths, filepath.Join(c.StateDir, "lima-"+name+".yaml"))
	}
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func (c *config) cleanup() error {
	if err := c.requireHostTools(); err != nil {
		return err
	}
	c.deleteClusterVMs()
	c.resetClusterState()
	logf("Deleted cluster VM harness instances; repo mirror and shared cert state were left intact")
	return nil
}

func (c *config) requireHostTools() error {
	for _, tool := range []string{"limactl", "curl", "openssl"} {
		if err := requireCmd(tool); err != nil {
			return err
		}
	}
	if c.RemoteMode != "mock" && c.RemoteMode != "real" {
		return errors.New("OPD_REMOTE must be mock or real")
	}
	if c.MockOpenDeploySource != "local" && c.MockOpenDeploySource != "real" {
		return errors.New("OPD_MOCK_OPENDEPLOY_SOURCE must be local or real")
	}
	if c.repoMirrorEnabled() {
		if err := requireCmd("go"); err != nil {
			return err
		}
	}
	if c.usesSelfBootstrap() {
		for _, tool := range []string{"go", "pnpm"} {
			if err := requireCmd(tool); err != nil {
				return err
			}
		}
	}
	if c.PrepareArtifacts {
		for _, tool := range []string{"git", "shasum"} {
			if err := requireCmd(tool); err != nil {
				return err
			}
		}
		if !cmdExists("skopeo") && !cmdExists("docker") {
			return errors.New("skopeo or docker is required to prepare mock OCI artifacts")
		}
	}
	return nil
}

func (c *config) repoMirrorEnabled() bool { return c.RemoteMode != "real" }

func (c *config) usesSelfBootstrap() bool {
	return c.RemoteMode == "mock" && c.MockOpenDeploySource == "local"
}

func (c *config) clusterVMNames() []string {
	return []string{c.PrimaryName, c.SecondaryName, c.Secondary2Name}
}

func (c *config) allVMNames() []string {
	return []string{c.PrimaryName, c.SecondaryName, c.Secondary2Name, c.RepoMirrorName}
}

func (c *config) syncTargetVMNames() []string {
	if c.repoMirrorEnabled() {
		return []string{c.PrimaryName, c.SecondaryName, c.Secondary2Name, c.RepoMirrorName}
	}
	return c.clusterVMNames()
}

func (c *config) mockArtifactReleaseFile(version, arch string) string {
	return filepath.Join(c.MockArtifactDir, "releases", filepath.FromSlash(c.ReleaseRepo), version, "opendeploy-linux-"+arch)
}

func (c *config) mockArtifactOCIArchive(image string) string {
	safe := strings.NewReplacer("/", "_", ":", "_").Replace(image)
	return filepath.Join(c.MockArtifactDir, "oci", safe+".tar")
}

func (c *config) ensureMockArtifacts() error {
	if !c.repoMirrorEnabled() {
		return nil
	}
	if c.PrepareArtifacts {
		logf("Refreshing mock artifact cache")
		if err := c.prepareMockArtifacts(); err != nil {
			return err
		}
	}
	missing := c.missingMockArtifacts()
	if len(missing) > 0 && !c.PrepareArtifacts {
		logf("Mock artifact cache is missing %d required file(s); preparing now", len(missing))
		if err := c.prepareMockArtifacts(); err != nil {
			return err
		}
		missing = c.missingMockArtifacts()
	}
	if len(missing) > 0 {
		for _, path := range missing {
			fmt.Fprintf(os.Stderr, "missing mock artifact after preparation: %s\n", path)
		}
		return errors.New("mock artifacts are incomplete after preparation")
	}
	return nil
}

func (c *config) missingMockArtifacts() []string {
	var missing []string
	if !isDir(c.MockArtifactDir) {
		missing = append(missing, c.MockArtifactDir)
	}
	for _, version := range words(c.RepoMirrorReleases) {
		if c.MockOpenDeploySource == "real" {
			for _, arch := range []string{"amd64", "arm64"} {
				path := c.mockArtifactReleaseFile(version, arch)
				if !fileNonEmpty(path) {
					missing = append(missing, path)
				}
			}
			path := filepath.Join(c.MockArtifactDir, "releases", filepath.FromSlash(c.ReleaseRepo), version, "sha256sums.txt")
			if !fileNonEmpty(path) {
				missing = append(missing, path)
			}
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		paths := []string{
			filepath.Join(c.MockArtifactDir, "releases", "containerd", "containerd", "v"+c.ContainerdVersion, fmt.Sprintf("containerd-%s-linux-%s.tar.gz", c.ContainerdVersion, arch)),
			filepath.Join(c.MockArtifactDir, "releases", "opencontainers", "runc", "v"+c.RuncVersion, "runc."+arch),
		}
		for _, path := range paths {
			if !fileNonEmpty(path) {
				missing = append(missing, path)
			}
		}
	}
	for _, spec := range words(c.RepoMirrorOCI) {
		src := strings.SplitN(spec, "=", 2)[0]
		archive := c.mockArtifactOCIArchive(src)
		if !fileNonEmpty(archive) {
			missing = append(missing, archive)
		}
	}
	for _, path := range []string{c.LimaArm64Image, c.LimaAmd64Image} {
		if !fileNonEmpty(path) {
			missing = append(missing, path)
		}
	}
	return missing
}

func (c *config) prepareMockArtifacts() error {
	for _, tool := range []string{"curl", "git", "shasum"} {
		if err := requireCmd(tool); err != nil {
			return err
		}
	}
	if !cmdExists("skopeo") && !cmdExists("docker") {
		return errors.New("skopeo or docker is required to prepare mock OCI artifacts")
	}
	if err := os.MkdirAll(c.MockArtifactDir, 0o755); err != nil {
		return err
	}
	if err := c.mirrorGitRepo("jptrs93/opsagent", "https://github.com/jptrs93/opsagent.git"); err != nil {
		return err
	}
	if err := c.mirrorGitRepo("jptrs93/jnotes", "https://github.com/jptrs93/jnotes.git"); err != nil {
		return err
	}
	if c.MockOpenDeploySource == "real" {
		for _, version := range words(c.RepoMirrorReleases) {
			if err := c.prepareRelease(version); err != nil {
				return err
			}
		}
	}
	latestDir := filepath.Join(c.MockArtifactDir, "releases", filepath.FromSlash(c.ReleaseRepo))
	if err := os.MkdirAll(latestDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(latestDir, "latest"), []byte(c.RepoMirrorLatest+"\n"), 0o644); err != nil {
		return err
	}
	if err := c.prepareRuntime(); err != nil {
		return err
	}
	if err := c.prepareOCI(); err != nil {
		return err
	}
	if err := downloadFile(c.LimaArm64URL, c.LimaArm64Image); err != nil {
		return err
	}
	if err := downloadFile(c.LimaAmd64URL, c.LimaAmd64Image); err != nil {
		return err
	}
	logf("Mock artifacts ready in %s", c.MockArtifactDir)
	return nil
}

func (c *config) mirrorGitRepo(ownerRepo, url string) error {
	dst := filepath.Join(c.MockArtifactDir, "git", filepath.FromSlash(ownerRepo)+".git")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if isDir(dst) {
		logf("Refreshing git mirror %s", ownerRepo)
		if err := runDir(dst, "git", "remote", "update", "--prune"); err != nil {
			return err
		}
	} else {
		logf("Cloning git mirror %s", ownerRepo)
		if err := run("git", "clone", "--mirror", url, dst); err != nil {
			return err
		}
	}
	return runDir(dst, "git", "update-server-info")
}

func (c *config) prepareRelease(version string) error {
	releaseDir := filepath.Join(c.MockArtifactDir, "releases", filepath.FromSlash(c.ReleaseRepo), version)
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		bin := filepath.Join(releaseDir, "opendeploy-linux-"+arch)
		url := fmt.Sprintf("https://github.com/%s/releases/download/%s/opendeploy-linux-%s", c.ReleaseRepo, version, arch)
		if err := downloadFile(url, bin); err != nil {
			return err
		}
		_ = os.Chmod(bin, 0o755)
	}
	return downloadFile(fmt.Sprintf("https://github.com/%s/releases/download/%s/sha256sums.txt", c.ReleaseRepo, version), filepath.Join(releaseDir, "sha256sums.txt"))
}

func (c *config) prepareRuntime() error {
	for _, arch := range []string{"amd64", "arm64"} {
		if err := downloadFile(
			fmt.Sprintf("https://github.com/containerd/containerd/releases/download/v%s/containerd-%s-linux-%s.tar.gz", c.ContainerdVersion, c.ContainerdVersion, arch),
			filepath.Join(c.MockArtifactDir, "releases", "containerd", "containerd", "v"+c.ContainerdVersion, fmt.Sprintf("containerd-%s-linux-%s.tar.gz", c.ContainerdVersion, arch)),
		); err != nil {
			return err
		}
		if err := downloadFile(
			fmt.Sprintf("https://github.com/opencontainers/runc/releases/download/v%s/runc.%s", c.RuncVersion, arch),
			filepath.Join(c.MockArtifactDir, "releases", "opencontainers", "runc", "v"+c.RuncVersion, "runc."+arch),
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) prepareOCI() error {
	for _, spec := range words(c.RepoMirrorOCI) {
		src := strings.SplitN(spec, "=", 2)[0]
		archive := c.mockArtifactOCIArchive(src)
		if fileNonEmpty(archive) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
			return err
		}
		if cmdExists("skopeo") {
			logf("Saving OCI image %s with skopeo", src)
			if err := run("skopeo", "copy", "--all", "docker://"+src, "oci-archive:"+archive+":"+src); err != nil {
				return err
			}
		} else {
			logf("Saving OCI image %s with containerized skopeo", src)
			if err := run("docker", "run", "--rm", "-v", filepath.Dir(archive)+":/out", "quay.io/skopeo/stable:v1.20.0",
				"copy", "--all", "docker://"+src, "oci-archive:/out/"+filepath.Base(archive)+":"+src); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *config) vmExists(name string) bool {
	out, err := output("limactl", "list", "--json")
	if err != nil || strings.TrimSpace(out) == "" {
		return false
	}
	var arr []map[string]any
	_ = json.Unmarshal([]byte(out), &arr)
	if len(arr) == 0 {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var item map[string]any
			if json.Unmarshal([]byte(line), &item) == nil {
				arr = append(arr, item)
			}
		}
	}
	for _, item := range arr {
		if item["name"] == name {
			return true
		}
	}
	return false
}

func (c *config) writeLimaYAML(name, role, cpus, memory, disk, yamlPath string) error {
	packages := map[string]string{
		"node":        "sudo ca-certificates curl git openssl python3 nix-bin nix-setup-systemd tcpdump conntrack nftables",
		"repo-mirror": "sudo ca-certificates curl git openssl tar gzip docker-registry skopeo",
	}[role]
	if packages == "" {
		return fmt.Errorf("unknown Lima VM role: %s", role)
	}
	if role == "node" && c.Goarch == "arm64" {
		packages += " qemu-user-static binfmt-support"
	}
	armURL, amdURL := c.LimaArm64URL, c.LimaAmd64URL
	if c.repoMirrorEnabled() {
		armURL, amdURL = c.LimaArm64Image, c.LimaAmd64Image
	}
	text := fmt.Sprintf(`vmType: %q
os: "Linux"
arch: %q
images:
- location: %q
  arch: "aarch64"
- location: %q
  arch: "x86_64"
cpus: %s
memory: %q
disk: %q
mounts: []
containerd:
  system: false
  user: false
networks:
- lima: %q
provision:
- mode: system
  script: |
    #!/usr/bin/env bash
    set -euxo pipefail
    export DEBIAN_FRONTEND=noninteractive
    mkdir -p /var/lib/opendeploy-vm-harness
    if [[ ! -f /var/lib/opendeploy-vm-harness/provisioned-%s ]]; then
      provision_start=$(date +%%s)
      apt_update_start=$(date +%%s)
      apt-get update
      apt_update_end=$(date +%%s)
      echo "opendeploy-vm-harness apt-get update (%s) took: $((apt_update_end - apt_update_start))s"
      apt_install_start=$(date +%%s)
      apt-get install -y --no-install-recommends %s
      apt_install_end=$(date +%%s)
      echo "opendeploy-vm-harness package install (%s) took: $((apt_install_end - apt_install_start))s"
      if [[ %q == "node" ]]; then
        mkdir -p /etc/nix
        printf 'experimental-features = nix-command flakes\nsandbox = true\nallowed-users = *\ntrusted-users = root opendeploy\n' > /etc/nix/nix.conf
        systemctl enable --now nix-daemon.service || true
      fi
      touch /var/lib/opendeploy-vm-harness/provisioned-%s
      provision_end=$(date +%%s)
      echo "opendeploy-vm-harness provisioning (%s) took: $((provision_end - provision_start))s"
    fi
`, c.VMType, c.LimaArch, armURL, amdURL, cpus, memory, disk, c.NetworkName, role, role, packages, packages, role, role, role)
	if err := os.MkdirAll(filepath.Dir(yamlPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(yamlPath, []byte(text), 0o644)
}

func (c *config) startVM(name, role string) error {
	cpus, memory, disk := c.NodeCPUs, c.NodeMemory, c.NodeDisk
	switch role {
	case "node":
	case "repo-mirror":
		cpus, memory, disk = c.RepoMirrorCPUs, c.RepoMirrorMemory, c.RepoMirrorDisk
	default:
		return fmt.Errorf("unknown VM role: %s", role)
	}
	if c.vmExists(name) {
		logf("Starting existing Lima VM %s", name)
		return run("limactl", "start", "--tty=false", "--timeout=30m", name)
	}
	yamlPath := filepath.Join(c.StateDir, "lima-"+name+".yaml")
	if err := c.writeLimaYAML(name, role, cpus, memory, disk, yamlPath); err != nil {
		return err
	}
	logf("Creating Lima VM %s (%s)", name, role)
	return run("limactl", "start", "--tty=false", "--timeout=30m", "--name="+name, yamlPath)
}

func (c *config) startAllVMs() error {
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	vms := make([]struct{ name, role string }, 0, len(c.clusterVMNames()))
	for _, name := range c.clusterVMNames() {
		vms = append(vms, struct{ name, role string }{name, "node"})
	}
	type vmStartResult struct {
		name   string
		start  time.Time
		finish time.Time
		err    error
	}
	results := make(chan vmStartResult, len(vms))
	for _, vm := range vms {
		go func() {
			start := time.Now()
			if c.Log != nil {
				c.Log.startLine(1, "vms."+stepKey(vm.name), vm.name, "start Lima VM")
			}
			err := c.startVM(vm.name, vm.role)
			finish := time.Now()
			if c.Log != nil {
				if err != nil {
					c.Log.failedLine(1, "vms."+stepKey(vm.name), vm.name, start, finish)
				} else {
					c.Log.finishLine(1, "vms."+stepKey(vm.name), vm.name, start, finish)
				}
			}
			results <- vmStartResult{name: vm.name, start: start, finish: finish, err: err}
		}()
	}
	resultByName := map[string]vmStartResult{}
	var firstErr error
	for range vms {
		result := <-results
		resultByName[result.name] = result
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
	}
	for _, vm := range vms {
		result := resultByName[vm.name]
		c.recordTiming(result.name, "start Lima VM", result.start, result.finish)
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func (c *config) deleteVM(name string) {
	logf("Deleting Lima VM %s", name)
	_ = quietRun("limactl", "delete", "--force", name)
}

func (c *config) deleteClusterVMs() {
	for _, name := range append(c.clusterVMNames(), "opendeploy-install-primary", "opendeploy-install-secondary", "opendeploy-e2e-runner") {
		c.deleteVM(name)
	}
}

func (c *config) deleteAllVMs() {
	for _, name := range append(c.allVMNames(), "opendeploy-install-primary", "opendeploy-install-secondary", "opendeploy-e2e-runner") {
		c.deleteVM(name)
	}
}

func (c *config) syncHostsAll() error {
	names := c.syncTargetVMNames()
	for _, vm := range c.clusterVMNames() {
		if err := c.substep(vm, "sync /etc/hosts", func() error { return c.syncHostsForVM(vm, names) }); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) syncHostsForVM(vm string, names []string) error {
	logf("Syncing /etc/hosts in %s", vm)
	script := `set -euo pipefail
names=("$@")
tmp=$(mktemp)
host_line() {
  local target=$1
  shift
  local ip
  ip=$(getent hosts "lima-${target}.internal" 2>/dev/null | awk '{print $1; exit}' || true)
  if [[ -n "$ip" ]]; then
    printf '%s' "$ip"
    local alias
    for alias in "$@"; do
      [[ -n "$alias" ]] && printf ' %s' "$alias"
    done
    printf '\n'
  fi
}
{
  echo "# opendeploy-vm-hosts-begin"
  for name in "${names[@]}"; do
    host_line "$name" "$name" "lima-${name}.internal"
  done
  host_line "$OPD_PRIMARY_NAME" "$OPD_WEB_HOST"
  if [[ "$OPD_REPO_MIRROR_ENABLED" == "true" && "$OPD_THIS_VM" != "$OPD_REPO_MIRROR_NAME" ]]; then
    host_line "$OPD_REPO_MIRROR_NAME" github.com api.github.com cache.nixos.org "$OPD_REPO_REGISTRY_HOST"
  fi
  echo "# opendeploy-vm-hosts-end"
} > "$tmp.block"
awk '
  $0 == "# opendeploy-vm-hosts-begin" {skip=1; next}
  $0 == "# opendeploy-vm-hosts-end" {skip=0; next}
  skip != 1 {print}
' /etc/hosts > "$tmp"
cat "$tmp.block" >> "$tmp"
install -m 0644 "$tmp" /etc/hosts
rm -f "$tmp" "$tmp.block"
`
	args := []string{"shell", "--tty=false", vm, "sudo", "env",
		"OPD_WEB_HOST=" + c.WebHost,
		"OPD_PRIMARY_NAME=" + c.PrimaryName,
		"OPD_THIS_VM=" + vm,
		"OPD_REPO_MIRROR_ENABLED=" + fmt.Sprint(c.repoMirrorEnabled()),
		"OPD_REPO_MIRROR_NAME=" + c.RepoMirrorName,
		"OPD_REPO_REGISTRY_HOST=" + c.RepoRegistryHost,
		"bash", "-s", "--"}
	args = append(args, names...)
	return runStdin(script, append([]string{"limactl"}, args...)...)
}

func (c *config) waitForSystemd(name string) error {
	var state string
	for i := 0; i < 60; i++ {
		state, _ = c.vmOutput(name, "systemctl", "is-system-running")
		state = strings.TrimSpace(state)
		if state == "running" || state == "degraded" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	_ = c.vmRun(name, "systemctl", "--no-pager", "status")
	return fmt.Errorf("systemd did not become ready in %s (state: %s)", name, state)
}

func (c *config) waitForService(name, service string) error {
	var state string
	for i := 0; i < 60; i++ {
		state, _ = c.vmOutput(name, "systemctl", "is-active", service)
		state = strings.TrimSpace(state)
		if state == "active" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	_ = c.vmRun(name, "systemctl", "status", service, "--no-pager")
	return fmt.Errorf("%s did not become active in %s (state: %s)", service, name, state)
}

func (c *config) waitForHealthz(name string) error {
	for i := 0; i < 90; i++ {
		if c.vmQuietRun(name, "curl", "-fsS", c.WebBaseURL+"/v1/healthz") == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	_ = c.vmRun(name, "systemctl", "status", "opendeploy", "--no-pager")
	return fmt.Errorf("OpenDeploy health check failed in %s", name)
}

func (c *config) waitForPrimaryNetproxy(name string) error {
	ctr := "/var/lib/opendeploy/runtime/bin/ctr"
	args := []string{"sudo", ctr, "--address", "/run/opendeploy/containerd.sock", "--namespace", "opendeploy"}
	for i := 0; i < 180; i++ {
		images, imageErr := c.vmOutput(name, append(args, "images", "check", "--quiet")...)
		containers, containerErr := c.vmOutput(name, append(args, "containers", "list")...)
		tasks, taskErr := c.vmOutput(name, append(args, "tasks", "list")...)
		netproxyID := ""
		for _, line := range strings.Split(containers, "\n") {
			if fields := strings.Fields(line); len(fields) > 1 && strings.Contains(line, "opendeploy-net:") {
				netproxyID = fields[0]
				break
			}
		}
		netproxyRunning := false
		for _, line := range strings.Split(tasks, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 2 && fields[0] == netproxyID && fields[len(fields)-1] == "RUNNING" {
				netproxyRunning = true
				break
			}
		}
		if imageErr == nil && containerErr == nil && taskErr == nil && strings.Contains(images, "opendeploy-net:") && netproxyRunning {
			return nil
		}
		time.Sleep(time.Second)
	}
	_ = c.vmRun(name, append(args, "images", "list")...)
	_ = c.vmRun(name, append(args, "containers", "list")...)
	_ = c.vmRun(name, append(args, "tasks", "list")...)
	return fmt.Errorf("opendeploy-net did not recover on restored primary %s", name)
}

func (c *config) verifyRepoMirrorForRun() error {
	if !c.repoMirrorEnabled() {
		return nil
	}
	if err := c.repoMirrorHealthCheck(); err != nil {
		return fmt.Errorf("repo mirror is required for OPD_REMOTE=mock but is not healthy: %w; run `go run ./test-orchestrator repo-mirror-up` from testing-vms", err)
	}
	if c.MockOpenDeploySource == "local" {
		if err := c.publishLocalRepoToRepoMirror(); err != nil {
			return err
		}
		if err := c.publishMockReleasesToRepoMirror(); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) repoMirrorHealthCheck() error {
	if !c.vmExists(c.RepoMirrorName) {
		return fmt.Errorf("Lima VM %s does not exist", c.RepoMirrorName)
	}
	if err := c.vmQuietRun(c.RepoMirrorName, "curl", "-kfsS", "https://localhost/healthz"); err != nil {
		return fmt.Errorf("HTTPS mirror health check failed")
	}
	if err := c.vmQuietRun(c.RepoMirrorName, "curl", "-kfsS", "https://localhost:"+c.RepoRegistryPort+"/v2/"); err != nil {
		return fmt.Errorf("OCI registry health check failed")
	}
	if err := c.vmQuietRun(c.RepoMirrorName, "curl", "-kfsS", "--resolve", "cache.nixos.org:443:127.0.0.1", "https://cache.nixos.org/nix-cache-info"); err != nil {
		return fmt.Errorf("Nix cache health check failed")
	}
	return nil
}

func (c *config) repoMirrorUp() error {
	if !c.repoMirrorEnabled() {
		return errors.New("repo mirror is disabled when OPD_REMOTE=real")
	}
	if err := c.requireHostTools(); err != nil {
		return err
	}
	for _, dir := range []string{c.StateDir, c.ResultsDir, c.ReportDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := c.initRunLog(); err != nil {
		return err
	}
	defer c.closeRunLog()
	defer c.writeTimings()
	if err := c.step("artifacts", "checking local mock artifact cache", c.ensureMockArtifacts); err != nil {
		return err
	}
	steps := []struct {
		name        string
		description string
		fn          func() error
	}{
		{"build", "preparing OpenDeploy test binaries", c.buildSelfOpenDeploy},
		{"certificates", "creating or reusing test CA and server certificate", c.ensureTestCerts},
		{"repo mirror vm", "starting Lima VM", func() error { return c.startVM(c.RepoMirrorName, "repo-mirror") }},
		{"hosts", "syncing repo mirror /etc/hosts entries", func() error { return c.syncHostsForVM(c.RepoMirrorName, c.syncTargetVMNames()) }},
		{"ca", "installing test CA into repo mirror", func() error { return c.installTestCA(c.RepoMirrorName) }},
		{"configure", "copying artifacts and starting mirror services", c.setupRepoMirrorVM},
		{"health", "checking repo mirror endpoints", c.repoMirrorHealthCheck},
	}
	for _, step := range steps {
		if err := c.step(step.name, step.description, step.fn); err != nil {
			return err
		}
	}
	logf("Repo mirror is up")
	return nil
}

func (c *config) repoMirrorDown() error {
	if err := c.requireHostTools(); err != nil {
		return err
	}
	c.deleteVM(c.RepoMirrorName)
	_ = os.Remove(filepath.Join(c.StateDir, "lima-"+c.RepoMirrorName+".yaml"))
	logf("Repo mirror VM deleted")
	return nil
}

func (c *config) repoMirrorStatus() error {
	if err := c.requireHostTools(); err != nil {
		return err
	}
	if err := c.repoMirrorHealthCheck(); err != nil {
		return err
	}
	logf("Repo mirror is healthy")
	return nil
}

func (c *config) ensureTestCerts() error {
	if err := os.MkdirAll(c.CertDir, 0o755); err != nil {
		return err
	}
	caReady := fileNonEmpty(c.CACert) && fileNonEmpty(c.CAKey) && certValidFor(c.CACert, 365*24*time.Hour)
	if !caReady {
		logf("Generating persistent VM test CA")
		for _, path := range []string{c.CACert, c.CAKey, filepath.Join(c.CertDir, "ca.srl")} {
			_ = os.Remove(path)
		}
		if err := quietRun("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "3650", "-subj", "/CN=OpenDeploy E2E Test CA", "-keyout", c.CAKey, "-out", c.CACert); err != nil {
			return err
		}
	}

	serverReady := fileNonEmpty(c.ServerCert) && fileNonEmpty(c.ServerKey) && fileNonEmpty(c.ServerBundle) && certValidFor(c.ServerCert, 24*time.Hour) && certSignedBy(c.ServerCert, c.CACert)
	for _, host := range []string{c.WebHost, "github.com", "api.github.com", "cache.nixos.org", c.RepoMirrorName, c.RepoRegistryHost, "localhost"} {
		serverReady = serverReady && certContainsDNS(c.ServerCert, host)
	}
	if !serverReady {
		logf("Issuing VM server certificate from persistent test CA")
		for _, path := range []string{c.ServerCert, c.ServerKey, c.ServerBundle, filepath.Join(c.CertDir, "server.csr"), filepath.Join(c.CertDir, "server.cnf")} {
			_ = os.Remove(path)
		}
		cnf := fmt.Sprintf(`[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = %s

[v3_req]
keyUsage = keyEncipherment, dataEncipherment, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = %s
DNS.2 = github.com
DNS.3 = api.github.com
DNS.4 = cache.nixos.org
DNS.5 = %s
DNS.6 = %s
DNS.7 = localhost
IP.1 = 127.0.0.1
`, c.WebHost, c.WebHost, c.RepoMirrorName, c.RepoRegistryHost)
		cnfPath := filepath.Join(c.CertDir, "server.cnf")
		if err := os.WriteFile(cnfPath, []byte(cnf), 0o644); err != nil {
			return err
		}
		if err := quietRun("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", c.ServerKey, "-out", filepath.Join(c.CertDir, "server.csr"), "-config", cnfPath); err != nil {
			return err
		}
		if err := quietRun("openssl", "x509", "-req", "-days", "30", "-in", filepath.Join(c.CertDir, "server.csr"), "-CA", c.CACert, "-CAkey", c.CAKey, "-CAcreateserial", "-out", c.ServerCert, "-extensions", "v3_req", "-extfile", cnfPath); err != nil {
			return err
		}
		if err := writeCertBundle(c.ServerCert, c.ServerKey, c.ServerBundle); err != nil {
			return err
		}
	}
	for _, host := range tlsIngressHosts {
		if err := c.ensureTLSIngressCert(host); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) tlsIngressCertPaths(host string) (cert, key, bundle, csr, cnf string) {
	name := "ingress-" + strings.ReplaceAll(host, ".", "-")
	return filepath.Join(c.CertDir, name+".crt"), filepath.Join(c.CertDir, name+".key"), filepath.Join(c.CertDir, name+"-bundle.pem"), filepath.Join(c.CertDir, name+".csr"), filepath.Join(c.CertDir, name+".cnf")
}

func (c *config) ensureTLSIngressCert(host string) error {
	cert, key, bundle, csr, cnfPath := c.tlsIngressCertPaths(host)
	ready := fileNonEmpty(cert) && fileNonEmpty(key) && fileNonEmpty(bundle) && certValidFor(cert, 24*time.Hour) && certSignedBy(cert, c.CACert) && certContainsDNS(cert, host)
	if ready {
		return nil
	}
	logf("Issuing TLS ingress certificate for %s", host)
	for _, path := range []string{cert, key, bundle, csr, cnfPath} {
		_ = os.Remove(path)
	}
	cnf := fmt.Sprintf(`[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = %s

[v3_req]
keyUsage = keyEncipherment, dataEncipherment, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = %s
`, host, host)
	if err := os.WriteFile(cnfPath, []byte(cnf), 0o644); err != nil {
		return err
	}
	if err := quietRun("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", csr, "-config", cnfPath); err != nil {
		return err
	}
	if err := quietRun("openssl", "x509", "-req", "-days", "30", "-in", csr, "-CA", c.CACert, "-CAkey", c.CAKey, "-CAcreateserial", "-out", cert, "-extensions", "v3_req", "-extfile", cnfPath); err != nil {
		return err
	}
	return writeCertBundle(cert, key, bundle)
}

func writeCertBundle(certPath, keyPath, bundlePath string) error {
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	return os.WriteFile(bundlePath, append(cert, key...), 0o600)
}

func (c *config) tlsIngressPlaywrightEnv() (map[string]string, error) {
	ca, err := os.ReadFile(c.CACert)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"OPD_TLS_INGRESS_CA_B64": base64.StdEncoding.EncodeToString(ca)}
	for _, host := range tlsIngressHosts {
		_, _, bundle, _, _ := c.tlsIngressCertPaths(host)
		contents, err := os.ReadFile(bundle)
		if err != nil {
			return nil, err
		}
		id := strings.ToUpper(strings.Split(host, ".")[0])
		env["OPD_TLS_INGRESS_CERT_"+id+"_B64"] = base64.StdEncoding.EncodeToString(contents)
	}
	return env, nil
}

func certContainsDNS(path, host string) bool {
	cmd := exec.Command("openssl", "x509", "-in", path, "-noout", "-text")
	out, err := cmd.Output()
	return err == nil && strings.Contains(string(out), "DNS:"+host)
}

func certValidFor(path string, validity time.Duration) bool {
	seconds := fmt.Sprintf("%d", int64(validity/time.Second))
	return quietRun("openssl", "x509", "-in", path, "-noout", "-checkend", seconds) == nil
}

func certSignedBy(path, caPath string) bool {
	return quietRun("openssl", "verify", "-CAfile", caPath, path) == nil
}

func (c *config) installTestCA(name string) error {
	if err := run("limactl", "copy", c.CACert, name+":/tmp/opendeploy-e2e-ca.crt"); err != nil {
		return err
	}
	if err := c.vmRun(name, "sudo", "install", "-m", "0644", "/tmp/opendeploy-e2e-ca.crt", "/usr/local/share/ca-certificates/opendeploy-e2e-ca.crt"); err != nil {
		return err
	}
	return c.vmRun(name, "sudo", "update-ca-certificates")
}

func (c *config) installTestCAAll() error {
	for _, vm := range c.clusterVMNames() {
		if err := c.substep(vm, "install CA certificate", func() error { return c.installTestCA(vm) }); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) buildSelfOpenDeploy() error {
	if !c.repoMirrorEnabled() || c.MockOpenDeploySource != "local" {
		return nil
	}
	logf("Building frontend assets for VM test binaries")
	if err := c.substep("frontend", "pnpm run build", func() error { return runDir(filepath.Join(c.RepoRoot, "frontend"), "pnpm", "run", "build") }); err != nil {
		return err
	}
	if err := c.substep("backend "+c.SelfVersion, "linux/"+c.Goarch, func() error { return c.buildOpenDeployLinux(c.SelfVersion, c.SelfBin) }); err != nil {
		return err
	}
	if c.repoMirrorEnabled() && c.MockOpenDeploySource == "local" {
		for _, version := range words(c.RepoMirrorReleases) {
			if err := c.substep("backend "+version, "linux/"+c.Goarch, func() error {
				return c.buildOpenDeployLinux(version, filepath.Join(c.MockReleaseBinDir, version, "opendeploy-linux-"+c.Goarch))
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *config) buildOpenDeployLinux(version, out string) error {
	logf("Building opendeploy %s for linux/%s", version, c.Goarch)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X github.com/jptrs93/opsagent/backend/util/version.Version="+version, "-o", out, ".")
	cmd.Dir = filepath.Join(c.RepoRoot, "backend")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+c.Goarch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *config) copySelfOpenDeploy(name string) error {
	if !fileNonEmpty(c.SelfBin) {
		return fmt.Errorf("local opendeploy binary not found: %s", c.SelfBin)
	}
	logf("Copying local opendeploy %s into %s", c.SelfVersion, name)
	if err := run("limactl", "copy", c.SelfBin, name+":/tmp/opendeploy"); err != nil {
		return err
	}
	return c.vmRun(name, "sudo", "install", "-m", "0755", "/tmp/opendeploy", "/usr/local/bin/opendeploy")
}

func (c *config) downloadOpenDeploy(name, version string) error {
	logf("Downloading opendeploy %s for linux/%s in %s", version, c.Goarch, name)
	script := `set -euo pipefail
curl -fsSL "https://github.com/${OPD_REPO}/releases/download/${OPD_VERSION}/opendeploy-linux-${OPD_ARCH}" -o /tmp/opendeploy
sudo install -m 0755 /tmp/opendeploy /usr/local/bin/opendeploy
`
	return c.vmEnvRun(name, map[string]string{"OPD_REPO": c.ReleaseRepo, "OPD_VERSION": version, "OPD_ARCH": c.Goarch}, script)
}

func (c *config) setupRepoMirrorVM() error {
	if !c.repoMirrorEnabled() {
		return nil
	}
	logf("Configuring repo mirror VM %s", c.RepoMirrorName)
	if err := c.substep("build mirror binary", "linux/"+c.Goarch, c.buildRepoMirror); err != nil {
		return err
	}
	if err := c.substep("install mirror binary", "copy binary and TLS files", func() error {
		for _, cp := range []struct{ src, dst string }{
			{c.RepoMirrorBin, c.RepoMirrorName + ":/tmp/repo-mirror"},
			{c.ServerCert, c.RepoMirrorName + ":/tmp/opendeploy-e2e-server.crt"},
			{c.ServerKey, c.RepoMirrorName + ":/tmp/opendeploy-e2e-server.key"},
		} {
			if err := run("limactl", "copy", cp.src, cp.dst); err != nil {
				return err
			}
		}
		for _, args := range [][]string{
			{"sudo", "install", "-m", "0755", "/tmp/repo-mirror", "/usr/local/bin/repo-mirror"},
			{"sudo", "install", "-d", "-m", "0755", "/etc/opendeploy-repo-mirror", "/srv/registry"},
			{"sudo", "install", "-m", "0644", "/tmp/opendeploy-e2e-server.crt", "/etc/opendeploy-repo-mirror/server.crt"},
			{"sudo", "install", "-m", "0600", "/tmp/opendeploy-e2e-server.key", "/etc/opendeploy-repo-mirror/server.key"},
		} {
			if err := c.vmRun(c.RepoMirrorName, args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := c.substep("registry", "configure local OCI registry", c.configureRepoMirrorRegistry); err != nil {
		return err
	}
	if err := c.substep("copy artifacts", "git, releases, and OCI archives", c.copyMockArtifactsToRepoMirror); err != nil {
		return err
	}
	if err := c.substep("prepare mirror", "index releases and import OCI images", func() error {
		releases := c.RepoMirrorReleases
		if c.MockOpenDeploySource == "local" {
			releases = ""
		}
		return c.vmRun(c.RepoMirrorName, append([]string{"sudo", "env", "OPD_REPO_MIRROR_REFRESH=" + boolString(c.RepoMirrorRefresh), "OPD_MOCK_ARTIFACT_DIR=/srv/mock-artifacts", "OPD_RELEASES=" + releases, "OPD_LATEST_RELEASE=" + c.RepoMirrorLatest, "OPD_ARCHES=amd64 arm64", "OPD_OCI_IMAGES=" + c.RepoMirrorOCI, "CONTAINERD_VERSION=" + c.ContainerdVersion, "RUNC_VERSION=" + c.RuncVersion}, "/usr/local/bin/repo-mirror", "prepare")...)
	}); err != nil {
		return err
	}
	if err := c.substep("publish git", "local checkout", c.publishLocalRepoToRepoMirror); err != nil {
		return err
	}
	if err := c.substep("publish releases", "mock OpenDeploy binaries", c.publishMockReleasesToRepoMirror); err != nil {
		return err
	}
	return c.substep("http", "start GitHub-shaped mirror service", c.configureRepoMirrorHTTP)
}

func (c *config) buildRepoMirror() error {
	logf("Building repo mirror for linux/%s", c.Goarch)
	if err := os.MkdirAll(filepath.Dir(c.RepoMirrorBin), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-o", c.RepoMirrorBin, ".")
	cmd.Dir = filepath.Join(c.ScriptDir, "repo-mirror")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+c.Goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *config) configureRepoMirrorRegistry() error {
	script := fmt.Sprintf(`set -euo pipefail
install -d -m 0755 /etc/docker/registry /srv/registry
cat >/etc/docker/registry/config.yml <<'CFG'
version: 0.1
log:
  fields:
    service: opendeploy-repo-mirror-registry
storage:
  filesystem:
    rootdirectory: /srv/registry
http:
  addr: :%s
  tls:
    certificate: /etc/opendeploy-repo-mirror/server.crt
    key: /etc/opendeploy-repo-mirror/server.key
CFG
systemctl disable --now docker-registry.service >/dev/null 2>&1 || true
cat >/etc/systemd/system/opendeploy-repo-mirror-registry.service <<'UNIT'
[Unit]
Description=OpenDeploy repo mirror OCI registry
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/docker-registry serve /etc/docker/registry/config.yml
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable opendeploy-repo-mirror-registry.service
systemctl restart opendeploy-repo-mirror-registry.service
`, c.RepoRegistryPort)
	if err := c.vmSudoScript(c.RepoMirrorName, script); err != nil {
		return err
	}
	if err := c.waitForService(c.RepoMirrorName, "opendeploy-repo-mirror-registry.service"); err != nil {
		return err
	}
	return c.vmBash(c.RepoMirrorName, fmt.Sprintf("for i in $(seq 1 60); do curl -fsS https://%s:%s/v2/ >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1", c.RepoRegistryHost, c.RepoRegistryPort))
}

func (c *config) copyMockArtifactsToRepoMirror() error {
	logf("Copying mock artifact cache to %s", c.RepoMirrorName)
	if err := c.vmRun(c.RepoMirrorName, "sudo", "rm", "-rf", "/srv/mock-artifacts", "/tmp/mock-artifacts"); err != nil {
		return err
	}
	if err := c.vmRun(c.RepoMirrorName, "mkdir", "-p", "/tmp/mock-artifacts"); err != nil {
		return err
	}
	for _, name := range []string{"git", "releases", "oci"} {
		path := filepath.Join(c.MockArtifactDir, name)
		if !isDir(path) {
			continue
		}
		if err := run("limactl", "copy", "-r", path, c.RepoMirrorName+":/tmp/mock-artifacts/"+name); err != nil {
			return err
		}
	}
	return c.vmRun(c.RepoMirrorName, "sudo", "bash", "-lc", "set -euo pipefail; mv /tmp/mock-artifacts /srv/mock-artifacts; chmod -R a+rX /srv/mock-artifacts")
}

func (c *config) configureRepoMirrorHTTP() error {
	script := `set -euo pipefail
cat >/etc/systemd/system/opendeploy-repo-mirror.service <<UNIT
[Unit]
Description=OpenDeploy GitHub-shaped repo mirror
After=network-online.target opendeploy-repo-mirror-registry.service
Wants=network-online.target opendeploy-repo-mirror-registry.service

[Service]
Environment=OPD_REPO_MIRROR_PORT=443
Environment=OPD_REPO_MIRROR_TLS=true
Environment=OPD_REPO_MIRROR_CERT=/etc/opendeploy-repo-mirror/server.crt
Environment=OPD_REPO_MIRROR_KEY=/etc/opendeploy-repo-mirror/server.key
Environment=OPD_REPO_MIRROR_PROXY_UNKNOWN=$OPD_PROXY_UNKNOWN
Environment=OPD_NIX_CACHE_MODE=$OPD_NIX_CACHE_MODE
ExecStart=/usr/local/bin/repo-mirror serve
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable opendeploy-repo-mirror.service
systemctl restart opendeploy-repo-mirror.service
`
	if err := c.vmEnvSudoScript(c.RepoMirrorName, map[string]string{"OPD_PROXY_UNKNOWN": c.RepoProxyUnknown, "OPD_NIX_CACHE_MODE": c.NixCacheMode}, script); err != nil {
		return err
	}
	if err := c.waitForService(c.RepoMirrorName, "opendeploy-repo-mirror.service"); err != nil {
		return err
	}
	return c.vmBash(c.RepoMirrorName, fmt.Sprintf("for i in $(seq 1 60); do curl -fsS https://%s/healthz >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1", c.RepoMirrorName))
}

func (c *config) publishLocalRepoToRepoMirror() error {
	logf("Publishing local git checkout to %s", c.RepoMirrorName)
	if err := c.vmRun(c.RepoMirrorName, "sudo", "rm", "-rf", "/tmp/opendeploy-local-git"); err != nil {
		return err
	}
	snapshotGit, cleanup, err := c.materializeLocalGitSnapshot()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := run("limactl", "copy", "-r", snapshotGit, c.RepoMirrorName+":/tmp/opendeploy-local-git"); err != nil {
		return err
	}
	script := `set -euo pipefail
dst="/srv/git/${OPD_OWNER_REPO}.git"
rm -rf "$dst"
mkdir -p "$(dirname "$dst")"
git clone --mirror /tmp/opendeploy-local-git "$dst" >/dev/null
git -C "$dst" update-server-info
rm -rf /tmp/opendeploy-local-git
`
	return c.vmEnvSudoScript(c.RepoMirrorName, map[string]string{"OPD_OWNER_REPO": "jptrs93/opsagent"}, script)
}

func (c *config) materializeLocalGitSnapshot() (string, func(), error) {
	dir, err := os.MkdirTemp(c.StateDir, "local-git-snapshot-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	out, listFilesErr := outputInDir(c.RepoRoot, "git", "ls-files", "-co", "--exclude-standard")
	if listFilesErr != nil {
		cleanup()
		return "", nil, listFilesErr
	}
	for _, rel := range strings.Split(out, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		src := filepath.Join(c.RepoRoot, rel)
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := copyFile(src, dst); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	if err := runDir(dir, "git", "init"); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runDir(dir, "git", "config", "user.email", "opendeploy-e2e@example.invalid"); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runDir(dir, "git", "config", "user.name", "OpenDeploy E2E"); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runDir(dir, "git", "add", "."); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runDir(dir, "git", "commit", "-m", "local e2e snapshot"); err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, ".git"), cleanup, nil
}

func (c *config) publishMockReleasesToRepoMirror() error {
	if c.MockOpenDeploySource != "local" {
		return c.writeLatestReleaseMarker()
	}
	if c.MockOpenDeploySource == "local" {
		for _, version := range words(c.RepoMirrorReleases) {
			bin := filepath.Join(c.MockReleaseBinDir, version, "opendeploy-linux-"+c.Goarch)
			if err := c.publishReleaseToRepoMirror(version, bin); err != nil {
				return err
			}
		}
	}
	return c.writeLatestReleaseMarker()
}

func (c *config) writeLatestReleaseMarker() error {
	script := `set -euo pipefail
mkdir -p "/srv/releases/$OPD_RELEASE_REPO"
printf "%s\n" "$OPD_LATEST" > "/srv/releases/$OPD_RELEASE_REPO/latest"
`
	return c.vmEnvSudoScript(c.RepoMirrorName, map[string]string{"OPD_RELEASE_REPO": c.ReleaseRepo, "OPD_LATEST": c.RepoMirrorLatest}, script)
}

func (c *config) publishReleaseToRepoMirror(version, bin string) error {
	if !fileNonEmpty(bin) {
		return fmt.Errorf("mock opendeploy binary not found: %s", bin)
	}
	sum, err := fileSHA256(bin)
	if err != nil {
		return err
	}
	logf("Publishing opendeploy %s to %s", version, c.RepoMirrorName)
	script := `set -euo pipefail
mkdir -p "$OPD_RELEASE_DIR"
printf "%s  opendeploy-linux-%s\n" "$OPD_SHA256" "$OPD_ARCH" > "$OPD_RELEASE_DIR/sha256sums.txt"
`
	if err := c.vmEnvSudoScript(c.RepoMirrorName, map[string]string{
		"OPD_RELEASE_DIR": "/srv/releases/" + c.ReleaseRepo + "/" + version,
		"OPD_VERSION":     version,
		"OPD_ARCH":        c.Goarch,
		"OPD_SHA256":      sum,
	}, script); err != nil {
		return err
	}
	// Use a per-version staging path and verify the installed hash: reusing one
	// /tmp path for every version has raced limactl copy against install and
	// published the previous version's bytes under the new version's URL.
	tmp := "/tmp/opendeploy-release-" + version
	if err := run("limactl", "copy", bin, c.RepoMirrorName+":"+tmp); err != nil {
		return err
	}
	dst := "/srv/releases/" + c.ReleaseRepo + "/" + version + "/opendeploy-linux-" + c.Goarch
	if err := c.vmRun(c.RepoMirrorName, "sudo", "install", "-m", "0755", tmp, dst); err != nil {
		return err
	}
	out, err := c.vmOutput(c.RepoMirrorName, "sha256sum", dst)
	if err != nil {
		return err
	}
	if got := strings.Fields(strings.TrimSpace(out))[0]; got != sum {
		return fmt.Errorf("published %s has sha256 %s, want %s", dst, got, sum)
	}
	return nil
}

func (c *config) configureGithubToken(name string) error {
	if c.OpenDeployGitHubToken == "" {
		return nil
	}
	script := `printf "\nOPENDEPLOY_GITHUB_TOKEN=%q\n" "$OPENDEPLOY_GITHUB_TOKEN" >> /etc/opendeploy/env`
	return c.vmEnvSudoScript(name, map[string]string{"OPENDEPLOY_GITHUB_TOKEN": c.OpenDeployGitHubToken}, script)
}

func (c *config) primaryEnrollmentFingerprint() (string, error) {
	script := `set -euo pipefail
hex=$(openssl s_client -connect 127.0.0.1:9444 -servername primary </dev/null 2>/dev/null \
  | openssl x509 -pubkey -noout \
  | openssl pkey -pubin -outform DER \
  | openssl dgst -sha256 -hex \
  | awk "{print \$2}")
printf "sha256:%s\n" "$hex"
`
	out, err := c.vmOutput(nameOr(c.PrimaryName), "bash", "-lc", script)
	return strings.TrimSpace(out), err
}

func nameOr(v string) string { return v }

func (c *config) installPrimary() error {
	installVersion := c.InstallVersion
	var err error
	if c.usesSelfBootstrap() {
		installVersion = c.SelfVersion
		err = c.copySelfOpenDeploy(c.PrimaryName)
	} else {
		err = c.downloadOpenDeploy(c.PrimaryName, installVersion)
	}
	if err != nil {
		return err
	}
	if err := run("limactl", "copy", c.ServerBundle, c.PrimaryName+":/tmp/opendeploy-web.pem"); err != nil {
		return err
	}
	logf("Installing primary in %s", c.PrimaryName)
	args := []string{"sudo", "opendeploy", "install", "primary", "--web-listen", ":443", "--acme-hosts", c.WebHost, "--web-tls-self-managed", "true", "--web-tls-cert-pem-file", "/tmp/opendeploy-web.pem", "--passkey-extra-origins", "https://" + c.WebHost + ":8443"}
	if !c.usesSelfBootstrap() {
		args = append(args, "--version", c.InstallVersion)
	}
	installOutput, err := c.vmOutput(c.PrimaryName, args...)
	logf("Primary installer output:\n%s", installOutput)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`Temporary\s+setup\s+password:\s+([^\s]+)`) // installer output contract
	m := re.FindStringSubmatch(installOutput)
	if len(m) != 2 {
		return errors.New("installer output did not include a temporary setup password")
	}
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.E2EEnvFile, []byte(fmt.Sprintf("OPD_SETUP_PASSWORD=%s\nOPD_INSTALL_VERSION=%s\n", shellQuote(m[1]), shellQuote(installVersion))), 0o600); err != nil {
		return err
	}
	if err := c.configureGithubToken(c.PrimaryName); err != nil {
		return err
	}
	if err := c.addOpenDeployToNixUsers(c.PrimaryName); err != nil {
		return err
	}
	if err := c.vmRun(c.PrimaryName, "sudo", "systemctl", "restart", "opendeploy.service"); err != nil {
		return err
	}
	if err := c.waitForService(c.PrimaryName, "opendeploy.service"); err != nil {
		return err
	}
	return c.waitForHealthz(c.PrimaryName)
}

func (c *config) installWorker(name string) error {
	var err error
	if c.usesSelfBootstrap() {
		err = c.copySelfOpenDeploy(name)
	} else {
		err = c.downloadOpenDeploy(name, c.InstallVersion)
	}
	if err != nil {
		return err
	}
	fp, err := c.primaryEnrollmentFingerprint()
	if err != nil {
		return err
	}
	logf("Installing secondary in %s", name)
	help, _ := c.vmCombinedOutput(name, "opendeploy", "install", "secondary", "-h")
	args := []string{"sudo", "opendeploy", "install", "secondary"}
	if !c.usesSelfBootstrap() {
		args = append(args, "--version", c.InstallVersion)
	}
	args = append(args, "--cluster-addr", c.PrimaryName+":9443", "--enrollment-addr", c.PrimaryName+":9444")
	if strings.Contains(help, "enrollment-fingerprint") {
		args = append(args, "--enrollment-fingerprint", fp)
	} else {
		logf("%s installer does not support --enrollment-fingerprint; continuing without it", name)
	}
	if err := c.vmRun(name, args...); err != nil {
		return err
	}
	if err := c.configureGithubToken(name); err != nil {
		return err
	}
	if err := c.addOpenDeployToNixUsers(name); err != nil {
		return err
	}
	if err := c.vmRun(name, "sudo", "systemctl", "restart", "opendeploy.service"); err != nil {
		return err
	}
	return c.waitForService(name, "opendeploy.service")
}

func (c *config) addOpenDeployToNixUsers(name string) error {
	if err := c.vmRun(name, "sudo", "groupadd", "-f", "nix-users"); err != nil {
		return err
	}
	return c.vmRun(name, "sudo", "usermod", "-aG", "nix-users", "opendeploy")
}

func (c *config) installCluster() error {
	for _, vm := range c.clusterVMNames() {
		if err := c.substep("wait systemd "+vm, "system manager ready", func() error { return c.waitForSystemd(vm) }); err != nil {
			return err
		}
	}
	if err := c.substep("primary", "install primary service", c.installPrimary); err != nil {
		return err
	}
	for _, worker := range []string{c.SecondaryName, c.Secondary2Name} {
		if err := c.substep(worker, "install worker service", func() error { return c.installWorker(worker) }); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) preparePlaywrightE2E() error {
	if err := requireCmd("docker"); err != nil {
		return err
	}
	if !c.PlaywrightBaseURLSet {
		if err := c.ensurePlaywrightTunnel(); err != nil {
			return err
		}
		if err := c.ensurePlaywrightSecondaryTunnels(); err != nil {
			return err
		}
		if err := c.ensurePlaywrightTLSIngressTunnel(); err != nil {
			return err
		}
	}
	if err := removeDirContentsExcept(c.ResultsDir, "run.log"); err != nil {
		return err
	}
	if err := os.MkdirAll(c.ResultsDir, 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(c.ReportDir); err != nil {
		return err
	}
	if err := os.MkdirAll(c.ReportDir, 0o755); err != nil {
		return err
	}
	workDir := filepath.Join(c.StateDir, "playwright-work")
	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	return nil
}

func (c *config) playwrightDockerRunArgs(envPairs map[string]string) []string {
	workDir := filepath.Join(c.StateDir, "playwright-work")
	args := []string{"docker", "run", "--rm", "-v", filepath.Join(c.ScriptDir, "e2e") + ":/src:ro", "-v", workDir + ":/work", "-v", c.ResultsDir + ":/work/test-results", "-v", c.ReportDir + ":/work/playwright-report", "-w", "/work"}
	if !c.PlaywrightBaseURLSet {
		args = append(args, "--add-host", c.WebHost+":127.0.0.1", "--add-host", "host.docker.internal:host-gateway")
	}
	for _, host := range words(env("OPD_PLAYWRIGHT_ADD_HOSTS", "")) {
		args = append(args, "--add-host", host)
	}
	keys := sortedKeys(envPairs)
	for _, k := range keys {
		args = append(args, "-e", k+"="+envPairs[k])
	}
	args = append(args, c.PlaywrightDockerImage, "bash", "-lc")
	return args
}

func (c *config) runPlaywrightDocker(envPairs map[string]string, command string) error {
	args := c.playwrightDockerRunArgs(envPairs)
	args = append(args, c.playwrightDockerScript(command))
	return run(args...)
}

func (c *config) runPlaywrightDockerFiltered(envPairs map[string]string, command string) error {
	args := c.playwrightDockerRunArgs(envPairs)
	args = append(args, c.playwrightDockerScript(command))
	return runFilteredOutput(args, "[opd-pw] ")
}

func (c *config) ensurePlaywrightTunnel() error {
	if err := requireCmd("ssh"); err != nil {
		return err
	}
	if c.playwrightTunnelHealthCheck() == nil {
		return nil
	}
	cmd, err := c.startSSHTunnel(c.PrimaryName, c.PlaywrightHostPort, "127.0.0.1", "443")
	if err != nil {
		return err
	}
	c.PlaywrightTunnelCmds = append(c.PlaywrightTunnelCmds, cmd)
	for i := 0; i < 30; i++ {
		if c.playwrightTunnelHealthCheck() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	c.stopPlaywrightTunnel()
	return fmt.Errorf("primary SSH tunnel did not become healthy on 127.0.0.1:%s", c.PlaywrightHostPort)
}

func (c *config) ensurePlaywrightSecondaryTunnels() error {
	if err := requireCmd("ssh"); err != nil {
		return err
	}
	secondaryIP, err := c.vmIPv4(c.SecondaryName)
	if err != nil {
		return err
	}
	for _, port := range words(c.PlaywrightSecondaryPorts) {
		cmd, err := c.startSSHTunnel(c.SecondaryName, port, secondaryIP, port)
		if err != nil {
			return err
		}
		c.PlaywrightTunnelCmds = append(c.PlaywrightTunnelCmds, cmd)
	}
	for _, port := range words(c.PlaywrightFilteredPorts) {
		cmd, err := c.startSSHTunnel(c.PrimaryName, port, secondaryIP, port)
		if err != nil {
			return err
		}
		c.PlaywrightTunnelCmds = append(c.PlaywrightTunnelCmds, cmd)
	}
	return nil
}

func (c *config) ensurePlaywrightTLSIngressTunnel() error {
	if err := requireCmd("ssh"); err != nil {
		return err
	}
	secondaryIP, err := c.vmIPv4(c.Secondary2Name)
	if err != nil {
		return err
	}
	cmd, err := c.startSSHTunnel(c.Secondary2Name, c.PlaywrightTLSIngressPort, secondaryIP, "443")
	if err != nil {
		return err
	}
	c.PlaywrightTunnelCmds = append(c.PlaywrightTunnelCmds, cmd)
	if err := waitForLocalTunnelListener(c.PlaywrightTLSIngressPort); err != nil {
		return fmt.Errorf("TLS ingress tunnel on 127.0.0.1:%s: %w", c.PlaywrightTLSIngressPort, err)
	}
	httpCmd, err := c.startSSHTunnel(c.Secondary2Name, c.PlaywrightHTTPIngressPort, secondaryIP, "80")
	if err != nil {
		return err
	}
	c.PlaywrightTunnelCmds = append(c.PlaywrightTunnelCmds, httpCmd)
	if err := waitForLocalTunnelListener(c.PlaywrightHTTPIngressPort); err != nil {
		return fmt.Errorf("HTTP ingress tunnel on 127.0.0.1:%s: %w", c.PlaywrightHTTPIngressPort, err)
	}
	return nil
}

// waitForLocalTunnelListener confirms the ssh -L listener accepts connections,
// so a bind conflict (ExitOnForwardFailure exits ssh silently) fails the run
// here rather than as an opaque test timeout later.
func waitForLocalTunnelListener(port string) error {
	var lastErr error
	for i := 0; i < 15; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Errorf("listener never became reachable: %w", lastErr)
}

func (c *config) startSSHTunnel(vm, localPort, remoteHost, remotePort string) (*exec.Cmd, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sshConfig := filepath.Join(home, ".lima", vm, "ssh.config")
	if !fileNonEmpty(sshConfig) {
		return nil, fmt.Errorf("SSH config not found for %s: %s", vm, sshConfig)
	}
	forward := fmt.Sprintf("127.0.0.1:%s:%s:%s", localPort, remoteHost, remotePort)
	cmd := exec.Command("ssh", "-F", sshConfig, "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes", "-N", "-L", forward, "lima-"+vm)
	if activeLogger != nil {
		cmd.Stdout = activeLogger.writer()
		cmd.Stderr = activeLogger.writer()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (c *config) playwrightTunnelHealthCheck() error {
	healthURL := "https://" + c.WebHost + ":" + c.PlaywrightHostPort + "/v1/healthz"
	resolve := fmt.Sprintf("%s:%s:127.0.0.1", c.WebHost, c.PlaywrightHostPort)
	return quietRun("curl", "--noproxy", "*", "-kfsS", "--resolve", resolve, healthURL)
}

func (c *config) stopPlaywrightTunnel() {
	for _, cmd := range c.PlaywrightTunnelCmds {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	c.PlaywrightTunnelCmds = nil
}

func (c *config) waitForPlaywrightWeb() error {
	logf("Waiting for Docker Playwright access to %s", c.PlaywrightBaseURL)
	healthURL := c.PlaywrightBaseURL + "/v1/healthz"
	for i := 0; i < 120; i++ {
		if quietRun(append(c.playwrightDockerBaseArgs(), "bash", "-lc", c.playwrightDockerScript("curl -kfsS "+shellQuote(healthURL)+" >/dev/null"))...) == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	_ = run(append(c.playwrightDockerBaseArgs(), "bash", "-lc", c.playwrightDockerScript("curl -kv "+shellQuote(healthURL)))...)
	return fmt.Errorf("Docker Playwright could not reach %s; set OPD_PLAYWRIGHT_BASE_URL to a Docker-reachable URL if needed", healthURL)
}

func (c *config) copyPlaywrightResults() {
	_ = os.MkdirAll(c.ResultsDir, 0o755)
	_ = os.MkdirAll(c.ReportDir, 0o755)
}

func (c *config) playwrightDockerBaseArgs() []string {
	args := []string{"docker", "run", "--rm"}
	if !c.PlaywrightBaseURLSet {
		args = append(args, "--add-host", c.WebHost+":127.0.0.1", "--add-host", "host.docker.internal:host-gateway")
	}
	for _, host := range words(env("OPD_PLAYWRIGHT_ADD_HOSTS", "")) {
		args = append(args, "--add-host", host)
	}
	args = append(args, c.PlaywrightDockerImage)
	return args
}

func (c *config) playwrightDockerScript(command string) string {
	if c.PlaywrightBaseURLSet {
		return command
	}
	proxy := fmt.Sprintf(`node -e 'const net=require("net"); const port=%s; const server=net.createServer((client)=>{const upstream=net.connect(port,"host.docker.internal"); client.pipe(upstream); upstream.pipe(client); client.on("error",()=>upstream.destroy()); upstream.on("error",()=>client.destroy());}); server.listen(443,"0.0.0.0");' &`, shellQuote(c.PlaywrightHostPort))
	return proxy + " sleep 1 && " + command
}

func (c *config) startIPv4EgressListener() error {
	script := fmt.Sprintf(`set -euo pipefail
cat > /tmp/opendeploy-ipv4-egress-listener.py <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = self.client_address[0].encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass

HTTPServer(("0.0.0.0", %d), Handler).serve_forever()
PY
if [[ -f /tmp/opendeploy-ipv4-egress-listener.pid ]]; then
  kill "$(cat /tmp/opendeploy-ipv4-egress-listener.pid)" 2>/dev/null || true
fi
nohup python3 /tmp/opendeploy-ipv4-egress-listener.py >/tmp/opendeploy-ipv4-egress-listener.log 2>&1 &
echo $! >/tmp/opendeploy-ipv4-egress-listener.pid
sleep 1
curl -fsS http://127.0.0.1:%d/ >/dev/null`, ipv4EgressListenerPort, ipv4EgressListenerPort)
	return c.vmBash(c.Secondary2Name, script)
}

func (c *config) runPlaywrightFlows() error {
	flowArgs := []string{}
	for _, flow := range strings.Split(env("FLOWS", "bootstrap-enroll-nixdocker"), ",") {
		flow = strings.TrimSpace(flow)
		if flow != "" {
			flowArgs = append(flowArgs, "flows/"+flow+".spec.js")
		}
	}
	if len(flowArgs) == 0 {
		return errors.New("no flows selected")
	}
	envFile, _ := loadShellEnvFile(c.E2EEnvFile)
	worker1MachineID, err := c.workerEnrollmentMachineID(c.SecondaryName)
	if err != nil {
		return err
	}
	worker2MachineID, err := c.workerEnrollmentMachineID(c.Secondary2Name)
	if err != nil {
		return err
	}
	worker1IPv4, err := c.vmIPv4(c.SecondaryName)
	if err != nil {
		return err
	}
	primaryIPv4, err := c.vmIPv4(c.PrimaryName)
	if err != nil {
		return err
	}
	worker2IPv4, err := c.vmIPv4(c.Secondary2Name)
	if err != nil {
		return err
	}
	tlsIngressEnv, err := c.tlsIngressPlaywrightEnv()
	if err != nil {
		return err
	}
	upgradeVersion := c.UpgradeVersion
	secondaryHost := c.SecondaryName
	tlsIngressHost := c.Secondary2Name
	tlsIngressPort := c.PlaywrightTLSIngressPort
	httpIngressPort := c.PlaywrightHTTPIngressPort
	if !c.PlaywrightBaseURLSet {
		secondaryHost = "host.docker.internal"
		tlsIngressHost = "host.docker.internal"
	} else {
		tlsIngressPort = "443"
		httpIngressPort = "80"
		if ip, err := c.vmIPv4(c.SecondaryName); err == nil && ip != "" {
			secondaryHost = ip
		}
		if secondary2IP, secondErr := c.vmIPv4(c.Secondary2Name); secondErr == nil && secondary2IP != "" {
			tlsIngressHost = secondary2IP
		}
	}
	logf("Upgrade target version: %s", upgradeVersion)
	envPairs := map[string]string{
		"OPD_BASE_URL":                    c.PlaywrightBaseURL,
		"OPD_BACKEND_S3_ENDPOINT":         "http://" + c.SecondaryName + ":9000",
		"OPD_IGNORE_HTTPS_ERRORS":         "true",
		"OPD_SECONDARY_HOST":              secondaryHost,
		"OPD_TLS_INGRESS_HOST":            tlsIngressHost,
		"OPD_TLS_INGRESS_PORT":            tlsIngressPort,
		"OPD_HTTP_INGRESS_PORT":           httpIngressPort,
		"OPD_WORKER_1_MACHINE_ID":         worker1MachineID,
		"OPD_WORKER_2_MACHINE_ID":         worker2MachineID,
		"OPD_IPV4_EGRESS_URL":             fmt.Sprintf("http://%s:%d/", worker2IPv4, ipv4EgressListenerPort),
		"OPD_IPV4_EGRESS_EXPECTED_SOURCE": worker1IPv4,
		"OPD_FILTERED_PORT_CLIENT_IP":     primaryIPv4,
		"OPD_SETUP_PASSWORD":              envFile["OPD_SETUP_PASSWORD"],
		"OPD_INSTALL_VERSION":             envFile["OPD_INSTALL_VERSION"],
		"OPD_UPGRADE_VERSION":             upgradeVersion,
		"OPD_BACKUP_RESTORE":              boolString(c.BackupRestore),
		"OPD_BACKUP_RESTORE_STATE":        "/work/test-results/backup-restore.env",
		"OPD_NETPOLICY_STATE":             "/work/test-results/netpolicy.env",
		"OPD_POSTGRES_IMAGE":              c.PostgresImage,
		"OPD_DECLARATIVE_POSTGRES_IMAGE":  c.DeclarativePostgresImage,
		"OPD_MINIO_IMAGE":                 c.MinioImage,
		"OPENDEPLOY_GITHUB_TOKEN":         c.OpenDeployGitHubToken,
	}
	for key, value := range tlsIngressEnv {
		envPairs[key] = value
	}
	if err := c.step("playwright container preparation", "Docker runner and E2E dependencies", func() error {
		if err := c.substep("prepare", "output directories and Docker access", c.preparePlaywrightE2E); err != nil {
			return err
		}
		if err := c.substep("health", c.PlaywrightBaseURL+"/v1/healthz", c.waitForPlaywrightWeb); err != nil {
			return err
		}
		if err := c.substep("dependencies", "npm install", func() error {
			return c.runPlaywrightDocker(envPairs, "set -euo pipefail && cp -R /src/. /work && npm install")
		}); err != nil {
			c.copyPlaywrightResults()
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	defer c.stopPlaywrightTunnel()
	c.startIngressPacketCapture()
	defer c.stopIngressPacketCapture()
	testCmd := []string{"npx", "playwright", "test"}
	for _, arg := range flowArgs {
		testCmd = append(testCmd, shellQuote(arg))
	}
	logf("Running Playwright flows: %s", strings.Join(flowArgs, " "))
	err = c.step("flows", strings.Join(flowArgs, " "), func() error {
		return c.runPlaywrightDockerFiltered(envPairs, "set -euo pipefail && "+strings.Join(testCmd, " "))
	})
	if err != nil {
		c.captureIngressDiagnostics()
	}
	c.copyPlaywrightResults()
	return err
}

func (c *config) backupRestore() error {
	stateFile := env("OPD_BACKUP_RESTORE_STATE_HOST", filepath.Join(c.ResultsDir, "backup-restore.env"))
	restoreInstallVersion := env("OPD_RESTORE_INSTALL_VERSION", c.UpgradeVersion)
	state := map[string]string{}
	if err := c.substep("load restore state", stateFile, func() error {
		loaded, err := loadShellEnvFile(stateFile)
		if err != nil {
			return fmt.Errorf("backup restore state file not found or invalid: %s: %w", stateFile, err)
		}
		for _, k := range []string{"OPD_RESTORE_S3_ACCESS_KEY_ID", "OPD_RESTORE_S3_SECRET_ACCESS_KEY", "OPD_RESTORE_S3_BUCKET", "OPD_RESTORE_S3_PATH", "OPD_RESTORE_S3_REGION", "OPD_RESTORE_S3_ENDPOINT", "OPD_RESTORE_RECOVERY_CODE"} {
			if loaded[k] == "" {
				return fmt.Errorf("%s is required in %s", k, stateFile)
			}
		}
		state = loaded
		return nil
	}); err != nil {
		return err
	}
	if err := c.substep("check host tools", "limactl, curl, openssl", c.requireHostTools); err != nil {
		return err
	}
	if err := c.substep("destroy source primary", "delete primary VM after Playwright verified backup replication", func() error {
		_ = c.vmQuietRun(c.PrimaryName, "sudo", "systemctl", "stop", "opendeploy.service")
		c.deleteVM(c.PrimaryName)
		return nil
	}); err != nil {
		return err
	}
	if err := c.substep("create replacement primary", "fresh Lima VM", func() error {
		return c.startVM(c.PrimaryName, "node")
	}); err != nil {
		return err
	}
	if err := c.substep("sync cluster hosts", "update primary/secondary/repo-mirror names after primary replacement", c.syncHostsAll); err != nil {
		return err
	}
	if err := c.substep("install test CA", c.PrimaryName, func() error {
		return c.installTestCA(c.PrimaryName)
	}); err != nil {
		return err
	}
	if err := c.substep("wait replacement systemd", c.PrimaryName, func() error {
		return c.waitForSystemd(c.PrimaryName)
	}); err != nil {
		return err
	}
	if err := c.substep("install opendeploy binary", restoreInstallVersion, func() error {
		return c.downloadOpenDeploy(c.PrimaryName, restoreInstallVersion)
	}); err != nil {
		return err
	}
	if err := c.substep("copy web TLS bundle", "replacement primary HTTPS certificate", func() error {
		return run("limactl", "copy", c.ServerBundle, c.PrimaryName+":/tmp/opendeploy-web.pem")
	}); err != nil {
		return err
	}
	cmd := []string{"sudo", "opendeploy", "install", "primary", "--web-listen", ":443", "--acme-hosts", c.WebHost, "--web-tls-self-managed", "true", "--web-tls-cert-pem-file", "/tmp/opendeploy-web.pem", "--passkey-extra-origins", "https://" + c.WebHost + ":8443", "--restore-backup", "true", "--restore-s3-access-key-id", state["OPD_RESTORE_S3_ACCESS_KEY_ID"], "--restore-s3-secret-access-key", state["OPD_RESTORE_S3_SECRET_ACCESS_KEY"], "--restore-s3-bucket", state["OPD_RESTORE_S3_BUCKET"], "--restore-s3-path", state["OPD_RESTORE_S3_PATH"], "--restore-s3-region", state["OPD_RESTORE_S3_REGION"], "--restore-s3-endpoint", state["OPD_RESTORE_S3_ENDPOINT"], "--recovery-code", state["OPD_RESTORE_RECOVERY_CODE"]}
	cmd = append(cmd, "--version", restoreInstallVersion)
	if err := c.substep("restore primary install", "opendeploy install primary --restore-backup", func() error {
		return c.vmRun(c.PrimaryName, cmd...)
	}); err != nil {
		return err
	}
	if err := c.substep("post-restore primary setup", "GitHub token and Nix user permissions", func() error {
		if err := c.configureGithubToken(c.PrimaryName); err != nil {
			return err
		}
		return c.addOpenDeployToNixUsers(c.PrimaryName)
	}); err != nil {
		return err
	}
	if err := c.substep("restart restored primary", "apply post-restore setup", func() error {
		return c.vmRun(c.PrimaryName, "sudo", "systemctl", "restart", "opendeploy.service")
	}); err != nil {
		return err
	}
	if err := c.substep("verify restored primary", "service active, healthz OK, and opendeploy-net running", func() error {
		if err := c.waitForService(c.PrimaryName, "opendeploy.service"); err != nil {
			return err
		}
		if err := c.waitForHealthz(c.PrimaryName); err != nil {
			return err
		}
		return c.waitForPrimaryNetproxy(c.PrimaryName)
	}); err != nil {
		return err
	}
	return nil
}

func (c *config) vmRun(name string, args ...string) error {
	return run(append([]string{"limactl", "shell", "--tty=false", name}, args...)...)
}

func (c *config) vmQuietRun(name string, args ...string) error {
	return quietRun(append([]string{"limactl", "shell", "--tty=false", name}, args...)...)
}

func (c *config) vmOutput(name string, args ...string) (string, error) {
	return output(append([]string{"limactl", "shell", "--tty=false", name}, args...)...)
}

func (c *config) workerEnrollmentMachineID(name string) (string, error) {
	out, err := c.vmOutput(name, "sudo", "cat", "/var/lib/opendeploy/enrollment-machine-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("secondary %s has no enrollment machine ID", name)
	}
	return id, nil
}

func (c *config) vmIPv4(name string) (string, error) {
	out, err := c.vmOutput(name, "hostname", "-I")
	if err != nil {
		return "", err
	}
	for _, field := range strings.Fields(out) {
		if strings.Contains(field, ".") {
			return field, nil
		}
	}
	return "", fmt.Errorf("no IPv4 address found for %s", name)
}

func (c *config) vmCombinedOutput(name string, args ...string) (string, error) {
	return combinedOutput(append([]string{"limactl", "shell", "--tty=false", name}, args...)...)
}

func (c *config) vmBash(name, script string) error {
	return c.vmRun(name, "bash", "-lc", script)
}

func (c *config) vmEnvRun(name string, envs map[string]string, script string) error {
	args := []string{"env"}
	for _, k := range sortedKeys(envs) {
		args = append(args, k+"="+envs[k])
	}
	args = append(args, "bash", "-lc", script)
	return c.vmRun(name, args...)
}

func (c *config) vmSudoScript(name, script string) error {
	return runStdin(script, "limactl", "shell", "--tty=false", name, "sudo", "bash", "-s")
}

func (c *config) vmEnvSudoScript(name string, envs map[string]string, script string) error {
	args := []string{"shell", "--tty=false", name, "sudo", "env"}
	for _, k := range sortedKeys(envs) {
		args = append(args, k+"="+envs[k])
	}
	args = append(args, "bash", "-s")
	return runStdin(script, append([]string{"limactl"}, args...)...)
}

func downloadFile(url, dst string) error {
	if fileNonEmpty(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	logf("Downloading %s", url)
	return run("curl", "-fL", "--retry", "3", "--retry-delay", "2", url, "-o", dst)
}

func loadShellEnvFile(path string) (map[string]string, error) {
	if !fileNonEmpty(path) {
		return map[string]string{}, os.ErrNotExist
	}
	script := "set -a; source " + shellQuote(path) + "; env -0"
	cmd := exec.Command("bash", "-lc", script)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, part := range bytes.Split(out, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		k, v, ok := bytes.Cut(part, []byte("="))
		if ok {
			result[string(k)] = string(v)
		}
	}
	return result, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1" || v == "yes"
}

func envInt(name string, fallback int) int {
	var out int
	if _, err := fmt.Sscanf(os.Getenv(name), "%d", &out); err == nil {
		return out
	}
	return fallback
}

func requireCmd(name string) error {
	if !cmdExists(name) {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(args ...string) error {
	if len(args) == 0 {
		return nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	return runCommand(cmd, nil, args...)
}

func quietRun(args ...string) error {
	if len(args) == 0 {
		return nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func runDir(dir string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	return runCommand(cmd, nil, args...)
}

func runStdin(stdin string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	return runCommand(cmd, nil, args...)
}

func output(args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	tail := newTailBuffer(16 * 1024)
	if activeLogger != nil {
		activeLogger.detail("$ %s", shellJoin(args))
		cmd.Stderr = io.MultiWriter(activeLogger.writer(), tail)
	} else {
		cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	}
	out, err := cmd.Output()
	if activeLogger != nil && len(out) > 0 {
		_, _ = activeLogger.writer().Write(out)
		if out[len(out)-1] != '\n' {
			_, _ = activeLogger.writer().Write([]byte("\n"))
		}
	}
	if err != nil && activeLogger != nil {
		activeLogger.commandFailed(err, tail.String())
	}
	return string(out), err
}

func outputInDir(dir string, args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	tail := newTailBuffer(16 * 1024)
	if activeLogger != nil {
		activeLogger.detail("$ %s", shellJoin(args))
		cmd.Stderr = io.MultiWriter(activeLogger.writer(), tail)
	} else {
		cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	}
	out, err := cmd.Output()
	if activeLogger != nil && len(out) > 0 {
		_, _ = activeLogger.writer().Write(out)
		if out[len(out)-1] != '\n' {
			_, _ = activeLogger.writer().Write([]byte("\n"))
		}
	}
	if err != nil && activeLogger != nil {
		activeLogger.commandFailed(err, tail.String())
	}
	return string(out), err
}

func combinedOutput(args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	if activeLogger != nil {
		activeLogger.detail("$ %s", shellJoin(args))
	}
	out, err := cmd.CombinedOutput()
	if activeLogger != nil && len(out) > 0 {
		_, _ = activeLogger.writer().Write(out)
		if out[len(out)-1] != '\n' {
			_, _ = activeLogger.writer().Write([]byte("\n"))
		}
	}
	if err != nil && activeLogger != nil {
		tail := string(out)
		if len(tail) > 16*1024 {
			tail = tail[len(tail)-16*1024:]
		}
		activeLogger.commandFailed(err, tail)
	}
	return string(out), err
}

func runFilteredOutput(args []string, prefix string) error {
	if len(args) == 0 {
		return nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	tail := newTailBuffer(16 * 1024)
	if activeLogger != nil {
		activeLogger.detail("$ %s", shellJoin(args))
		writer := &filteredConsoleWriter{logger: activeLogger, tail: tail, prefix: prefix}
		cmd.Stdout = writer
		cmd.Stderr = writer
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if err != nil && activeLogger != nil {
		activeLogger.commandFailed(err, tail.String())
	}
	return err
}

type filteredConsoleWriter struct {
	logger *runLogger
	tail   *tailBuffer
	prefix string
	buf    []byte
	mu     sync.Mutex
}

func (w *filteredConsoleWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logger != nil {
		_, _ = w.logger.writer().Write(p)
	}
	if w.tail != nil {
		_, _ = w.tail.Write(p)
	}
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r")
		w.buf = w.buf[idx+1:]
		if strings.HasPrefix(line, w.prefix) {
			fmt.Println(strings.TrimPrefix(line, w.prefix))
		}
	}
	return len(p), nil
}

func runCommand(cmd *exec.Cmd, stdin io.Reader, displayArgs ...string) error {
	if stdin != nil {
		cmd.Stdin = stdin
	}
	tail := newTailBuffer(16 * 1024)
	if activeLogger != nil {
		activeLogger.detail("$ %s", shellJoin(displayArgs))
		writer := io.MultiWriter(activeLogger.writer(), tail)
		cmd.Stdout = writer
		cmd.Stderr = writer
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if err != nil && activeLogger != nil {
		activeLogger.commandFailed(err, tail.String())
	}
	return err
}

type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(maxBytes int) *tailBuffer {
	return &tailBuffer{max: maxBytes}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func fileNonEmpty(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func words(s string) []string { return strings.Fields(s) }

func wordListContains(list, want string) bool {
	for _, item := range words(list) {
		if item == want {
			return true
		}
	}
	return false
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func logf(format string, args ...any) {
	if activeLogger != nil {
		activeLogger.detail(format, args...)
		return
	}
	fmt.Printf(format+"\n", args...)
}
