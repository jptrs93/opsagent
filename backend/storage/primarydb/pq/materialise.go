package pq

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// rowEnvelope is the write-log envelope a materialised row carries: the
// commit that last wrote it, the commit's clock, and its actor. CreatedTime
// is the row's first write; the reducer receives the envelope of the write
// it applies, without it.
type rowEnvelope struct {
	Seq         int64
	EventTime   int64
	Author      int64
	CreatedTime int64
	Logged      *loggedDeploymentFacts
}

// reducedTypes are the entity types whose tables the reducer owns. Writers
// of these types return mutations only; Commit materialises them.
var reducedTypes = []apigen.CoreEntityType{
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE,
	apigen.CoreEntityType_CORE_ENTITY_NODE,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS,
	apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS,
	apigen.CoreEntityType_CORE_ENTITY_SECRET,
	apigen.CoreEntityType_CORE_ENTITY_CONFIG,
	apigen.CoreEntityType_CORE_ENTITY_ASSET,
	apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY,
	apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY,
	apigen.CoreEntityType_CORE_ENTITY_SPACE,
	apigen.CoreEntityType_CORE_ENTITY_USER,
	apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE,
	apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION,
	apigen.CoreEntityType_CORE_ENTITY_USER_SESSION,
	apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET,
	apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT,
	apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG,
}

var materialisedTables = []string{
	"value_names", "secret_versions", "secrets", "config_versions", "configs", "value_directories",
	"asset_keys", "asset_versions", "assets", "asset_directories",
	"spaces", "users", "network_policies", "authz_rule_templates", "authz_grants", "authz_global_rules",
	"agent_sessions", "user_sessions", "nix_store_resets", "secret_keyslots", "system_config",
	"deployments", "deployment_versions", "scheduled_instances", "scheduled_instance_status", "nodes", "node_status",
}

const rebuildPageSeqs = 2_000

// NextEntityID allocates the next id of a reduced type. Ids are never reused:
// the counter only moves forward, here and in the reducer.
func (q *Queries) NextEntityID(ctx context.Context, t apigen.CoreEntityType) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `INSERT INTO entity_ids (entity_type, next) VALUES (?, 2)
ON CONFLICT (entity_type) DO UPDATE SET next = next + 1
RETURNING next - 1`, int64(t)).Scan(&id)
	return id, err
}

