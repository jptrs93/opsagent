package secondary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestPrimaryGithubCredentialsProviderCaches(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/cluster/github-credentials" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/protobuf")
		_, _ = w.Write((&apigen.GithubCredentials{Token: "secret-token"}).Encode())
	}))
	defer server.Close()

	provider := NewPrimaryGithubCredentialsProvider(server.URL, server.Client())
	for i := 0; i < 2; i++ {
		creds, err := provider.GithubCredentials(context.Background())
		if err != nil {
			t.Fatalf("GithubCredentials: %v", err)
		}
		if creds.Token != "secret-token" {
			t.Fatalf("Token = %q; want secret-token", creds.Token)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d; want 1", requests)
	}
}
