package imageref

import (
	"fmt"
	"strings"
)

const dockerHubRegistry = "registry-1.docker.io"

type Repository struct {
	Registry string
	Name     string
	Version  string
}

func Parse(raw string) (Repository, error) {
	image := strings.TrimSpace(raw)
	if image == "" {
		return Repository{}, fmt.Errorf("image is required")
	}
	image, hadScheme := stripImageScheme(image)
	image = strings.TrimSuffix(image, "/")
	version := imageTagOrDigest(image)
	image = stripImageTagOrDigest(image)
	parts := strings.Split(image, "/")
	if len(parts) == 0 || parts[0] == "" {
		return Repository{}, fmt.Errorf("invalid image")
	}

	registry := dockerHubRegistry
	rawRegistry := ""
	nameParts := parts
	if looksLikeRegistry(parts[0]) {
		rawRegistry = parts[0]
		registry = canonicalRegistry(parts[0])
		nameParts = parts[1:]
	}
	if len(nameParts) > 0 && nameParts[0] == "v2" && shouldStripRegistryAPIPrefix(hadScheme, rawRegistry) {
		nameParts = nameParts[1:]
	}
	if len(nameParts) == 0 || nameParts[0] == "" {
		return Repository{}, fmt.Errorf("invalid image repository")
	}
	if registry == dockerHubRegistry && len(nameParts) == 1 {
		nameParts = append([]string{"library"}, nameParts...)
	}
	return Repository{Registry: registry, Name: strings.Join(nameParts, "/"), Version: version}, nil
}

func RepositoryURL(raw string) (string, error) {
	repo, err := Parse(raw)
	if err != nil {
		return "", err
	}
	return repo.URL(), nil
}

func RepositoryRef(raw string) (string, error) {
	repo, err := Parse(raw)
	if err != nil {
		return "", err
	}
	return repo.Ref(), nil
}

func Ref(raw, version string) (string, error) {
	repo, err := RepositoryRef(raw)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(version, "sha256:") {
		return repo + "@" + version, nil
	}
	return repo + ":" + version, nil
}

func (r Repository) URL() string {
	return fmt.Sprintf("https://%s/v2/%s", r.Registry, r.Name)
}

func (r Repository) Ref() string {
	registry := r.Registry
	if registry == dockerHubRegistry {
		registry = "docker.io"
	}
	return registry + "/" + r.Name
}

func stripImageScheme(image string) (string, bool) {
	for _, prefix := range []string{"docker://", "https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(image), prefix) {
			return image[len(prefix):], true
		}
	}
	return image, false
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

func canonicalRegistry(registry string) string {
	if isDockerHubRegistry(registry) {
		return dockerHubRegistry
	}
	return registry
}

func isDockerHubRegistry(registry string) bool {
	switch strings.ToLower(registry) {
	case dockerHubRegistry, "docker.io", "index.docker.io":
		return true
	default:
		return false
	}
}

func shouldStripRegistryAPIPrefix(hadScheme bool, rawRegistry string) bool {
	return hadScheme || strings.EqualFold(rawRegistry, dockerHubRegistry)
}