func (q *Queries) bumpEntityID(ctx context.Context, t apigen.CoreEntityType, id int64) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO entity_ids (entity_type, next) VALUES (?, ?)
ON CONFLICT (entity_type) DO UPDATE SET next = MAX(next, excluded.next)`, int64(t), id+1)
	return err
}

// ReduceUpdate materialises the mutations of an update the tables have not
// seen yet, in order, then applies retention to the deployments they touched.
// Within one transaction an update is reduced incrementally: Commit reduces
// the writer's mutations before the triggers run and each trigger's
// mutations after it, so every reader inside the commit sees the rows the
// mutations before it describe.
func (q *Queries) ReduceUpdate(ctx context.Context, u *apigen.CoreWriteUpdate) error {
	start := 0
	if q.applied != nil {
		start = q.applied[u]
	}
	if start >= len(u.Mutations) {
		return nil
	}
	affected := map[int64]struct{}{}
	for _, m := range u.Mutations[start:] {
		if err := q.affectedDeployment(ctx, m, affected); err != nil {
			return err
		}
		if err := q.Reduce(ctx, u.Seq, u.Time, u.Actor, m); err != nil {
			return err
		}
	}
	if q.applied != nil {
		q.applied[u] = len(u.Mutations)
	}
	for _, m := range u.Mutations[start:] {
		if m.Type() == apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS && m.Delete == nil {
			if err := q.deleteOrphanScheduledInstanceStatus(ctx, m.EntityID()); err != nil {
				return err
			}
		}
	}
	for id := range affected {
		if err := q.retainDeployment(ctx, id); err != nil {
			return fmt.Errorf("retaining deployment %d at seq %d: %w", id, u.Seq, err)
		}
	}
	return nil
}

// Apply appends mutations to an update inside a commit and materialises
// them at once, for a trigger whose later reads depend on them.
func (q *Queries) Apply(ctx context.Context, u *apigen.CoreWriteUpdate, ms ...Mutation) error {
	AppendMutations(u, ms...)
	return q.ReduceUpdate(ctx, u)
}

func (q *Queries) affectedDeployment(ctx context.Context, m *apigen.CoreMutation, affected map[int64]struct{}) error {
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		affected[m.EntityID()] = struct{}{}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if e := m.Entity(); e != nil && e.ScheduledInstance != nil {
			affected[int64(e.ScheduledInstance.DeploymentID)] = struct{}{}
		}
		deployment, ok, err := q.scheduledInstanceDeploymentID(ctx, m.EntityID())
		if err != nil {
			return err
		}
		if ok {
			affected[deployment] = struct{}{}
		}
	}
	return nil
}

// Reduce applies one logged mutation to the tables of its entity type: a
// create or update upserts the rows the payload describes and stamps the
// mutation with the entity's meta as the rows now hold it, a delete removes
// the rows.
func (q *Queries) Reduce(ctx context.Context, seq, eventTime int64, actor int32, m *apigen.CoreMutation) error {
	env := rowEnvelope{Seq: seq, EventTime: eventTime, Author: int64(actor)}
	if facts, ok := q.logged[m]; ok {
		env.Logged = &facts
	}
	id := m.EntityID()
	e := m.Entity()
	if e == nil && m.Delete == nil {
		return fmt.Errorf("reduce %v %d: mutation has no payload", m.Type(), id)
	}
	meta := newMeta(env)
	var err error
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		if m.Delete != nil {
			return q.deleteDeploymentRow(ctx, id)
		}
		err = q.reduceDeployment(ctx, env, meta, id, e.Deployment)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if m.Delete != nil {
			return q.deleteScheduledInstanceRows(ctx, id)
		}
		err = q.reduceScheduledInstance(ctx, env, meta, id, e.ScheduledInstance)
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		if m.Delete != nil {
			return q.deleteNodeRows(ctx, id)
		}
		err = q.reduceNode(ctx, env, meta, id, e.Node)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		if m.Delete != nil {
			return q.deleteScheduledInstanceStatusRow(ctx, id)
		}
		err = q.reduceScheduledInstanceStatus(ctx, env, meta, id, e.ScheduledInstanceStatus)
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		if m.Delete != nil {
			return q.deleteNodeStatusRow(ctx, id)
		}
		err = q.reduceNodeStatus(ctx, env, meta, id, e.NodeStatus)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		if m.Delete != nil {
			return q.deleteSecretRows(ctx, id)
		}
		err = q.reduceSecret(ctx, env, meta, id, e.Secret)
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		if m.Delete != nil {
			return q.deleteConfigRows(ctx, id)
		}
		err = q.reduceConfig(ctx, env, meta, id, e.Config)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		if m.Delete != nil {
			return q.deleteAssetRows(ctx, id)
		}
		err = q.reduceAsset(ctx, env, meta, id, e.Asset)
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		if m.Delete != nil {
			return q.deleteValueDirectoryRows(ctx, id)
		}
		err = q.reduceValueDirectory(ctx, env, meta, id, e.ValueDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		if m.Delete != nil {
			return q.deleteAssetDirectoryRows(ctx, id)
		}
		err = q.reduceAssetDirectory(ctx, env, meta, id, e.AssetDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		if m.Delete != nil {
			return q.deleteSpaceRow(ctx, id)
		}
		err = q.reduceSpace(ctx, env, meta, id, e.Space)
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		if m.Delete != nil {
			return q.deleteUserRow(ctx, id)
		}
		err = q.reduceUser(ctx, env, meta, id, e.User)
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		if m.Delete != nil {
			return q.deleteNetworkPolicyRow(ctx, id)
		}
		err = q.reduceNetworkPolicy(ctx, env, meta, id, e.NetworkPolicy)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE:
		if m.Delete != nil {
			return q.deleteAuthzRuleTemplateRow(ctx, id)
		}
		err = q.reduceAuthzRuleTemplate(ctx, env, meta, id, e.AuthzRuleTemplate)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		if m.Delete != nil {
			return q.deleteAuthzGrantRow(ctx, id)
		}
		err = q.reduceAuthzGrant(ctx, env, meta, id, e.AuthzGrant)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		if m.Delete != nil {
			return q.deleteAuthzGlobalRuleRow(ctx, id)
		}
		err = q.reduceAuthzGlobalRule(ctx, env, meta, id, e.AuthzGlobalRule)
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		if m.Delete != nil {
			return q.deleteAgentSessionRow(ctx, id)
		}
		err = q.reduceAgentSession(ctx, env, meta, id, e.AgentSession)
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		if m.Delete != nil {
			return q.deleteUserSessionRow(ctx, id)
		}
		err = q.reduceUserSession(ctx, env, meta, id, e.UserSession)
	case apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		if m.Delete != nil {
			return q.deleteNixStoreResetRow(ctx, id)
		}
		err = q.reduceNixStoreReset(ctx, env, meta, id, e.NixStoreReset)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:
		if m.Delete != nil {
			return q.deleteSecretKeyslotRow(ctx, id)
		}
		err = q.reduceSecretKeyslot(ctx, env, meta, id, e.SecretKeyslot)
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:
		if m.Delete != nil {
			return q.deleteSystemConfigRow(ctx, id)
		}
		err = q.reduceSystemConfig(ctx, env, meta, id, e.SystemConfig)
	default:
		return fmt.Errorf("reduce %v %d at seq %d: unknown entity type", m.Type(), id, seq)
	}
	if err != nil {
		return fmt.Errorf("reduce %v %d at seq %d: %w", m.Type(), id, seq, err)
	}
	m.SetMeta(meta)
	return q.bumpEntityID(ctx, m.Type(), id)
}

// RebuildFromLog empties every table the reducer owns and replays the write
// log into them. The caller runs it inside a transaction.
func (q *Queries) RebuildFromLog(ctx context.Context) error {
	for _, table := range materialisedTables {
		if _, err := q.db.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	for _, t := range reducedTypes {
		if _, err := q.db.ExecContext(ctx, `DELETE FROM entity_ids WHERE entity_type = ?`, int64(t)); err != nil {
			return err
		}
	}
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		return err
	}
	q.logged = map[*apigen.CoreMutation]loggedDeploymentFacts{}
	defer func() { q.logged = nil }()
	for after := int64(-1); after < seq; {
		upTo := min(after+rebuildPageSeqs, seq)
		events, err := q.WriteEventsInRange(ctx, after, upTo)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := q.ReduceUpdate(ctx, e); err != nil {
				return err
			}
		}
		clear(q.logged)
		after = upTo
	}
	return nil
}

func (q *Queries) setValueName(ctx context.Context, space, parent int64, name string, kind apigen.CoreEntityType, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_names WHERE kind = ? AND id = ?`, int64(kind), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO value_names (space_id, parent_id, name, kind, id) VALUES (?, ?, ?, ?, ?)`, space, parent, name, int64(kind), id)
	return err
}

func (q *Queries) setAssetKey(ctx context.Context, space, parent int64, key string, kind apigen.CoreEntityType, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(kind), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO asset_keys (space_id, parent_id, key, kind, id) VALUES (?, ?, ?, ?, ?)`, space, parent, key, int64(kind), id)
	return err
}

