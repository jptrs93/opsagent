package state

import (
	"bytes"
	"cmp"
	"context"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type foldState = map[apigen.CoreEntityType]map[int64]*apigen.CoreEntity

func canonicalUpdate(update WriteUpdate) []byte {
	cp := update
	cp.Mutations = slices.Clone(update.Mutations)
	sort.Slice(cp.Mutations, func(i, j int) bool { return bytes.Compare(cp.Mutations[i].Encode(), cp.Mutations[j].Encode()) < 0 })
	return cp.Encode()
}

func assertUpdateMatchesRows(t *testing.T, s *Service, update WriteUpdate) {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(s.q.GetGlobalSeq(ctx))
	if update.Seq != seq {
		t.Fatalf("published sequence = %d, database sequence = %d", update.Seq, seq)
	}
	assertUpdateReplays(t, s, update)
}

func assertUpdateReplays(t *testing.T, s *Service, update WriteUpdate) {
	t.Helper()
	events := pq.Events(erru.Must(s.q.MutationsInRange(context.Background(), update.Seq-1, update.Seq)))
	if len(events) != 1 {
		t.Fatalf("seq %d replays as %d events", update.Seq, len(events))
	}
	if !bytes.Equal(canonicalUpdate(update), canonicalUpdate(*events[0])) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, *events[0])
	}
}

func foldInto(state foldState, events ...*apigen.CoreWriteUpdate) {
	for _, e := range events {
		for _, m := range e.Mutations {
			if state[m.Type()] == nil {
				state[m.Type()] = map[int64]*apigen.CoreEntity{}
			}
			if m.Delete != nil {
				delete(state[m.Type()], m.EntityID())
				continue
			}
			state[m.Type()][m.EntityID()] = m.Entity()
		}
	}
}

func fullFold(t *testing.T, q *pq.Queries) foldState {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(q.GetGlobalSeq(ctx))
	state := foldState{}
	foldInto(state, pq.Events(erru.Must(q.MutationsInRange(ctx, -1, seq)))...)
	return state
}

func bootstrapFold(t *testing.T, q *pq.Queries) foldState {
	t.Helper()
	state := foldState{}
	foldInto(state, pq.Events(erru.Must(q.BootstrapMutations(context.Background())))...)
	return state
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func assertFoldEqual(t *testing.T, label string, got, want foldState) {
	t.Helper()
	types := map[apigen.CoreEntityType]bool{}
	for typ := range got {
		types[typ] = true
	}
	for typ := range want {
		types[typ] = true
	}
	for _, typ := range sortedKeys(types) {
		ids := map[int64]bool{}
		for id := range got[typ] {
			ids[id] = true
		}
		for id := range want[typ] {
			ids[id] = true
		}
		for _, id := range sortedKeys(ids) {
			g, w := got[typ][id], want[typ][id]
			switch {
			case g == nil:
				t.Fatalf("%s: %v %d missing from the fold, want %+v", label, typ, id, w)
			case w == nil:
				t.Fatalf("%s: %v %d unexpected in the fold: %+v", label, typ, id, g)
			case !bytes.Equal(g.Encode(), w.Encode()):
				t.Fatalf("%s: %v %d differs\ngot: %+v\nwant: %+v", label, typ, id, g, w)
			}
		}
	}
}

// retainForBootstrap applies the bootstrap retention rule to a full fold:
// finalized instances stay only as the newest final of an ordinal with no
// live instance under a live deployment, and statuses follow their instance.
func retainForBootstrap(full foldState) foldState {
	out := foldState{}
	for typ, entities := range full {
		out[typ] = maps.Clone(entities)
	}
	instances := full[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE]
	type ordinal struct{ deployment, ordinal int32 }
	live := map[ordinal]bool{}
	newestFinal := map[ordinal]int64{}
	for id, e := range instances {
		k := ordinal{e.ScheduledInstance.DeploymentID, e.ScheduledInstance.InstanceOrdinal}
		if !e.ScheduledInstance.State.IsFinal() {
			live[k] = true
		} else if id > newestFinal[k] {
			newestFinal[k] = id
		}
	}
	for id, e := range instances {
		inst := e.ScheduledInstance
		k := ordinal{inst.DeploymentID, inst.InstanceOrdinal}
		if inst.State.IsFinal() && (live[k] || newestFinal[k] != id || full[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT][int64(inst.DeploymentID)] == nil) {
			delete(out[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE], id)
		}
	}
	for id := range full[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS] {
		if out[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE][id] == nil {
			delete(out[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS], id)
		}
	}
	return out
}

func assertBootstrapPromotesCreates(t *testing.T, q *pq.Queries) {
	t.Helper()
	type key struct {
		typ apigen.CoreEntityType
		id  int64
	}
	seen := map[key]bool{}
	for _, e := range pq.Events(erru.Must(q.BootstrapMutations(context.Background()))) {
		for _, m := range e.Mutations {
			k := key{m.Type(), m.EntityID()}
			if seen[k] {
				continue
			}
			seen[k] = true
			if kind := m.Kind(); kind != apigen.AuthzVerb_AUTHZ_VERB_CREATE && kind != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
				t.Fatalf("bootstrap opens %v %d with %v at seq %d", k.typ, k.id, kind, e.Seq)
			}
		}
	}
}

