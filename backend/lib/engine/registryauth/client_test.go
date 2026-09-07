package registryauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/opencontainers/go-digest"
)

type providerFunc func(context.Context) (*githubcredentials.GithubCredentials, error)

func (f providerFunc) LoadCredentials(ctx context.Context) (*githubcredentials.GithubCredentials, error) {
	return f(ctx)
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
func useTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	t.Cleanup(func() { http.DefaultClient = old })
}

func TestCredentialScope(t *testing.T) {
	for _, tc := range []struct {
		url, auth     string
		basic, bearer bool
	}{
		{"https://ghcr.io/token?scope=repository:acme/app:pull", "", true, false},
		{"https://ghcr.io/v2/acme/app/manifests/latest", "Bearer registry-token", false, true},
		{"https://ghcr.io/v2/acme/app/manifests/latest", "Basic copied-secret", false, false},
		{"http://ghcr.io/token", "Bearer registry-token", false, false},
		{"https://ghcr.io:444/token", "Bearer registry-token", false, false},
		{"https://sub.ghcr.io/token", "Bearer registry-token", false, false},
		{"https://ghcr.io.evil.example/token", "Bearer registry-token", false, false},
		{"https://cdn.example/blob", "Bearer registry-token", false, false},
		{"https://ghcr.io/token/other", "", false, false},
	} {
		t.Run(tc.url+tc.auth, func(t *testing.T) {
			transport := ghcrTransport{token: "pat-secret", base: transportFunc(func(r *http.Request) (*http.Response, error) {
				user, pass, ok := r.BasicAuth()
				if ok != tc.basic || (ok && (user != "token" || pass != "pat-secret")) {
					t.Fatalf("unexpected basic credentials for %s", r.URL)
				}
				if tc.bearer {
					if r.Header.Get("Authorization") != "Bearer registry-token" {
						t.Fatal("missing registry bearer")
					}
				} else if !tc.basic && r.Header.Get("Authorization") != "" {
					t.Fatal("credentials escaped scope")
				}
				return response(r, 200, ""), nil
			})}
			req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
			req.Header.Set("Authorization", tc.auth)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if req.Header.Get("Authorization") != tc.auth {
				t.Fatal("transport mutated caller's headers")
			}
		})
	}
}

func TestCredentialsLoadedPerGHCRClient(t *testing.T) {
	calls := 0
	provider := providerFunc(func(context.Context) (*githubcredentials.GithubCredentials, error) {
		calls++
		return &githubcredentials.GithubCredentials{Token: fmt.Sprintf("pat-%d", calls)}, nil
	})
	for _, ref := range []string{"postgres", "ghcr.io.evil.example/acme/app", "ghcr.io:444/acme/app"} {
		if _, err := Client(context.Background(), ref, provider); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("loaded GitHub credentials for another registry")
	}
	for i := 1; i <= 2; i++ {
		client, err := Client(context.Background(), "ghcr.io/acme/app", provider)
		if err != nil {
			t.Fatal(err)
		}
		if client.Transport.(ghcrTransport).token != fmt.Sprintf("pat-%d", i) {
			t.Fatal("stale credential")
		}
	}
	want := errors.New("secret unavailable")
	if _, err := Client(context.Background(), "ghcr.io/acme/app", providerFunc(func(context.Context) (*githubcredentials.GithubCredentials, error) { return nil, want })); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	client, err := Client(context.Background(), "ghcr.io/acme/app", nil)
	if err != nil || client.Transport.(ghcrTransport).token != "" {
		t.Fatal("nil provider should be anonymous")
	}
}

func TestTokenRedirectDoesNotForwardCredentials(t *testing.T) {
	calls := 0
	useTransport(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if _, pass, ok := r.BasicAuth(); !ok || pass != "pat-secret" {
				t.Fatal("missing token endpoint auth")
			}
			resp := response(r, http.StatusFound, "")
			resp.Header.Set("Location", "https://sub.ghcr.io/token")
			return resp, nil
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("credential forwarded on redirect")
		}
		return response(r, 200, `{"token":"registry-token"}`), nil
	}))
	client, err := Client(context.Background(), "ghcr.io/acme/app", providerFunc(func(context.Context) (*githubcredentials.GithubCredentials, error) {
		return &githubcredentials.GithubCredentials{Token: "pat-secret"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("https://ghcr.io/token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != 2 {
		t.Fatalf("requests = %d", calls)
	}
}

// Exercise the real containerd resolver and fetcher without requiring a daemon.
func TestContainerdResolveAndFetch(t *testing.T) {
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`
	manifestDigest := digest.FromString(manifest)
	exchanges, fetches := 0, 0
	useTransport(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ghcr.io" {
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		if r.URL.Path == "/token" {
			exchanges++
			if _, pass, ok := r.BasicAuth(); !ok || pass != "pat-secret" {
				t.Fatal("missing PAT on token exchange")
			}
			if r.URL.Query().Get("scope") != "repository:acme/app:pull" {
				t.Fatalf("scope = %s", r.URL.RawQuery)
			}
			return response(r, 200, `{"token":"registry-token","expires_in":300}`), nil
		}
		if r.Header.Get("Authorization") == "" {
			resp := response(r, 401, "")
			resp.Header.Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:acme/app:pull"`)
			return resp, nil
		}
		if r.Header.Get("Authorization") != "Bearer registry-token" {
			t.Fatal("PAT used for image content")
		}
		if r.Method == http.MethodGet {
			fetches++
		}
		resp := response(r, 200, manifest)
		resp.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		resp.Header.Set("Docker-Content-Digest", manifestDigest.String())
		return resp, nil
	}))
	ctx := context.Background()
	client, err := Client(ctx, "ghcr.io/acme/app:latest", providerFunc(func(context.Context) (*githubcredentials.GithubCredentials, error) {
		return &githubcredentials.GithubCredentials{Token: "pat-secret"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	resolver := docker.NewResolver(docker.ResolverOptions{Client: client})
	name, desc, err := resolver.Resolve(ctx, "ghcr.io/acme/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Digest != manifestDigest {
		t.Fatalf("digest = %s", desc.Digest)
	}
	fetcher, err := resolver.Fetcher(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(body) != manifest {
		t.Fatalf("fetch failed: %v", err)
	}
	if exchanges != 1 || fetches != 1 {
		t.Fatalf("exchanges=%d fetches=%d", exchanges, fetches)
	}
}
