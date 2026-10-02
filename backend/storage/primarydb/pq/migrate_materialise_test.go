package pq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// The v0.0.614 shapes of the sixteen moved event tables, verbatim. The
// legacy status history table carried the name the materialised table has
// now, so the test puts it back under that name to exercise the rename.
const legacyDDL = `
DROP INDEX IF EXISTS idx_scheduled_instance_status_deployment_id;
DROP TABLE scheduled_instance_status;
CREATE TABLE node_event_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq     INTEGER NOT NULL,
    event_time     INTEGER NOT NULL,
    created_time   INTEGER NOT NULL,
    author         INTEGER NOT NULL,
    node_id        INTEGER NOT NULL,
    version        INTEGER NOT NULL,
    name           TEXT    NOT NULL,
    identifier     TEXT    NOT NULL,
    enrolled_time  INTEGER NOT NULL,
    status         INTEGER NOT NULL,
    roles          TEXT    NOT NULL,
    addresses      TEXT    NOT NULL,
    wg_public_key  TEXT    NOT NULL,
    allowed_spaces TEXT    NOT NULL,
    event_type     INTEGER NOT NULL,
    host_addresses TEXT NOT NULL DEFAULT '[]',
    enrollment_requested_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (node_id, version)
);
CREATE TABLE node_status_log (
    node_id INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    global_seq INTEGER NOT NULL DEFAULT 0,
    event_time INTEGER NOT NULL DEFAULT 0,
    last_connected_at INTEGER NOT NULL DEFAULT 0,
    is_connected INTEGER NOT NULL DEFAULT 0,
    opendeploy_version TEXT NOT NULL DEFAULT '',
    remote_address TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, updated_at)
);
CREATE TABLE deployment_event_log (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq               INTEGER NOT NULL,
    event_time               INTEGER NOT NULL,
    created_time             INTEGER NOT NULL,
    author                   INTEGER NOT NULL,
    deployment_id            INTEGER NOT NULL CHECK (deployment_id BETWEEN 1 AND 16777215),
    version                  INTEGER NOT NULL,
    spec_version             INTEGER NOT NULL,
    space_assignment_version INTEGER NOT NULL,
    name_version             INTEGER NOT NULL,
    spec_changed             INTEGER NOT NULL DEFAULT 0,
    space_assignment_changed INTEGER NOT NULL DEFAULT 0,
    name_changed             INTEGER NOT NULL DEFAULT 0,
    scheduling_version       INTEGER NOT NULL DEFAULT 0,
    scheduling_changed       INTEGER NOT NULL DEFAULT 0,
    value                    BLOB NOT NULL,
    event_type               INTEGER NOT NULL DEFAULT 0,
    UNIQUE (deployment_id, version)
);
CREATE TABLE scheduled_instance_event_log (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq              INTEGER NOT NULL,
    event_time              INTEGER NOT NULL,
    created_time            INTEGER NOT NULL,
    scheduled_instance_id   INTEGER NOT NULL,
    version                 INTEGER NOT NULL,
    deployment_id           INTEGER NOT NULL,
    deployment_version      INTEGER NOT NULL,
    deployment_spec_version INTEGER NOT NULL,
    node_id                 INTEGER NOT NULL,
    instance_ordinal        INTEGER NOT NULL,
    space_id                INTEGER NOT NULL,
    state                   INTEGER NOT NULL,
    UNIQUE (scheduled_instance_id, version)
);
CREATE TABLE scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    global_seq              INTEGER NOT NULL DEFAULT 0,
    event_time              INTEGER NOT NULL DEFAULT 0,
    deployment_id           INTEGER NOT NULL DEFAULT 0,
    preparer_spec_version   INTEGER,
    preparer_artifact       TEXT,
    preparer_inputs_status  INTEGER NOT NULL DEFAULT 0,
    preparer_image_status   INTEGER NOT NULL DEFAULT 0,
    runner_spec_version     INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    runner_exit_code        INTEGER,
    PRIMARY KEY (scheduled_instance_id, updated_at)
);
CREATE INDEX idx_scheduled_instance_status_deployment ON scheduled_instance_status(deployment_id, updated_at);
CREATE INDEX idx_scheduled_instance_status_seq ON scheduled_instance_status (global_seq);
CREATE TABLE space_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,
    author      INTEGER NOT NULL,
    space_id    INTEGER NOT NULL CHECK (space_id BETWEEN 0 AND 65535),
    event_type  INTEGER NOT NULL,
    name        TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE user_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,
    author      INTEGER NOT NULL,
    user_id     INTEGER NOT NULL,
    event_type  INTEGER NOT NULL,
    name        TEXT    NOT NULL,
    data_blob   BLOB    NOT NULL,
    created_at  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE network_policy_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,
    created_time INTEGER NOT NULL,
    author       INTEGER NOT NULL,
    policy_id    INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,
    UNIQUE (policy_id, version)
);
CREATE TABLE authz_rule_template_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,
    created_time INTEGER NOT NULL,
    author       INTEGER NOT NULL,
    template_id  INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    name         TEXT    NOT NULL,
    builtin      INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,
    UNIQUE (template_id, version)
);
CREATE TABLE authz_grant_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,
    created_time INTEGER NOT NULL,
    author       INTEGER NOT NULL,
    grant_id     INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    user_id      INTEGER NOT NULL,
    template_id  INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,
    UNIQUE (grant_id, version)
);
CREATE TABLE global_access_rule_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,
    created_time INTEGER NOT NULL,
    author       INTEGER NOT NULL,
    rule_id      INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    name         TEXT    NOT NULL,
    disabled     INTEGER NOT NULL DEFAULT 0,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,
    UNIQUE (rule_id, version)
);
CREATE TABLE agent_session_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,
    author             INTEGER NOT NULL,
    session_id         TEXT    NOT NULL,
    event_type         INTEGER NOT NULL,
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    token_hash         BLOB    NOT NULL,
    token_prefix       TEXT    NOT NULL,
    revoked_at         INTEGER NOT NULL DEFAULT 0,
    status             INTEGER NOT NULL DEFAULT 2,
    requesting_address TEXT    NOT NULL DEFAULT '',
    approval_code      TEXT    NOT NULL DEFAULT '',
    approved_at        INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE user_session_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,
    author             INTEGER NOT NULL,
    session_id         TEXT    NOT NULL,
    event_type         INTEGER NOT NULL,
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    token_hash         BLOB    NOT NULL,
    revoked_at         INTEGER NOT NULL DEFAULT 0,
    kind               INTEGER NOT NULL DEFAULT 0,
    requesting_address TEXT    NOT NULL DEFAULT '',
    user_agent         TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE nix_store_reset_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,
    author       INTEGER NOT NULL,
    repo         TEXT    NOT NULL,
    event_type   INTEGER NOT NULL,
    requested_at INTEGER NOT NULL
);
CREATE TABLE secret_keyslot_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,
    author      INTEGER NOT NULL,
    kind        INTEGER NOT NULL,
    node_id     INTEGER NOT NULL,
    event_type  INTEGER NOT NULL,
    smk_version INTEGER NOT NULL,
    wrapped_smk BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    kdf_salt    BLOB
);
CREATE TABLE system_config_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,
    author      INTEGER NOT NULL,
    event_type  INTEGER NOT NULL,
    config_blob BLOB    NOT NULL
);
`

