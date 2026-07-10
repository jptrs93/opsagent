package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/containerimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
)

type operatorTestStore struct{}

func (operatorTestStore) MustWriteDeploymentStatus(int32, func(*apigen.DeploymentStatus) bool) {}
func (operatorTestStore) MustFetchSnapshotAndSubscribe(string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
	return nil, nil, func() {}
}

type recordingOperatorStore struct {
	mu     sync.Mutex
	status apigen.DeploymentStatus
}

func (s *recordingOperatorStore) MustWriteDeploymentStatus(_ int32, update func(*apigen.DeploymentStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.status)
}

func (s *recordingOperatorStore) MustFetchSnapshotAndSubscribe(string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
	return nil, nil, func() {}
}

func (s *recordingOperatorStore) preparerStatus() apigen.PreparerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Preparer
}

type failingSecretProvider struct {
	called bool
}

func (p *failingSecretProvider) FetchSecrets(context.Context, []int32) (map[int32]string, error) {
	p.called = true
	return nil, errors.New("secret unavailable")
}

func TestReAttachPreparerLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		status    apigen.PreparerStatus
		configure func(*apigen.DeploymentConfig)
	}{
		{
			name:   "ready artifact is reused",
			status: apigen.PreparerStatus{DeploymentConfigVersion: 4, Status: apigen.PreparationStatus_READY},
		},
		{
			name:   "installed system deployment is reused",
			status: apigen.PreparerStatus{},
			configure: func(dep *apigen.DeploymentConfig) {
				dep.Spec.Runner.Systemd = apigen.SystemdRunnerConfig{Name: "opendeploy.service"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := DeploymentOperator{
				Store:         operatorTestStore{},
				RuntimeInputs: runtimeinputs.New(nil, nil, nil),
			}
			dep := &apigen.DeploymentConfig{
				Version:      4,
				DesiredState: apigen.DesiredState{Version: "v1"},
				Spec: apigen.DeploymentSpec{Prepare: apigen.PrepareConfig{
					NixDockerBuild: &apigen.NixDockerBuildConfig{},
				}},
			}
			if tt.configure != nil {
				tt.configure(dep)
			}

			handle := op.reAttachPreparer(dep, tt.status)

			if handle.Version() != dep.Version {
				t.Fatalf("handle version = %d, want %d", handle.Version(), dep.Version)
			}
		})
	}
}

func TestStartPreparerStopsBeforeArtifactWhenRuntimeInputsFail(t *testing.T) {
	oldOutputDir := ainit.StaticConfig.PrepareOutputDir
	ainit.StaticConfig.PrepareOutputDir = t.TempDir()
	defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

	secretID := int32(7)
	dep := &apigen.DeploymentConfig{
		ID:           11,
		Version:      3,
		DesiredState: apigen.DesiredState{Version: "v1"},
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "registry.example/app"}},
			Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"TOKEN": {SecretID: &secretID},
			}}},
		},
	}
	store := &recordingOperatorStore{}
	secrets := &failingSecretProvider{}
	op := DeploymentOperator{
		Store:          store,
		ContainerImage: containerimage.New(ctrd.New("unused", "unused")),
		RuntimeInputs:  runtimeinputs.New(nil, secrets, nil),
	}

	handle := op.startPreparer(dep)
	handle.Cancel()

	if !secrets.called {
		t.Fatal("runtime inputs were not prepared")
	}
	if got := store.preparerStatus().Status; got != apigen.PreparationStatus_FAILED {
		t.Fatalf("preparer status = %v, want FAILED", got)
	}
}
