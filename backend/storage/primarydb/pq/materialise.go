package pq

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
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
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE,
	apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION,
	apigen.CoreEntityType_CORE_ENTITY_USER_SESSION,
	apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET,
	apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT,
	apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG,
}

var materialisedTables = []string{
	"value_keys", "secret_versions", "secrets", "config_versions", "configs", "value_directories",
	"asset_keys", "asset_versions", "assets", "asset_directories",
	"spaces", "users", "network_policies", "authz_grant_templates", "authz_grants", "authz_global_rules",
	"agent_sessions", "user_sessions", "nix_store_resets", "secret_keyslots", "system_config",
	"deployments", "deployment_versions", "scheduled_instances", "scheduled_instance_status", "nodes", "node_status",
}

const rebuildPageSeqs = 2_000

// NextEntityID allocates the next id of a reduced type. Ids are never reused:
// the counter only moves forward, here and in the reducer.
func (q *Queries) NextEntityID(ctx context.Context, t apigen.CoreEntityType) (uint64, error) {
	var id uint64
	err := q.db.QueryRowContext(ctx, `INSERT INTO entity_ids (entity_type, next) VALUES (?, 2)
ON CONFLICT (entity_type) DO UPDATE SET next = next + 1
RETURNING next - 1`, int64(t)).Scan(&id)
	return id, err
}

func (q *Queries) bumpEntityID(ctx context.Context, t apigen.CoreEntityType, id uint64) error {
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
	return q.reduceUpdate(ctx, u, nil)
}

