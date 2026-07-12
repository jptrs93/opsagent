package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeOperatorStore struct {
	mu       sync.Mutex
	status   apigen.DeploymentStatus
	statuses []apigen.RunnerStatus
}

func (s *fakeOperatorStore) MustWriteDeploymentStatus(deploymentID int32, f func(*apigen.DeploymentStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.DeploymentID == 0 {
		s.status.DeploymentID = deploymentID
	}
	if !f(&s.status) {
		return
	}
	s.statuses = append(s.statuses, s.status.Runner)
}

func (s *fakeOperatorStore) MustFetchSnapshotAndSubscribe(string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
	return nil, make(chan apigen.DeploymentWithStatus), func() {}
}

func (s *fakeOperatorStore) runnerStatuses() []apigen.RunnerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apigen.RunnerStatus, len(s.statuses))
	copy(out, s.statuses)
	return out
}

func TestSystemdReAttachPublishesSelfObservedRunning(t *testing.T) {
	binPath, artifactPath := systemdTestSymlink(t)
	store := &fakeOperatorStore{}
	dep := systemdTestDeployment(binPath)
	prev := apigen.RunnerStatus{
		DeploymentConfigVersion: 5,
		RunningArtifact:         "/old/artifact",
		RunningPid:              123,
		Status:                  apigen.RunningStatus_STARTING,
		NumberOfRestarts:        2,
	}

	r := reAttachSystemdRunner(store, dep, prev)
	r.Stop()

	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want 1", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("status = %v, want RUNNING", got.Status)
	}
	if got.DeploymentConfigVersion != prev.DeploymentConfigVersion {
		t.Fatalf("deployment config version = %d, want %d", got.DeploymentConfigVersion, prev.DeploymentConfigVersion)
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

func TestObserveExistingSystemdRunnerPublishesCurrentProcessRunning(t *testing.T) {
	binPath, artifactPath := systemdTestSymlink(t)
	store := &fakeOperatorStore{}
	dep := systemdTestDeployment(binPath)

	r := observeExistingSystemdRunner(store, dep)
	r.Stop()

	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want 1", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("status = %v, want RUNNING", got.Status)
	}
	if got.DeploymentConfigVersion != dep.Version {
		t.Fatalf("deployment config version = %d, want %d", got.DeploymentConfigVersion, dep.Version)
	}
	if got.RunningPid != int32(os.Getpid()) {
		t.Fatalf("running pid = %d, want %d", got.RunningPid, os.Getpid())
	}
	if got.RunningArtifact != artifactPath {
		t.Fatalf("running artifact = %q, want %q", got.RunningArtifact, artifactPath)
	}
}

func TestSystemdRestartLeavesStatusStartingForRestartedProcess(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "bin", "opendeploy")
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
	dep := systemdTestDeployment(binPath)
	dep.Version = 9
	preparerStatus := apigen.PreparerStatus{
		DeploymentConfigVersion: dep.Version,
		Artifact:                artifactPath,
	}

	restartCalls := 0
	prevRestart := systemctlRestartCommand
	systemctlRestartCommand = func(context.Context, string) (string, error) {
		restartCalls++
		return "", nil
	}
	defer func() { systemctlRestartCommand = prevRestart }()

	r := newSystemdRunnerWithRestart(store, dep, preparerStatus)
	r.Stop()

	if restartCalls != 1 {
		t.Fatalf("restart calls = %d, want 1", restartCalls)
	}
	if target, evalErr := filepath.EvalSymlinks(binPath); evalErr != nil || target != resolvedArtifactPath {
		t.Fatalf("bin symlink target = %q, %v; want %q", target, evalErr, resolvedArtifactPath)
	}
	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runner status writes = %d, want only initial STARTING", len(statuses))
	}
	got := statuses[0]
	if got.Status != apigen.RunningStatus_STARTING {
		t.Fatalf("status = %v, want STARTING", got.Status)
	}
	if got.DeploymentConfigVersion != dep.Version {
		t.Fatalf("deployment config version = %d, want %d", got.DeploymentConfigVersion, dep.Version)
	}
	if got.RunningArtifact != artifactPath {
		t.Fatalf("running artifact = %q, want %q", got.RunningArtifact, artifactPath)
	}
}

func systemdTestDeployment(binPath string) *apigen.DeploymentConfig {
	return &apigen.DeploymentConfig{
		ID:      1,
		Version: 7,
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{Systemd: apigen.SystemdRunnerConfig{
			Name:    "opendeploy",
			BinPath: binPath,
		}}},
	}
}

func systemdTestSymlink(t *testing.T) (string, string) {
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
	resolvedArtifactPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	return binPath, resolvedArtifactPath
}