func commitForTest(t *testing.T, q *Queries, seq, now int64, actor int32, ms ...*apigen.CoreMutation) {
	t.Helper()
	ctx := context.Background()
	u := &apigen.CoreWriteUpdate{Seq: seq, Time: now, Actor: actor, Mutations: ms}
	if err := q.Tx(ctx, func(tx *Queries) error {
		if err := tx.ReduceUpdate(ctx, u); err != nil {
			return err
		}
		if err := tx.InsertWriteEvent(ctx, u); err != nil {
			return err
		}
		return tx.SetGlobalSeq(ctx, seq)
	}); err != nil {
		t.Fatal(err)
	}
}

func create(t apigen.CoreEntityType, id int64, e *apigen.CoreEntity) *apigen.CoreMutation {
	return &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: t, EntityID: id, Entity: e}}
}

func update(t apigen.CoreEntityType, id int64, e *apigen.CoreEntity) *apigen.CoreMutation {
	return &apigen.CoreMutation{Update: &apigen.UpdateMutation{EntityType: t, EntityID: id, Entity: e}}
}

func del(t apigen.CoreEntityType, id int64) *apigen.CoreMutation {
	return &apigen.CoreMutation{Delete: &apigen.DeleteMutation{EntityType: t, EntityID: id}}
}

