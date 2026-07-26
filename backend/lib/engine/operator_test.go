package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage"
)

const testScheduledInstanceID int32 = 42

type operatorTestStore struct{}

func (operatorTestStore) MustWriteScheduledInstanceStatus(int32, func(*apigen.ScheduledInstanceStatus) bool) {
}
func (operatorTestStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

type recordingOperatorStore struct {
	mu     sync.Mutex
	status apigen.ScheduledInstanceStatus
}

func (s *recordingOperatorStore) MustWriteScheduledInstanceStatus(_ int32, update func(*apigen.ScheduledInstanceStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.status)
}

func (s *recordingOperatorStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

func (s *recordingOperatorStore) preparerStatus() apigen.PreparerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Preparer
}

func (s *recordingOperatorStore) scheduledStatus() apigen.ScheduledInstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

type failingSecretProvider struct {
	called bool
}

func (p *failingSecretProvider) FetchSecrets(context.Context, []int32) (map[int32]string, error) {
	p.called = true
	return nil, errors.New("secret unavailable")
}

func TestReAttachPreparerLifecycle(t *testing.T) {
	t.Run("ready artifact is reused", func(t *testing.T) {
		op := DeploymentOperator{
			Store:         operatorTestStore{},
			RuntimeInputs: runtimeinputs.New(nil, nil, nil),
			ImageReady:    func(context.Context, string) error { return nil },
		}
		dep := &apigen.DeploymentConfig{
			Version: 4,
			Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild{}},
				Version: "v1",
			}},
		}
		handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.PreparerStatus{
			DeploymentConfigVersion: 4,
			Status:                  apigen.PreparationStatus_READY,
		})
		if handle.Version() != dep.Version {
			t.Fatalf("handle version = %d, want %d", handle.Version(), dep.Version)
		}
	})

	t.Run("empty systemd status starts prepare", func(t *testing.T) {
		oldOutputDir := ainit.StaticConfig.PrepareOutputDir
		ainit.StaticConfig.PrepareOutputDir = t.TempDir()
		defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

		store := &recordingOperatorStore{}
		op := DeploymentOperator{
			Store:         store,
			RuntimeInputs: runtimeinputs.New(nil, nil, nil),
		}
		dep := &apigen.DeploymentConfig{
			ID:      1,
			Version: 4,
			Spec: apigen.DeploymentSpec{
				SystemdSpec: &apigen.SystemdSpec{
					Source:  &apigen.GithubRelease{},
					Runtime: &apigen.SystemdRuntime{Name: "opendeploy.service"},
					Version: "v1",
				},
			},
		}
		handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.PreparerStatus{})
		handle.Cancel()
		if handle.Version() != dep.Version {
			t.Fatalf("handle version = %d, want %d", handle.Version(), dep.Version)
		}
		// Empty status must not be treated as already-installed; prepare should run.
		if store.preparerStatus().IsZero() {
			t.Fatal("expected preparer status write from startPreparer, got empty (systemd empty-status shortcut still active?)")
		}
	})
}

func TestStartPreparerStopsBeforeArtifactWhenRuntimeInputsFail(t *testing.T) {
	oldOutputDir := ainit.StaticConfig.PrepareOutputDir
	ainit.StaticConfig.PrepareOutputDir = t.TempDir()
	defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

	secretID := int32(7)
	dep := &apigen.DeploymentConfig{
		ID:      11,
		Version: 3,
		Spec: apigen.DeploymentSpec{
			Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "registry.example/app"}},
				Version: "v1",
				Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
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

	handle := op.startPreparer(testScheduledInstanceID, dep)
	handle.Cancel()

	if !secrets.called {
		t.Fatal("runtime inputs were not prepared")
	}
	if got := store.preparerStatus().Status; got != apigen.PreparationStatus_FAILED {
		t.Fatalf("preparer status = %v, want FAILED", got)
	}
}

func TestInitialTerminateWithoutStatusIsAcknowledged(t *testing.T) {
	store := &recordingOperatorStore{}
	subs := &pubsubu.PubSub[apigen.ScheduledInstanceState]{}
	sub := subs.Subscribe(func(_, state apigen.ScheduledInstanceState) bool {
		return state.Instance.ID == testScheduledInstanceID
	})
	initial := apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:           testScheduledInstanceID,
			DeploymentID: 11,
			State:        apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE,
		},
		Config: apigen.DeploymentConfig{ID: 11, Version: 3},
	}
	done := make(chan struct{})
	go func() {
		DeploymentOperator{Store: store}.Run(sub, &initial)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for store.scheduledStatus().Runner.Status != apigen.RunningStatus_STOPPED && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.scheduledStatus().Runner.Status; got != apigen.RunningStatus_STOPPED {
		t.Fatalf("initial terminate status = %v, want STOPPED", got)
	}
	sub.Ch <- apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{ID: testScheduledInstanceID, State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED},
		Config:   initial.Config,
		Status:   store.scheduledStatus(),
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operator did not exit after FINALIZED")
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
	dep := &apigen.DeploymentConfig{
		ID:      12,
		Version: 4,
		Spec: apigen.DeploymentSpec{
			Container1Spec: &apigen.ContainerSpec{Version: "v1", Running: true},
		},
	}

	handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.PreparerStatus{
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
