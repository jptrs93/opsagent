package pq

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// mutationTable is one append-only table read back as mutations: the select
// that yields one row's envelope and payload, the scan that converts it, and
// the predicate that keeps the newest live row per entity for a bootstrap.
type mutationTable struct {
	query      string
	seq        string
	rowID      string
	latestLive string
	scan       func(scanner) (Mutation, error)
}

const (
	configEventColumns        = `id, global_seq, event_time, created_time, author, config_id, version, value_version, name, value_directory_id, space_id, value, event_type`
	assetEventColumns         = `id, global_seq, event_time, created_time, author, asset_id, version, value_version, key, asset_directory_id, space_id, size_bytes, sha256, storage_key, event_type`
	networkPolicyEventColumns = `id, global_seq, event_time, created_time, author, policy_id, version, data_blob, event_type`
	authzGrantEventColumns    = `id, global_seq, event_time, created_time, author, grant_id, version, user_id, template_id, data_blob, event_type`
	authzTemplateEventColumns = `id, global_seq, event_time, created_time, author, template_id, version, name, builtin, data_blob, event_type`
	globalRuleEventColumns    = `id, global_seq, event_time, created_time, author, rule_id, version, name, disabled, data_blob, event_type`
)

func scanMeta(row scanner, extra ...any) (int64, EventMeta, error) {
	var id int64
	var meta EventMeta
	err := row.Scan(append([]any{&id, &meta.GlobalSeq, &meta.EventTime, &meta.Author, &meta.EventType}, extra...)...)
	return id, meta, err
}

func scanAuthzRuleTemplateEvent(row scanner) (AuthzRuleTemplateEvent, error) {
	var i AuthzRuleTemplateEvent
	err := row.Scan(&i.ID, &i.GlobalSeq, &i.EventTime, &i.CreatedTime, &i.Author, &i.TemplateID, &i.Version, &i.Name, &i.Builtin, &i.DataBlob, &i.EventType)
	return i, err
}

func scanGlobalAccessRuleEvent(row scanner) (GlobalAccessRuleEvent, error) {
	var i GlobalAccessRuleEvent
	err := row.Scan(&i.ID, &i.GlobalSeq, &i.EventTime, &i.CreatedTime, &i.Author, &i.RuleID, &i.Version, &i.Name, &i.Disabled, &i.DataBlob, &i.EventType)
	return i, err
}

func latestPer(table, key string) string {
	return `id IN (SELECT MAX(id) FROM ` + table + ` GROUP BY ` + key + `) AND event_type != 3`
}

