package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestListReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.RequestURI(), "/repos/acme/widget/releases?per_page=50"; got != want {
			t.Errorf("request URI = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		if got, want := r.Header.Get("Accept"), "application/vnd.github+json"; got != want {
			t.Errorf("Accept = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.3"}]`))
	}))
	defer server.Close()

	client := NewClient(WithAPIBaseURL(server.URL))
	releases, err := client.ListReleases(context.Background(), "https://github.com/acme/widget.git", 50)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "v1.2.3" {
		t.Fatalf("releases = %#v", releases)
	}
}

func TestDownloadAssetFollowsRedirect(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("artifact"))
	}))
	defer assetServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Accept"), "application/octet-stream"; got != want {
			t.Errorf("Accept = %q, want %q", got, want)
		}
		http.Redirect(w, r, assetServer.URL+"/artifact", http.StatusFound)
	}))
	defer apiServer.Close()

	dstPath := filepath.Join(t.TempDir(), "artifact")
	client := NewClient()
	if err := client.DownloadAsset(context.Background(), apiServer.URL+"/asset", dstPath); err != nil {
		t.Fatalf("DownloadAsset: %v", err)
	}
	contents, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "artifact"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}
