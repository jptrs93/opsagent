package versionprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const dockerHubRegistry = "registry-1.docker.io"

// ContainerImageVersionProvider lists public image tags via the Docker Registry
// HTTP API. It supports Kubernetes-style image names, including Docker Hub
// shorthand such as "postgres" and "library/postgres".
type ContainerImageVersionProvider struct{}

func (ContainerImageVersionProvider) ListScopes(ctx context.Context, cfg *apigen.PrepareConfig) ([]string, error) {
	return nil, nil
}

func (ContainerImageVersionProvider) ListVersions(ctx context.Context, cfg *apigen.PrepareConfig, scope string) ([]*apigen.Version, error) {
	if cfg == nil || cfg.ContainerImage.Image == "" {
		return nil, fmt.Errorf("container image config missing")
	}
	ref, err := parseImageRepository(cfg.ContainerImage.Image)
	if err != nil {
		return nil, err
	}
	if ref.version != "" {
		ok, err := imageVersionExists(ctx, ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("image version %q not found", ref.version)
		}
		return []*apigen.Version{{ID: ref.version, Label: ref.version}}, nil
	}
	tags, err := listImageTags(ctx, ref)
	if err != nil {
		return nil, err
	}
	versions := make([]*apigen.Version, 0, len(tags))
	for _, tag := range tags {
		versions = append(versions, &apigen.Version{ID: tag, Label: tag})
	}
	return versions, nil
}

type imageRepository struct {
	registry string
	name     string
	version  string
}

func ContainerImageRepositoryURL(raw string) (string, error) {
	ref, err := parseImageRepository(raw)
	if err != nil {
		return "", err
	}
	return ref.repositoryURL(), nil
}

func (r imageRepository) repositoryURL() string {
	return fmt.Sprintf("https://%s/v2/%s", r.registry, r.name)
}

func parseImageRepository(raw string) (imageRepository, error) {
	image := strings.TrimSpace(raw)
	if image == "" {
		return imageRepository{}, fmt.Errorf("image is required")
	}
	image = strings.TrimPrefix(image, "docker://")
	image = strings.TrimPrefix(image, "https://")
	image = strings.TrimPrefix(image, "http://")
	image = strings.TrimSuffix(image, "/")
	version := imageTagOrDigest(image)
	image = stripImageTagOrDigest(image)
	parts := strings.Split(image, "/")
	if len(parts) == 0 || parts[0] == "" {
		return imageRepository{}, fmt.Errorf("invalid image")
	}

	registry := dockerHubRegistry
	nameParts := parts
	if looksLikeRegistry(parts[0]) {
		registry = parts[0]
		nameParts = parts[1:]
	}
	if len(nameParts) == 0 || nameParts[0] == "" {
		return imageRepository{}, fmt.Errorf("invalid image repository")
	}
	if registry == dockerHubRegistry && len(nameParts) == 1 {
		nameParts = append([]string{"library"}, nameParts...)
	}
	return imageRepository{registry: registry, name: strings.Join(nameParts, "/"), version: version}, nil
}

func imageTagOrDigest(image string) string {
	if idx := strings.IndexByte(image, '@'); idx >= 0 {
		return image[idx+1:]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon > lastSlash {
		return image[lastColon+1:]
	}
	return ""
}

func stripImageTagOrDigest(image string) string {
	if idx := strings.IndexByte(image, '@'); idx >= 0 {
		return image[:idx]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon > lastSlash {
		return image[:lastColon]
	}
	return image
}

func looksLikeRegistry(first string) bool {
	return strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost"
}

func listImageTags(ctx context.Context, ref imageRepository) ([]string, error) {
	client := http.DefaultClient
	nextURL := fmt.Sprintf("%s/tags/list?n=100", ref.repositoryURL())
	var token string
	var tags []string

	for nextURL != "" {
		resp, err := registryGet(ctx, client, nextURL, token)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			challenge := resp.Header.Get("WWW-Authenticate")
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			token, err = registryBearerToken(ctx, client, challenge)
			if err != nil {
				return nil, err
			}
			resp, err = registryGet(ctx, client, nextURL, token)
			if err != nil {
				return nil, err
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("registry tag listing failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var out struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			return nil, err
		}
		tags = append(tags, out.Tags...)
		nextURL = nextRegistryPageURL(nextURL, resp.Header.Get("Link"))
		resp.Body.Close()
	}

	sort.Strings(tags)
	for i, j := 0, len(tags)-1; i < j; i, j = i+1, j-1 {
		tags[i], tags[j] = tags[j], tags[i]
	}
	return tags, nil
}

func imageVersionExists(ctx context.Context, ref imageRepository) (bool, error) {
	client := http.DefaultClient
	manifestURL := fmt.Sprintf("%s/manifests/%s", ref.repositoryURL(), url.PathEscape(ref.version))
	var token string
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		if resp.StatusCode == http.StatusUnauthorized && token == "" {
			challenge := resp.Header.Get("WWW-Authenticate")
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			token, err = registryBearerToken(ctx, client, challenge)
			if err != nil {
				return false, err
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true, nil
		}
		if resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("registry manifest lookup failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return false, fmt.Errorf("registry manifest lookup failed")
}

func registryGet(ctx context.Context, client *http.Client, rawURL string, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

func nextRegistryPageURL(currentURL string, linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, link := range strings.Split(linkHeader, ",") {
		section, params, ok := strings.Cut(strings.TrimSpace(link), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		section = strings.TrimSpace(section)
		if !strings.HasPrefix(section, "<") || !strings.HasSuffix(section, ">") {
			continue
		}
		next := strings.TrimSuffix(strings.TrimPrefix(section, "<"), ">")
		if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
			return next
		}
		base, err := url.Parse(currentURL)
		if err != nil {
			return ""
		}
		rel, err := url.Parse(next)
		if err != nil {
			return ""
		}
		return base.ResolveReference(rel).String()
	}
	return ""
}

func registryBearerToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	params, ok := parseBearerChallenge(challenge)
	if !ok {
		return "", fmt.Errorf("registry requires unsupported authentication")
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry auth challenge missing realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for _, key := range []string{"service", "scope"} {
		if value := params[key]; value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry token request failed: status %d", resp.StatusCode)
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token != "" {
		return out.Token, nil
	}
	if out.AccessToken != "" {
		return out.AccessToken, nil
	}
	return "", fmt.Errorf("registry token response missing token")
}

func parseBearerChallenge(header string) (map[string]string, bool) {
	trimmed := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return nil, false
	}
	challenge := strings.TrimSpace(trimmed[len("Bearer "):])
	params := make(map[string]string)
	for _, part := range splitChallengeParams(challenge) {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.ToLower(key)] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	return params, true
}

func splitChallengeParams(s string) []string {
	var parts []string
	start := 0
	inQuote := false
	for i, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}