var mutationTables = []mutationTable{
	{query: `SELECT ` + deploymentEventColumns + ` FROM deployment_event_log`, seq: "global_seq", rowID: "id",
		scan: func(row scanner) (Mutation, error) {
			e, err := scanDeploymentEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return DeploymentMutation(e), nil
		}},
	{query: `SELECT ` + scheduledInstanceEventColumns + ` FROM scheduled_instance_event_log`, seq: "global_seq", rowID: "id",
		scan: func(row scanner) (Mutation, error) {
			e, err := scanScheduledInstanceEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return ScheduledInstanceMutation(e), nil
		}},
	{query: `SELECT ` + nodeEventColumns + ` FROM node_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("node_event_log", "node_id"),
		scan: func(row scanner) (Mutation, error) {
			e, err := scanNodeEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return NodeMutation(e), nil
		}},
	{query: `SELECT ` + secretEventColumns + `, e.smk_version, e.ciphertext, e.nonce FROM secret_event_log e`, seq: "e.global_seq", rowID: "e.id",
		scan: func(row scanner) (Mutation, error) {
			e, sealed, err := scanSealedSecretEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return SecretMutation(e, sealed), nil
		}},
	{query: `SELECT ` + configEventColumns + ` FROM config_event_log`, seq: "global_seq", rowID: "id",
		scan: func(row scanner) (Mutation, error) {
			e, err := scanConfigEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return ConfigMutation(e), nil
		}},
	{query: `SELECT ` + assetEventColumns + ` FROM asset_event_log`, seq: "global_seq", rowID: "id",
		scan: func(row scanner) (Mutation, error) {
			e, err := scanAssetEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return AssetMutation(&e), nil
		}},
	{query: `SELECT ` + networkPolicyEventColumns + ` FROM network_policy_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("network_policy_event_log", "policy_id"),
		scan: func(row scanner) (Mutation, error) {
			e, err := scanNetworkPolicyEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return NetworkPolicyMutation(e), nil
		}},
	{query: `SELECT id, global_seq, event_time, author, event_type, space_id, name FROM space_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("space_event_log", "space_id"),
		scan: func(row scanner) (Mutation, error) {
			var space apigen.Space
			id, meta, err := scanMeta(row, &space.ID, &space.Name)
			if err != nil {
				return Mutation{}, err
			}
			m := SpaceMutation(meta, space)
			m.RowID = id
			return m, nil
		}},
	{query: `SELECT ` + userColumns + ` FROM user_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("user_event_log", "user_id"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanUserRow(row)
			if err != nil {
				return Mutation{}, err
			}
			return UserMutation(r), nil
		}},
	{query: `SELECT id, global_seq, event_time, author, event_type, directory_id, space_id, name, parent_id, created_at FROM value_directory_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("value_directory_event_log", "directory_id"),
		scan: func(row scanner) (Mutation, error) {
			d := &apigen.ValueDirectory{}
			var createdAt int64
			id, meta, err := scanMeta(row, &d.ID, &d.SpaceID, &d.Name, &d.ParentID, &createdAt)
			if err != nil {
				return Mutation{}, err
			}
			d.CreatedAt, d.Author = millisToTime(createdAt), int32(meta.Author)
			m := ValueDirectoryMutation(meta, d)
			m.RowID = id
			return m, nil
		}},
	{query: `SELECT id, global_seq, event_time, author, event_type, directory_id, space_id, key, parent_id, created_at FROM asset_directory_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("asset_directory_event_log", "directory_id"),
		scan: func(row scanner) (Mutation, error) {
			var d apigen.AssetDirectory
			var createdAt int64
			id, meta, err := scanMeta(row, &d.ID, &d.SpaceID, &d.Key, &d.ParentID, &createdAt)
			if err != nil {
				return Mutation{}, err
			}
			d.CreatedAt, d.Author = millisToTime(createdAt), int32(meta.Author)
			m := AssetDirectoryMutation(meta, d)
			m.RowID = id
			return m, nil
		}},
	{query: `SELECT ` + authzTemplateEventColumns + ` FROM authz_rule_template_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("authz_rule_template_event_log", "template_id"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanAuthzRuleTemplateEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return AuthzRuleTemplateMutation(r)
		}},
	{query: `SELECT ` + authzGrantEventColumns + ` FROM authz_grant_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("authz_grant_event_log", "grant_id"),
		scan: func(row scanner) (Mutation, error) {
			e, err := scanAuthzGrantEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return AuthzGrantMutation(&e), nil
		}},
	{query: `SELECT ` + globalRuleEventColumns + ` FROM global_access_rule_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("global_access_rule_event_log", "rule_id"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanGlobalAccessRuleEvent(row)
			if err != nil {
				return Mutation{}, err
			}
			return GlobalAccessRuleMutation(r)
		}},
	{query: `SELECT id, global_seq, event_time, author, event_type, config_blob FROM system_config_event_log`, seq: "global_seq", rowID: "id",
		latestLive: `id = (SELECT MAX(id) FROM system_config_event_log)`,
		scan: func(row scanner) (Mutation, error) {
			var blob []byte
			id, meta, err := scanMeta(row, &blob)
			if err != nil {
				return Mutation{}, err
			}
			cfg, err := apigen.DecodeSystemConfig(blob)
			if err != nil {
				return Mutation{}, err
			}
			m := SystemConfigMutation(meta, cfg)
			m.RowID = id
			return m, nil
		}},
	{query: `SELECT rowid, global_seq, event_time, ` + scheduledInstanceStatusColumns + ` FROM scheduled_instance_status`, seq: "global_seq", rowID: "rowid",
		latestLive: `(scheduled_instance_id, updated_at) IN (SELECT scheduled_instance_id, MAX(updated_at) FROM scheduled_instance_status GROUP BY scheduled_instance_id)`,
		scan: func(row scanner) (Mutation, error) {
			var rowID, seq, eventTime int64
			var r scheduledInstanceStatusRow
			if err := row.Scan(append([]any{&rowID, &seq, &eventTime}, r.fields()...)...); err != nil {
				return Mutation{}, err
			}
			m := ScheduledInstanceStatusMutation(seq, eventTime, r.toStatus())
			m.RowID = rowID
			return m, nil
		}},
	{query: `SELECT rowid, global_seq, event_time, ` + nodeStatusColumns + ` FROM node_status_log`, seq: "global_seq", rowID: "rowid",
		latestLive: `(node_id, updated_at) IN (SELECT node_id, MAX(updated_at) FROM node_status_log GROUP BY node_id)`,
		scan: func(row scanner) (Mutation, error) {
			var rowID, seq, eventTime, nodeID, updatedAt, lastConnectedAt, isConnected int64
			var opendeployVersion, remoteAddress string
			if err := row.Scan(&rowID, &seq, &eventTime, &nodeID, &updatedAt, &lastConnectedAt, &isConnected, &opendeployVersion, &remoteAddress); err != nil {
				return Mutation{}, err
			}
			m := NodeStatusMutation(seq, eventTime, nodeStatusFrom(nodeID, updatedAt, lastConnectedAt, isConnected, opendeployVersion, remoteAddress))
			m.RowID = rowID
			return m, nil
		}},
	{query: `SELECT ` + agentSessionColumns + ` FROM agent_session_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("agent_session_event_log", "session_id"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanAgentSession(row)
			if err != nil {
				return Mutation{}, err
			}
			return AgentSessionMutation(r), nil
		}},
	{query: `SELECT ` + userSessionColumns + ` FROM user_session_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("user_session_event_log", "session_id"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanUserSession(row)
			if err != nil {
				return Mutation{}, err
			}
			return UserSessionMutation(r), nil
		}},
	{query: `SELECT ` + nixStoreResetColumns + ` FROM nix_store_reset_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("nix_store_reset_event_log", "repo"),
		scan: func(row scanner) (Mutation, error) {
			r, err := scanNixStoreResetRow(row)
			if err != nil {
				return Mutation{}, err
			}
			return NixStoreResetMutation(r), nil
		}},
	{query: `SELECT id, global_seq, event_time, author, event_type, kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt FROM secret_keyslot_event_log`, seq: "global_seq", rowID: "id",
		latestLive: latestPer("secret_keyslot_event_log", "kind, node_id"),
		scan: func(row scanner) (Mutation, error) {
			var k SecretKeyslot
			id, meta, err := scanMeta(row, &k.Kind, &k.NodeID, &k.SmkVersion, &k.WrappedSmk, &k.Nonce, &k.KdfSalt)
			if err != nil {
				return Mutation{}, err
			}
			k.UpdatedAt = meta.EventTime
			m := SecretKeyslotMutation(meta, k)
			m.RowID = id
			return m, nil
		}},
}

