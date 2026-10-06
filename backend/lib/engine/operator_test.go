package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/goutil/timeu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/opendeployrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/storage"
)

const testScheduledInstanceID uint64 = 42

type operatorTestStore struct{}

func (operatorTestStore) MustWriteScheduledInstanceStatus(uint64, func(*apigen.ScheduledInstanceStatus) bool) {
}
func (operatorTestStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

type recordingOperatorStore struct {
	mu     sync.Mutex
	status apigen.ScheduledInstanceStatus
}

func (s *recordingOperatorStore) MustWriteScheduledInstanceStatus(instanceID uint64, update func(*apigen.ScheduledInstanceStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !update(&s.status) {
		return
	}
	if s.status.ScheduledInstanceID != instanceID {
		panic(fmt.Sprintf("status written for instance %d, want %d", s.status.ScheduledInstanceID, instanceID))
	}
	if err := s.status.Validate(); err != nil {
		panic(err)
	}
}

func (s *recordingOperatorStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

func (s *recordingOperatorStore) preparerStatus() apigen.PreparerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Preparer.Value
}

func (s *recordingOperatorStore) scheduledStatus() apigen.ScheduledInstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

type failingSecretProvider struct {
	called bool
}

func (p *failingSecretProvider) FetchSecrets(context.Context, []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
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

func (p *unavailableThenReadySecretProvider) FetchSecrets(_ context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if p.attempts <= p.failures {
		return nil, errors.New("primary unavailable")
	}
	values := make(map[apigen.ValueRef]string, len(refs))
	for _, ref := range refs {
		values[ref] = p.value
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

func secretRefDeployment(secret apigen.ValueRef) *apigen.DeploymentRecord {
	return remoteImageDeployment(12, 4, apigen.ContainerRuntime{EnvVars: map[string]apigen.EnvVar{"TOKEN": {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: secret.Secret()}}}}})
}

func remoteImageDeployment(id uint64, specVersion uint32, runtime apigen.ContainerRuntime) *apigen.DeploymentRecord {
	return &apigen.DeploymentRecord{
		Deployment: apigen.Deployment{
			ID:   id,
			Name: "app",
			Spec: apigen.DeploymentSpec{
				Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
					Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "registry.example/app"}}},
					Runtime:         runtime,
					Version:         "v1",
					UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
				}}},
				Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
			},
			Scheduling: apigen.DedicatedScheduling(true, 1),
		},
		Meta: apigen.EntityMeta{Version: specVersion, SpecVersion: specVersion},
	}
}

// A secondary restarting while the primary is unreachable must not re-prepare an
// artifact that is already built and present. Re-preparing fetches the same
// inputs from the same unreachable place, so it only publishes PREPARING then
// FAILED — a state nothing retries out of, leaving the instance wedged until
// someone edits the config.
func TestReAttachPreparerDefersToRetryWhenRuntimeInputsUnavailable(t *testing.T) {
	fixedRuntimeInputsBackoff(t, time.Millisecond)
	secretID := apigen.ValueRef{ID: 7, Version: 1}
	dep := secretRefDeployment(secretID)
	store := &recordingOperatorStore{}
	secrets := &unavailableThenReadySecretProvider{failures: 3, value: "s3cret"}
	inputs := runtimeinputs.New(nil, secrets, nil)
	op := DeploymentOperator{
		Store:         store,
		RuntimeInputs: inputs,
		ImageReady:    func(context.Context, string) error { return nil },
	}

	handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.Some(apigen.PreparerStatus{
		DeploymentSpecVersion: dep.Meta.SpecVersion,
		Artifact:              "registry.example/app:v1",
		Inputs:                apigen.InputsStatus_INPUTS_STATUS_READY,
		Image:                 apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY),
	}))
	if handle.SpecVersion() != dep.Meta.SpecVersion {
		t.Fatalf("handle version = %d, want %d", handle.SpecVersion(), dep.Meta.SpecVersion)
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
	for time.Now().Before(deadline) && store.preparerStatus().Inputs != apigen.InputsStatus_INPUTS_STATUS_READY {
		time.Sleep(time.Millisecond)
	}
	got := store.preparerStatus()
	if got.Rollup() != apigen.PreparationStatus_PREPARATION_STATUS_READY {
		t.Fatalf("rollup = %v during input retry, want READY", got.Rollup())
	}
	if got.Artifact != "registry.example/app:v1" {
		t.Fatalf("artifact = %q during input retry, want it preserved", got.Artifact)
	}
	if got.ImageStage() != apigen.ImageStatus_IMAGE_STATUS_READY {
		t.Fatalf("image stage = %v during input retry, want READY", got.ImageStage())
	}
	if got.Inputs != apigen.InputsStatus_INPUTS_STATUS_READY {
		t.Fatalf("inputs stage = %v after recovery, want READY", got.Inputs)
	}
	handle.Cancel()
}

