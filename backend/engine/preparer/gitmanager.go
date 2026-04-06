package preparer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type GitManager interface {
	FetchRepoInfo(repoURL string) (*RepoInfo, error)
	ListBranches(repoURL string) ([]string, error)
	GetCommitLog(repoURL string, branch string, limit int) ([]CommitInfo, error)
}

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

type GitManagerImpl struct {
	dataDir     string
	githubToken string
}

func NewGitManager(dataDir string, githubToken string) *GitManagerImpl {
	return &GitManagerImpl{dataDir: dataDir, githubToken: githubToken}
}

func (g *GitManagerImpl) resolveCloneURL(repoURL string) string {
	if g.githubToken != "" {
		return fmt.Sprintf("https://x-access-token:%s@%s.git", g.githubToken, repoURL)
	}
	return fmt.Sprintf("https://%s.git", repoURL)
}

func (g *GitManagerImpl) FetchRepoInfo(repoURL string) (*RepoInfo, error) {
	cloneURL := g.resolveCloneURL(repoURL)
	out, err := exec.Command("git", "ls-remote", "--symref", cloneURL, "HEAD").Output()
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

func (g *GitManagerImpl) ListBranches(repoURL string) ([]string, error) {
	cloneURL := g.resolveCloneURL(repoURL)
	out, err := exec.Command("git", "ls-remote", "--heads", cloneURL).Output()
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
	return branches, nil
}

// repoOwnerName extracts "owner/repo" from a URL like "github.com/owner/repo".
func repoOwnerName(repoURL string) (string, error) {
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

// GetCommitLog fetches recent commits from the GitHub API.
func (g *GitManagerImpl) GetCommitLog(repoURL string, branch string, limit int) ([]CommitInfo, error) {
	ownerRepo, err := repoOwnerName(repoURL)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/commits?per_page=%d", ownerRepo, limit)
	if branch != "" {
		url += "&sha=" + branch
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.githubToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}

	var ghCommits []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&ghCommits); err != nil {
		return nil, fmt.Errorf("decoding github response: %w", err)
	}

	// Fetch the default branch name to tag the HEAD commit
	defaultBranch := branch
	if defaultBranch == "" {
		defaultBranch = g.fetchDefaultBranch(ownerRepo)
	}

	commits := make([]CommitInfo, 0, len(ghCommits))
	for i, gc := range ghCommits {
		ci := CommitInfo{
			Hash:    gc.SHA,
			Message: gc.Commit.Message,
			Author:  gc.Commit.Author.Name,
			Time:    gc.Commit.Committer.Date,
		}
		// First commit in the list is the branch tip
		if i == 0 && defaultBranch != "" {
			ci.Branch = defaultBranch
		}
		commits = append(commits, ci)
	}
	return commits, nil
}

func (g *GitManagerImpl) fetchDefaultBranch(ownerRepo string) string {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s", ownerRepo), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.githubToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.NewDecoder(resp.Body).Decode(&repo) == nil {
		return repo.DefaultBranch
	}
	return ""
}

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string   `json:"message"`
		Author    ghPerson `json:"author"`
		Committer ghPerson `json:"committer"`
	} `json:"commit"`
}

type ghPerson struct {
	Name string    `json:"name"`
	Date time.Time `json:"date"`
}