func (q *Queries) mutationRows(ctx context.Context, t mutationTable, where string, args ...any) ([]Mutation, error) {
	rows, err := q.db.QueryContext(ctx, t.query+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mutation
	for rows.Next() {
		m, err := t.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func sortMutations(ms []Mutation) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.GlobalSeq != b.GlobalSeq {
			return a.GlobalSeq < b.GlobalSeq
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.RowID < b.RowID
	})
}

func isStatusTable(t apigen.CoreEntityType) bool {
	return t == apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS || t == apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS
}

// MutationsInRange returns every row with after < global_seq <= upTo across
// all entity tables, ordered by seq, entity type, then row id. after < 0
// includes the genesis rows at seq 0: rows copied from pre-event tables, and
// for the two observed status tables only the newest row per entity, since
// a stale report keeps seq 0 for good and is never replayed.
func (q *Queries) MutationsInRange(ctx context.Context, after, upTo int64) ([]Mutation, error) {
	var out []Mutation
	for i, t := range mutationTables {
		status := isStatusTable(apigen.CoreEntityType(i + 1))
		low := after
		if status && low < 0 {
			low = 0
			if upTo >= 0 {
				genesis, err := q.mutationRows(ctx, t, `WHERE `+t.seq+` = 0 AND `+t.latestLive+` ORDER BY `+t.rowID)
				if err != nil {
					return nil, err
				}
				out = append(out, genesis...)
			}
		}
		ms, err := q.mutationRows(ctx, t, `WHERE `+t.seq+` > ? AND `+t.seq+` <= ? ORDER BY `+t.seq+`, `+t.rowID, low, upTo)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	sortMutations(out)
	return out, nil
}

// BootstrapMutations is the compacted history: every retained mutation with
// its original envelope, ordered as MutationsInRange orders them. Retention
// per type: every row of every live secret, config, and asset; a deployment's
// newest row plus every version a retained scheduled instance pins, with the
// delete row of a deleted deployment kept only while an instance pins it;
// non-final scheduled instances plus the newest final per ordinal without a
// live one; and for every other type the newest live row per entity.
func (q *Queries) BootstrapMutations(ctx context.Context) ([]Mutation, error) {
	var out []Mutation
	latest, err := q.ListLatestDeploymentEvents(ctx)
	if err != nil {
		return nil, err
	}
	deleted := map[int32]bool{}
	retained := map[int32]map[int32]bool{}
	keep := func(e *apigen.DeploymentEvent) {
		if retained[e.DeploymentID] == nil {
			retained[e.DeploymentID] = map[int32]bool{}
		}
		if !retained[e.DeploymentID][e.Version] {
			retained[e.DeploymentID][e.Version] = true
			out = append(out, DeploymentMutation(e))
		}
	}
	for _, e := range latest {
		deleted[e.DeploymentID] = e.Deleted()
		if !e.Deleted() {
			keep(e)
		}
	}
	instances, err := q.RetainedScheduledInstances(ctx, deleted)
	if err != nil {
		return nil, err
	}
	included := map[int32]bool{}
	for _, e := range instances {
		pinned, err := q.GetDeploymentEventByVersion(ctx, GetDeploymentEventByVersionParams{DeploymentID: int64(e.Value.DeploymentID), Version: int64(e.Value.DeploymentVersion)})
		if err != nil {
			return nil, err
		}
		keep(pinned)
		out = append(out, ScheduledInstanceMutation(e))
		included[e.ScheduledInstanceID] = true
	}
	for _, e := range latest {
		if e.Deleted() && retained[e.DeploymentID] != nil {
			keep(e)
		}
	}
	for i, t := range mutationTables {
		typ := apigen.CoreEntityType(i + 1)
		var ms []Mutation
		switch typ {
		case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
			continue
		case apigen.CoreEntityType_CORE_ENTITY_SECRET:
			ms, err = q.mutationRows(ctx, t, `WHERE e.secret_id IN (SELECT secret_id FROM secret_event_log WHERE `+latestPer("secret_event_log", "secret_id")+`) ORDER BY e.id`)
		case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
			ms, err = q.mutationRows(ctx, t, `WHERE config_id IN (SELECT config_id FROM config_event_log WHERE `+latestPer("config_event_log", "config_id")+`) ORDER BY id`)
		case apigen.CoreEntityType_CORE_ENTITY_ASSET:
			ms, err = q.mutationRows(ctx, t, `WHERE asset_id IN (SELECT asset_id FROM asset_event_log WHERE `+latestPer("asset_event_log", "asset_id")+`) ORDER BY id`)
		default:
			ms, err = q.mutationRows(ctx, t, `WHERE `+t.latestLive+` ORDER BY `+t.rowID)
		}
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if typ == apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS && !included[int32(m.ID)] {
				continue
			}
			out = append(out, m)
		}
	}
	sortMutations(out)
	promoteFirstToCreate(out)
	return out, nil
}

// RetainedScheduledInstances returns the instances a bootstrap keeps: every
// non-final instance plus, per ordinal of a deployment not in deleted that
// has no non-final instance, its newest final one.
func (q *Queries) RetainedScheduledInstances(ctx context.Context, deleted map[int32]bool) ([]*apigen.ScheduledInstanceEvent, error) {
	instances, err := q.ListNonFinalScheduledInstances(ctx)
	if err != nil {
		return nil, err
	}
	type ordinalKey struct{ deployment, ordinal int32 }
	live := map[ordinalKey]bool{}
	for _, e := range instances {
		live[ordinalKey{e.Value.DeploymentID, e.Value.InstanceOrdinal}] = true
	}
	finals, err := q.ListLatestScheduledInstancePerOrdinal(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range finals {
		key := ordinalKey{e.Value.DeploymentID, e.Value.InstanceOrdinal}
		if !deleted[e.Value.DeploymentID] && e.Value.State.IsFinal() && !live[key] {
			instances = append(instances, e)
		}
	}
	return instances, nil
}

var entityColumns = map[apigen.CoreEntityType]string{
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:          "deployment_id",
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:  "scheduled_instance_id",
	apigen.CoreEntityType_CORE_ENTITY_NODE:                "node_id",
	apigen.CoreEntityType_CORE_ENTITY_SECRET:              "secret_id",
	apigen.CoreEntityType_CORE_ENTITY_CONFIG:              "config_id",
	apigen.CoreEntityType_CORE_ENTITY_ASSET:               "asset_id",
	apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:      "policy_id",
	apigen.CoreEntityType_CORE_ENTITY_SPACE:               "space_id",
	apigen.CoreEntityType_CORE_ENTITY_USER:                "user_id",
	apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:     "directory_id",
	apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:     "directory_id",
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE: "template_id",
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:         "grant_id",
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:   "rule_id",
}

func tableOf(query string) string {
	_, after, _ := strings.Cut(query, " FROM ")
	return strings.Fields(after)[0]
}

// LatestMutation returns the newest row of one entity as a mutation, a
// delete row included, for the types whose rows carry an entity id column.
func (q *Queries) LatestMutation(ctx context.Context, t apigen.CoreEntityType, id int64) (Mutation, error) {
	column, ok := entityColumns[t]
	if !ok {
		return Mutation{}, fmt.Errorf("entity type %d has no latest-row lookup", t)
	}
	table := mutationTables[t-1]
	ms, err := q.mutationRows(ctx, table, `WHERE `+table.rowID+` = (SELECT MAX(id) FROM `+tableOf(table.query)+` WHERE `+column+` = ?)`, id)
	if err != nil {
		return Mutation{}, err
	}
	if len(ms) == 0 {
		return Mutation{}, sql.ErrNoRows
	}
	return ms[0], nil
}

// promoteFirstToCreate re-emits the first retained mutation of each entity
// as a create, since the create row it replays may have been compacted away.
func promoteFirstToCreate(ms []Mutation) {
	type key struct {
		t  apigen.CoreEntityType
		id int64
	}
	seen := map[key]bool{}
	for i := range ms {
		k := key{ms[i].Type, ms[i].ID}
		if seen[k] {
			continue
		}
		seen[k] = true
		if ms[i].EventType != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			ms[i].EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
		}
	}
}

// VisibilityChangesSince reports whether any row written after seq could
// change what a viewer is allowed to see: a template, global rule, space,
// node, or network policy row, a grant row for the user, or a deployment,
// secret, config, or asset whose row moved it to another space. It is the
// reconnect form of the live reset predicate and is deliberately pessimistic.
func (q *Queries) VisibilityChangesSince(ctx context.Context, seq int64, userID int64) (bool, error) {
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT EXISTS(SELECT 1 FROM authz_rule_template_event_log WHERE global_seq > ?)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM global_access_rule_event_log WHERE global_seq > ?)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM space_event_log WHERE global_seq > ?)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM node_event_log WHERE global_seq > ?)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM network_policy_event_log WHERE global_seq > ?)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM authz_grant_event_log WHERE global_seq > ? AND user_id = ?)`, []any{seq, userID}},
		{`SELECT EXISTS(SELECT 1 FROM deployment_event_log WHERE global_seq > ? AND space_assignment_changed != 0)`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM secret_event_log e WHERE e.global_seq > ? AND e.space_id != (SELECT p.space_id FROM secret_event_log p WHERE p.secret_id = e.secret_id AND p.id < e.id ORDER BY p.id DESC LIMIT 1))`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM config_event_log e WHERE e.global_seq > ? AND e.space_id != (SELECT p.space_id FROM config_event_log p WHERE p.config_id = e.config_id AND p.id < e.id ORDER BY p.id DESC LIMIT 1))`, []any{seq}},
		{`SELECT EXISTS(SELECT 1 FROM asset_event_log e WHERE e.global_seq > ? AND e.space_id != (SELECT p.space_id FROM asset_event_log p WHERE p.asset_id = e.asset_id AND p.id < e.id ORDER BY p.id DESC LIMIT 1))`, []any{seq}},
	}
	for _, check := range checks {
		var found bool
		if err := q.db.QueryRowContext(ctx, check.query, check.args...).Scan(&found); err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// LatestSeqOf returns the global_seq of an entity's newest row, or 0 when
// the entity has no row. It is the upper bound an expected_seq is checked
// against.
func (q *Queries) LatestSeqOf(ctx context.Context, t apigen.CoreEntityType, id int64) (int64, error) {
	var table, key string
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		table, key = "deployment_event_log", "deployment_id"
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		table, key = "node_event_log", "node_id"
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		table, key = "network_policy_event_log", "policy_id"
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		table, key = "secret_event_log", "secret_id"
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		table, key = "config_event_log", "config_id"
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		table, key = "asset_event_log", "asset_id"
	default:
		panic("LatestSeqOf: unsupported entity type")
	}
	var seq int64
	err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(global_seq), 0) FROM `+table+` WHERE `+key+` = ?`, id).Scan(&seq)
	return seq, err
}
