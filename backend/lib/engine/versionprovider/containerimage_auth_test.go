package versionprovider

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

type registryTestTransport func(*http.Request) (*http.Response, error)

func (f registryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type registryTestCredentials struct{ token string }

func (p registryTestCredentials) LoadCredentials(context.Context) (*githubcredentials.GithubCredentials, error) {
	return &githubcredentials.GithubCredentials{Token: p.token}, nil
}

func TestGHCRDiscoveryAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, image, pat, realm string
		status                  int
		want                    []string
	}{
		{"tags", "ghcr.io/acme/app", "pat-secret", "https://ghcr.io/token", 200, []string{"v2", "v1"}},
		{"tag manifest", "ghcr.io/acme/app:v1", "pat-secret", "https://ghcr.io/token", 200, []string{"v1"}},
		{"digest manifest", "ghcr.io/acme/app@sha256:abc", "pat-secret", "https://ghcr.io/token", 200, []string{"sha256:abc"}},
		{"anonymous", "ghcr.io/acme/app", "", "https://ghcr.io/token", 200, []string{"v2", "v1"}},
		{"rejected credentials", "ghcr.io/acme/app", "pat-secret", "https://ghcr.io/token", 403, nil},
		{"untrusted realm", "ghcr.io/acme/app", "pat-secret", "https://auth.example/token", 403, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = old })
			exchanges := 0
			http.DefaultClient = &http.Client{Transport: registryTestTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, ""
				headers := make(http.Header)
				switch {
				case r.URL.Path == "/token":
					exchanges++
					_, pass, basic := r.BasicAuth()
					trusted := r.URL.Host == "ghcr.io"
					if trusted && tc.pat != "" {
						if !basic || pass != tc.pat {
							t.Fatal("missing configured PAT")
						}
					} else if r.Header.Get("Authorization") != "" {
						t.Fatal("credentials escaped scope")
					}
					status, body = tc.status, `{"token":"registry-bearer"}`
				case r.URL.Host == "cdn.example":
					if r.Header.Get("Authorization") != "" {
						t.Fatal("registry bearer leaked through pagination")
					}
					body = `{"tags":["v2"]}`
				default:
					if r.Header.Get("Authorization") == "" {
						status = 401
						headers.Set("WWW-Authenticate", `Bearer realm="`+tc.realm+`",service="ghcr.io",scope="repository:acme/app:pull"`)
					} else {
						if r.Header.Get("Authorization") != "Bearer registry-bearer" {
							t.Fatal("PAT sent to registry API")
						}
						if strings.HasSuffix(r.URL.Path, "/tags/list") {
							body = `{"tags":["v1"]}`
							headers.Set("Link", `<https://cdn.example/page2>; rel="next"`)
						} else {
							body = `{}`
						}
					}
				}
				return &http.Response{Request: r, StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			versions, err := ListContainerImageTags(context.Background(), tc.image, registryTestCredentials{tc.pat})
			if tc.status != 200 {
				if err == nil {
					t.Fatal("expected auth failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, v := range versions {
				got = append(got, v.ID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("versions = %v, want %v", got, tc.want)
			}
			if exchanges != 1 {
				t.Fatalf("token exchanges = %d", exchanges)
			}
		})
	}
}