// nextValueVersion numbers the value a mutation carries: the identity's
// current version when the value equals that version's row, the next one
// otherwise. A value the table has never seen is version 1.
func (q *Queries) nextValueVersion(ctx context.Context, table string, id int64, unchanged func(version int64) (bool, error)) (int64, error) {
	var current int64
	err := q.db.QueryRowContext(ctx, `SELECT value_version FROM `+table+` WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	same, err := unchanged(current)
	if err != nil {
		return 0, err
	}
	if same {
		return current, nil
	}
	return current + 1, nil
}

func (q *Queries) reduceSecret(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, s *apigen.Secret) error {
	if s == nil || s.Fs == nil {
		return fmt.Errorf("payload has no identity")
	}
	version, err := q.nextValueVersion(ctx, "secrets", id, func(current int64) (bool, error) {
		v, err := q.GetSecretVersion(ctx, apigen.ValueRef{ID: int32(id), Version: int32(current)})
		return err == nil && v.SmkVersion == s.SmkVersion && bytes.Equal(v.Ciphertext, s.Ciphertext) && bytes.Equal(v.Nonce, s.Nonce), err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO secrets (id, space_id, directory_id, name, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, name = excluded.name,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(s.SpaceID), int64(s.Fs.DirectoryID), s.Fs.Name, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO secret_versions (secret_id, value_version, seq, event_time, author, smk_version, ciphertext, nonce)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (secret_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, s.SmkVersion, s.Ciphertext, s.Nonce); err != nil {
		return err
	}
	meta.ValueVersion = int32(version)
	return q.setValueName(ctx, int64(s.SpaceID), int64(s.Fs.DirectoryID), s.Fs.Name, apigen.CoreEntityType_CORE_ENTITY_SECRET, id)
}

func (q *Queries) deleteSecretRows(ctx context.Context, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_names WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM secret_versions WHERE secret_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceConfig(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, c *apigen.Config) error {
	if c == nil || c.Fs == nil {
		return fmt.Errorf("payload has no identity")
	}
	version, err := q.nextValueVersion(ctx, "configs", id, func(current int64) (bool, error) {
		v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(id), Version: int32(current)})
		return err == nil && v.Value == c.Value, err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO configs (id, space_id, directory_id, name, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, name = excluded.name,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(c.SpaceID), int64(c.Fs.DirectoryID), c.Fs.Name, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO config_versions (config_id, value_version, seq, event_time, author, value)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (config_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, c.Value); err != nil {
		return err
	}
	meta.ValueVersion = int32(version)
	return q.setValueName(ctx, int64(c.SpaceID), int64(c.Fs.DirectoryID), c.Fs.Name, apigen.CoreEntityType_CORE_ENTITY_CONFIG, id)
}

func (q *Queries) deleteConfigRows(ctx context.Context, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_names WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_CONFIG), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM config_versions WHERE config_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM configs WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceAsset(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, a *apigen.Asset) error {
	if a == nil || a.Fs == nil {
		return fmt.Errorf("payload has no identity")
	}
	version, err := q.nextValueVersion(ctx, "assets", id, func(current int64) (bool, error) {
		v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(id), Version: int32(current)})
		return err == nil && v.Sha256 == a.Sha256 && v.SizeBytes == a.SizeBytes && v.StorageKey == a.StorageKey, err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO assets (id, space_id, directory_id, key, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, key = excluded.key,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(a.SpaceID), int64(a.Fs.DirectoryID), a.Fs.Key, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO asset_versions (asset_id, value_version, seq, event_time, author, sha256, size_bytes, storage_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (asset_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, a.Sha256, a.SizeBytes, a.StorageKey); err != nil {
		return err
	}
	meta.ValueVersion = int32(version)
	return q.setAssetKey(ctx, int64(a.SpaceID), int64(a.Fs.DirectoryID), a.Fs.Key, apigen.CoreEntityType_CORE_ENTITY_ASSET, id)
}

func (q *Queries) deleteAssetRows(ctx context.Context, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_ASSET), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_versions WHERE asset_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceValueDirectory(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, d *apigen.ValueDirectory) error {
	if d == nil {
		return fmt.Errorf("payload has no directory")
	}
	if err := q.upsert(ctx, meta, `INSERT INTO value_directories (id, space_id, parent_id, name, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id, name = excluded.name,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(d.SpaceID), int64(d.ParentID), d.Name, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.setValueName(ctx, int64(d.SpaceID), int64(d.ParentID), d.Name, apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, id)
}

func (q *Queries) deleteValueDirectoryRows(ctx context.Context, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_names WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM value_directories WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceAssetDirectory(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, d *apigen.AssetDirectory) error {
	if d == nil {
		return fmt.Errorf("payload has no directory")
	}
	if err := q.upsert(ctx, meta, `INSERT INTO asset_directories (id, space_id, parent_id, key, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id, key = excluded.key,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(d.SpaceID), int64(d.ParentID), d.Key, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.setAssetKey(ctx, int64(d.SpaceID), int64(d.ParentID), d.Key, apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, id)
}

func (q *Queries) deleteAssetDirectoryRows(ctx context.Context, id int64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM asset_directories WHERE id = ?`, id)
	return err
}
