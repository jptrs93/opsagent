package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

type testCredentialsProvider struct {
	token string
}

func (p testCredentialsProvider) LoadCredentials(context.Context) (*githubcredentials.GithubCredentials, error) {
	return &githubcredentials.GithubCredentials{Token: p.token}, nil
}

func TestListReleasesAuthenticatesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.RequestURI(), "/repos/acme/widget/releases?per_page=50"; got != want {
			t.Errorf("request URI = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer secret"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Accept"), "application/vnd.github+json"; got != want {
			t.Errorf("Accept = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.3"}]`))
	}))
	defer server.Close()

	client := NewClient(nil, testCredentialsProvider{token: "secret"}, WithAPIBaseURL(server.URL))
	releases, err := client.ListReleases(context.Background(), "https://github.com/acme/widget.git", 50)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "v1.2.3" {
		t.Fatalf("releases = %#v", releases)
	}
}

func TestDownloadAssetStripsAuthAfterRedirect(t *testing.T) {
	var redirectedAuth string
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("artifact"))
	}))
	defer assetServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer secret"; got != want {
			t.Errorf("initial Authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Accept"), "application/octet-stream"; got != want {
			t.Errorf("initial Accept = %q, want %q", got, want)
		}
		http.Redirect(w, r, assetServer.URL+"/artifact", http.StatusFound)
	}))
	defer apiServer.Close()

	dstPath := filepath.Join(t.TempDir(), "artifact")
	client := NewClient(nil, testCredentialsProvider{token: "secret"})
	if err := client.DownloadAsset(context.Background(), apiServer.URL+"/asset", dstPath); err != nil {
		t.Fatalf("DownloadAsset: %v", err)
	}
	if redirectedAuth != "" {
		t.Fatalf("redirected Authorization = %q, want empty", redirectedAuth)
	}
	contents, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "artifact"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}