// Cancel must return promptly even mid-backoff, because the operator calls it
// synchronously when the spec version moves on or the instance is finalized.
func TestRetryRuntimeInputsCancelInterruptsBackoff(t *testing.T) {
	fixedRuntimeInputsBackoff(t, time.Hour)

	secretID := apigen.ValueRef{ID: 7, Version: 1}
	op := DeploymentOperator{
		Store:         &recordingOperatorStore{},
		RuntimeInputs: runtimeinputs.New(nil, &failingSecretProvider{}, nil),
	}
	handle := op.retryRuntimeInputs(testScheduledInstanceID, secretRefDeployment(secretID), apigen.PreparerStatus{
		Artifact: "registry.example/app:v1",
		Inputs:   apigen.InputsStatus_INPUTS_STATUS_READY,
		Image:    apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY),
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
		dep := remoteImageDeployment(3, 4, apigen.ContainerRuntime{})
		dep.Deployment.Spec.Container().Source = apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{}}}
		handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.Some(apigen.PreparerStatus{
			DeploymentSpecVersion: 4,
			Inputs:                apigen.InputsStatus_INPUTS_STATUS_READY,
			Image:                 apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY),
		}))
		if handle.SpecVersion() != dep.Meta.SpecVersion {
			t.Fatalf("handle version = %d, want %d", handle.SpecVersion(), dep.Meta.SpecVersion)
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
		dep := &apigen.DeploymentRecord{
			Deployment: apigen.Deployment{ID: 1, SpaceID: internaldeploy.SpaceID, Name: internaldeploy.SelfName, Spec: selfSpecAt("v1"), Scheduling: apigen.DedicatedScheduling(true, 1)},
			Meta:       apigen.EntityMeta{Version: 4, SpecVersion: 4},
		}
		handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.Maybe[apigen.PreparerStatus]{})
		handle.Cancel()
		if handle.SpecVersion() != dep.Meta.SpecVersion {
			t.Fatalf("handle version = %d, want %d", handle.SpecVersion(), dep.Meta.SpecVersion)
		}
		// Empty status must not be treated as already-installed; prepare should run.
		if !store.scheduledStatus().Preparer.Present {
			t.Fatal("expected preparer status write from startPreparer, got empty (opendeploy empty-status shortcut still active?)")
		}
	})
}

func TestStartPreparerStopsBeforeArtifactWhenRuntimeInputsFail(t *testing.T) {
	oldOutputDir := ainit.StaticConfig.PrepareOutputDir
	ainit.StaticConfig.PrepareOutputDir = t.TempDir()
	defer func() { ainit.StaticConfig.PrepareOutputDir = oldOutputDir }()

	dep := secretRefDeployment(apigen.ValueRef{ID: 7, Version: 1})
	dep.Deployment.ID, dep.Meta.SpecVersion = 11, 3
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
	if got := store.preparerStatus().Rollup(); got != apigen.PreparationStatus_PREPARATION_STATUS_FAILED {
		t.Fatalf("preparer status = %v, want FAILED", got)
	}
}

func TestInitialTerminateWithoutStatusIsAcknowledged(t *testing.T) {
	store := &recordingOperatorStore{}
	subs := &pubsubu.PubSub[[]apigen.ScheduledInstanceState]{}
	sub := subs.Subscribe(func(_, batch []apigen.ScheduledInstanceState) bool {
		return len(batch) == 1 && batch[0].Instance.ID == testScheduledInstanceID
	})
	initial := apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:         testScheduledInstanceID,
			NodeID:     1,
			State:      apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE,
			Deployment: apigen.DeploymentRef{DeploymentID: 11, Version: 3},
		},
		Config: *remoteImageDeployment(11, 3, apigen.ContainerRuntime{}),
	}
	done := make(chan struct{})
	go func() {
		DeploymentOperator{Store: store}.Run(sub, &initial)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for store.scheduledStatus().Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_STOPPED && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.scheduledStatus().Runner; !got.Present || got.Value.Status != apigen.RunningStatus_RUNNING_STATUS_STOPPED {
		t.Fatalf("initial terminate status = %v, want STOPPED", got)
	}
	sub.Ch <- []apigen.ScheduledInstanceState{{
		Instance: apigen.ScheduledInstance{ID: testScheduledInstanceID, NodeID: 1, State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, Deployment: initial.Instance.Deployment},
		Config:   initial.Config,
		Status:   apigen.Some(store.scheduledStatus()),
	}}
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
	dep := remoteImageDeployment(12, 4, apigen.ContainerRuntime{})

	handle := op.reAttachPreparer(testScheduledInstanceID, dep, apigen.Some(apigen.PreparerStatus{
		DeploymentSpecVersion: dep.Meta.SpecVersion,
		Artifact:              "example/app:v1",
		Inputs:                apigen.InputsStatus_INPUTS_STATUS_READY,
		Image:                 apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY),
	}))
	handle.Cancel()

	if !imageChecked {
		t.Fatal("persisted ready image was not checked")
	}
	if got := store.preparerStatus().Rollup(); got != apigen.PreparationStatus_PREPARATION_STATUS_FAILED {
		t.Fatalf("preparer status = %v, want FAILED after same-version preparation ran", got)
	}
}

func selfSpecAt(version string) apigen.DeploymentSpec {
	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadVersion(version); err != nil {
		panic(err)
	}
	return *spec
}
