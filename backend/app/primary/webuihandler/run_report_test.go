package webuihandler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestRunReportCarriesTheCrashReasonOfARunTheAgentEnded(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	spec.Workload.Value.Container.Version = "1.25"
	created := createTestDeployment(store, "primary", 1, "web", &spec)
	inst := statetest.CreateScheduledInstance(store, created.Deployment.ID, created.Meta.SpecVersion, created.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	started := time.Now().Add(-time.Minute)
	write := func(restarts uint32, status apigen.RunningStatus, exitCode apigen.Maybe[int32], reason apigen.CrashReason) {
		scheduledinstances.WriteStatus(store, inst.ID, func(s *apigen.ScheduledInstanceStatus) bool {
			s.BumpUpdatedAt()
			runner := apigen.RunnerStatus{
				DeploymentSpecVersion: created.Meta.SpecVersion,
				Status:                status,
				NumberOfRestarts:      restarts,
				LastRestartAt:         apigen.Some(started),
				ExitCode:              exitCode,
				CrashReason:           reason,
			}
			if status == apigen.RunningStatus_RUNNING_STATUS_RUNNING {
				runner.RunningPid = apigen.Some(uint32(42))
			}
			s.Runner = apigen.Some(runner)
			return true
		})
	}
	none := apigen.CrashReason_CRASH_REASON_UNSPECIFIED
	write(0, apigen.RunningStatus_RUNNING_STATUS_RUNNING, apigen.Maybe[int32]{}, none)
	write(0, apigen.RunningStatus_RUNNING_STATUS_CRASHED, apigen.Some(int32(143)), apigen.CrashReason_CRASH_REASON_NETWORK_RECOVERY_FAILED)
	write(1, apigen.RunningStatus_RUNNING_STATUS_RUNNING, apigen.Maybe[int32]{}, none)
	write(1, apigen.RunningStatus_RUNNING_STATUS_CRASHED, apigen.Some(int32(1)), none)
	write(1, apigen.RunningStatus_RUNNING_STATUS_CRASHED, apigen.Some(int32(1)), apigen.CrashReason_CRASH_REASON_READINESS_TIMED_OUT)
	write(2, apigen.RunningStatus_RUNNING_STATUS_RUNNING, apigen.Maybe[int32]{}, none)
	write(2, apigen.RunningStatus_RUNNING_STATUS_CRASHED, apigen.Some(int32(2)), none)

	h := &Handler{SystemConfig: testConfigService(t, store), Store: store, Queries: store.Queries(), NodeID: created.Deployment.PlacementNodeID()}
	for _, tc := range []struct {
		run      int32
		exitCode int32
		reason   apigen.CrashReason
	}{
		{1, 143, apigen.CrashReason_CRASH_REASON_NETWORK_RECOVERY_FAILED},
		{2, 1, apigen.CrashReason_CRASH_REASON_READINESS_TIMED_OUT},
		{3, 2, none},
	} {
		report, err := h.PostV1DeploymentsRunReport(apigen.Context{}, &apigen.DeploymentRunReportRequest{ScheduledInstanceID: inst.ID, Run: tc.run})
		if err != nil {
			t.Fatalf("run %d: PostV1DeploymentsRunReport = %v", tc.run, err)
		}
		if report.Status != apigen.RunningStatus_RUNNING_STATUS_CRASHED {
			t.Fatalf("run %d: status = %v, want CRASHED", tc.run, report.Status)
		}
		if !report.ExitCode.Present || report.ExitCode.Value != tc.exitCode {
			t.Fatalf("run %d: exit code = %+v, want %d", tc.run, report.ExitCode, tc.exitCode)
		}
		if report.CrashReason != tc.reason {
			t.Fatalf("run %d: crash reason = %v, want %v", tc.run, report.CrashReason, tc.reason)
		}
	}
}
