package preparer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

type GitManager struct {
	dataDir     string
	credentials githubcredentials.Provider
	locks       sync.Map
}

func NewGitManager(dataDir string, provider githubcredentials.Provider) *GitManager {
	return &GitManager{dataDir: dataDir, credentials: provider}
}

func (g *GitManager) ResolveCloneURL(ctx context.Context, repoURL string) (string, error) {
	creds, err := g.credentials.LoadCredentials(ctx)
	if err != nil {
		return "", err
	}
	return resolveCloneURL(repoURL, creds.Token)
}

func resolveCloneURL(repoURL string, githubToken string) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", fmt.Errorf("repo is required")
	}
	if strings.ContainsAny(repoURL, "\x00\n\r") {
		return "", fmt.Errorf("repo URL contains invalid characters")
	}
	if _, err := os.Stat(repoURL); err == nil {
		return repoURL, nil
	}
	if strings.HasPrefix(repoURL, "git@") {
		return injectGithubToken(repoURL, githubToken)
	}
	if strings.Contains(repoURL, "://") {
		u, err := url.Parse(repoURL)
		if err == nil && (u.Scheme == "https" || u.Scheme == "http") && !strings.HasSuffix(u.Path, ".git") {
			u.Path += ".git"
			repoURL = u.String()
		}
		return injectGithubToken(repoURL, githubToken)
	}

	parts := strings.Split(repoURL, "/")
	if len(parts) < 3 || parts[0] == "" {
		return "", fmt.Errorf("cannot parse git repo from %q", repoURL)
	}
	cloneURL := "https://" + strings.TrimSuffix(repoURL, ".git") + ".git"
	return injectGithubToken(cloneURL, githubToken)
}

func injectGithubToken(cloneURL string, githubToken string) (string, error) {
	if githubToken == "" || strings.HasPrefix(cloneURL, "git@") {
		return cloneURL, nil
	}
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host != "github.com" || u.Scheme != "https" {
		return cloneURL, nil
	}
	u.User = url.UserPassword("x-access-token", githubToken)
	return u.String(), nil
}