var allNodeStatuses = []int64{0, 1, 2, 3, 4, 5, 6, 7, 8}

// assertFoldMatchesLiveTables checks a fold of the full event range against
// the live tables: the same entity ids as the pq getters list, the payload
// of the newest row, and the getter's own view of the entity.
func assertFoldMatchesLiveTables(t *testing.T, s *Service, fold foldState) {
	t.Helper()
	ctx := context.Background()
	q := s.q
	entity := func(m pq.Mutation) *apigen.CoreEntity { return &m.Entity }
	live := foldState{}
	put := func(typ apigen.CoreEntityType, id int64, e *apigen.CoreEntity) {
		if live[typ] == nil {
			live[typ] = map[int64]*apigen.CoreEntity{}
		}
		live[typ][id] = e
	}
	for _, e := range erru.Must(q.ListLatestDeploymentEvents(ctx)) {
		if !e.Deleted() {
			put(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, int64(e.DeploymentID), entity(pq.DeploymentMutation(e)))
		}
	}
	for _, e := range erru.Must(q.ListLatestScheduledInstanceEvents(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, int64(e.ScheduledInstanceID), entity(pq.ScheduledInstanceMutation(e)))
	}
	for _, row := range erru.Must(q.ListNodeRows(ctx, allNodeStatuses)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NODE, int64(row.Event.NodeID), entity(pq.NodeMutation(&row.Event)))
	}
	for _, e := range erru.Must(q.ListLatestLiveSecretEvents(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SECRET, int64(e.SecretID), entity(pq.SecretMutation(e, pq.SealedValue{})))
	}
	for _, e := range erru.Must(q.ListLatestLiveConfigEvents(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_CONFIG, int64(e.ConfigID), entity(pq.ConfigMutation(e)))
	}
	for _, e := range erru.Must(q.ListLatestLiveNetworkPolicyEvents(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, int64(e.NetworkPolicyID), entity(pq.NetworkPolicyMutation(e)))
	}
	for _, sp := range erru.Must(q.ListSpaces(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SPACE, int64(sp.ID), entity(pq.SpaceMutation(pq.EventMeta{}, sp)))
	}
	for _, u := range erru.Must(q.ListUsers(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_USER, int64(u.ID), entity(pq.UserMutation(erru.Must(q.GetUserRow(ctx, int64(u.ID))))))
	}
	for _, typ := range []apigen.CoreEntityType{
		apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, apigen.CoreEntityType_CORE_ENTITY_NODE,
		apigen.CoreEntityType_CORE_ENTITY_SECRET, apigen.CoreEntityType_CORE_ENTITY_CONFIG, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY,
		apigen.CoreEntityType_CORE_ENTITY_SPACE, apigen.CoreEntityType_CORE_ENTITY_USER,
	} {
		ids := map[int64]bool{}
		for id := range fold[typ] {
			ids[id] = true
		}
		for id := range live[typ] {
			ids[id] = true
		}
		for _, id := range sortedKeys(ids) {
			got, want := fold[typ][id], live[typ][id]
			switch {
			case got == nil:
				t.Fatalf("%v %d is live in the tables but absent from the fold", typ, id)
			case want == nil:
				t.Fatalf("%v %d is in the fold but not live: %+v", typ, id, got)
			}
			latest := erru.Must(q.LatestMutation(ctx, typ, id))
			if !bytes.Equal(got.Encode(), latest.Entity.Encode()) {
				t.Fatalf("%v %d fold differs from the newest row\ngot: %+v\nrow: %+v", typ, id, got, latest.Entity)
			}
			projected := *got
			if typ == apigen.CoreEntityType_CORE_ENTITY_SECRET {
				secret := *got.Secret
				secret.SmkVersion, secret.Ciphertext, secret.Nonce = 0, nil, nil
				projected.Secret = &secret
			}
			if !bytes.Equal(projected.Encode(), want.Encode()) {
				t.Fatalf("%v %d fold differs from the getter\ngot: %+v\nwant: %+v", typ, id, projected, want)
			}
		}
	}
}

