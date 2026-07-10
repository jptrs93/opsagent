package git

import (
	"context"
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

func TestManagerUsesGitForCommitAndPathValidation(t *testing.T) {
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
	initialCommit := strings.TrimSpace(runTestGit(t, repoDir, "rev-parse", "HEAD"))

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

	exists, err := git.CommitExists(ctx, repoDir, initialCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("CommitExists(%s) = false, want true", initialCommit)
	}

	exists, err = git.CommitExists(ctx, repoDir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("CommitExists(deadbeef...) = true, want false")
	}

	exists, err = git.PathExists(ctx, repoDir, "flake.nix", initialCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("PathExists(flake.nix) = false, want true")
	}

	exists, err = git.PathExists(ctx, repoDir, "nix", initialCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("PathExists(nix directory) = false, want true")
	}
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
