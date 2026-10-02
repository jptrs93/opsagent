package state

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
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

// canonicalUpdate encodes an update with its mutations in a stable order and
// without meta, the shape the write log holds.
func canonicalUpdate(update WriteUpdate) []byte {
	cp := update
	cp.Mutations = make([]*apigen.CoreMutation, 0, len(update.Mutations))
	for _, m := range update.Mutations {
		cp.Mutations = append(cp.Mutations, withoutMeta(m))
	}
	sort.Slice(cp.Mutations, func(i, j int) bool { return bytes.Compare(cp.Mutations[i].Encode(), cp.Mutations[j].Encode()) < 0 })
	return cp.Encode()
}

func withoutMeta(m *apigen.CoreMutation) *apigen.CoreMutation {
	switch {
	case m.Create != nil:
		c := *m.Create
		c.Meta = nil
		return &apigen.CoreMutation{Create: &c}
	case m.Update != nil:
		u := *m.Update
		u.Meta = nil
		return &apigen.CoreMutation{Update: &u}
	}
	return m
}

// assertUpdateMeta checks that every create and update a commit published
// carries the meta of its rows: a creation time, the commit's own envelope
// unless a stale status report lost to the row it holds, and the counters of
// its type.
func assertUpdateMeta(t *testing.T, update WriteUpdate) {
	t.Helper()
	for _, m := range update.Mutations {
		if m.Delete != nil {
			if m.Meta() != nil {
				t.Fatalf("delete of %v %d carries meta at seq %d", m.Type(), m.EntityID(), update.Seq)
			}
			continue
		}
		meta := m.Meta()
		if meta == nil || meta.CreatedTime == 0 || meta.UpdatedSeq == 0 || meta.UpdatedSeq > update.Seq || meta.Deleted {
			t.Fatalf("%v %d published without row meta at seq %d: %+v", m.Type(), m.EntityID(), update.Seq, meta)
		}
		if meta.UpdatedSeq == update.Seq && (meta.UpdatedTime != update.Time || meta.UpdatedActor != update.Actor) {
			t.Fatalf("%v %d meta envelope %+v differs from the commit %d at %d by %d", m.Type(), m.EntityID(), meta, update.Seq, update.Time, update.Actor)
		}
		assertCountersForType(t, m.Type(), meta)
	}
}

func assertCountersForType(t *testing.T, typ apigen.CoreEntityType, meta *apigen.EntityMeta) {
	t.Helper()
	deployment := typ == apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT
	value := typ == apigen.CoreEntityType_CORE_ENTITY_SECRET || typ == apigen.CoreEntityType_CORE_ENTITY_CONFIG || typ == apigen.CoreEntityType_CORE_ENTITY_ASSET
	if (meta.Version > 0) != deployment || (meta.SpecVersion > 0) != deployment || (meta.ValueVersion > 0) != value {
		t.Fatalf("%v meta carries the wrong counters: %+v", typ, meta)
	}
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
	events := erru.Must(s.q.WriteEventsInRange(context.Background(), update.Seq-1, update.Seq))
	if len(events) != 1 {
		t.Fatalf("seq %d replays as %d events", update.Seq, len(events))
	}
	if !bytes.Equal(canonicalUpdate(update), canonicalUpdate(*events[0])) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, *events[0])
	}
	for _, m := range events[0].Mutations {
		if m.Meta() != nil {
			t.Fatalf("write log replays meta for %v %d at seq %d", m.Type(), m.EntityID(), update.Seq)
		}
	}
	assertUpdateMeta(t, update)
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
	foldInto(state, erru.Must(q.WriteEventsInRange(ctx, -1, seq))...)
	return state
}

// snapshotFold is the state a subscriber holds after the opening snapshot:
// the newest entry of every entity, and nothing for a deleted one.
func snapshotFold(t *testing.T, q *pq.Queries) foldState {
	t.Helper()
	state := foldState{}
	for _, e := range erru.Must(q.Snapshot(context.Background())) {
		if e.Meta.Deleted {
			continue
		}
		if state[e.EntityType] == nil {
			state[e.EntityType] = map[int64]*apigen.CoreEntity{}
		}
		state[e.EntityType][e.EntityID] = e.Entity
	}
	return state
}

