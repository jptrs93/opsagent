package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/goutil/timeu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/opendeployrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
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

// unavailableThenReadySecretProvider models a primary that is not accepting
// connections yet and then comes up, which is the ordinary shape of a rollout.
type unavailableThenReadySecretProvider struct {
	mu       sync.Mutex
	failures int
	attempts int
	value    string
}

func (p *unavailableThenReadySecretProvider) FetchSecrets(_ context.Context, ids []int32) (map[int32]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if p.attempts <= p.failures {
		return nil, errors.New("primary unavailable")
	}
	values := make(map[int32]string, len(ids))
	for _, id := range ids {
		values[id] = p.value
	}
	return values, nil
}

func (p *unavailableThenReadySecretProvider) attemptCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts
}

// fixedRuntimeInputsBackoff pins the retry interval so tests do not wait out the
// production 2s-and-doubling schedule.
func fixedRuntimeInputsBackoff(t *testing.T, interval time.Duration) {
	t.Helper()
	old := newRuntimeInputsBackoff
	newRuntimeInputsBackoff = func() *timeu.Backoff {
		return &timeu.Backoff{
			MaxDuration: interval,
			F:           func(time.Duration) time.Duration { return interval },
		}
	}
	t.Cleanup(func() { newRuntimeInputsBackoff = old })
}

func secretRefDeployment(secretID *int32) *apigen.DeploymentConfig {
	return &apigen.DeploymentConfig{
		ID:      12,
		Version: 4,
		Spec: apigen.DeploymentSpec{
			Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "registry.example/app"}},
				Version: "v1",
				Running: true,
				Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
					"TOKEN": {SecretVersionID: secretID},
				}},
			},
		},
	}
}

// A worker restarting while the primary is unreachable must not re-prepare an
// artifact that is already built and present. Re-preparing fetches the same
// inputs from the same unreachable place, so it only publishes PREPARING then
// FAILED — a state nothing retries out of, leaving the instance wedged until
// someone edits the config.
func TestReAttachPreparerDefersToRetryWhenRuntimeInputsUnavailable(t *testing.T) {
	fixedRuntimeInputsBackoff(t, time.Millisecond)
	secretID := int32(7)
	dep := secretRefDeployment(&secretID)
	store := &recordingOperatorStore{}
	secrets := &unavailableThenReadySecretProvider{failures: 3, value: "s3cret"}
	inputs := runtimeinputs.New(nil, secrets, nil)
	op := DeploymentOperator{
		Store:         store,
		RuntimeInputs: inputs,
		ImageReady:    func(context.Context, string) error { return nil },
	}

	handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.PreparerStatus{
		DeploymentConfigVersion: dep.Version,
		Artifact:                "registry.example/app:v1",
		Inputs:                  apigen.InputsStatus_INPUTS_READY,
		Image:                   apigen.ImageStatus_IMAGE_READY,
	})
	if handle.Version() != dep.Version {
		t.Fatalf("handle version = %d, want %d", handle.Version(), dep.Version)
	}

	// The retry must actually refill the in-memory cache: nothing else writes to
	// it, and the container runner resolves env references from it on respawn.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if value, ok := inputs.ResolveSecret(secretID); ok {
			if value != "s3cret" {
				t.Fatalf("resolved secret = %q, want %q", value, "s3cret")
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := inputs.ResolveSecret(secretID); !ok {
		t.Fatalf("secret cache was never refilled after %d attempts", secrets.attemptCount())
	}
	// The retry publishes the inputs stage so a stuck instance is visible, but
	// the rollup that gates runner start and the recorded artifact must both
	// survive it — the artifact is built, only input distribution failed.
	//
	// EnsureReady fills the cache before the goroutine publishes recovery, so the
	// loop above can win the race against that write. Poll for it.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && store.preparerStatus().Inputs != apigen.InputsStatus_INPUTS_READY {
		time.Sleep(time.Millisecond)
	}
	got := store.preparerStatus()
	if got.Rollup() != apigen.PreparationStatus_READY {
		t.Fatalf("rollup = %v during input retry, want READY", got.Rollup())
	}
	if got.Artifact != "registry.example/app:v1" {
		t.Fatalf("artifact = %q during input retry, want it preserved", got.Artifact)
	}
	if got.Image != apigen.ImageStatus_IMAGE_READY {
		t.Fatalf("image stage = %v during input retry, want READY", got.Image)
	}
	if got.Inputs != apigen.InputsStatus_INPUTS_READY {
		t.Fatalf("inputs stage = %v after recovery, want READY", got.Inputs)
	}
	handle.Cancel()
}

// Cancel must return promptly even mid-backoff, because the operator calls it
// synchronously when the config version moves on or the instance is finalized.
func TestRetryRuntimeInputsCancelInterruptsBackoff(t *testing.T) {
	fixedRuntimeInputsBackoff(t, time.Hour)

	secretID := int32(7)
	op := DeploymentOperator{
		Store:         &recordingOperatorStore{},
		RuntimeInputs: runtimeinputs.New(nil, &failingSecretProvider{}, nil),
	}
	handle := op.retryRuntimeInputs(testScheduledInstanceID, secretRefDeployment(&secretID), apigen.PreparerStatus{
		Artifact: "registry.example/app:v1",
		Image:    apigen.ImageStatus_IMAGE_READY,
	})

	cancelled := make(chan struct{})
	go func() {
		handle.Cancel()
		close(cancelled)
	}()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel blocked on the retry backoff")
	}
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
			Inputs:                  apigen.InputsStatus_INPUTS_READY,
			Image:                   apigen.ImageStatus_IMAGE_READY,
		})
		if handle.Version() != dep.Version {
			t.Fatalf("handle version = %d, want %d", handle.Version(), dep.Version)
		}
	})

	t.Run("empty opendeploy status starts prepare", func(t *testing.T) {
		oldOutputDir := ainit.StaticConfig.PrepareOutputDir
		ainit.StaticConfig.PrepareOutputDir = t.TempDir()
		defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

		store := &recordingOperatorStore{}
		op := DeploymentOperator{
			Store:             store,
			RuntimeInputs:     runtimeinputs.New(nil, nil, nil),
			OpendeployRelease: opendeployrelease.New(t.TempDir(), githubrepo.NewClient(githubrepo.WithAPIBaseURL("http://127.0.0.1:1"))),
		}
		dep := &apigen.DeploymentConfig{
			ID:      1,
			Version: 4,
			Spec: apigen.DeploymentSpec{
				OpendeploySpec: &apigen.OpendeploySpec{
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
			t.Fatal("expected preparer status write from startPreparer, got empty (opendeploy empty-status shortcut still active?)")
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
					"TOKEN": {SecretVersionID: &secretID},
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
	if got := store.preparerStatus().Rollup(); got != apigen.PreparationStatus_FAILED {
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
		Inputs:                  apigen.InputsStatus_INPUTS_READY,
		Image:                   apigen.ImageStatus_IMAGE_READY,
	})
	handle.Cancel()

	if !imageChecked {
		t.Fatal("persisted ready image was not checked")
	}
	if got := store.preparerStatus().Rollup(); got != apigen.PreparationStatus_FAILED {
		t.Fatalf("preparer status = %v, want FAILED after same-version preparation ran", got)
	}
}