func (q *Queries) reduceUpdate(ctx context.Context, u *apigen.CoreWriteUpdate, logged map[int]loggedDeploymentFacts) error {
	start := 0
	if q.applied != nil {
		start = q.applied[u]
	}
	if start >= len(u.Mutations) {
		return nil
	}
	affected := map[uint64]struct{}{}
	for i := start; i < len(u.Mutations); i++ {
		m := &u.Mutations[i]
		if err := q.affectedDeployment(ctx, m, affected); err != nil {
			return err
		}
		env := rowEnvelope{Seq: u.Seq, EventTime: u.Time, Author: u.Actor}
		if facts, ok := logged[i]; ok {
			env.Logged = &facts
		}
		if err := q.reduce(ctx, env, m); err != nil {
			return err
		}
	}
	if q.applied != nil {
		q.applied[u] = len(u.Mutations)
	}
	for i := start; i < len(u.Mutations); i++ {
		m := &u.Mutations[i]
		if m.Type() == apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS && m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
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

func (q *Queries) affectedDeployment(ctx context.Context, m *apigen.CoreMutation, affected map[uint64]struct{}) error {
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		affected[m.EntityID()] = struct{}{}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if e := m.Entity(); e != nil && e.Value.ScheduledInstance != nil {
			affected[e.Value.ScheduledInstance.Deployment.DeploymentID] = struct{}{}
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

// reduce applies one logged mutation to the tables of its entity type: a
// create or update upserts the rows the payload describes and stamps the
// mutation with the entity's meta as the rows now hold it, a delete removes
// the rows.
func (q *Queries) reduce(ctx context.Context, env rowEnvelope, m *apigen.CoreMutation) error {
	id := m.EntityID()
	e := m.Entity()
	deleted := m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE
	if e == nil && !deleted {
		return fmt.Errorf("reduce %v %d: mutation has no payload", m.Type(), id)
	}
	var v apigen.CoreEntityValueOneof
	if e != nil {
		v = e.Value
	}
	meta := newMeta(env)
	var err error
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		if deleted {
			return q.deleteDeploymentRow(ctx, id)
		}
		err = q.reduceDeployment(ctx, env, meta, id, v.Deployment)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if deleted {
			return q.deleteScheduledInstanceRows(ctx, id)
		}
		err = q.reduceScheduledInstance(ctx, env, meta, id, v.ScheduledInstance)
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		if deleted {
			return q.deleteNodeRows(ctx, id)
		}
		err = q.reduceNode(ctx, env, meta, id, v.Node)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		if deleted {
			return q.deleteScheduledInstanceStatusRow(ctx, id)
		}
		err = q.reduceScheduledInstanceStatus(ctx, env, meta, id, v.ScheduledInstanceStatus)
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		if deleted {
			return q.deleteNodeStatusRow(ctx, id)
		}
		err = q.reduceNodeStatus(ctx, env, meta, id, v.NodeStatus)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		if deleted {
			return q.deleteSecretRows(ctx, id)
		}
		err = q.reduceSecret(ctx, env, meta, id, v.Secret)
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		if deleted {
			return q.deleteConfigRows(ctx, id)
		}
		err = q.reduceConfig(ctx, env, meta, id, v.Config)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		if deleted {
			return q.deleteAssetRows(ctx, id)
		}
		err = q.reduceAsset(ctx, env, meta, id, v.Asset)
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		if deleted {
			return q.deleteValueDirectoryRows(ctx, id)
		}
		err = q.reduceValueDirectory(ctx, env, meta, id, v.ValueDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		if deleted {
			return q.deleteAssetDirectoryRows(ctx, id)
		}
		err = q.reduceAssetDirectory(ctx, env, meta, id, v.AssetDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		if deleted {
			return q.deleteSpaceRow(ctx, id)
		}
		err = q.reduceSpace(ctx, env, meta, id, v.Space)
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		if deleted {
			return q.deleteUserRow(ctx, id)
		}
		err = q.reduceUser(ctx, env, meta, id, v.User)
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		if deleted {
			return q.deleteNetworkPolicyRow(ctx, id)
		}
		err = q.reduceNetworkPolicy(ctx, env, meta, id, v.NetworkPolicy)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE:
		if deleted {
			return q.deleteAuthzGrantTemplateRow(ctx, id)
		}
		err = q.reduceAuthzGrantTemplate(ctx, env, meta, id, v.AuthzGrantTemplate)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		if deleted {
			return q.deleteAuthzGrantRow(ctx, id)
		}
		err = q.reduceAuthzGrant(ctx, env, meta, id, v.AuthzGrant)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		if deleted {
			return q.deleteAuthzGlobalRuleRow(ctx, id)
		}
		err = q.reduceAuthzGlobalRule(ctx, env, meta, id, v.AuthzGlobalRule)
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		if deleted {
			return q.deleteAgentSessionRow(ctx, id)
		}
		err = q.reduceAgentSession(ctx, env, meta, id, v.AgentSession)
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		if deleted {
			return q.deleteUserSessionRow(ctx, id)
		}
		err = q.reduceUserSession(ctx, env, meta, id, v.UserSession)
	case apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		if deleted {
			return q.deleteNixStoreResetRow(ctx, id)
		}
		err = q.reduceNixStoreReset(ctx, env, meta, id, v.NixStoreReset)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:
		if deleted {
			return q.deleteSecretKeyslotRow(ctx, id)
		}
		err = q.reduceSecretKeyslot(ctx, env, meta, id, v.SecretKeyslot)
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:
		if deleted {
			return q.deleteSystemConfigRow(ctx, id)
		}
		err = q.reduceSystemConfig(ctx, env, meta, id, v.SystemConfig)
	default:
		return fmt.Errorf("reduce %v %d at seq %d: unknown entity type", m.Type(), id, env.Seq)
	}
	if err != nil {
		return fmt.Errorf("reduce %v %d at seq %d: %w", m.Type(), id, env.Seq, err)
	}
	m.SetMeta(*meta)
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
	for after := int64(-1); after < seq; {
		upTo := min(after+rebuildPageSeqs, seq)
		events, err := q.loggedWriteEventsInRange(ctx, after, upTo)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := q.reduceUpdate(ctx, e.update, e.logged); err != nil {
				return err
			}
		}
		after = upTo
	}
	return nil
}

func (q *Queries) setValueKey(ctx context.Context, space, parent uint64, key string, kind apigen.CoreEntityType, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_keys WHERE kind = ? AND id = ?`, int64(kind), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO value_keys (space_id, parent_id, key, kind, id) VALUES (?, ?, ?, ?, ?)`, space, parent, key, int64(kind), id)
	return err
}

func (q *Queries) setAssetKey(ctx context.Context, space, parent uint64, key string, kind apigen.CoreEntityType, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(kind), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO asset_keys (space_id, parent_id, key, kind, id) VALUES (?, ?, ?, ?, ?)`, space, parent, key, int64(kind), id)
	return err
}

// nextValueVersion numbers the value a mutation carries: the identity's
// current version when the value equals that version's row, the next one
// otherwise. A value the table has never seen is version 1.
func (q *Queries) nextValueVersion(ctx context.Context, table string, id uint64, unchanged func(version uint32) (bool, error)) (uint32, error) {
	var current uint32
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

func (q *Queries) reduceSecret(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, s *apigen.Secret) error {
	if s == nil {
		return fmt.Errorf("payload has no identity")
	}
	if !s.Sealed.Present {
		return fmt.Errorf("payload has no sealed value")
	}
	sealed := s.Sealed.Value
	version, err := q.nextValueVersion(ctx, "secrets", id, func(current uint32) (bool, error) {
		v, err := q.GetSecretVersion(ctx, apigen.ValueRef{ID: id, Version: current})
		return err == nil && v.SmkVersion == sealed.SmkVersion && bytes.Equal(v.Ciphertext, sealed.Ciphertext) && bytes.Equal(v.Nonce, sealed.Nonce), err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO secrets (id, space_id, directory_id, key, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, key = excluded.key,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, s.SpaceID, s.Fs.DirectoryID.Value, s.Fs.Key, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO secret_versions (secret_id, value_version, seq, event_time, author, smk_version, ciphertext, nonce)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (secret_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, sealed.SmkVersion, sealed.Ciphertext, sealed.Nonce); err != nil {
		return err
	}
	meta.ValueVersion = version
	return q.setValueKey(ctx, s.SpaceID, s.Fs.DirectoryID.Value, s.Fs.Key, apigen.CoreEntityType_CORE_ENTITY_SECRET, id)
}

func (q *Queries) deleteSecretRows(ctx context.Context, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM secret_versions WHERE secret_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceConfig(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, c *apigen.Config) error {
	if c == nil {
		return fmt.Errorf("payload has no identity")
	}
	version, err := q.nextValueVersion(ctx, "configs", id, func(current uint32) (bool, error) {
		v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: id, Version: current})
		return err == nil && v.Value == c.Value, err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO configs (id, space_id, directory_id, key, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, key = excluded.key,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, c.SpaceID, c.Fs.DirectoryID.Value, c.Fs.Key, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO config_versions (config_id, value_version, seq, event_time, author, value)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (config_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, c.Value); err != nil {
		return err
	}
	meta.ValueVersion = version
	return q.setValueKey(ctx, c.SpaceID, c.Fs.DirectoryID.Value, c.Fs.Key, apigen.CoreEntityType_CORE_ENTITY_CONFIG, id)
}

func (q *Queries) deleteConfigRows(ctx context.Context, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_CONFIG), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM config_versions WHERE config_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM configs WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceAsset(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, a *apigen.Asset) error {
	if a == nil {
		return fmt.Errorf("payload has no identity")
	}
	sha := hex.EncodeToString(a.Sha256)
	version, err := q.nextValueVersion(ctx, "assets", id, func(current uint32) (bool, error) {
		v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: id, Version: current})
		return err == nil && v.Sha256 == sha && v.SizeBytes == int64(a.SizeBytes) && v.StorageKey == a.StorageKey, err
	})
	if err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO assets (id, space_id, directory_id, key, value_version, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, directory_id = excluded.directory_id, key = excluded.key,
  value_version = excluded.value_version, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, a.SpaceID, a.Fs.DirectoryID.Value, a.Fs.Key, version, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO asset_versions (asset_id, value_version, seq, event_time, author, sha256, size_bytes, storage_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (asset_id, value_version) DO NOTHING`,
		id, version, env.Seq, env.EventTime, env.Author, sha, int64(a.SizeBytes), a.StorageKey); err != nil {
		return err
	}
	meta.ValueVersion = version
	return q.setAssetKey(ctx, a.SpaceID, a.Fs.DirectoryID.Value, a.Fs.Key, apigen.CoreEntityType_CORE_ENTITY_ASSET, id)
}

func (q *Queries) deleteAssetRows(ctx context.Context, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_ASSET), id); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_versions WHERE asset_id = ?`, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceValueDirectory(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, d *apigen.ValueDirectory) error {
	if d == nil {
		return fmt.Errorf("payload has no directory")
	}
	if err := q.upsert(ctx, meta, `INSERT INTO value_directories (id, space_id, parent_id, key, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id, key = excluded.key,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, d.SpaceID, d.ParentID.Value, d.Key, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.setValueKey(ctx, d.SpaceID, d.ParentID.Value, d.Key, apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, id)
}

func (q *Queries) deleteValueDirectoryRows(ctx context.Context, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM value_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM value_directories WHERE id = ?`, id)
	return err
}

func (q *Queries) reduceAssetDirectory(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, d *apigen.AssetDirectory) error {
	if d == nil {
		return fmt.Errorf("payload has no directory")
	}
	if err := q.upsert(ctx, meta, `INSERT INTO asset_directories (id, space_id, parent_id, key, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id, key = excluded.key,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, d.SpaceID, d.ParentID.Value, d.Key, env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.setAssetKey(ctx, d.SpaceID, d.ParentID.Value, d.Key, apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, id)
}

func (q *Queries) deleteAssetDirectoryRows(ctx context.Context, id uint64) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM asset_keys WHERE kind = ? AND id = ?`, int64(apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY), id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM asset_directories WHERE id = ?`, id)
	return err
}