func createSecretForTest(s *Service, name string) int64 {
	ctx := context.Background()
	var id int64
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextSecretID(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		sealed := pq.SealedValue{SmkVersion: 1, Ciphertext: []byte{byte(id)}, Nonce: []byte{2}}
		written, err := q.InsertSecretEvent(ctx, pq.SecretEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, Author: 1, SecretID: id,
			Version: 1, ValueVersion: 1, ValueChanged: 1, Name: name, SpaceID: 1,
			SmkVersion: sealed.SmkVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, EventType: pq.EventCreate})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SecretMutation(written, sealed)), nil
	}))
	return id
}

func carrySecretForTest(s *Service, id int64, name string, eventType int64) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		prev, err := q.GetLatestSecretEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		written, sealed, err := q.InsertSecretCarryEvent(ctx, pq.SecretEvent{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, Author: 1, SecretID: id,
			Version: int64(prev.Version) + 1, ValueVersion: int64(prev.ValueVersion), Name: name, ValueDirectoryID: int64(prev.Value.Fs.DirectoryID), SpaceID: int64(prev.Value.SpaceID), EventType: eventType})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SecretMutation(written, sealed)), nil
	}))
}

func createConfigForTest(s *Service, name, value string) int64 {
	ctx := context.Background()
	var id int64
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextConfigID(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		event := apigen.ConfigEvent{Seq: seq, EventTime: now, CreatedTime: now, Author: 1, ConfigID: int32(id), Version: 1, ValueVersion: 1,
			Value: apigen.Config{Fs: &apigen.ConfigFs{Name: name}, SpaceID: 1, Value: value}, EventType: apigen.EventType_EVENT_TYPE_CREATE}
		if err := q.InsertConfigEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.ConfigMutation(&event)), nil
	}))
	return id
}

func writeConfigForTest(s *Service, id int64, eventType apigen.EventType, value string) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		prev, err := q.GetLatestConfigEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := *prev
		event.EventID, event.Seq, event.EventTime, event.EventType = 0, seq, time.Now().UnixMilli(), eventType
		event.Version++
		fs := *prev.Value.Fs
		event.Value.Fs = &fs
		if eventType == apigen.EventType_EVENT_TYPE_UPDATE {
			event.ValueVersion++
			event.Value.Value = value
		}
		if err := q.InsertConfigEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.ConfigMutation(&event)), nil
	}))
}

func createNetworkPolicyForTest(s *Service, author int32) int32 {
	ctx := context.Background()
	var event apigen.NetworkPolicyEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextNetworkPolicyID(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		event = apigen.NetworkPolicyEvent{NetworkPolicyID: int32(id), Version: 1, Seq: seq, Author: author, EventType: apigen.EventType_EVENT_TYPE_CREATE, CreatedTime: now, EventTime: now,
			Value: apigen.NetworkPolicy{Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
				Source:      &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1},
				Destination: &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}}}
		if err := q.InsertNetworkPolicyEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.NetworkPolicyMutation(&event)), nil
	}))
	return event.NetworkPolicyID
}

func deleteNetworkPolicyForTest(s *Service, id int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		prev, err := q.GetLatestNetworkPolicyEvent(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		event := *prev
		event.EventID, event.Seq, event.EventTime, event.EventType = 0, seq, time.Now().UnixMilli(), apigen.EventType_EVENT_TYPE_DELETE
		event.Version++
		if err := q.InsertNetworkPolicyEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.NetworkPolicyMutation(&event)), nil
	}))
}

