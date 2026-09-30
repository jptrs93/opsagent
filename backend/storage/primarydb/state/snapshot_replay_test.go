package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func bootstrapDeploymentMutations(t *testing.T, q *pq.Queries, deploymentID int32) int {
	t.Helper()
	count := 0
	for _, m := range erru.Must(q.BootstrapMutations(context.Background())) {
		if m.Type == apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT && m.ID == int64(deploymentID) {
			count++
		}
	}
	return count
}

func TestBootstrapEqualsReplayThroughCreateUpdateDeleteAndFinalization(t *testing.T) {
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
				assertFoldEqual(t, label, bootstrapFold(t, s.q), retainForBootstrap(replay))
				assertBootstrapPromotesCreates(t, s.q)
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
	if got := bootstrapDeploymentMutations(t, s.q, cfg.DeploymentID); got != 2 {
		t.Fatalf("deleted deployment must retain pin and tombstone, bootstrap holds %d mutations", got)
	}
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	check("finalization releases deleted deployment")
	if got := bootstrapDeploymentMutations(t, s.q, cfg.DeploymentID); got != 0 {
		t.Fatalf("unreferenced versions not pruned, bootstrap holds %d mutations", got)
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
	commit := func(mutate func(q *pq.Queries, seq int64) (*WriteUpdate, error)) {
		t.Helper()
		if err := s.Commit(ctx, nil, mutate); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	configEvent := apigen.ConfigEvent{EventTime: now, CreatedTime: now, Author: 1, ConfigID: 1, Version: 1, ValueVersion: 1,
		Value: apigen.Config{Fs: &apigen.ConfigFs{Name: "config"}, SpaceID: 1, Value: "one"}, EventType: apigen.EventType_EVENT_TYPE_CREATE}
	writeConfig := func(eventType apigen.EventType, name string) {
		commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
			event := configEvent
			event.EventID, event.Seq, event.EventType = 0, seq, eventType
			event.Value.Fs = &apigen.ConfigFs{Name: name}
			if err := q.InsertConfigEvent(ctx, &event); err != nil {
				return nil, err
			}
			configEvent = event
			configEvent.Version++
			return pq.NewUpdate(pq.ConfigMutation(&event)), nil
		})
	}
	writeConfig(apigen.EventType_EVENT_TYPE_CREATE, "config")
	check("create config")
	writeConfig(apigen.EventType_EVENT_TYPE_UPDATE, "renamed")
	check("rename config retains history")
	writeConfig(apigen.EventType_EVENT_TYPE_DELETE, "renamed")
	check("delete config removes history")
	a := setAssetByKeyForTest(s, "asset", []byte("value"))
	check("create asset")
	setAssetByKeyForTest(s, "asset", []byte("value two"))
	check("update asset retains history")
	deleteAssetForTest(s, a.AssetID)
	check("delete asset removes history")
	sealed := pq.SealedValue{SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{1}}
	commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		written, err := q.InsertSecretEvent(ctx, pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, Author: 1, SecretID: 1,
			Version: 1, ValueVersion: 1, ValueChanged: 1, Name: "secret", SpaceID: 1,
			SmkVersion: sealed.SmkVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, EventType: pq.EventCreate})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SecretMutation(written, sealed)), nil
	})
	check("create secret")
	commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		written, carried, err := q.InsertSecretCarryEvent(ctx, pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, SecretID: 1,
			Version: 2, ValueVersion: 1, Name: "renamed", SpaceID: 1, EventType: pq.EventUpdate})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SecretMutation(written, carried)), nil
	})
	check("rename secret carries the sealed value")
	commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		written, carried, err := q.InsertSecretCarryEvent(ctx, pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, SecretID: 1,
			Version: 3, ValueVersion: 1, Name: "renamed", SpaceID: 1, EventType: pq.EventDelete})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SecretMutation(written, carried)), nil
	})
	check("delete secret removes history")
	space := createSpaceForTest(s, "empty")
	check("create space")
	deleteSpaceForTest(s, space.ID)
	check("delete space")
	var dir *apigen.ValueDirectory
	commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextValueDirectoryID(ctx)
		if err != nil {
			return nil, err
		}
		var m pq.Mutation
		dir, m, err = q.InsertValueDirectoryEvent(ctx, pq.ValueDirectoryEventParams{EventMeta: pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: 1, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, DirectoryID: id, SpaceID: 1, Name: "folder", CreatedAt: now})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	})
	check("create value directory")
	commit(func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		_, m, err := q.InsertValueDirectoryEvent(ctx, pq.ValueDirectoryEvent(dir, pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: 1, EventType: apigen.AuthzVerb_AUTHZ_VERB_DELETE}))
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	})
	check("delete value directory")
	assetDir := createAssetDirectoryForTest(s, 1, 0, "folder", 1)
	check("create asset directory")
	deleteAssetDirectoryForTest(s, int32(assetDir.ID))
	check("delete asset directory")
}