// assertSnapshotMatchesRebuild is the meta oracle: the snapshot of the live
// tables, entry for entry including meta, equals the snapshot of tables
// rebuilt from the write log alone. The rebuild runs in a transaction that is
// rolled back.
func assertSnapshotMatchesRebuild(t *testing.T, q *pq.Queries) {
	t.Helper()
	ctx := context.Background()
	live := erru.Must(q.Snapshot(ctx))
	assertSnapshotWellFormed(t, live)
	var rebuilt []*apigen.MaterialisedEntity
	rollback := errors.New("rollback")
	if err := q.Tx(ctx, func(tx *pq.Queries) error {
		if err := tx.RebuildFromLog(ctx); err != nil {
			return err
		}
		var err error
		rebuilt, err = tx.Snapshot(ctx)
		if err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rebuild: %v", err)
	}
	if len(live) != len(rebuilt) {
		t.Fatalf("snapshot holds %d entries, rebuilt %d", len(live), len(rebuilt))
	}
	for i := range live {
		if !bytes.Equal(live[i].Encode(), rebuilt[i].Encode()) {
			t.Fatalf("snapshot entry %d differs from the rebuild\nlive: %+v %+v\nrebuilt: %+v %+v", i, live[i].Meta, live[i].Entity, rebuilt[i].Meta, rebuilt[i].Entity)
		}
	}
}

// assertSnapshotWellFormed checks the shape of a snapshot: every entry
// carries a payload and meta with a creation time and write envelope, the
// counters of its type, and the versions of one entity in ascending order.
func assertSnapshotWellFormed(t *testing.T, entries []*apigen.MaterialisedEntity) {
	t.Helper()
	type key struct {
		typ apigen.CoreEntityType
		id  int64
	}
	previous := map[key]*apigen.EntityMeta{}
	closed := map[apigen.CoreEntityType]bool{}
	for i, e := range entries {
		meta := e.Meta
		if e.Entity == nil || meta == nil || meta.CreatedTime == 0 || meta.UpdatedTime == 0 {
			t.Fatalf("snapshot entry %d (%v %d) is malformed: %+v %+v", i, e.EntityType, e.EntityID, meta, e.Entity)
		}
		if i > 0 && entries[i-1].EntityType != e.EntityType {
			closed[entries[i-1].EntityType] = true
		}
		if closed[e.EntityType] {
			t.Fatalf("snapshot entry %d (%v) reopens a type", i, e.EntityType)
		}
		assertCountersForType(t, e.EntityType, meta)
		k := key{e.EntityType, e.EntityID}
		if p := previous[k]; p != nil {
			if meta.Version <= p.Version && meta.ValueVersion <= p.ValueVersion {
				t.Fatalf("snapshot repeats %v %d without a later version: %+v after %+v", k.typ, k.id, meta, p)
			}
			if meta.Deleted != p.Deleted || meta.CreatedTime != p.CreatedTime {
				t.Fatalf("snapshot versions of %v %d disagree on identity meta: %+v after %+v", k.typ, k.id, meta, p)
			}
		} else if meta.ValueVersion > 1 {
			t.Fatalf("snapshot opens %v %d at value version %d, want every version", k.typ, k.id, meta.ValueVersion)
		}
		if meta.Deleted && e.EntityType != apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT {
			t.Fatalf("snapshot carries a deleted %v %d", k.typ, k.id)
		}
		previous[k] = meta
	}
}

// snapshotEntries returns the entries of one entity in snapshot order.
func snapshotEntries(t *testing.T, q *pq.Queries, typ apigen.CoreEntityType, id int64) []*apigen.MaterialisedEntity {
	t.Helper()
	var out []*apigen.MaterialisedEntity
	for _, e := range erru.Must(q.Snapshot(context.Background())) {
		if e.EntityType == typ && e.EntityID == id {
			out = append(out, e)
		}
	}
	return out
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

// retainForSnapshot applies the snapshot retention rule to a full fold:
// finalized instances stay only as the newest final of an ordinal with no
// live instance under a live deployment, and statuses follow their instance.
func retainForSnapshot(full foldState) foldState {
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

var allNodeStatuses = []int64{0, 1, 2, 3, 4, 5, 6, 7, 8}

var oracleTypes = []apigen.CoreEntityType{
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, apigen.CoreEntityType_CORE_ENTITY_NODE,
	apigen.CoreEntityType_CORE_ENTITY_SECRET, apigen.CoreEntityType_CORE_ENTITY_CONFIG, apigen.CoreEntityType_CORE_ENTITY_ASSET,
	apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY,
	apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, apigen.CoreEntityType_CORE_ENTITY_SPACE, apigen.CoreEntityType_CORE_ENTITY_USER,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE,
	apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION, apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET,
	apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS,
}

// assertFoldMatchesLiveTables checks a fold of the full event range, under
// the retention rule, against the live tables: the same entity ids as the pq
// getters list, the payload of the newest row, and the getter's own view of
// the entity.
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
	fold = retainForSnapshot(fold)
	for _, e := range erru.Must(q.ListActiveDeployments(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, int64(e.DeploymentID), entity(pq.DeploymentMutation(e)))
	}
	for _, e := range erru.Must(q.ListRetainedScheduledInstances(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, int64(e.ScheduledInstanceID), entity(pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, e)))
	}
	for _, row := range erru.Must(q.ListNodeRows(ctx, allNodeStatuses)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NODE, int64(row.Event.NodeID), entity(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, &row.Event)))
	}
	for _, st := range erru.Must(q.ListLatestScheduledInstanceStatuses(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, int64(st.ScheduledInstanceID), entity(pq.ScheduledInstanceStatusMutation(0, 0, st)))
	}
	for _, st := range erru.Must(q.ListLatestNodeStatuses(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS, int64(st.NodeID), entity(pq.NodeStatusMutation(0, 0, st)))
	}
	for _, row := range erru.Must(q.ListSecretRows(ctx)) {
		v := erru.Must(q.GetSecretVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)}))
		put(apigen.CoreEntityType_CORE_ENTITY_SECRET, row.ID, entity(pq.SecretMutation(pq.EventMeta{}, row.ID, pq.SecretEntity(row, v))))
	}
	for _, row := range erru.Must(q.ListConfigRows(ctx)) {
		v := erru.Must(q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)}))
		put(apigen.CoreEntityType_CORE_ENTITY_CONFIG, row.ID, entity(pq.ConfigMutation(pq.EventMeta{}, row.ID, pq.ConfigEntity(row, v))))
	}
	for _, row := range erru.Must(q.ListAssetRows(ctx)) {
		v := erru.Must(q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)}))
		put(apigen.CoreEntityType_CORE_ENTITY_ASSET, row.ID, entity(pq.AssetMutation(pq.EventMeta{}, row.ID, pq.AssetEntity(row, v))))
	}
	for _, d := range erru.Must(q.ListValueDirectories(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, int64(d.ID), entity(pq.ValueDirectoryMutation(pq.EventMeta{}, d)))
	}
	for _, d := range erru.Must(q.ListAssetDirectories(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, int64(d.ID), entity(pq.AssetDirectoryMutation(pq.EventMeta{}, d)))
	}
	for _, e := range erru.Must(q.ListNetworkPolicies(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, int64(e.NetworkPolicyID), entity(pq.NetworkPolicyMutation(pq.EventMeta{}, int64(e.NetworkPolicyID), e.Value)))
	}
	for _, sp := range erru.Must(q.ListSpaces(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SPACE, int64(sp.ID), entity(pq.SpaceMutation(pq.EventMeta{}, sp)))
	}
	for _, r := range erru.Must(q.ListUserRows(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_USER, r.ID, entity(pq.UserMutation(pq.EventMeta{}, pq.UserEntity(r))))
	}
	for _, r := range erru.Must(q.ListAuthzRuleTemplates(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, r.ID, entity(pq.AuthzRuleTemplateMutation(pq.EventMeta{}, erru.Must(pq.AuthzRuleTemplateEntity(r)))))
	}
	for _, r := range erru.Must(q.ListAuthzGrants(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, r.ID, entity(pq.AuthzGrantMutation(pq.EventMeta{}, r.ID, erru.Must(pq.AuthzGrantEntity(r)))))
	}
	for _, r := range erru.Must(q.ListAuthzGlobalRules(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, r.ID, entity(pq.AuthzGlobalRuleMutation(pq.EventMeta{}, erru.Must(pq.AuthzGlobalRuleEntity(r)))))
	}
	for _, r := range erru.Must(q.ListAllAgentSessions(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, r.ID, entity(pq.AgentSessionMutation(pq.EventMeta{}, r.ID, r.Entity())))
	}
	for _, r := range erru.Must(q.ListAllUserSessions(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_USER_SESSION, r.ID, entity(pq.UserSessionMutation(pq.EventMeta{}, r.ID, r.Entity())))
	}
	for _, r := range erru.Must(q.ListNixStoreResetRows(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET, r.ID, entity(pq.NixStoreResetMutation(pq.EventMeta{}, r.ID, r.Entity())))
	}
	for _, k := range erru.Must(q.ListSecretKeyslots(ctx)) {
		put(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, pq.SecretKeyslotEntityID(k), entity(pq.SecretKeyslotMutation(pq.EventMeta{}, k)))
	}
	if r, err := q.GetSystemConfig(ctx); err == nil {
		put(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, pq.SystemConfigEntityID, entity(pq.SystemConfigMutation(pq.EventMeta{}, erru.Must(apigen.DecodeSystemConfig(r.ConfigBlob)))))
	}
	for _, typ := range oracleTypes {
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
			if !bytes.Equal(got.Encode(), latest.Entity().Encode()) {
				t.Fatalf("%v %d fold differs from the newest logged payload\ngot: %+v\nlog: %+v", typ, id, got, latest.Entity())
			}
			if !bytes.Equal(got.Encode(), want.Encode()) {
				t.Fatalf("%v %d fold differs from the getter\ngot: %+v\nwant: %+v", typ, id, got, want)
			}
		}
	}
}