func (g *GitManager) FetchRepoInfo(ctx context.Context, repoURL string) (*RepoInfo, error) {
	cloneURL, err := g.ResolveCloneURL(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--symref", cloneURL, "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("ls-remote %s: %w", repoURL, err)
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

func (g *GitManager) ListBranches(ctx context.Context, repoURL string) ([]string, error) {
	cloneURL, err := g.ResolveCloneURL(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--heads", cloneURL).Output()
	if err != nil {
		return nil, fmt.Errorf("ls-remote --heads %s: %w", repoURL, err)
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

// RepoOwnerName extracts "owner/repo" from a URL like "github.com/owner/repo".
func RepoOwnerName(repoURL string) (string, error) {
	repoURL = strings.TrimPrefix(repoURL, "https://")
	repoURL = strings.TrimPrefix(repoURL, "http://")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	// "github.com/owner/repo" -> "owner/repo"
	parts := strings.SplitN(repoURL, "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		return "", fmt.Errorf("cannot parse owner/repo from %q", repoURL)
	}
	return parts[1], nil
}

func (g *GitManager) GetCommitLog(ctx context.Context, repoURL string, branch string, limit int) ([]CommitInfo, error) {
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

	if err := g.fetchMetadataRef(ctx, repoDir, branch, "tree:0", limit); err != nil {
		return nil, err
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

func (g *GitManager) CommitExists(ctx context.Context, repoURL string, commit string) (bool, error) {
	commit, err := cleanGitArg("commit", commit)
	if err != nil {
		return false, err
	}
	repoDir, err := g.ensureMetadataRepo(ctx, repoURL)
	if err != nil {
		return false, err
	}
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if g.gitObjectExists(ctx, repoDir, commit+"^{commit}") {
		return true, nil
	}
	if err := g.fetchMetadataRef(ctx, repoDir, commit, "tree:0", 1); err != nil {
		if isMissingGitRefError(err) {
			return false, nil
		}
		return false, err
	}
	return g.gitObjectExists(ctx, repoDir, "FETCH_HEAD^{commit}"), nil
}

func isMissingGitRefError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "not our ref") || strings.Contains(msg, "Server does not allow request for unadvertised object")
}

func (g *GitManager) PathExists(ctx context.Context, repoURL string, repoPath string, ref string) (bool, error) {
	repoPath, err := cleanGitTreePath(repoPath)
	if err != nil {
		return false, err
	}
	ref, err = cleanGitArg("ref", defaultGitRef(ref))
	if err != nil {
		return false, err
	}
	repoDir, err := g.ensureMetadataRepo(ctx, repoURL)
	if err != nil {
		return false, err
	}
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if exists, ok := g.localPathExists(ctx, repoDir, ref, repoPath); ok {
		return exists, nil
	}
	if err := g.fetchMetadataRef(ctx, repoDir, ref, "blob:none", 1); err != nil {
		return false, err
	}
	exists, _ := g.localPathExists(ctx, repoDir, "FETCH_HEAD", repoPath)
	return exists, nil
}

func (g *GitManager) EnsureCheckout(ctx context.Context, repoURL string, ref string, logFile io.Writer) (string, error) {
	if logFile == nil {
		logFile = io.Discard
	}
	ref, err := cleanGitArg("ref", ref)
	if err != nil {
		return "", err
	}
	cloneURL, err := g.ResolveCloneURL(ctx, repoURL)
	if err != nil {
		return "", err
	}
	repoDir := g.worktreeDir(repoURL)
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		fmt.Fprintf(logFile, "[%s] fetching %s for %s\n", time.Now().Format(time.RFC3339), ref, repoURL)
		if err := g.runGitLogged(ctx, repoDir, logFile, "remote", "set-url", "origin", cloneURL); err != nil {
			return "", err
		}
	} else {
		fmt.Fprintf(logFile, "[%s] cloning %s into %s\n", time.Now().Format(time.RFC3339), repoURL, repoDir)
		if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
			return "", fmt.Errorf("creating repo dir: %w", err)
		}
		if err := g.runGitLogged(ctx, "", logFile, "clone", "--filter=blob:none", "--no-checkout", cloneURL, repoDir); err != nil {
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

func (g *GitManager) ensureMetadataRepo(ctx context.Context, repoURL string) (string, error) {
	cloneURL, err := g.ResolveCloneURL(ctx, repoURL)
	if err != nil {
		return "", err
	}
	repoDir := g.metadataDir(repoURL)
	lock := g.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(repoDir, "config")); err != nil {
		if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
			return "", fmt.Errorf("creating git metadata cache dir: %w", err)
		}
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

func (g *GitManager) fetchMetadataRef(ctx context.Context, repoDir string, ref string, filter string, depth int) error {
	args := []string{"fetch", fmt.Sprintf("--depth=%d", depth), "--filter=" + filter, "origin", ref}
	_, err := g.runGit(ctx, repoDir, args...)
	return err
}

func (g *GitManager) gitObjectExists(ctx context.Context, repoDir string, object string) bool {
	_, err := g.runGit(ctx, repoDir, "cat-file", "-e", object)
	return err == nil
}

func (g *GitManager) localPathExists(ctx context.Context, repoDir string, ref string, repoPath string) (bool, bool) {
	if !g.gitObjectExists(ctx, repoDir, ref+"^{commit}") || !g.gitObjectExists(ctx, repoDir, ref+"^{tree}") {
		return false, false
	}
	return g.gitObjectExists(ctx, repoDir, ref+":"+repoPath), true
}

func (g *GitManager) runGitLogged(ctx context.Context, dir string, logWriter io.Writer, args ...string) error {
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

func (g *GitManager) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
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

func (g *GitManager) metadataDir(repoURL string) string {
	return filepath.Join(g.dataDir, "git-cache", "metadata", repoKey(repoURL)+".git")
}

func (g *GitManager) worktreeDir(repoURL string) string {
	return filepath.Join(g.dataDir, "git-cache", "worktrees", repoKey(repoURL))
}

func (g *GitManager) repoLock(repoURL string) *sync.Mutex {
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
	clean := path.Clean(strings.Trim(strings.TrimSpace(repoPath), "/"))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("path must be relative to the repository")
	}
	return clean, nil
}
