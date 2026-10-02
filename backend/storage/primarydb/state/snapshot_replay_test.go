package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSnapshotEqualsReplayThroughCreateUpdateDeleteAndFinalization(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := testNode(s, "primary")
	replay := fullFold(t, s.q)
	replaySeq := erru.Must(s.q.GetGlobalSeq(ctx))
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(label string) {
		t.Helper()
		count := 0
		for {
			select {
			case update := <-sub:
				count++
				if update.Seq != replaySeq+1 {
					t.Fatalf("%s: transaction seq %d after %d", label, update.Seq, replaySeq)
				}
				assertUpdateMatchesRows(t, s, update)
				foldInto(replay, &update)
				replaySeq = update.Seq
			default:
				if count != 1 {
					t.Fatalf("%s published %d updates, want one", label, count)
				}
				assertFoldMatchesLiveTables(t, s, replay)
				assertFoldEqual(t, label, snapshotFold(t, s.q), retainForSnapshot(replay))
				assertSnapshotMatchesRebuild(t, s.q)
				return
			}
		}
	}
	testNode(s, "another")
	check("new node")
	setNodeStatusForTest(s, node.Identifier, true, time.Now())
	check("node observed state")
	cfg := mustCreateDeploymentForNodeRunning(s, apigen.Context{}, 1, "api", node.ID, false, testSpecWithVersion("v1"))
	check("create deployment")
	inst := createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("pin original version")
	writeInstanceStatusForTest(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING, RunningPid: 42}
	})
	check("instance observed state")
	mustSetDeploymentWorkloadState(s, apigen.Context{}, cfg.DeploymentID, "v2", false)
	check("update desired while pinned")
	deleteDeployment(s, apigen.Context{}, cfg.DeploymentID)
	check("delete while pinned")
	if got := snapshotEntries(t, s.q, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, int64(cfg.DeploymentID)); len(got) != 1 || !got[0].Meta.Deleted || got[0].Meta.Version != 1 {
		t.Fatalf("deleted deployment must retain the pinned version marked deleted, snapshot holds %+v", got)
	}
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	check("finalization releases deleted deployment")
	if got := snapshotEntries(t, s.q, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, int64(cfg.DeploymentID)); len(got) != 0 {
		t.Fatalf("unreferenced versions not pruned, snapshot holds %d entries", len(got))
	}
	cfg = mustCreateDeploymentForNodeRunning(s, apigen.Context{}, 1, "api", node.ID, false, testSpecWithVersion("v1"))
	check("create another deployment")
	inst = createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("new run")
	writeInstanceStatusForTest(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_STOPPED}
	})
	check("final observed state")
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	check("retain final for live deployment")
	createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("supersede final")
	config := createConfigForTest(s, "config", "one")
	check("create config")
	renameConfigForTest(s, config, "renamed")
	check("rename config retains history")
	writeConfigForTest(s, config, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, "two")
	check("update config appends a version")
	writeConfigForTest(s, config, apigen.AuthzVerb_AUTHZ_VERB_DELETE, "")
	check("delete config removes history")
	a := setAssetByKeyForTest(s, "asset", []byte("value"))
	check("create asset")
	setAssetByKeyForTest(s, "asset", []byte("value two"))
	check("update asset retains history")
	deleteAssetForTest(s, a.AssetID)
	check("delete asset removes history")
	secret := createSecretForTest(s, "secret")
	check("create secret")
	carrySecretForTest(s, secret, "renamed", apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	check("rename secret carries the sealed value")
	appendSecretVersionForTest(s, secret, []byte{7})
	check("update secret appends a version")
	carrySecretForTest(s, secret, "renamed", apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	check("delete secret removes history")
	space := createSpaceForTest(s, "empty")
	check("create space")
	deleteSpaceForTest(s, space.ID)
	check("delete space")
	dir := createValueDirectoryForTest(s, 1, 0, "folder", 1)
	check("create value directory")
	deleteValueDirectoryForTest(s, dir.ID)
	check("delete value directory")
	assetDir := createAssetDirectoryForTest(s, 1, 0, "folder", 1)
	check("create asset directory")
	deleteAssetDirectoryForTest(s, int32(assetDir.ID))
	check("delete asset directory")
}