func createNetworkPolicyForTest(s *Service, author int32) int32 {
	ctx := context.Background()
	var id int64
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		policy := apigen.NetworkPolicy{Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
			Source:      &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1},
			Destination: &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(author), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.NetworkPolicyMutation(meta, id, policy)), nil
	}))
	return int32(id)
}

func deleteNetworkPolicyForTest(s *Service, id int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetNetworkPolicy(ctx, int64(id)); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, int64(id))), nil
	}))
}

func createUserForTest(s *Service, name string) int64 {
	ctx := context.Background()
	var id int64
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: id, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.UserMutation(meta, apigen.User{ID: int32(id), Name: name, Credentials: []byte{}})), nil
	}))
	return id
}

func insertRuleTemplateForTest(t *testing.T, s *Service, name string, author int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		record := apigen.AuthzRuleTemplate{ID: id, Name: name, Spec: &apigen.AuthzRuleTemplateSpec{}}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.AuthzRuleTemplateMutation(meta, record)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func deleteRuleTemplateForTest(t *testing.T, s *Service, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetAuthzRuleTemplate(ctx, id); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, id)), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func insertGlobalRuleForTest(t *testing.T, s *Service, name string, author int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		record := apigen.AuthzGlobalRule{ID: id, Name: name, Spec: &apigen.AuthzGlobalRuleSpec{Deny: true}}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.AuthzGlobalRuleMutation(meta, record)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func deleteGlobalRuleForTest(t *testing.T, s *Service, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetAuthzGlobalRule(ctx, id); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, id)), nil
	}); err != nil {
		t.Fatal(err)
	}
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
	appendSecretVersionForTest(s, kept, []byte{9})
	carrySecretForTest(s, kept, "kept-renamed", apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	gone := createSecretForTest(s, "gone")
	carrySecretForTest(s, gone, "gone", apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	config := createConfigForTest(s, "config", "one")
	writeConfigForTest(s, config, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, "two")
	renameConfigForTest(s, config, "config-renamed")
	writeConfigForTest(s, config, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, "three")
	deletedConfig := createConfigForTest(s, "deleted", "one")
	writeConfigForTest(s, deletedConfig, apigen.AuthzVerb_AUTHZ_VERB_DELETE, "")
	folder := createValueDirectoryForTest(s, 1, 0, "folder", 1)
	deleteValueDirectoryForTest(s, createValueDirectoryForTest(s, 1, folder.ID, "gone", 1).ID)
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
	deleteAssetDirectoryForTest(s, createAssetDirectoryForTest(s, 1, dir.ID, "gone", 1).ID)
	insertGrantForTest(t, s, 7, 3, 123)
	deleteGrantForTest(t, s, insertGrantForTest(t, s, 8, 3, 124))
	insertRuleTemplateForTest(t, s, "kept", 3)
	deleteRuleTemplateForTest(t, s, insertRuleTemplateForTest(t, s, "gone", 3))
	insertGlobalRuleForTest(t, s, "kept", 3)
	deleteGlobalRuleForTest(t, s, insertGlobalRuleForTest(t, s, "gone", 3))
	seedLatestOnlyHistory(t, s)
	return equivalenceScenario{pinned: pinned, released: released, draining: draining, empty: empty}
}

func TestFullRangeFoldMatchesLiveTables(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	seedEquivalenceScenario(t, s)
	fold := fullFold(t, s.q)
	for _, typ := range oracleTypes {
		if len(fold[typ]) == 0 {
			t.Fatalf("scenario left no live %v", typ)
		}
	}
	assertFoldMatchesLiveTables(t, s, fold)
}

func TestSnapshotFoldMatchesFullRangeFold(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	scenario := seedEquivalenceScenario(t, s)
	full := fullFold(t, s.q)
	want := retainForSnapshot(full)
	if len(want[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE]) >= len(full[apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE]) {
		t.Fatal("scenario left nothing for the snapshot to compact")
	}
	assertFoldEqual(t, "snapshot", snapshotFold(t, s.q), want)
	assertSnapshotMatchesRebuild(t, s.q)
	deployment := apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT
	if got := snapshotEntries(t, s.q, deployment, int64(scenario.pinned.DeploymentID)); len(got) != 2 || got[0].Meta.Version != 1 || got[1].Meta.Version != 2 || got[0].Meta.Deleted || got[1].Meta.Deleted {
		t.Fatalf("snapshot holds %+v for the pinned deployment, want the pinned and the newest version", got)
	}
	if got := snapshotEntries(t, s.q, deployment, int64(scenario.released.DeploymentID)); len(got) != 0 {
		t.Fatalf("snapshot holds %d entries of the released deployment", len(got))
	}
	if got := snapshotEntries(t, s.q, deployment, int64(scenario.draining.DeploymentID)); len(got) != 1 || !got[0].Meta.Deleted || got[0].Meta.Version != 1 {
		t.Fatalf("snapshot holds %+v for the draining deployment, want the pinned version marked deleted", got)
	}
	if got := snapshotEntries(t, s.q, deployment, int64(scenario.empty.DeploymentID)); len(got) != 0 {
		t.Fatalf("snapshot holds %d entries of the deleted empty deployment", len(got))
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
	foldInto(published, erru.Must(s.q.WriteEventsInRange(ctx, -1, before))...)
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

func createAgentSessionForTest(t *testing.T, s *Service, sessionID string, userID int32) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION)
		if err != nil {
			return nil, err
		}
		doc := &apigen.AgentSession{ID: sessionID, UserID: userID, Status: apigen.AgentSessionStatus_AGENT_SESSION_PENDING, RequestingAddress: "10.0.0.1", ApprovalCode: "1234"}
		return pq.NewUpdate(pq.AgentSessionMutation(pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, id, doc)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func approveAgentSessionForTest(t *testing.T, s *Service, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		row, err := q.GetAgentSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		row.Status, row.ApprovedAt, row.TokenHash, row.TokenPrefix, row.ExpiresAt = int64(apigen.AgentSessionStatus_AGENT_SESSION_APPROVED), 1_700_000_100, []byte{9, 9}, "a_abcd", 1_700_100_000
		return pq.NewUpdate(pq.AgentSessionMutation(pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(row.UserID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}, row.ID, row.Entity())), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func createUserSessionForTest(t *testing.T, s *Service, sessionID string, userID int32, revoked bool) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)
		if err != nil {
			return nil, err
		}
		doc := &apigen.UserSession{ID: sessionID, UserID: userID, ExpiresAt: time.Unix(1_700_200_000, 0), TokenHash: []byte{1, 2}, UserAgent: "test"}
		return pq.NewUpdate(pq.UserSessionMutation(pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(userID), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, id, doc)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if revoked {
		if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
			row, err := q.GetUserSession(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			row.RevokedAt = 1_700_000_500
			return pq.NewUpdate(pq.UserSessionMutation(pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(userID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}, row.ID, row.Entity())), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func requestNixStoreResetForTest(t *testing.T, s *Service, repo string, requestedAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: requestedAt, Author: 1, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		var id int64
		switch previous, err := q.GetNixStoreReset(ctx, repo); {
		case errors.Is(err, sql.ErrNoRows):
			meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
			if id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		default:
			id = previous.ID
		}
		return pq.NewUpdate(pq.NixStoreResetMutation(meta, id, &apigen.NixStoreReset{Repo: repo, RequestedAt: requestedAt})), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writeKeyslotForTest(t *testing.T, s *Service, kind apigen.SecretKeyslotKind, nodeID int64, smkVersion int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		now := time.Now().UnixMilli()
		slot := pq.SecretKeyslot{Kind: kind, NodeID: nodeID, SmkVersion: smkVersion, WrappedSmk: []byte{byte(smkVersion)}, Nonce: []byte{2}, UpdatedAt: now}
		if kind == apigen.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY {
			slot.KdfSalt = []byte{3}
		}
		verb := apigen.AuthzVerb_AUTHZ_VERB_CREATE
		for _, k := range erru.Must(q.ListSecretKeyslots(ctx)) {
			if k.Kind == kind && k.NodeID == nodeID {
				verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
			}
		}
		return pq.NewUpdate(pq.SecretKeyslotMutation(pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: 7, EventType: verb}, slot)), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func deleteKeyslotsForTest(t *testing.T, s *Service, nodeID int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		deletes, err := q.NodeSecretKeyslotDeletes(ctx, pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}, nodeID)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(deletes...), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writeSystemConfigForTest(t *testing.T, s *Service, hash string) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		verb := apigen.AuthzVerb_AUTHZ_VERB_UPDATE
		if _, err := q.GetSystemConfig(ctx); errors.Is(err, sql.ErrNoRows) {
			verb = apigen.AuthzVerb_AUTHZ_VERB_CREATE
		} else if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.SystemConfigMutation(pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: 1, EventType: verb}, &apigen.SystemConfig{MasterPasswordHash: hash})), nil
	}); err != nil {
		t.Fatal(err)
	}
}

// seedLatestOnlyHistory writes creates and updates of the session, reset,
// keyslot, and settings types, with one keyslot removed again.
func seedLatestOnlyHistory(t *testing.T, s *Service) {
	t.Helper()
	createAgentSessionForTest(t, s, "agent-pending", 1)
	createAgentSessionForTest(t, s, "agent-approved", 1)
	approveAgentSessionForTest(t, s, "agent-approved")
	createUserSessionForTest(t, s, "user-live", 1, false)
	createUserSessionForTest(t, s, "user-revoked", 2, true)
	requestNixStoreResetForTest(t, s, "github.com/acme/app", 1_000)
	requestNixStoreResetForTest(t, s, "github.com/acme/app", 2_000)
	requestNixStoreResetForTest(t, s, "github.com/acme/lib", 3_000)
	writeKeyslotForTest(t, s, apigen.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE, 1, 1)
	writeKeyslotForTest(t, s, apigen.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE, 2, 1)
	writeKeyslotForTest(t, s, apigen.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY, 0, 1)
	writeKeyslotForTest(t, s, apigen.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY, 0, 2)
	deleteKeyslotsForTest(t, s, 2)
	writeSystemConfigForTest(t, s, "one")
	writeSystemConfigForTest(t, s, "two")
}