// seedLegacyHistory writes a log with creates, updates, and deletes of every
// moved type, and returns the seq it reached.
func seedLegacyHistory(t *testing.T, q *Queries) int64 {
	t.Helper()
	const (
		space    = apigen.CoreEntityType_CORE_ENTITY_SPACE
		user     = apigen.CoreEntityType_CORE_ENTITY_USER
		policy   = apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY
		template = apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE
		grant    = apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT
		rule     = apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE
		agent    = apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION
		session  = apigen.CoreEntityType_CORE_ENTITY_USER_SESSION
		reset    = apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET
		keyslot  = apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT
		settings = apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG
		dep      = apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT
		inst     = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE
		node     = apigen.CoreEntityType_CORE_ENTITY_NODE
		instSt   = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS
		nodeSt   = apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS
	)
	at := func(unix int64) time.Time { return time.Unix(unix, 0) }
	dp := func(id int32, version, specVersion int32, name, image string) *apigen.CoreEntity {
		return &apigen.CoreEntity{Deployment: &apigen.Deployment{Name: name, SpaceID: 1,
			Scheduling: apigen.DedicatedScheduling(true, 1),
			Spec:       apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: image}}}}}}
	}
	si := func(id, deployment, version, specVersion, ordinal int32, state apigen.ScheduledInstanceTarget) *apigen.CoreEntity {
		return &apigen.CoreEntity{ScheduledInstance: &apigen.ScheduledInstance{ID: id, DeploymentID: deployment, DeploymentVersion: version, DeploymentSpecVersion: specVersion,
			NodeID: 1, InstanceOrdinal: ordinal, SpaceID: 1, State: state}}
	}
	nd := func(name string, status apigen.NodeLifecycleStatus, roles []int32, underlay string, requested int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{Node: &apigen.Node{Status: status, EnrollmentRequestedAt: requested,
			Operator: apigen.NodeOperator{Name: name, EnrolledTime: 1_700_000_000_500, Roles: roles, AllowedSpaces: []int32{0, 2}},
			Reported: apigen.NodeReported{Identifier: name + "-id", UnderlayAddress: underlay, WgPublicKey: "wg-" + name, HostAddresses: []string{"203.0.113.9"}}}}
	}
	ist := func(instance, deployment int32, clock int64, status apigen.RunningStatus, exit *int32) *apigen.CoreEntity {
		return &apigen.CoreEntity{ScheduledInstanceStatus: &apigen.ScheduledInstanceStatus{ScheduledInstanceID: instance, DeploymentID: deployment, UpdatedAt: time.Unix(0, clock),
			Preparer: apigen.PreparerStatus{DeploymentSpecVersion: 1, Artifact: "sha256:abc", Inputs: apigen.InputsStatus_INPUTS_READY},
			Runner:   apigen.RunnerStatus{DeploymentSpecVersion: 1, RunningPid: 42, Status: status, NumberOfRestarts: 1, LastRestartAt: time.UnixMilli(1_700_000_001_000), ExitCode: exit, NetworkDiagnostics: []string{"ok"}}}}
	}
	nst := func(id int32, clock int64, connected bool, version string) *apigen.CoreEntity {
		return &apigen.CoreEntity{NodeStatus: &apigen.NodeStatus{NodeID: id, UpdatedAt: time.Unix(0, clock), IsConnected: connected, LastConnectedAt: time.UnixMilli(1_700_000_002_000), OpendeployVersion: version, RemoteAddress: "10.0.0.2"}}
	}
	exit := int32(3)
	as := func(id string, status apigen.AgentSessionStatus, approved int64) *apigen.CoreEntity {
		doc := &apigen.AgentSession{ID: id, UserID: 1, Status: status, RequestingAddress: "10.0.0.1", ApprovalCode: "1234"}
		if approved != 0 {
			doc.ApprovedAt, doc.ExpiresAt, doc.TokenHash, doc.TokenPrefix = at(approved), at(approved+3600), []byte{9, 9}, "a_abcd"
		}
		return &apigen.CoreEntity{AgentSession: doc}
	}
	us := func(id string, userID int32, revoked int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{UserSession: &apigen.UserSession{ID: id, UserID: userID, ExpiresAt: at(1_700_200_000), RevokedAt: unixTime(revoked), TokenHash: []byte{1, 2}, UserAgent: "test"}}
	}
	nr := func(repo string, requested int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{NixStoreReset: &apigen.NixStoreReset{Repo: repo, RequestedAt: requested}}
	}
	ks := func(kind apigen.SecretKeyslotKind, node int32, version int64, updated int64) *apigen.CoreEntity {
		doc := &apigen.SecretKeyslot{Kind: kind, NodeID: node, SmkVersion: version, WrappedSmk: []byte{byte(version)}, Nonce: []byte{2}, UpdatedAt: updated}
		if kind == apigen.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY {
			doc.KdfSalt = []byte{3}
		}
		return &apigen.CoreEntity{SecretKeyslot: doc}
	}
	cfg := func(hash string) *apigen.CoreEntity {
		return &apigen.CoreEntity{SystemConfig: &apigen.SystemConfig{MasterPasswordHash: hash}}
	}
	const (
		machine  = apigen.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE
		recovery = apigen.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY
	)
	peer := &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}
	np := func(created int64, port int32) *apigen.CoreEntity {
		return &apigen.CoreEntity{NetworkPolicy: &apigen.NetworkPolicy{Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW, Source: peer, Destination: peer,
			Ports: []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: port}}}}
	}
	tpl := func(id int64, name string, builtin bool, created int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{AuthzRuleTemplate: &apigen.AuthzRuleTemplate{ID: id, Name: name, Builtin: builtin,
			Spec: &apigen.AuthzRuleTemplateSpec{Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: name}}}}}
	}
	gr := func(userID int64, created int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{AuthzGrant: &apigen.AuthzGrant{UserID: userID, TemplateID: 1,
			Spec: &apigen.AuthzGrantSpec{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{userID}}}}}}
	}
	gl := func(id int64, name string, created int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{AuthzGlobalRule: &apigen.AuthzGlobalRule{ID: id, Name: name, Spec: &apigen.AuthzGlobalRuleSpec{Deny: true}}}
	}
	usr := func(id int32, name string, created int64) *apigen.CoreEntity {
		return &apigen.CoreEntity{User: &apigen.User{ID: id, Name: name, Credentials: (&apigen.InternalUser{ID: id, Name: name, WebAuthNID: []byte{byte(id)}}).Encode()}}
	}
	sp := func(id int32, name string) *apigen.CoreEntity {
		return &apigen.CoreEntity{Space: &apigen.Space{ID: id, Name: name}}
	}
	seq := int64(0)
	step := func(actor int32, ms ...*apigen.CoreMutation) {
		seq++
		commitForTest(t, q, seq, 1000+seq, actor, ms...)
	}
	step(3, create(space, 2, sp(2, "team")), create(space, 3, sp(3, "gone")))
	step(3, update(space, 2, sp(2, "team-renamed")))
	step(3, del(space, 3))
	step(1, create(user, 1, usr(1, "alice", 1004)))
	step(2, create(user, 2, usr(2, "bob", 1005)))
	step(1, update(user, 1, usr(1, "alice", 1004)))
	step(2, del(user, 2))
	step(3, create(policy, 1, np(1008, 80)), create(policy, 2, np(1008, 81)))
	step(3, update(policy, 1, np(1008, 443)))
	step(3, del(policy, 2))
	step(3, create(template, 1, tpl(1, "cluster_admin", true, 1011)), create(template, 2, tpl(2, "custom", false, 1011)), create(template, 3, tpl(3, "doomed", false, 1011)))
	step(3, update(template, 2, tpl(2, "custom-renamed", false, 1011)))
	step(3, del(template, 3))
	step(3, create(grant, 1, gr(1, 1014)), create(grant, 2, gr(2, 1014)))
	step(3, del(grant, 2))
	step(3, create(rule, 1, gl(1, "default_user_visibility", 1016)), create(rule, 2, gl(2, "removed", 1016)))
	step(3, del(rule, 2))
	step(1, create(agent, 1, as("agent-a", apigen.AgentSessionStatus_AGENT_SESSION_PENDING, 0)), create(agent, 2, as("agent-b", apigen.AgentSessionStatus_AGENT_SESSION_PENDING, 0)))
	step(1, update(agent, 1, as("agent-a", apigen.AgentSessionStatus_AGENT_SESSION_APPROVED, 1_700_000_100)))
	step(1, update(agent, 2, as("agent-b", apigen.AgentSessionStatus_AGENT_SESSION_REJECTED, 0)))
	step(1, create(session, 1, us("user-live", 1, 0)), create(session, 2, us("user-revoked", 2, 0)))
	step(2, update(session, 2, us("user-revoked", 2, 1_700_000_500)))
	step(3, create(reset, 1, nr("github.com/acme/app", 1_000)))
	step(3, update(reset, 1, nr("github.com/acme/app", 2_000)), create(reset, 2, nr("github.com/acme/lib", 3_000)))
	step(0, create(keyslot, 1*256+int64(machine), ks(machine, 1, 1, 1000+seq+1)), create(keyslot, 2*256+int64(machine), ks(machine, 2, 1, 1000+seq+1)))
	step(7, create(keyslot, int64(recovery), ks(recovery, 0, 1, 1000+seq+1)))
	step(7, update(keyslot, int64(recovery), ks(recovery, 0, 2, 1000+seq+1)))
	step(0, del(keyslot, 2*256+int64(machine)))
	step(1, create(settings, SystemConfigEntityID, cfg("one")))
	step(1, update(settings, SystemConfigEntityID, cfg("two")))
	step(0, create(node, 1, nd("primary", apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL, []int32{0}, "192.0.2.1", 0)))
	step(0, create(node, 2, nd("worker", apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED, []int32{1}, "192.0.2.2", 1_700_000_003_000)))
	step(1, update(node, 2, nd("worker", apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL, []int32{1}, "192.0.2.2", 0)))
	step(0, create(node, 3, nd("gone", apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL, []int32{1}, "192.0.2.3", 0)))
	step(1, update(node, 3, nd("gone", apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED, []int32{1}, "192.0.2.3", 0)))
	step(0, create(nodeSt, 1, nst(1, 5_000, true, "v1")))
	step(0, update(nodeSt, 1, nst(1, 5_001, false, "v1")), create(nodeSt, 2, nst(2, 6_000, true, "v2")))
	step(1, create(dep, 1, dp(1, 1, 1, "web", "web:1")))
	step(1, create(inst, 1, si(1, 1, 1, 1, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)))
	step(0, create(instSt, 1, ist(1, 1, 7_000, apigen.RunningStatus_RUNNING, nil)))
	step(1, update(dep, 1, dp(1, 2, 2, "web", "web:2")))
	step(1, create(inst, 3, si(3, 1, 2, 2, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)))
	step(0, create(instSt, 3, ist(3, 1, 8_000, apigen.RunningStatus_RUNNING, nil)))
	step(0, update(instSt, 3, ist(3, 1, 8_001, apigen.RunningStatus_STOPPED, &exit)))
	step(1, update(inst, 3, si(3, 1, 2, 2, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)))
	step(2, update(dep, 1, dp(1, 3, 2, "web-renamed", "web:2")))
	step(0, update(instSt, 1, ist(1, 1, 7_001, apigen.RunningStatus_STOPPED, &exit)))
	step(1, update(inst, 1, si(1, 1, 1, 1, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)), create(inst, 2, si(2, 1, 3, 2, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)))
	step(1, create(dep, 2, dp(2, 1, 1, "short-lived", "x:1")))
	step(1, del(dep, 2))
	step(1, create(dep, 3, dp(3, 1, 1, "pinned", "p:1")))
	step(1, create(inst, 4, si(4, 3, 1, 1, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)))
	step(0, create(instSt, 4, ist(4, 3, 9_000, apigen.RunningStatus_RUNNING, nil)))
	step(1, update(dep, 3, dp(3, 2, 2, "pinned", "p:2")))
	step(2, del(dep, 3))
	return seq
}

