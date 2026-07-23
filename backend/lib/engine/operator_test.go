package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage"
)

type operatorTestStore struct{}

func (operatorTestStore) MustWriteDeploymentStatus(int32, func(*apigen.DeploymentStatus) bool) {}
func (operatorTestStore) MustFetchSnapshotAndSubscribe(storage.DeploymentPredicate) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
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

func (s *recordingOperatorStore) MustFetchSnapshotAndSubscribe(storage.DeploymentPredicate) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
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
		configure func(*apigen.DeploymentConfig2)
	}{
		{
			name:   "ready artifact is reused",
			status: apigen.PreparerStatus{DeploymentConfigVersion: 4, Status: apigen.PreparationStatus_READY},
		},
		{
			name:   "installed system deployment is reused",
			status: apigen.PreparerStatus{},
			configure: func(dep *apigen.DeploymentConfig2) {
				dep.Spec.Container1Spec = nil
				dep.Spec.SystemdSpec = &apigen.SystemdSpec{
					Source:  &apigen.GithubRelease{},
					Runtime: &apigen.SystemdRuntime{Name: "opendeploy.service"},
					Version: "v1",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := DeploymentOperator{
				Store:         operatorTestStore{},
				RuntimeInputs: runtimeinputs.New(nil, nil, nil),
				ImageReady:    func(context.Context, string) error { return nil },
			}
			dep := &apigen.DeploymentConfig2{
				Version: 4,
				Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
					Source:  apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild2{}},
					Version: "v1",
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
	dep := &apigen.DeploymentConfig2{
		ID:      11,
		Version: 3,
		Spec: apigen.DeploymentSpec2{
			Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "registry.example/app"}},
				Version: "v1",
				Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
					"TOKEN": {SecretID: &secretID},
				}},
			},
		},
	}
	store := &recordingOperatorStore{}
	secrets := &failingSecretProvider{}
	op := DeploymentOperator{
		Store:         store,
		RuntimeInputs: runtimeinputs.New(nil, secrets, nil),
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

func TestReAttachPreparerRepreparesUnavailableImage(t *testing.T) {
	oldOutputDir := ainit.StaticConfig.PrepareOutputDir
	ainit.StaticConfig.PrepareOutputDir = t.TempDir()
	defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

	store := &recordingOperatorStore{}
	imageChecked := false
	op := DeploymentOperator{
		Store:         store,
		RuntimeInputs: runtimeinputs.New(nil, nil, nil),
		ImageReady: func(context.Context, string) error {
			imageChecked = true
			return errors.New("image unavailable")
		},
	}
	dep := &apigen.DeploymentConfig2{
		ID:      12,
		Version: 4,
		Spec: apigen.DeploymentSpec2{
			Container1Spec: &apigen.ContainerSpec{Version: "v1", Running: true},
		},
	}

	handle := op.reAttachPreparer(dep, apigen.PreparerStatus{
		DeploymentConfigVersion: dep.Version,
		Artifact:                "example/app:v1",
		Status:                  apigen.PreparationStatus_READY,
	})
	handle.Cancel()

	if !imageChecked {
		t.Fatal("persisted ready image was not checked")
	}
	if got := store.preparerStatus().Status; got != apigen.PreparationStatus_FAILED {
		t.Fatalf("preparer status = %v, want FAILED after same-version preparation ran", got)
	}
}
