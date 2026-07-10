package secondary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestPrimaryGithubCredentialsProviderCachesByChangedAt(t *testing.T) {
	requests := 0
	changedAt := time.Unix(123, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/cluster/github-credentials" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/protobuf")
		token := "secret-token"
		if requests > 1 {
			token = "ignored-token"
		}
		_, _ = w.Write((&apigen.GithubCredentials{Token: token, ChangedAt: changedAt}).Encode())
	}))
	defer server.Close()

	provider := NewPrimaryGithubCredentialsProvider(server.URL, server.Client())
	for i := 0; i < 2; i++ {
		creds, err := provider.LoadCredentials(context.Background())
		if err != nil {
			t.Fatalf("LoadCredentials: %v", err)
		}
		if creds.Token != "secret-token" {
			t.Fatalf("Token = %q; want secret-token", creds.Token)
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d; want 2", requests)
	}
}