// writeLegacyTables derives the v0.0.614 event tables from the write log: one
// row per logged mutation, deletes carrying the previous document. Session
// and reset rows keep the legacy id rule: the create row id is the entity id,
// later rows land above it.
func writeLegacyTables(t *testing.T, q *Queries, through int64) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range strings.Split(legacyDDL, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := q.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	events, err := q.WriteEventsInRange(ctx, -1, through)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		t  apigen.CoreEntityType
		id int64
	}
	last := map[key]*apigen.CoreEntity{}
	versions := map[key]int64{}
	specVersions := map[key]int64{}
	previousSpec := map[key]*apigen.DeploymentSpec{}
	laterRow := int64(1000)
	for _, e := range events {
		for _, m := range e.Mutations {
			k := key{m.Type(), m.EntityID()}
			entity := m.Entity()
			if entity == nil {
				entity = last[k]
			}
			last[k] = entity
			versions[k]++
			op := int64(m.Kind())
			rowID := k.id
			if versions[k] > 1 {
				laterRow++
				rowID = laterRow
			}
			var err error
			switch m.Type() {
			case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
				a := entity.AgentSession
				_, err = q.db.ExecContext(ctx, `INSERT INTO agent_session_event_log (id, global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					rowID, e.Seq, e.Time, e.Actor, a.ID, op, a.UserID, e.Time/1000, unixOrZero(a.ExpiresAt), notNullBlob(a.TokenHash), a.TokenPrefix, int64(a.Status), a.RequestingAddress, a.ApprovalCode, unixOrZero(a.ApprovedAt))
			case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
				u := entity.UserSession
				_, err = q.db.ExecContext(ctx, `INSERT INTO user_session_event_log (id, global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					rowID, e.Seq, e.Time, e.Actor, u.ID, op, u.UserID, e.Time/1000, unixOrZero(u.ExpiresAt), notNullBlob(u.TokenHash), unixOrZero(u.RevokedAt), int64(u.Kind), u.RequestingAddress, u.UserAgent)
			case apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
				r := entity.NixStoreReset
				_, err = q.db.ExecContext(ctx, `INSERT INTO nix_store_reset_event_log (id, global_seq, event_time, author, repo, event_type, requested_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					rowID, e.Seq, e.Time, e.Actor, r.Repo, op, r.RequestedAt)
			case apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:
				s := entity.SecretKeyslot
				_, err = q.db.ExecContext(ctx, `INSERT INTO secret_keyslot_event_log (global_seq, event_time, author, kind, node_id, event_type, smk_version, wrapped_smk, nonce, kdf_salt) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Actor, int64(s.Kind), s.NodeID, op, s.SmkVersion, s.WrappedSmk, s.Nonce, s.KdfSalt)
			case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:
				_, err = q.db.ExecContext(ctx, `INSERT INTO system_config_event_log (global_seq, event_time, author, event_type, config_blob) VALUES (?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Actor, op, entity.SystemConfig.Encode())
			case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
				d := *entity.Deployment
				if previous := previousSpec[k]; previous == nil || !DeploymentSpecsEqual(previous, &d.Spec) {
					specVersions[k]++
				}
				previousSpec[k] = &d.Spec
				_, err = q.db.ExecContext(ctx, `INSERT INTO deployment_event_log (global_seq, event_time, created_time, author, deployment_id, version, spec_version, space_assignment_version, name_version, value, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], specVersions[k], d.Encode(), op)
			case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
				i := entity.ScheduledInstance
				_, err = q.db.ExecContext(ctx, `INSERT INTO scheduled_instance_event_log (global_seq, event_time, created_time, scheduled_instance_id, version, deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, k.id, versions[k], i.DeploymentID, i.DeploymentVersion, i.DeploymentSpecVersion, i.NodeID, i.InstanceOrdinal, i.SpaceID, int64(i.State))
			case apigen.CoreEntityType_CORE_ENTITY_NODE:
				n := entity.Node
				_, err = q.db.ExecContext(ctx, `INSERT INTO node_event_log (global_seq, event_time, created_time, author, node_id, version, name, identifier, enrolled_time, status, roles, addresses, wg_public_key, allowed_spaces, event_type, host_addresses, enrollment_requested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], n.Operator.Name, n.Reported.Identifier, n.Operator.EnrolledTime, int64(n.Status), jsonList(n.Operator.Roles), jsonList([]string{n.Reported.UnderlayAddress}), n.Reported.WgPublicKey, `[2, 0]`, op, jsonList(n.Reported.HostAddresses), n.EnrollmentRequestedAt)
			case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
				st := entity.ScheduledInstanceStatus
				_, err = q.db.ExecContext(ctx, `INSERT INTO scheduled_instance_status (scheduled_instance_id, updated_at, global_seq, event_time, deployment_id, preparer_spec_version, preparer_artifact, preparer_inputs_status, preparer_image_status, runner_spec_version, runner_pid, runner_artifact, runner_status, runner_num_restarts, runner_last_restart_at, runner_extra_blob, runner_exit_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					k.id, clockToNanos(st.UpdatedAt), e.Seq, e.Time, st.DeploymentID, st.Preparer.DeploymentSpecVersion, st.Preparer.Artifact, int64(st.Preparer.Inputs), int64(st.Preparer.Image),
					st.Runner.DeploymentSpecVersion, st.Runner.RunningPid, st.Runner.RunningArtifact, int64(st.Runner.Status), st.Runner.NumberOfRestarts, st.Runner.LastRestartAt.UnixMilli(), runnerStatusExtraBlob(st.Runner), nullableExit(st.Runner.ExitCode))
			case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
				st := entity.NodeStatus
				_, err = q.db.ExecContext(ctx, `INSERT INTO node_status_log (node_id, updated_at, global_seq, event_time, last_connected_at, is_connected, opendeploy_version, remote_address) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					k.id, clockToNanos(st.UpdatedAt), e.Seq, e.Time, timeToMillis(st.LastConnectedAt), boolToInt(st.IsConnected), st.OpendeployVersion, st.RemoteAddress)
			case apigen.CoreEntityType_CORE_ENTITY_SPACE:
				_, err = q.db.ExecContext(ctx, `INSERT INTO space_event_log (global_seq, event_time, author, space_id, event_type, name) VALUES (?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Actor, k.id, op, entity.Space.Name)
			case apigen.CoreEntityType_CORE_ENTITY_USER:
				_, err = q.db.ExecContext(ctx, `INSERT INTO user_event_log (global_seq, event_time, author, user_id, event_type, name, data_blob, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Actor, k.id, op, entity.User.Name, entity.User.Credentials, e.Time)
			case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
				value := *entity.NetworkPolicy
				value.ID = 0
				_, err = q.db.ExecContext(ctx, `INSERT INTO network_policy_event_log (global_seq, event_time, created_time, author, policy_id, version, data_blob, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], value.Encode(), op)
			case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE:
				r := entity.AuthzRuleTemplate
				_, err = q.db.ExecContext(ctx, `INSERT INTO authz_rule_template_event_log (global_seq, event_time, created_time, author, template_id, version, name, builtin, data_blob, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], r.Name, r.Builtin, r.Spec.Encode(), op)
			case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
				g := entity.AuthzGrant
				_, err = q.db.ExecContext(ctx, `INSERT INTO authz_grant_event_log (global_seq, event_time, created_time, author, grant_id, version, user_id, template_id, data_blob, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], g.UserID, g.TemplateID, g.Spec.Encode(), op)
			case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
				r := entity.AuthzGlobalRule
				_, err = q.db.ExecContext(ctx, `INSERT INTO global_access_rule_event_log (global_seq, event_time, created_time, author, rule_id, version, name, data_blob, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					e.Seq, e.Time, e.Time, e.Actor, k.id, versions[k], r.Name, r.Spec.Encode(), op)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func nullableExit(code *int32) any {
	if code == nil {
		return nil
	}
	return int64(*code)
}

func tableRows(t *testing.T, dbPath, table string) []string {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	q := &Queries{db: &conn{DBTX: db, root: db}}
	rows, err := q.rowFacts(context.Background(), `SELECT * FROM `+table+` ORDER BY 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestMaterialiseLegacyTablesVerifiesAndDrops(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	q := Open(dbPath)
	through := seedLegacyHistory(t, q)
	tables := []string{"spaces", "users", "network_policies", "authz_rule_templates", "authz_grants", "authz_global_rules",
		"agent_sessions", "user_sessions", "nix_store_resets", "secret_keyslots", "system_config", "entity_ids",
		"nodes", "node_status", "deployments", "deployment_versions", "scheduled_instances", "scheduled_instance_status"}
	before := map[string][]string{}
	for _, table := range tables {
		before[table] = tableRows(t, dbPath, table)
		if len(before[table]) == 0 {
			t.Fatalf("scenario left %s empty", table)
		}
	}
	if got := strings.Join(before["scheduled_instances"], ";"); strings.Contains(got, "[1 1 ") || !strings.Contains(got, "[3 1 2") {
		t.Fatalf("retention: superseded final 1 kept or ordinal final 3 dropped: %s", got)
	}
	if len(before["deployments"]) != 1 || len(before["deployment_versions"]) != 3 || len(before["scheduled_instances"]) != 3 || len(before["scheduled_instance_status"]) != 2 {
		t.Fatalf("retention left deployments %d, versions %d, instances %d, statuses %d", len(before["deployments"]), len(before["deployment_versions"]), len(before["scheduled_instances"]), len(before["scheduled_instance_status"]))
	}
	writeLegacyTables(t, q, through)
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = Open(dbPath)
	defer q.Close()
	present, err := q.existingTables(context.Background(), legacyEventTables)
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 0 {
		t.Fatalf("legacy tables still present after the move: %v", present)
	}
	assertLegacyBackup(t, dbPath)
	var reshaped int64
	if err := q.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_table_info('scheduled_instance_status') WHERE name = 'seq'`).Scan(&reshaped); err != nil || reshaped != 1 {
		t.Fatalf("scheduled_instance_status was not reshaped: %d, %v", reshaped, err)
	}
	for _, table := range tables {
		if after := tableRows(t, dbPath, table); !reflect.DeepEqual(before[table], after) {
			t.Fatalf("%s changed across the move\nbefore:\n%s\nafter:\n%s", table, strings.Join(before[table], "\n"), strings.Join(after, "\n"))
		}
	}
}

func TestMaterialiseLegacyTablesRefusesAMismatch(t *testing.T) {
	for _, tamper := range []string{
		`UPDATE space_event_log SET name = 'other' WHERE space_id = 2 AND event_type = 2`,
		`UPDATE authz_rule_template_event_log SET builtin = 0 WHERE template_id = 1`,
		`UPDATE authz_grant_event_log SET template_id = 9 WHERE grant_id = 1`,
		`DELETE FROM global_access_rule_event_log WHERE rule_id = 1`,
		`UPDATE agent_session_event_log SET status = 9 WHERE session_id = 'agent-a' AND event_type = 2`,
		`UPDATE user_session_event_log SET revoked_at = 5 WHERE session_id = 'user-live'`,
		`UPDATE nix_store_reset_event_log SET requested_at = 1 WHERE repo = 'github.com/acme/app' AND event_type = 2`,
		`DELETE FROM secret_keyslot_event_log WHERE event_type = 3`,
		`UPDATE secret_keyslot_event_log SET wrapped_smk = X'ff' WHERE kind = 2 AND event_type = 2`,
		`UPDATE system_config_event_log SET config_blob = X'' WHERE id = (SELECT MAX(id) FROM system_config_event_log)`,
		`UPDATE deployment_event_log SET spec_version = 9 WHERE deployment_id = 1 AND version = 3`,
		`DELETE FROM scheduled_instance_event_log WHERE scheduled_instance_id = 2`,
		`UPDATE scheduled_instance_event_log SET state = 1 WHERE scheduled_instance_id = 1 AND version = 2`,
		`UPDATE scheduled_instance_status SET runner_exit_code = 9 WHERE scheduled_instance_id = 3 AND updated_at = 8001`,
		`UPDATE node_event_log SET name = 'other' WHERE id = (SELECT MAX(id) FROM node_event_log WHERE node_id = 2)`,
		`UPDATE node_event_log SET host_addresses = '["203.0.113.8"]' WHERE node_id = 1`,
		`UPDATE node_status_log SET remote_address = 'x' WHERE node_id = 1 AND updated_at = 5001`,
	} {
		t.Run(tamper, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "primary.db")
			q := Open(dbPath)
			through := seedLegacyHistory(t, q)
			writeLegacyTables(t, q, through)
			if _, err := q.db.ExecContext(context.Background(), tamper); err != nil {
				t.Fatal(err)
			}
			if err := q.Close(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("Open accepted legacy tables that disagree with the write log")
				}
				if !strings.Contains(fmt.Sprint(r), "the write log rebuilds") {
					t.Fatalf("refused for another reason: %v", r)
				}
				if !strings.Contains(fmt.Sprint(r), "kept at "+dbPath+".pre-materialise") {
					t.Fatalf("refusal does not name the copy of the file: %v", r)
				}
				present, err := (&Queries{db: &conn{DBTX: sqlitedb.MustOpen(dbPath)}}).existingTables(context.Background(), legacyEventTables)
				if err != nil || len(present) != 16 {
					t.Fatalf("refused start left %v, %v", present, err)
				}
				assertLegacyBackup(t, dbPath)
				defer func() {
					if recover() == nil {
						t.Fatal("a retry accepted the same tables")
					}
					assertLegacyBackup(t, dbPath)
				}()
				Open(dbPath).Close()
			}()
			Open(dbPath).Close()
		})
	}
}

// assertLegacyBackup checks the copy taken before the move is the database
// as v0.0.614 left it: the old status history under the materialised table's
// name, every event table present, and no materialised table yet.
func assertLegacyBackup(t *testing.T, dbPath string) {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath + ".pre-materialise")
	defer db.Close()
	q := &Queries{db: &conn{DBTX: db, root: db}}
	var legacyStatus int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduled_instance_status') WHERE name = 'global_seq'`).Scan(&legacyStatus); err != nil || legacyStatus != 1 {
		t.Fatalf("backup status table is not the legacy shape: %d, %v", legacyStatus, err)
	}
	present, err := q.existingTables(context.Background(), legacyEventTables)
	if err != nil || len(present) != 15 {
		t.Fatalf("backup holds legacy tables %v, %v, want the fifteen the scenario wrote", present, err)
	}
	if materialised, err := q.existingTables(context.Background(), []string{"deployments", "deployment_versions", "nodes", "scheduled_instances"}); err != nil || len(materialised) != 4 {
		t.Fatalf("backup materialised tables %v, %v", materialised, err)
	}
	var rows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployment_event_log`).Scan(&rows); err != nil || rows == 0 {
		t.Fatalf("backup deployment_event_log rows %d, %v", rows, err)
	}
}

func TestOpenRefusesADatabaseFromBeforeTheWriteLogUntouched(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpenWriter(dbPath)
	for _, stmt := range []string{
		`CREATE TABLE global_seq (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL)`,
		`INSERT INTO global_seq (id, value) VALUES (1, 7)`,
		`CREATE TABLE scheduled_instance_status (id INTEGER PRIMARY KEY, scheduled_instance_id INTEGER NOT NULL, global_seq INTEGER NOT NULL)`,
		`CREATE INDEX idx_scheduled_instance_status_seq ON scheduled_instance_status (global_seq)`,
		`CREATE TABLE deployment_event_log (id INTEGER PRIMARY KEY, deployment_id INTEGER NOT NULL)`,
		`INSERT INTO deployment_event_log (deployment_id) VALUES (1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	schema := func() []string {
		rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var typ, name string
			var ddl sql.NullString
			if err := rows.Scan(&typ, &name, &ddl); err != nil {
				t.Fatal(err)
			}
			out = append(out, typ+" "+name+" "+ddl.String)
		}
		return out
	}
	before := schema()
	func() {
		defer func() {
			r := recover()
			if r == nil || !strings.Contains(fmt.Sprint(r), "start this database on v0.0.614 once before upgrading") {
				t.Fatalf("Open on a database without a write log: %v", r)
			}
		}()
		Open(dbPath).Close()
	}()
	if after := schema(); !reflect.DeepEqual(before, after) {
		t.Fatalf("the refused start changed the file\nbefore:\n%s\nafter:\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
	if _, err := os.Stat(dbPath + ".pre-materialise"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused start left a copy: %v", err)
	}
	db.Close()
}
