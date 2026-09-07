// Package registryauth supplies GHCR authentication to registry discovery and pulls.
package registryauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

// Client creates an operation-scoped client. Credentials are loaded afresh so
// configuration changes and secret rotation apply to the next discovery or pull.
// Other registries never load or receive the configured GitHub credential.
func Client(ctx context.Context, image string, provider githubcredentials.Provider) (*http.Client, error) {
	ref, err := imageref.Parse(image)
	if err != nil {
		return nil, err
	}
	client := *http.DefaultClient
	if !strings.EqualFold(ref.Registry, "ghcr.io") {
		return &client, nil
	}
	var token string
	if provider != nil {
		creds, err := provider.LoadCredentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading GitHub registry credentials: %w", err)
		}
		if creds != nil {
			token = creds.Token
		}
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = ghcrTransport{base: base, token: token}
	return &client, nil
}

type ghcrTransport struct {
	base  http.RoundTripper
	token string
}

func (t ghcrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	trusted := req.URL.Scheme == "https" && strings.EqualFold(req.URL.Host, "ghcr.io") && req.URL.User == nil
	// Apply the PAT only at the known token endpoint, never at a challenge's
	// arbitrary realm. Both discovery and containerd use this GET token exchange.
	// Inject at transport time so redirects cannot copy the PAT from the request.
	if trusted && req.URL.Path == "/token" && req.Method == http.MethodGet && t.token != "" {
		req.SetBasicAuth("token", t.token)
	} else if !trusted || strings.HasPrefix(strings.ToLower(req.Header.Get("Authorization")), "basic ") {
		// Signed blob URLs may live elsewhere. Allow their download but never
		// forward registry bearer tokens to those hosts, including subdomains.
		req.Header.Del("Authorization")
	}
	return t.base.RoundTrip(req)
}
