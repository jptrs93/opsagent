package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/util/version"
)

const opendeployTestInstanceID int32 = 1

type fakeOperatorStore struct {
	mu       sync.Mutex
	status   apigen.ScheduledInstanceStatus
	statuses []apigen.RunnerStatus
}

func (s *fakeOperatorStore) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.ScheduledInstanceID == 0 {
		s.status.ScheduledInstanceID = instanceID
	}
	if !f(&s.status) {
		return
	}
	s.statuses = append(s.statuses, s.status.Runner)
}

func (s *fakeOperatorStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	return nil, make(chan apigen.ScheduledInstanceState), func() {}
}

func (s *fakeOperatorStore) runnerStatuses() []apigen.RunnerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apigen.RunnerStatus, len(s.statuses))
	copy(out, s.statuses)
	return out
}

func TestOpendeployAttachKeepsPreviousStatusFields(t *testing.T) {
	artifactPath := opendeployTestSymlink(t)
	store := &fakeOperatorStore{}
	dep := opendeployTestDeployment()
	prev := apigen.RunnerStatus{
		DeploymentSpecVersion: 5,
		RunningArtifact:       "/old/artifact",
		RunningPid:            123,
		Status:                apigen.RunningStatus_STARTING,
		NumberOfRestarts:      2,
	}

	r := attachOpendeployRunner(store, opendeployTestInstanceID, dep, prev)
	r.Stop()

	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want 1", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("status = %v, want RUNNING", got.Status)
	}
	if got.DeploymentSpecVersion != prev.DeploymentSpecVersion {
		t.Fatalf("deployment spec version = %d, want %d", got.DeploymentSpecVersion, prev.DeploymentSpecVersion)
	}
	if got.RunningPid != int32(os.Getpid()) {
		t.Fatalf("running pid = %d, want %d", got.RunningPid, os.Getpid())
	}
	if got.RunningArtifact != artifactPath {
		t.Fatalf("running artifact = %q, want %q", got.RunningArtifact, artifactPath)
	}
	if got.NumberOfRestarts != prev.NumberOfRestarts {
		t.Fatalf("number of restarts = %d, want %d", got.NumberOfRestarts, prev.NumberOfRestarts)
	}
}

func TestOpendeployAttachWithoutPreviousStatusPublishesCurrentProcessRunning(t *testing.T) {
	artifactPath := opendeployTestSymlink(t)
	store := &fakeOperatorStore{}
	dep := opendeployTestDeployment()

	r := attachOpendeployRunner(store, opendeployTestInstanceID, dep, apigen.RunnerStatus{})
	r.Stop()

	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want 1", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("status = %v, want RUNNING", got.Status)
	}
	if got.DeploymentSpecVersion != dep.SpecVersion {
		t.Fatalf("deployment spec version = %d, want %d", got.DeploymentSpecVersion, dep.SpecVersion)
	}
	if got.RunningPid != int32(os.Getpid()) {
		t.Fatalf("running pid = %d, want %d", got.RunningPid, os.Getpid())
	}
	if got.RunningArtifact != artifactPath {
		t.Fatalf("running artifact = %q, want %q", got.RunningArtifact, artifactPath)
	}
}

func TestReAttachRunningAttachesOnlyMatchingOpendeployBuild(t *testing.T) {
	opendeployTestSymlink(t)
	matchingStore := &fakeOperatorStore{}
	matching := opendeployTestDeployment()
	matching.Def.Spec.OpendeploySpec.Version = version.Version
	matchingRunner := ReAttachRunning(matchingStore, nil, opendeployTestInstanceID, matching, apigen.RunnerStatus{})
	matchingRunner.Stop()
	statuses := matchingStore.runnerStatuses()
	if len(statuses) != 1 || statuses[0].Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("matching build statuses = %+v, want one RUNNING", statuses)
	}

	mismatchedStore := &fakeOperatorStore{}
	mismatched := opendeployTestDeployment()
	mismatched.Def.Spec.OpendeploySpec.Version = version.Version + "-next"
	stale := apigen.RunnerStatus{DeploymentSpecVersion: mismatched.SpecVersion, Status: apigen.RunningStatus_STARTING}
	mismatchedRunner := ReAttachRunning(mismatchedStore, nil, opendeployTestInstanceID, mismatched, stale)
	mismatchedRunner.Stop()
	if statuses := mismatchedStore.runnerStatuses(); len(statuses) != 0 {
		t.Fatalf("mismatched build published statuses: %+v", statuses)
	}
	if mismatchedRunner.SpecVersion() != -1 {
		t.Fatalf("mismatched runner version = %d, want stopped sentinel", mismatchedRunner.SpecVersion())
	}
}

func TestOpendeployRestartLeavesStatusStartingForRestartedProcess(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "bin", "opendeploy")
	overrideOpendeployBinPath(t, binPath)
	artifactPath := filepath.Join(tmp, "releases", "opendeploy")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedArtifactPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeOperatorStore{}
	dep := opendeployTestDeployment()
	dep.SpecVersion = 9
	preparerStatus := apigen.PreparerStatus{
		DeploymentSpecVersion: dep.SpecVersion,
		Artifact:              artifactPath,
	}

	restartCalls := 0
	prevRestart := systemctlRestartCommand
	systemctlRestartCommand = func(context.Context, string) (string, error) {
		restartCalls++
		return "", nil
	}
	defer func() { systemctlRestartCommand = prevRestart }()

	r := newOpendeployRunnerWithRestart(store, opendeployTestInstanceID, dep, preparerStatus)
	r.Stop()

	if restartCalls != 1 {
		t.Fatalf("restart calls = %d, want 1", restartCalls)
	}
	if target, err := filepath.EvalSymlinks(binPath); err != nil || target != resolvedArtifactPath {
		t.Fatalf("bin symlink target = %q, %v; want %q", target, err, resolvedArtifactPath)
	}
	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want only initial STARTING", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_STARTING {
		t.Fatalf("status = %v, want STARTING", got.Status)
	}
	if got.DeploymentSpecVersion != dep.SpecVersion {
		t.Fatalf("deployment spec version = %d, want %d", got.DeploymentSpecVersion, dep.SpecVersion)
	}
	if got.RunningArtifact != artifactPath {
		t.Fatalf("running artifact = %q, want %q", got.RunningArtifact, artifactPath)
	}
}

func opendeployTestDeployment() *apigen.Deployment {
	return &apigen.Deployment{
		ID:          1,
		SpecVersion: 7,
		Def:         apigen.DeploymentDef{Spec: apigen.DeploymentSpec{OpendeploySpec: &apigen.OpendeploySpec{}}},
	}
}

func overrideOpendeployBinPath(t *testing.T, binPath string) {
	t.Helper()
	prev := opendeployBinPath
	opendeployBinPath = binPath
	t.Cleanup(func() { opendeployBinPath = prev })
}

func opendeployTestSymlink(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "releases", "opendeploy")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(tmp, "bin", "opendeploy")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(artifactPath, binPath); err != nil {
		t.Fatal(err)
	}
	overrideOpendeployBinPath(t, binPath)
	resolvedArtifactPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	return resolvedArtifactPath
}
