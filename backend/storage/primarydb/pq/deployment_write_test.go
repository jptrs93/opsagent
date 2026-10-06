package pq

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func testSpec() apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func TestDeploymentBuildersPreserveAttributionAndVersionFacts(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := apigen.Context{Ctx: t.Context(), User: &apigen.User{ID: 7}, Delegated: true}
	def := &apigen.Deployment{Name: "app", SpaceID: 1, Spec: testSpec(), Scheduling: apigen.DedicatedScheduling(true, 1)}
	now := time.UnixMilli(time.Now().UnixMilli())
	commit := func(seq int64, record *apigen.DeploymentRecord) {
		t.Helper()
		commitForTest(t, q, seq, record.Meta.UpdatedTime, record.Meta.UpdatedActor, DeploymentMutation(record).Wire())
		if record.Deleted() {
			return
		}
		row, err := q.GetDeploymentVersion(ctx, record.Deployment.ID, record.Meta.Version)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, row) {
			t.Fatalf("builder returned a different projection from reads\ngot: %+v\nrow: %+v", record, row)
		}
	}
	created := DeploymentCreateRecord(ctx, 42, 100, now, def)
	if created.Deployment.ID != 42 || created.Meta.UpdatedSeq != 100 || created.Meta.UpdatedActor != -7 || created.Meta.Version != 1 || created.Deleted() || created.Meta.CreatedTime != now.UnixMilli() || created.Meta.UpdatedTime != now.UnixMilli() {
		t.Fatalf("create metadata = %+v", created)
	}
	commit(100, created)
	if record, err := q.DeploymentUpdateRecord(ctx, 42, 101, now.Add(time.Second), def); !errors.Is(err, ErrDeploymentUnchanged) || record != nil {
		t.Fatalf("unchanged update: %+v %v", record, err)
	}
	def.Name = "renamed"
	updated, err := q.DeploymentUpdateRecord(ctx, 42, 101, now.Add(time.Second), def)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Meta.UpdatedActor != -7 || updated.Meta.UpdatedSeq != 101 || updated.Meta.Version != 2 || updated.Meta.SpecVersion != created.Meta.SpecVersion || updated.Deleted() || updated.Meta.CreatedTime != created.Meta.CreatedTime || updated.Meta.UpdatedTime != now.Add(time.Second).UnixMilli() {
		t.Fatalf("update metadata = %+v", updated)
	}
	commit(101, updated)
	if rows, err := q.ListRetainedDeploymentVersions(ctx); err != nil || len(rows) != 1 || rows[0].Meta.Version != 2 {
		t.Fatalf("unpinned superseded version retained: %+v, %v", rows, err)
	}
	var stored []byte
	if err := q.db.QueryRowContext(ctx, `SELECT value FROM deployment_versions WHERE deployment_id = 42 AND version = 2`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if blob, err := apigen.DecodeDeployment(stored); err != nil || blob.Name != "renamed" || blob.ID != 0 {
		t.Fatalf("stored blob = %+v %v", blob, err)
	}
	if meta, err := q.MetaOf(ctx, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, 42); err != nil || meta.Version != 2 || meta.SpecVersion != 1 || meta.CreatedTime != now.UnixMilli() || meta.UpdatedSeq != 101 || meta.UpdatedActor != -7 {
		t.Fatalf("row meta = %+v %v", meta, err)
	}
	deleted, err := q.DeploymentDeleteRecord(ctx, 42, 102, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted() || deleted.Meta.UpdatedActor != -7 || deleted.Meta.UpdatedSeq != 102 || deleted.Meta.Version != 2 || deleted.Meta.SpecVersion != updated.Meta.SpecVersion || !reflect.DeepEqual(deleted.Deployment, updated.Deployment) {
		t.Fatalf("delete metadata = %+v", deleted)
	}
	if m := DeploymentMutation(deleted).Wire(); m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		t.Fatalf("delete mutation = %+v", m)
	}
	commit(102, deleted)
	if _, err := q.GetLatestDeployment(ctx, 42); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted deployment still current: %v", err)
	}
	if rows, err := q.ListRetainedDeploymentVersions(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("unpinned versions retained after delete: %d, %v", len(rows), err)
	}
	for _, id := range []uint64{42, 43} {
		if record, err := q.DeploymentUpdateRecord(ctx, id, 103, now, def); !errors.Is(err, sql.ErrNoRows) || record != nil {
			t.Fatalf("update deleted/missing %d: %+v %v", id, record, err)
		}
		if record, err := q.DeploymentDeleteRecord(ctx, id, 103, now); !errors.Is(err, sql.ErrNoRows) || record != nil {
			t.Fatalf("delete deleted/missing %d: %+v %v", id, record, err)
		}
	}
	history, err := q.ListDeploymentHistory(ctx, 42)
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %d rows, %v", len(history), err)
	}
	if !reflect.DeepEqual(history[0], created) || !reflect.DeepEqual(history[1], updated) || !reflect.DeepEqual(history[2], deleted) {
		t.Fatalf("history differs from the builders\n%+v\n%+v\n%+v", history[0], history[1], history[2])
	}
	tombstones, err := q.ListDeletedDeployments(ctx)
	if err != nil || len(tombstones) != 1 || !reflect.DeepEqual(tombstones[0], deleted) {
		t.Fatalf("deleted listing = %+v, %v", tombstones, err)
	}
	seq, err := q.GetGlobalSeq(context.Background())
	if err != nil || seq != 102 {
		t.Fatalf("global counter = %d, %v", seq, err)
	}
}
