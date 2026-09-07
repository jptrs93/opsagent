package clusterhandler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

type countingGithubCredentials struct{ calls int }

func (p *countingGithubCredentials) LoadCredentials(context.Context) (*githubcredentials.GithubCredentials, error) {
	p.calls++
	return &githubcredentials.GithubCredentials{Token: "test-pat"}, nil
}

func TestGHCRWorkerCredentialAuthorization(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	provider := &countingGithubCredentials{}
	handler := &Handler{store: store, githubCredentials: provider}
	for _, tc := range []struct {
		name, image string
		allowed     bool
	}{
		{"ghcr", "ghcr.io/acme/private", true},
		{"ghcr-url", "https://ghcr.io/v2/acme/private:latest", true},
		{"ghcr-digest", "ghcr.io/acme/private@sha256:abc", true},
		{"docker-hub", "postgres", false},
		{"subdomain", "sub.ghcr.io/acme/private", false},
		{"lookalike", "ghcr.io.evil.example/acme/private", false},
		{"port", "ghcr.io:444/acme/private", false},
		{"no-assignment", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := store.EnsurePrimaryNode(tc.name, tc.name)
			if tc.image != "" {
				spec := &apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
					Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: tc.image}},
					Version: "latest", Running: true,
				}}
				dep := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, tc.name, node.ID, spec)
				store.CreateScheduledInstanceForTest(dep.ID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
			}
			before := provider.calls
			ctx := apigen.Context{Ctx: context.WithValue(context.Background(), machineCtxKey{}, tc.name)}
			creds, err := handler.GetV1ClusterGithubCredentials(ctx)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				if creds.Token != "test-pat" || provider.calls != before+1 {
					t.Fatal("worker did not receive configured credential")
				}
			} else {
				if err != clusterForbiddenErr {
					t.Fatalf("error = %v, want forbidden", err)
				}
				if creds != nil || provider.calls != before {
					t.Fatal("credential loaded for unauthorized worker")
				}
			}
		})
	}
}