func createUserForTest(s *Service, name string) int64 {
	ctx := context.Background()
	var id int64
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextUserID(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		if err := q.InsertUserEvent(ctx, pq.UserEventParams{EventMeta: pq.EventMeta{GlobalSeq: seq, EventTime: now, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, UserID: id, Name: name, DataBlob: []byte{}, CreatedAt: now}); err != nil {
			return nil, err
		}
		row, err := q.GetUserRow(ctx, id)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.UserMutation(row)), nil
	}))
	return id
}

type equivalenceScenario struct {
	pinned   *apigen.DeploymentEvent
	released *apigen.DeploymentEvent
	draining *apigen.DeploymentEvent
	empty    *apigen.DeploymentEvent
}

// seedEquivalenceScenario writes every entity type the fold oracles compare,
// with history the bootstrap compacts: a deployment whose older version a
// live instance pins, ordinals with a retained final, a superseded final, a
// deployment deleted while pinned and then released, one deleted while an
// instance still drains, one deleted with no instance, and value histories
// with renames, updates, and deletes.
func seedEquivalenceScenario(t *testing.T, s *Service) equivalenceScenario {
	t.Helper()
	ctx := apigen.Context{}
	node := testNode(s, "primary")
	other := testNode(s, "secondary")
	setNodeStatusForTest(s, node.Identifier, true, time.Now())
	setNodeStatusForTest(s, other.Identifier, false, time.Now())
	setNodeStatusForTest(s, node.Identifier, true, time.Now())
	running := func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING, RunningPid: 42}
	}
	stopped := func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_STOPPED}
	}
	pinned := mustCreateDeploymentForNodeRunning(s, ctx, 1, "pinned", node.ID, true, testSpecWithVersion("v1"))
	oldRun := createScheduledInstanceForTest(s, pinned.DeploymentID, pinned.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, oldRun.ID, running)
	mustSetDeploymentWorkloadState(s, ctx, pinned.DeploymentID, "v2", true)
	newRun := createScheduledInstanceForTest(s, pinned.DeploymentID, pinned.Version+1, other.ID, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, newRun.ID, running)
	writeInstanceStatusForTest(s, newRun.ID, running)
	retainedFinal := createScheduledInstanceForTest(s, pinned.DeploymentID, pinned.Version+1, node.ID, 2, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, retainedFinal.ID, stopped)
	setScheduledInstanceState(s, retainedFinal.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	superseded := createScheduledInstanceForTest(s, pinned.DeploymentID, pinned.Version+1, node.ID, 3, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, superseded.ID, stopped)
	setScheduledInstanceState(s, superseded.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	replacement := createScheduledInstanceForTest(s, pinned.DeploymentID, pinned.Version+1, node.ID, 3, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, replacement.ID, running)

	released := mustCreateDeploymentForNodeRunning(s, ctx, 1, "released", node.ID, true, testSpecWithVersion("v1"))
	releasedRun := createScheduledInstanceForTest(s, released.DeploymentID, released.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, releasedRun.ID, running)
	deleteDeployment(s, ctx, released.DeploymentID)
	writeInstanceStatusForTest(s, releasedRun.ID, stopped)
	setScheduledInstanceState(s, releasedRun.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	draining := mustCreateDeploymentForNodeRunning(s, ctx, 1, "draining", other.ID, true, testSpecWithVersion("v1"))
	drainingRun := createScheduledInstanceForTest(s, draining.DeploymentID, draining.Version, other.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, drainingRun.ID, running)
	deleteDeployment(s, ctx, draining.DeploymentID)
	setScheduledInstanceState(s, drainingRun.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)

	empty := mustCreateDeploymentForNodeRunning(s, ctx, 1, "empty", node.ID, false, testSpecWithVersion("v1"))
	moveDeploymentSpace(s, ctx, empty.DeploymentID, 2)
	deleteDeployment(s, ctx, empty.DeploymentID)

	kept := createSecretForTest(s, "kept")
	carrySecretForTest(s, kept, "kept-renamed", pq.EventUpdate)
	gone := createSecretForTest(s, "gone")
	carrySecretForTest(s, gone, "gone", pq.EventDelete)
	config := createConfigForTest(s, "config", "one")
	writeConfigForTest(s, config, apigen.EventType_EVENT_TYPE_UPDATE, "two")
	deletedConfig := createConfigForTest(s, "deleted", "one")
	writeConfigForTest(s, deletedConfig, apigen.EventType_EVENT_TYPE_DELETE, "")
	asset := setAssetByKeyForTest(s, "asset", []byte("value"))
	setAssetByKeyForTest(s, "asset", []byte("value two"))
	deleted := setAssetByKeyForTest(s, "deleted", []byte("value"))
	deleteAssetForTest(s, deleted.AssetID)
	if asset.AssetID == deleted.AssetID {
		t.Fatal("asset ids collided")
	}
	createNetworkPolicyForTest(s, 1)
	deleteNetworkPolicyForTest(s, createNetworkPolicyForTest(s, 2))
	createSpaceForTest(s, "kept")
	deleteSpaceForTest(s, createSpaceForTest(s, "gone").ID)
	createUserForTest(s, "alice")
	createUserForTest(s, "bob")
	dir := createAssetDirectoryForTest(s, 1, 0, "dir", 1)
	deleteAssetDirectoryForTest(s, int32(dir.ID))
	insertGrantForTest(t, s, 7, 3, 123)
	deleteGrantForTest(t, s, insertGrantForTest(t, s, 8, 3, 124))
	return equivalenceScenario{pinned: pinned, released: released, draining: draining, empty: empty}
}

func TestFullRangeFoldMatchesLiveTables(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	seedEquivalenceScenario(t, s)
	fold := fullFold(t, s.q)
	for _, typ := range []apigen.CoreEntityType{
		apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, apigen.CoreEntityType_CORE_ENTITY_NODE,
		apigen.CoreEntityType_CORE_ENTITY_SECRET, apigen.CoreEntityType_CORE_ENTITY_CONFIG, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY,
		apigen.CoreEntityType_CORE_ENTITY_SPACE, apigen.CoreEntityType_CORE_ENTITY_USER,
	} {
		if len(fold[typ]) == 0 {
			t.Fatalf("scenario left no live %v", typ)
		}
	}
	assertFoldMatchesLiveTables(t, s, fold)
}

func TestBootstrapFoldMatchesFullRangeFold(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	scenario := seedEquivalenceScenario(t, s)
	full := fullFold(t, s.q)
	want := retainForBootstrap(full)
	if len(want[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE]) >= len(full[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE]) {
		t.Fatal("scenario left nothing for the bootstrap to compact")
	}
	assertFoldEqual(t, "bootstrap", bootstrapFold(t, s.q), want)
	assertBootstrapPromotesCreates(t, s.q)
	if got := bootstrapDeploymentMutations(t, s.q, scenario.pinned.DeploymentID); got != 2 {
		t.Fatalf("bootstrap holds %d versions of the pinned deployment, want the pinned and the newest", got)
	}
	if got := bootstrapDeploymentMutations(t, s.q, scenario.released.DeploymentID); got != 0 {
		t.Fatalf("bootstrap holds %d mutations of the released deployment", got)
	}
	if got := bootstrapDeploymentMutations(t, s.q, scenario.draining.DeploymentID); got != 2 {
		t.Fatalf("bootstrap holds %d mutations of the draining deployment, want the pinned version and its tombstone", got)
	}
	if got := bootstrapDeploymentMutations(t, s.q, scenario.empty.DeploymentID); got != 0 {
		t.Fatalf("bootstrap holds %d mutations of the deleted empty deployment", got)
	}
}

func TestPublishedUpdatesMatchReplayedEvents(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	before := erru.Must(s.q.GetGlobalSeq(ctx))
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	seedEquivalenceScenario(t, s)
	after := erru.Must(s.q.GetGlobalSeq(ctx))
	replay := fullFold(t, s.q)
	published := foldState{}
	foldInto(published, pq.Events(erru.Must(s.q.MutationsInRange(ctx, -1, before)))...)
	next := before + 1
	for next <= after {
		select {
		case update := <-sub:
			if update.Seq != next {
				t.Fatalf("published seq %d, want %d", update.Seq, next)
			}
			assertUpdateReplays(t, s, update)
			foldInto(published, &update)
			next++
		case <-time.After(3 * time.Second):
			t.Fatalf("no update published for seq %d", next)
		}
	}
	select {
	case update := <-sub:
		t.Fatalf("unexpected update after the last commit: %+v", update)
	default:
	}
	assertFoldEqual(t, "published stream", published, replay)
}
