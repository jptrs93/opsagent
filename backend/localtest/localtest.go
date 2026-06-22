package localtest

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const BaseURL = "http://opendeploy-local-repo:8080"

func Enabled() bool {
	return os.Getenv("OPENDEPLOY_LOCAL_TEST") == "true"
}

func APIURL(path string) string {
	return BaseURL + cleanPath(path)
}

func DownloadURL(path string) string {
	return BaseURL + cleanPath(path)
}

func GithubRepoURL(ownerRepo string) string {
	return fmt.Sprintf("%s/%s.git", BaseURL, strings.Trim(strings.TrimSuffix(ownerRepo, ".git"), "/"))
}

func RewriteGithubRepoURL(repoURL string) (string, bool) {
	if !Enabled() {
		return repoURL, false
	}
	ownerRepo, ok := githubOwnerRepo(repoURL)
	if !ok {
		return repoURL, false
	}
	return GithubRepoURL(ownerRepo), true
}

func githubOwnerRepo(repoURL string) (string, bool) {
	repoURL = strings.TrimSpace(repoURL)
	repoURL = strings.TrimSuffix(repoURL, ".git")
	if repoURL == "" || strings.ContainsAny(repoURL, "\x00\n\r") {
		return "", false
	}

	if strings.HasPrefix(repoURL, "git@github.com:") {
		return cleanOwnerRepo(strings.TrimPrefix(repoURL, "git@github.com:"))
	}

	if strings.Contains(repoURL, "://") {
		u, err := url.Parse(repoURL)
		if err != nil || u.Host != "github.com" {
			return "", false
		}
		return cleanOwnerRepo(strings.TrimPrefix(u.Path, "/"))
	}

	if strings.HasPrefix(repoURL, "github.com/") {
		return cleanOwnerRepo(strings.TrimPrefix(repoURL, "github.com/"))
	}
	return "", false
}

func cleanOwnerRepo(ownerRepo string) (string, bool) {
	parts := strings.Split(strings.Trim(ownerRepo, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func cleanPath(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}
