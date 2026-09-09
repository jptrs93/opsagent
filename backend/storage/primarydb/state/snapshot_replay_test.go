package state

import (
	"context"
	"encoding/json"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// This deliberately small test reducer expresses the wire contract independently
// of the snapshot SQL. It has no store reads and folds entire committed updates.
func foldSnapshot(s *apigen.Snapshot, u any) {
	old := reflect.ValueOf(s).Elem()
	incoming := reflect.ValueOf(u)
	for i := 0; i < old.NumField(); i++ {
		name := old.Type().Field(i).Name
		dst := old.Field(i)
		src := incoming.FieldByName(name)
		if !src.IsValid() || name == "Seq" {
			continue
		}
		if dst.Kind() == reflect.Pointer {
			if !src.IsNil() {
				dst.Set(src)
			}
			continue
		}
		if dst.Kind() != reflect.Slice {
			continue
		}
		if src.Kind() == reflect.Pointer {
			if !src.IsNil() {
				dst.Set(src.Elem().FieldByName("Items"))
			}
			continue
		}
		if src.Len() == 0 {
			continue
		}
		ids := map[string]string{"DeploymentEvents": "DeploymentID", "ScheduledInstanceEvents": "ScheduledInstanceID", "InstanceStatuses": "ScheduledInstanceID", "NodeEvents": "NodeID", "NodeStatuses": "NodeID", "SecretEvents": "SecretID", "ConfigEvents": "ConfigID", "AssetEvents": "AssetID", "NetworkPolicyEvents": "NetworkPolicyID", "AuthzGrantEvents": "AuthzGrantID"}
		idField := ids[name]
		if idField == "" {
			idField = "ID"
		}
		history := name == "DeploymentEvents" || name == "SecretEvents" || name == "ConfigEvents" || name == "AssetEvents"
		for j := 0; j < src.Len(); j++ {
			item := src.Index(j)
			value := item.Elem()
			id := value.FieldByName(idField).Interface()
			deleted := false
			if flag := value.FieldByName("Deleted"); flag.IsValid() {
				deleted = flag.Bool()
			}
			if event := value.FieldByName("EventType"); event.IsValid() && event.Int() == 3 && name != "DeploymentEvents" {
				deleted = true
			}
			kept := reflect.MakeSlice(dst.Type(), 0, dst.Len()+1)
			for k := 0; k < dst.Len(); k++ {
				previous := dst.Index(k).Elem()
				same := reflect.DeepEqual(id, previous.FieldByName(idField).Interface())
				if same && history && !deleted {
					same = previous.FieldByName("Version").Int() == value.FieldByName("Version").Int()
				}
				if !same {
					kept = reflect.Append(kept, dst.Index(k))
				}
			}
			if !deleted {
				kept = reflect.Append(kept, item)
			}
			dst.Set(kept)
		}
	}
	if core, ok := u.(apigen.CoreUpdate); ok {
		s.Seq = core.Seq
	}
	// The deployment row selector retains live placements or the newest final
	// per ordinal, and pins only their versions plus the latest desired event.
	latest := map[int32]*apigen.DeploymentEvent{}
	for _, e := range s.DeploymentEvents {
		if latest[e.DeploymentID] == nil || e.Version > latest[e.DeploymentID].Version {
			latest[e.DeploymentID] = e
		}
	}
	type ordinal struct{ deployment, ordinal int32 }
	live := map[ordinal]bool{}
	final := map[ordinal]int32{}
	for _, e := range s.ScheduledInstanceEvents {
		k := ordinal{e.Value.DeploymentID, e.Value.InstanceOrdinal}
		if !e.Value.State.IsFinal() {
			live[k] = true
		} else if e.ScheduledInstanceID > final[k] {
			final[k] = e.ScheduledInstanceID
		}
	}
	held := map[int32]bool{}
	pins := map[[2]int32]bool{}
	instances := s.ScheduledInstanceEvents[:0]
	for _, e := range s.ScheduledInstanceEvents {
		k := ordinal{e.Value.DeploymentID, e.Value.InstanceOrdinal}
		if e.Value.State.IsFinal() && (live[k] || e.ScheduledInstanceID != final[k] || latest[e.Value.DeploymentID].Deleted()) {
			continue
		}
		instances = append(instances, e)
		held[e.ScheduledInstanceID] = true
		pins[[2]int32{e.Value.DeploymentID, e.Value.DeploymentVersion}] = true
	}
	s.ScheduledInstanceEvents = instances
	for id, e := range latest {
		if !e.Deleted() {
			pins[[2]int32{id, e.Version}] = true
			continue
		}
		for pin := range pins {
			if pin[0] == id {
				pins[[2]int32{id, e.Version}] = true
				break
			}
		}
	}
	events := s.DeploymentEvents[:0]
	for _, e := range s.DeploymentEvents {
		if pins[[2]int32{e.DeploymentID, e.Version}] {
			events = append(events, e)
		}
	}
	s.DeploymentEvents = events
	statuses := s.InstanceStatuses[:0]
	for _, e := range s.InstanceStatuses {
		if held[e.ScheduledInstanceID] {
			statuses = append(statuses, e)
		}
	}
	s.InstanceStatuses = statuses
}

func canonicalSnapshot(s *apigen.Snapshot) string {
	// Sort each array by its complete wire value; nil and empty are equivalent.
	copy := *s
	v := reflect.ValueOf(&copy).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Slice {
			continue
		}
		a := reflect.MakeSlice(f.Type(), f.Len(), f.Len())
		reflect.Copy(a, f)
		sort.Slice(a.Interface(), func(i, j int) bool {
			x, _ := json.Marshal(a.Index(i).Interface())
			y, _ := json.Marshal(a.Index(j).Interface())
			return string(x) < string(y)
		})
		f.Set(a)
	}
	b, _ := json.Marshal(copy)
	return string(b)
}

