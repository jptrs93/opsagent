package git

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

type RepoInfo struct {
	LatestCommit string
	Branch       string
}

type CommitInfo struct {
	Hash    string
	Message string
	Author  string
	Time    time.Time
	Branch  string
}

type VersionDiscovery struct {
	Branches       []string
	SelectedBranch string
	Commits        []CommitInfo
}

var fullCommitHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var credentialURLPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s]*@`)

// githubCloneURLPrefix scopes the injected Authorization header in githubAuthEnv.
const githubCloneURLPrefix = "https://github.com/"

type Manager struct {
	cacheDir    string
	credentials githubcredentials.Provider
	locks       sync.Map
}

func NewManager(cacheDir string, provider githubcredentials.Provider) *Manager {
	return &Manager{cacheDir: cacheDir, credentials: provider}
}

// ResolveCloneURL normalizes a repository reference into a credential-free
// clone URL. The GitHub token is supplied out of band by gitEnv, so the URL is
// safe to persist in .git/config and to pass as a command-line argument.
func (g *Manager) ResolveCloneURL(repoURL string) (string, error) {
	return resolveCloneURL(repoURL)
}

func resolveCloneURL(repoURL string) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", fmt.Errorf("repo is required")
	}
	if strings.ContainsAny(repoURL, "\x00\n\r") {
		return "", fmt.Errorf("repo URL contains invalid characters")
	}
	if strings.HasPrefix(repoURL, "-") {
		return "", fmt.Errorf("repo URL must not begin with %q", "-")
	}
	if _, err := os.Stat(repoURL); err == nil {
		return repoURL, nil
	}
	if strings.HasPrefix(repoURL, "git@") {
		return repoURL, nil
	}
	if strings.Contains(repoURL, "://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return "", fmt.Errorf("cannot parse git repo from %q", redactCredentialURLs(repoURL))
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("unsupported git URL scheme %q", u.Scheme)
		}
		if !strings.HasSuffix(u.Path, ".git") {
			u.Path += ".git"
			repoURL = u.String()
		}
		return repoURL, nil
	}

	parts := strings.Split(repoURL, "/")
	if len(parts) < 3 || parts[0] == "" {
		return "", fmt.Errorf("cannot parse git repo from %q", redactCredentialURLs(repoURL))
	}
	return "https://" + strings.TrimSuffix(repoURL, ".git") + ".git", nil
}

// gitEnv builds the environment for a git invocation. Every git command runs
// with it so that credential handling is uniform and cannot be omitted at an
// individual call site.
func (g *Manager) gitEnv(ctx context.Context) ([]string, error) {
	creds, err := g.credentials.LoadCredentials(ctx)
	if err != nil {
		return nil, err
	}
	parent := os.Environ()
	env := make([]string, 0, len(parent)+4)
	for _, entry := range parent {
		// Drop inherited values so an ambient GIT_CONFIG_COUNT cannot displace
		// or renumber the settings below.
		if strings.HasPrefix(entry, "GIT_CONFIG_") || strings.HasPrefix(entry, "GIT_TERMINAL_PROMPT=") {
			continue
		}
		env = append(env, entry)
	}
	// A missing or rejected token must fail the command rather than block on an
	// interactive credential prompt.
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	return append(env, githubAuthEnv(creds.Token)...), nil
}

// githubAuthEnv supplies the GitHub token as a github.com-scoped Authorization
// header through GIT_CONFIG_* rather than embedding it in the clone URL.
//
// A credential in the URL is written to .git/config by clone and remote
// set-url, leaving the token at rest inside the checkout that Nix builds read.
// A credential passed as a command-line argument is readable by every local
// user through /proc/<pid>/cmdline. Neither applies here: the environment of a
// process is readable only by its own user, and nothing below reaches disk.
//
// Scoping the header to https://github.com/ keeps it off requests to any other
// host. Requires git 2.31 or newer for GIT_CONFIG_COUNT.
func githubAuthEnv(token string) []string {
	if token == "" {
		return nil
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + githubCloneURLPrefix + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + credential,
	}
}

func (g *Manager) FetchRepoInfo(ctx context.Context, repoURL string) (*RepoInfo, error) {
	cloneURL, err := g.ResolveCloneURL(repoURL)
	if err != nil {
		return nil, err
	}
	out, err := g.runGitStdout(ctx, "ls-remote", "--symref", "--", cloneURL, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("ls-remote %s: %w", redactCredentialURLs(repoURL), err)
	}
	info := &RepoInfo{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ref:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				info.Branch = strings.TrimPrefix(parts[1], "refs/heads/")
			}
		}
		if strings.HasSuffix(strings.TrimSpace(line), "HEAD") {
			parts := strings.Fields(line)
			if len(parts) >= 1 && len(parts[0]) == 40 {
				info.LatestCommit = parts[0]
			}
		}
	}
	return info, nil
}

func (g *Manager) ListBranches(ctx context.Context, repoURL string) ([]string, error) {
	cloneURL, err := g.ResolveCloneURL(repoURL)
	if err != nil {
		return nil, err
	}
	out, err := g.runGitStdout(ctx, "ls-remote", "--heads", "--", cloneURL)
	if err != nil {
		return nil, fmt.Errorf("ls-remote --heads %s: %w", redactCredentialURLs(repoURL), err)
	}
	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			branches = append(branches, strings.TrimPrefix(parts[1], "refs/heads/"))
		}
	}
	if len(branches) == 0 {
		return nil, errors.New("git repo exists but there are no branches")
	}
	return branches, nil
}

func (g *Manager) GetCommitLog(ctx context.Context, repoURL string, branch string, limit int) ([]CommitInfo, error) {
	if limit <= 0 {
		limit = 30
	}
	branch, err := cleanGitArg("branch", defaultGitRef(branch))
	if err != nil {
		return nil, err
	}
	repoDir, err := g.ensureMetadataRepo(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if fetchErr := g.fetchMetadataRef(ctx, repoDir, branch, "tree:0", limit); fetchErr != nil {
		return nil, fetchErr
	}

	out, err := g.runGit(ctx, repoDir, "log", fmt.Sprintf("--max-count=%d", limit), "--format=%H%x00%s%x00%an%x00%cI%x1e", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	commits := []CommitInfo{}
	for i, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) < 4 {
			continue
		}
		commitTime, _ := time.Parse(time.RFC3339, fields[3])
		ci := CommitInfo{Hash: fields[0], Message: fields[1], Author: fields[2], Time: commitTime}
		if i == 0 && branch != "HEAD" {
			ci.Branch = branch
		}
		commits = append(commits, ci)
	}
	return commits, nil
}

// DiscoverVersions fetches remote branches once, then reads branches and commits
// from the local metadata cache.
func (g *Manager) DiscoverVersions(ctx context.Context, repoURL string, requestedBranch string, limit int) (*VersionDiscovery, error) {
	if limit <= 0 {
		limit = 25
	}
	repoDir, err := g.ensureMetadataRepo(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if _, err := g.runGit(ctx, repoDir, "fetch", "--prune", fmt.Sprintf("--depth=%d", limit), "--filter=tree:0", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, err
	}
	out, err := g.runGit(ctx, repoDir, "for-each-ref", "--format=%(refname:strip=3)", "refs/remotes/origin")
	if err != nil {
		return nil, err
	}
	branches := []string{}
	for _, branch := range strings.Fields(out) {
		if branch != "HEAD" {
			branches = append(branches, branch)
		}
	}
	selectedBranch := selectVersionBranch(branches, requestedBranch)
	result := &VersionDiscovery{Branches: branches, SelectedBranch: selectedBranch}
	if selectedBranch == "" {
		return result, nil
	}

	out, err = g.runGit(ctx, repoDir, "log", fmt.Sprintf("--max-count=%d", limit), "--format=%H%x00%s%x00%an%x00%cI%x1e", "refs/remotes/origin/"+selectedBranch)
	if err != nil {
		return nil, err
	}
	for i, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) < 4 {
			continue
		}
		commitTime, _ := time.Parse(time.RFC3339, fields[3])
		ci := CommitInfo{Hash: fields[0], Message: fields[1], Author: fields[2], Time: commitTime}
		if i == 0 {
			ci.Branch = selectedBranch
		}
		result.Commits = append(result.Commits, ci)
	}
	return result, nil
}

func selectVersionBranch(branches []string, requestedBranch string) string {
	requestedBranch = strings.TrimSpace(requestedBranch)
	if requestedBranch != "" && slices.Contains(branches, requestedBranch) {
		return requestedBranch
	}
	for _, preferred := range []string{"main", "master", "prod"} {
		if slices.Contains(branches, preferred) {
			return preferred
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

// ValidateExactCommit verifies that commit is a full hash available from the
// remote repository. It always fetches the requested hash, even if it is
// already present in the metadata cache.
func (g *Manager) ValidateExactCommit(ctx context.Context, repoURL string, commit string) error {
	_, err := g.validateExactSource(ctx, repoURL, commit, "")
	return err
}

// ValidateExactNixSource verifies an exact remote commit and a regular
// flake.nix file in that commit's tree. commitValid is true when an error is
// limited to flake path validation.
func (g *Manager) ValidateExactNixSource(ctx context.Context, repoURL string, commit string, flakePath string) (commitValid bool, err error) {
	clean, err := CleanFlakePath(flakePath)
	if err != nil {
		return false, err
	}
	return g.validateExactSource(ctx, repoURL, commit, clean)
}

func (g *Manager) validateExactSource(ctx context.Context, repoURL string, commit string, flakePath string) (bool, error) {
	if err := ValidateFullCommitHash(commit); err != nil {
		return false, err
	}
	repoDir, err := g.ensureMetadataRepo(ctx, repoURL)
	if err != nil {
		return false, err
	}
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if err := g.fetchMetadataRef(ctx, repoDir, commit, "blob:none", 1); err != nil {
		if isMissingGitRefError(err) {
			return false, fmt.Errorf("commit %q was not found in the remote repository", commit)
		}
		return false, fmt.Errorf("fetching exact commit: %w", err)
	}
	fetched, err := g.runGit(ctx, repoDir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return false, fmt.Errorf("verifying fetched commit: %w", err)
	}
	fetched = strings.TrimSpace(fetched)
	if !strings.EqualFold(fetched, commit) {
		return false, fmt.Errorf("remote returned commit %q instead of requested commit %q", fetched, commit)
	}
	if flakePath == "" {
		return true, nil
	}

	out, err := g.runGit(ctx, repoDir, "ls-tree", "-z", "FETCH_HEAD", "--", ":(literal)"+flakePath)
	if err != nil {
		return true, fmt.Errorf("inspecting flake path %q: %w", flakePath, err)
	}
	entry := strings.TrimSuffix(out, "\x00")
	if entry == "" {
		return true, fmt.Errorf("flake path %q does not exist at commit %q", flakePath, commit)
	}
	metadata, entryPath, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(metadata)
	if !ok || len(fields) != 3 {
		return true, fmt.Errorf("unexpected Git tree entry for flake path %q", flakePath)
	}
	if entryPath != flakePath {
		return true, fmt.Errorf("Git returned path %q while checking flake path %q", entryPath, flakePath)
	}
	if fields[0] != "100644" && fields[0] != "100755" || fields[1] != "blob" {
		return true, fmt.Errorf("flake path %q is not a regular Git file", flakePath)
	}
	return true, nil
}

func isMissingGitRefError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "not our ref") || strings.Contains(msg, "Server does not allow request for unadvertised object")
}

func (g *Manager) EnsureCheckout(ctx context.Context, repoURL string, ref string, logFile io.Writer) (string, error) {
	if logFile == nil {
		logFile = io.Discard
	}
	ref, err := cleanGitArg("ref", ref)
	if err != nil {
		return "", err
	}
	cloneURL, err := g.ResolveCloneURL(repoURL)
	if err != nil {
		return "", err
	}
	repoDir := g.worktreeDir(repoURL)
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		fmt.Fprintf(logFile, "[%s] fetching %s for %s\n", time.Now().Format(time.RFC3339), ref, redactCredentialURLs(repoURL))
		if err := g.runGitLogged(ctx, repoDir, logFile, "remote", "set-url", "origin", cloneURL); err != nil {
			return "", err
		}
	} else {
		fmt.Fprintf(logFile, "[%s] cloning %s into %s\n", time.Now().Format(time.RFC3339), redactCredentialURLs(repoURL), repoDir)
		if err := g.runGitLogged(ctx, "", logFile, "clone", "--filter=blob:none", "--no-checkout", "--", cloneURL, repoDir); err != nil {
			return "", err
		}
	}
	if err := g.runGitLogged(ctx, repoDir, logFile, "fetch", "--filter=blob:none", "origin", ref); err != nil {
		return "", err
	}
	if err := g.runGitLogged(ctx, repoDir, logFile, "reset", "--hard"); err != nil {
		return "", err
	}
	if err := g.runGitLogged(ctx, repoDir, logFile, "clean", "-fdx"); err != nil {
		return "", err
	}
	if err := g.runGitLogged(ctx, repoDir, logFile, "checkout", "--force", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return repoDir, nil
}

func (g *Manager) ensureMetadataRepo(ctx context.Context, repoURL string) (string, error) {
	cloneURL, err := g.ResolveCloneURL(repoURL)
	if err != nil {
		return "", err
	}
	repoDir := g.metadataDir(repoURL)
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(repoDir, "config")); err != nil {
		if _, err := g.runGit(ctx, "", "init", "--bare", repoDir); err != nil {
			return "", err
		}
	}
	if _, err := g.runGit(ctx, repoDir, "remote", "get-url", "origin"); err != nil {
		if _, err := g.runGit(ctx, repoDir, "remote", "add", "origin", cloneURL); err != nil {
			return "", err
		}
	} else if _, err := g.runGit(ctx, repoDir, "remote", "set-url", "origin", cloneURL); err != nil {
		return "", err
	}
	return repoDir, nil
}

func (g *Manager) fetchMetadataRef(ctx context.Context, repoDir string, ref string, filter string, depth int) error {
	args := []string{"fetch", fmt.Sprintf("--depth=%d", depth), "--filter=" + filter, "origin", ref}
	_, err := g.runGit(ctx, repoDir, args...)
	return err
}

func (g *Manager) runGitLogged(ctx context.Context, dir string, logWriter io.Writer, args ...string) error {
	cmdStr := sanitizeCommandForLogs("git", args)
	fmt.Fprintf(logWriter, "$ %s\n", cmdStr)
	out, err := g.runGit(ctx, dir, args...)
	if strings.TrimSpace(out) != "" {
		fmt.Fprint(logWriter, out)
		if !strings.HasSuffix(out, "\n") {
			fmt.Fprintln(logWriter)
		}
	}
	if err != nil {
		fmt.Fprintf(logWriter, "ERROR %v\n", err)
	}
	return err
}

func (g *Manager) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env, err := g.gitEnv(ctx)
	if err != nil {
		return "", err
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmdStr := sanitizeCommandForLogs("git", args)
		msg := redactGithubToken(string(out))
		if strings.TrimSpace(msg) == "" {
			return string(out), fmt.Errorf("%s: %w", cmdStr, err)
		}
		return string(out), fmt.Errorf("%s: %w: %s", cmdStr, err, strings.TrimSpace(msg))
	}
	return string(out), nil
}

// runGitStdout runs git for callers that parse stdout and must not have stderr
// interleaved into it.
func (g *Manager) runGitStdout(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	env, err := g.gitEnv(ctx)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd.Output()
}

func (g *Manager) metadataDir(repoURL string) string {
	return filepath.Join(g.cacheDir, "metadata", repoKey(repoURL)+".git")
}

func (g *Manager) worktreeDir(repoURL string) string {
	return filepath.Join(g.cacheDir, "worktrees", repoKey(repoURL))
}

func (g *Manager) repoLock(repoURL string) *sync.Mutex {
	lock, _ := g.locks.LoadOrStore(repoKey(repoURL), &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func repoKey(repoURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(repoURL)))
	return hex.EncodeToString(sum[:])
}

func defaultGitRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "HEAD"
	}
	return ref
}

func cleanGitArg(name string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\n\r") {
		return "", fmt.Errorf("%s contains invalid characters", name)
	}
	return value, nil
}

func cleanGitTreePath(repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	clean := path.Clean(repoPath)
	if clean == "." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("path must be relative to the repository")
	}
	if strings.ContainsAny(clean, "\x00\n\r") {
		return "", fmt.Errorf("path contains invalid characters")
	}
	return clean, nil
}

// CleanFlakePath canonicalizes a safe repository-relative flake file path.
func CleanFlakePath(flakePath string) (string, error) {
	clean, err := cleanGitTreePath(flakePath)
	if err != nil {
		return "", err
	}
	if path.Base(clean) != "flake.nix" {
		return "", fmt.Errorf("flake path basename must be flake.nix")
	}
	return clean, nil
}

// ValidateFullCommitHash rejects branches, tags, abbreviated hashes, and
// revision expressions where an immutable Nix version is required.
func ValidateFullCommitHash(commit string) error {
	if !fullCommitHashPattern.MatchString(commit) {
		return fmt.Errorf("commit must be a full 40-character hexadecimal hash")
	}
	return nil
}

func sanitizeCommandForLogs(name string, args []string) string {
	if len(args) == 0 {
		return name
	}

	safeArgs := make([]string, 0, len(args))
	for _, arg := range args {
		safeArgs = append(safeArgs, redactGithubToken(arg))
	}

	return name + " " + strings.Join(safeArgs, " ")
}

func redactGithubToken(s string) string {
	const prefix = "x-access-token:"
	idx := strings.Index(s, prefix)
	if idx == -1 {
		return redactCredentialURLs(s)
	}

	afterPrefix := idx + len(prefix)
	atIdx := strings.Index(s[afterPrefix:], "@")
	if atIdx == -1 {
		return redactCredentialURLs(s)
	}

	atIdx = afterPrefix + atIdx
	return redactCredentialURLs(s[:afterPrefix] + "***" + s[atIdx:])
}

func redactCredentialURLs(s string) string {
	return credentialURLPattern.ReplaceAllString(s, "${1}***@")
}
