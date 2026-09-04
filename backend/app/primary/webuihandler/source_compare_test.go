package webuihandler

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func imageSpec(image string) *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Source: apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: image}},
	}}
}

func TestSameDesiredVersionSourceIgnoresImageTag(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"ghcr.io/acme/app", "ghcr.io/acme/app:1.2", true},
		{"ghcr.io/acme/app:1.1", "ghcr.io/acme/app@sha256:abc", true},
		{"localhost:5000/app:1", "localhost:5000/app:2", true},
		{"postgres", "docker.io/library/postgres:16", true},
		{"ghcr.io/acme/app", "ghcr.io/acme/other", false},
	}
	for _, tc := range cases {
		if got := sameDesiredVersionSource(imageSpec(tc.a), imageSpec(tc.b)); got != tc.same {
			t.Errorf("sameDesiredVersionSource(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}
