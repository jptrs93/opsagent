package versionprovider

import "testing"

func TestContainerImageRepositoryNormalization(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantURL string
		wantRef string
	}{
		{
			name:    "docker hub shorthand",
			image:   "postgres",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "docker hub namespace",
			image:   "library/postgres:18",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "docker hub registry alias",
			image:   "docker.io/library/postgres:18",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "docker hub registry api host",
			image:   "registry-1.docker.io/library/postgres:18",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "docker hub registry api path",
			image:   "registry-1.docker.io/v2/library/postgres:18",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "docker hub registry api url",
			image:   "https://registry-1.docker.io/v2/library/postgres:18",
			wantURL: "https://registry-1.docker.io/v2/library/postgres",
			wantRef: "docker.io/library/postgres",
		},
		{
			name:    "non docker registry",
			image:   "ghcr.io/jptrs93/opsagent:latest",
			wantURL: "https://ghcr.io/v2/jptrs93/opsagent",
			wantRef: "ghcr.io/jptrs93/opsagent",
		},
		{
			name:    "non docker registry api url",
			image:   "https://ghcr.io/v2/jptrs93/opsagent:latest",
			wantURL: "https://ghcr.io/v2/jptrs93/opsagent",
			wantRef: "ghcr.io/jptrs93/opsagent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, err := ContainerImageRepositoryURL(tt.image)
			if err != nil {
				t.Fatalf("ContainerImageRepositoryURL() error = %v", err)
			}
			if gotURL != tt.wantURL {
				t.Fatalf("ContainerImageRepositoryURL() = %q, want %q", gotURL, tt.wantURL)
			}

			gotRef, err := ContainerImageRepositoryRef(tt.image)
			if err != nil {
				t.Fatalf("ContainerImageRepositoryRef() error = %v", err)
			}
			if gotRef != tt.wantRef {
				t.Fatalf("ContainerImageRepositoryRef() = %q, want %q", gotRef, tt.wantRef)
			}
		})
	}
}

func TestContainerImageRef(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		version string
		want    string
	}{
		{
			name:    "tag",
			image:   "https://registry-1.docker.io/v2/library/postgres:17",
			version: "18",
			want:    "docker.io/library/postgres:18",
		},
		{
			name:    "digest",
			image:   "postgres",
			version: "sha256:abc123",
			want:    "docker.io/library/postgres@sha256:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ContainerImageRef(tt.image, tt.version)
			if err != nil {
				t.Fatalf("ContainerImageRef() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ContainerImageRef() = %q, want %q", got, tt.want)
			}
		})
	}
}