func TestSnapshotEqualsReplayThroughCreateUpdateDeleteAndFinalization(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	node := testNode(s, "primary")
	replay := s.BuildSnapshot(context.Background())
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(label string) {
		t.Helper()
		count := 0
		for {
			select {
			case update := <-sub:
				count++
				if update.Seq != replay.Seq+1 {
					t.Fatalf("%s: transaction seq %d after %d", label, update.Seq, replay.Seq)
				}
				assertUpdateMatchesRows(t, s, update)
				for _, e := range update.DeploymentEvents {
					if e.Seq != update.Seq {
						t.Fatal("deployment seq differs from transaction")
					}
				}
				for _, e := range update.ScheduledInstanceEvents {
					if e.Seq != update.Seq {
						t.Fatal("instance seq differs from transaction")
					}
				}
				foldSnapshot(replay, update)
				replay.Seq = update.Seq
			default:
				if count != 1 {
					t.Fatalf("%s published %d updates, want one", label, count)
				}
				got, want := canonicalSnapshot(s.BuildSnapshot(context.Background())), canonicalSnapshot(replay)
				if got != want {
					t.Fatalf("%s: snapshot differs from replay\nsnapshot: %s\nreplay: %s", label, got, want)
				}
				return
			}
		}
	}
	testNode(s, "another")
	check("new node and initial observed state")
	cfg := mustCreateDeploymentForNode(s, apigen.Context{}, 1, "api", node.ID, testSpecWithState("v1", false))
	check("create deployment")
	inst := createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("pin original version")
	mustSetDeploymentWorkloadState(s, apigen.Context{}, cfg.DeploymentID, "v2", false)
	check("update desired while pinned")
	deleteDeployment(s, apigen.Context{}, cfg.DeploymentID)
	check("delete while pinned")
	if len(replay.DeploymentEvents) != 2 {
		t.Fatal("deleted deployment must retain pin and tombstone")
	}
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	check("finalization releases deleted deployment")
	if len(replay.DeploymentEvents) != 0 {
		t.Fatal("unreferenced versions not pruned")
	}
	cfg = mustCreateDeploymentForNode(s, apigen.Context{}, 1, "api", node.ID, testSpecWithState("v1", false))
	check("create another deployment")
	inst = createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("new run")
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	check("retain final for live deployment")
	createScheduledInstanceForTest(s, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	check("supersede final")
	commit := func(mutate func(q *pq.Queries, seq int64) (*Update, error)) {
		t.Helper()
		if err := s.Commit(context.Background(), nil, mutate); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	configEvent := apigen.ConfigEvent{EventTime: now, CreatedTime: now, Author: 1, ConfigID: 1, Version: 1, ValueVersion: 1, SpaceVersion: 1,
		Value: apigen.Config{Fs: &apigen.ConfigFs{Name: "config"}, SpaceID: 1, Value: "one"}, EventType: apigen.EventType_EVENT_TYPE_CREATE}
	writeConfig := func(eventType apigen.EventType, name string) {
		commit(func(q *pq.Queries, seq int64) (*Update, error) {
			event := configEvent
			event.EventID, event.Seq, event.EventType = 0, seq, eventType
			event.Value.Fs = &apigen.ConfigFs{Name: name}
			if err := q.InsertConfigEvent(context.Background(), &event); err != nil {
				return nil, err
			}
			configEvent = event
			configEvent.Version++
			return &Update{ConfigEvents: []*apigen.ConfigEvent{&event}}, nil
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
	deleteAssetForTest(s, a.AssetID)
	check("delete asset removes history")
	commit(func(q *pq.Queries, seq int64) (*Update, error) {
		written, err := q.InsertSecretEvent(context.Background(), pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, Author: 1, SecretID: 1,
			Version: 1, ValueVersion: 1, SpaceVersion: 1, ValueChanged: 1, SpaceChanged: 1, Name: "secret", SpaceID: 1,
			SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{1}, EventType: pq.EventCreate})
		if err != nil {
			return nil, err
		}
		return &Update{SecretEvents: []*apigen.SecretEvent{written}}, nil
	})
	check("create secret")
	commit(func(q *pq.Queries, seq int64) (*Update, error) {
		written, err := q.InsertSecretCarryEvent(context.Background(), pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, SecretID: 1,
			Version: 2, ValueVersion: 1, SpaceVersion: 1, Name: "secret", SpaceID: 1, EventType: pq.EventDelete})
		if err != nil {
			return nil, err
		}
		return &Update{SecretEvents: []*apigen.SecretEvent{written}}, nil
	})
	check("delete secret removes history")
	space := createSpaceForTest(s, "empty")
	check("space and node transaction")
	deleteSpaceForTest(s, space.ID)
	check("hard delete space")
	var dir *apigen.ValueDirectory
	commit(func(q *pq.Queries, seq int64) (*Update, error) {
		var err error
		dir, err = q.InsertValueDirectory(context.Background(), pq.InsertValueDirectoryParams{SpaceID: 1, Name: "folder", CreatedAt: now, Author: 1})
		if err != nil {
			return nil, err
		}
		return &Update{ValueDirectories: []*apigen.ValueDirectory{dir}}, nil
	})
	check("create value directory")
	commit(func(q *pq.Queries, seq int64) (*Update, error) {
		if err := q.DeleteValueDirectory(context.Background(), int64(dir.ID)); err != nil {
			return nil, err
		}
		tombstone := *dir
		tombstone.Deleted = true
		return &Update{ValueDirectories: []*apigen.ValueDirectory{&tombstone}}, nil
	})
	check("hard delete value directory")

	assetDir := createAssetDirectoryForTest(s, 1, 0, "folder", 1)
	check("create asset directory")
	deleteAssetDirectoryForTest(s, int32(assetDir.ID))
	check("hard delete asset directory")

}
