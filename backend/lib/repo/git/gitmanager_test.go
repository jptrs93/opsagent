package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

type testGithubCredentialsProvider struct {
	token string
}

func (p testGithubCredentialsProvider) LoadCredentials(context.Context) (*githubcredentials.GithubCredentials, error) {
	return &githubcredentials.GithubCredentials{Token: p.token}, nil
}

func TestManagerResolveCloneURLNormalizesGithubRepo(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{
			name: "bare github host path",
			repo: "github.com/acme/widget",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
		{
			name: "https url",
			repo: "https://github.com/acme/widget",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
		{
			name: "https url with git suffix",
			repo: "https://github.com/acme/widget.git",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewManager("", testGithubCredentialsProvider{token: "secret"}).ResolveCloneURL(context.Background(), tt.repo)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("ResolveCloneURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagerUsesGitForDiscovery(t *testing.T) {
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runTestGit(t, "", "init", repoDir)
	runTestGit(t, repoDir, "config", "user.email", "test@example.com")
	runTestGit(t, repoDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(repoDir, "flake.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, "nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "nix", "default.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", ".")
	runTestGit(t, repoDir, "commit", "-m", "initial commit")
	runTestGit(t, repoDir, "branch", "-M", "prod")
	runTestGit(t, repoDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repoDir, "app.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", ".")
	runTestGit(t, repoDir, "commit", "-m", "feature commit")
	featureCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	git := NewManager(filepath.Join(t.TempDir(), "cache"), testGithubCredentialsProvider{})
	branches, err := git.ListBranches(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(branches, "feature") {
		t.Fatalf("ListBranches() = %v, want feature", branches)
	}

	commits, err := git.GetCommitLog(ctx, repoDir, "feature", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) == 0 || commits[0].Hash != featureCommit || commits[0].Message != "feature commit" {
		t.Fatalf("GetCommitLog()[0] = %#v, want feature commit %s", commits, featureCommit)
	}

	discovery, err := git.DiscoverVersions(ctx, repoDir, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.SelectedBranch != "prod" {
		t.Fatalf("selected branch = %q, want prod", discovery.SelectedBranch)
	}
	if len(discovery.Commits) == 0 {
		t.Fatal("expected commits for selected branch")
	}

}

func TestManagerValidatesExactOldCommitBeyondDiscoveryWindow(t *testing.T) {
	ctx := context.Background()
	repoDir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "flake.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", "flake.nix")
	runTestGit(t, repoDir, "commit", "-m", "old source")
	oldCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	for i := 0; i < 30; i++ {
		if err := os.WriteFile(filepath.Join(repoDir, "counter"), []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		runTestGit(t, repoDir, "add", "counter")
		runTestGit(t, repoDir, "commit", "-m", fmt.Sprintf("change %d", i))
	}

	manager := NewManager(filepath.Join(t.TempDir(), "cache"), testGithubCredentialsProvider{})
	commits, err := manager.GetCommitLog(ctx, repoDir, "HEAD", 25)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(commits, func(commit CommitInfo) bool { return commit.Hash == oldCommit }) {
		t.Fatal("old commit unexpectedly appeared in 25-entry discovery window")
	}
	if commitValid, err := manager.ValidateExactNixSource(ctx, repoDir, oldCommit, "flake.nix"); err != nil || !commitValid {
		t.Fatalf("ValidateExactNixSource() = (%v, %v), want (true, nil)", commitValid, err)
	}
}

func TestManagerExactValidationRejectsRefsAndShortHashes(t *testing.T) {
	manager := NewManager(t.TempDir(), testGithubCredentialsProvider{})
	for _, commit := range []string{"main", "HEAD", "deadbeef", strings.Repeat("a", 39), strings.Repeat("g", 40), strings.Repeat("a", 40) + "^"} {
		t.Run(commit, func(t *testing.T) {
			if err := manager.ValidateExactCommit(context.Background(), "unused", commit); err == nil || !strings.Contains(err.Error(), "full 40-character") {
				t.Fatalf("ValidateExactCommit(%q) error = %v, want full hash rejection", commit, err)
			}
		})
	}
}

func TestManagerExactValidationAlwaysContactsRemote(t *testing.T) {
	repoDir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "flake.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", "flake.nix")
	runTestGit(t, repoDir, "commit", "-m", "source")
	commit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))
	manager := NewManager(filepath.Join(t.TempDir(), "cache"), testGithubCredentialsProvider{})
	if err := manager.ValidateExactCommit(context.Background(), repoDir, commit); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.ValidateExactCommit(ctx, repoDir, commit); err == nil {
		t.Fatal("warm-cache validation succeeded with canceled context; expected a new Git operation")
	}
}

func TestManagerExactValidationRedactsCredentialURL(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "cache"), testGithubCredentialsProvider{})
	err := manager.ValidateExactCommit(context.Background(), "https://user:super-secret@127.0.0.1:1/acme/app.git", strings.Repeat("a", 40))
	if err == nil {
		t.Fatal("expected inaccessible repository error")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("error leaked credential URL: %v", err)
	}
}

func TestManagerExactNixSourceRequiresRegularGitFile(t *testing.T) {
	repoDir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "real.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "flake.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", ".")
	runTestGit(t, repoDir, "commit", "-m", "regular")
	regularCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	if err := os.Remove(filepath.Join(repoDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, "flake.nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "flake.nix", "default.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", "-A")
	runTestGit(t, repoDir, "commit", "-m", "directory")
	directoryCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	if err := os.RemoveAll(filepath.Join(repoDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.nix", filepath.Join(repoDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", "-A")
	runTestGit(t, repoDir, "commit", "-m", "symlink")
	symlinkCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	if err := os.Remove(filepath.Join(repoDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repoDir, "add", "-A")
	runTestGit(t, repoDir, "commit", "-m", "missing")
	missingCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	runTestGit(t, repoDir, "update-index", "--add", "--cacheinfo", "160000,"+regularCommit+",flake.nix")
	runTestGit(t, repoDir, "commit", "-m", "gitlink")
	gitlinkCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

	manager := NewManager(filepath.Join(t.TempDir(), "cache"), testGithubCredentialsProvider{})
	if commitValid, err := manager.ValidateExactNixSource(context.Background(), repoDir, regularCommit, "./flake.nix"); err != nil || !commitValid {
		t.Fatalf("regular file validation = (%v, %v), want (true, nil)", commitValid, err)
	}
	for name, commit := range map[string]string{
		"directory": directoryCommit,
		"symlink":   symlinkCommit,
		"missing":   missingCommit,
		"submodule": gitlinkCommit,
	} {
		t.Run(name, func(t *testing.T) {
			commitValid, err := manager.ValidateExactNixSource(context.Background(), repoDir, commit, "flake.nix")
			if err == nil || !commitValid {
				t.Fatalf("ValidateExactNixSource() = (%v, %v), want verified commit and flake error", commitValid, err)
			}
		})
	}
	if commitValid, err := manager.ValidateExactNixSource(context.Background(), repoDir, regularCommit, ":(glob)**/flake.nix"); err == nil || !commitValid {
		t.Fatalf("pathspec magic validation = (%v, %v), want verified commit and missing literal path", commitValid, err)
	}
}

func TestCleanFlakePath(t *testing.T) {
	if got, err := CleanFlakePath("./nix/../flake.nix"); err != nil || got != "flake.nix" {
		t.Fatalf("CleanFlakePath() = (%q, %v), want flake.nix", got, err)
	}
	for _, flakePath := range []string{"/flake.nix", "../flake.nix", "nix/../../flake.nix", "default.nix", "flake.nix\nother"} {
		t.Run(flakePath, func(t *testing.T) {
			if _, err := CleanFlakePath(flakePath); err == nil {
				t.Fatalf("CleanFlakePath(%q) succeeded", flakePath)
			}
		})
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runTestGit(t, "", "init", repoDir)
	runTestGit(t, repoDir, "config", "user.email", "test@example.com")
	runTestGit(t, repoDir, "config", "user.name", "Test User")
	return repoDir
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}
