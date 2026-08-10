package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/instancecache"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const OpendeploySpaceID int32 = internaldeploy.SpaceID
const DefaultSpaceID int32 = 1

func normalizedUserSpaceID(spaceID int32) int32 {
	if spaceID <= 0 {
		return DefaultSpaceID
	}
	return spaceID
}

// Service is the primary's state store. Two mechanisms coordinate access:
//
//   - Mu is the writer freeze: every method that writes the database holds it,
//     so a read-check-write sequence under Mu (version match, sibling-key
//     uniqueness, ...) sees a database no other writer can change.
//   - q.Tx groups multiple writes so they commit atomically. It is required for
//     every multi-statement write — readers do not take Mu, so without a tx
//     they (and a crash) could observe the group half-applied. A single
//     statement is already atomic; it needs no tx.
//
// Reads therefore run under Mu before the tx; the tx wraps only the writes.
type Service struct {
	// Cache is the shared scheduled-instance runtime view. Its Mu is the
	// storage-wide mutex: every method that used to lock the deployment
	// store's mutex locks it.
	*instancecache.Cache

	// q is the SQL layer: every query — sqlc-generated or hand-written —
	// is a method on it. Service owns caches, locking, and notification.
	q *pq.Queries

	// configCache holds the latest desired DeploymentConfig per deployment id.
	// Used by primary scheduler/APIs only — never as the pinned config source for
	// a scheduled-instance snapshot.
	configCache map[int32]*apigen.DeploymentConfig
	// latestFinalCache retains the last incarnation of an ordinal after it is
	// finalized, so a stopped deployment can still show how its final run ended.
	// At most one entry per ordinal, and only while no live instance supersedes
	// it, so it never competes with the live cache for the same ordinal.
	latestFinalCache map[instanceOrdinalKey]*apigen.ScheduledInstanceState

	configSubs       *pubsubu.PubSub[apigen.DeploymentConfig]
	userSubs         *pubsubu.PubSub[apigen.User]
	backupStatusMu   sync.RWMutex
	backupStatus     apigen.BackupStatus
	backupStatusSubs *pubsubu.PubSub[apigen.BackupStatus]
	secretStatusSubs *pubsubu.PubSub[apigen.SecretsStatusResponse]
	secretMetaSubs   *pubsubu.PubSub[apigen.SecretMeta]
	userConfigSubs   *pubsubu.PubSub[apigen.ConfigMeta]
	valueDirSubs     *pubsubu.PubSub[apigen.ValueDirectory]
	spaceSubs        *pubsubu.PubSub[apigen.Space]
	assetSubs        *pubsubu.PubSub[apigen.AssetMeta]
	assetDirSubs     *pubsubu.PubSub[apigen.AssetDirectory]
	enrollmentSubs   *pubsubu.PubSub[apigen.EnrollmentRequestStatus]
	nodeSubs         *pubsubu.PubSub[apigen.ClusterNode]
	nodeStatusSubs   *pubsubu.PubSub[apigen.ClusterNodeStatus]
	// Carries the storage record rather than the proto, because subscribers have
	// to filter by user id before yielding: an agent session belongs to one
	// operator, unlike everything else broadcast here.
	agentSessionSubs *pubsubu.PubSub[AgentSessionRecord]
}

func Open(dbPath string) *Service {
	s := &Service{
		q:                pq.Open(dbPath),
		configCache:      make(map[int32]*apigen.DeploymentConfig),
		latestFinalCache: make(map[instanceOrdinalKey]*apigen.ScheduledInstanceState),
		configSubs:       &pubsubu.PubSub[apigen.DeploymentConfig]{},
		userSubs:         &pubsubu.PubSub[apigen.User]{},
		backupStatusSubs: &pubsubu.PubSub[apigen.BackupStatus]{},
		secretStatusSubs: &pubsubu.PubSub[apigen.SecretsStatusResponse]{},
		secretMetaSubs:   &pubsubu.PubSub[apigen.SecretMeta]{},
		userConfigSubs:   &pubsubu.PubSub[apigen.ConfigMeta]{},
		valueDirSubs:     &pubsubu.PubSub[apigen.ValueDirectory]{},
		spaceSubs:        &pubsubu.PubSub[apigen.Space]{},
		assetSubs:        &pubsubu.PubSub[apigen.AssetMeta]{},
		assetDirSubs:     &pubsubu.PubSub[apigen.AssetDirectory]{},
		enrollmentSubs:   &pubsubu.PubSub[apigen.EnrollmentRequestStatus]{},
		nodeSubs:         &pubsubu.PubSub[apigen.ClusterNode]{},
		nodeStatusSubs:   &pubsubu.PubSub[apigen.ClusterNodeStatus]{},
		agentSessionSubs: &pubsubu.PubSub[AgentSessionRecord]{},
	}
	s.Cache = instancecache.New(s.persistStatus)
	s.loadCache()
	return s
}

// persistStatus is the instancecache persistence hook: it durably appends a
// status row, panicking on failure per the storage error policy.
func (s *Service) persistStatus(ctx context.Context, st *apigen.ScheduledInstanceStatus) {
	if err := s.q.InsertScheduledInstanceStatus(ctx, scheduledInstanceStatusProtoToInsertParams(st)); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}
}

func (s *Service) Close() error {
	return s.q.Close()
}

// InvalidateNodeRuntimeState clears node-local observations that cannot
// survive restoring the primary database onto a replacement host. Desired
// config remains unchanged; scheduled instance statuses for the node are cleared.
func (s *Service) InvalidateNodeRuntimeState(nodeID int32) (int64, error) {
	if nodeID <= 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()

	count, err := s.q.DeleteScheduledInstanceStatusesForNode(context.Background(), int64(nodeID), int64(OpendeploySpaceID), internaldeploy.SelfName)
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	for _, state := range s.Scheduled {
		if state.Instance.NodeID != nodeID {
			continue
		}
		if internaldeploy.IsSelfConfig(&state.Config) {
			continue
		}
		state.Status = apigen.ScheduledInstanceStatus{}
	}
	return count, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// --- auth: users ---

var ErrNotFound = fmt.Errorf("not found")

func (s *Service) WriteUser(user *apigen.InternalUser) {
	ctx := context.Background()
	if err := s.q.UpsertUser(ctx, pq.UpsertUserParams{
		ID:       int64(user.ID),
		Name:     user.Name,
		DataBlob: user.Encode(),
	}); err != nil {
		panic(fmt.Sprintf("UpsertUser: %v", err))
	}
	s.userSubs.Notify(apigen.User{ID: user.ID, Name: user.Name})
}

func (s *Service) ListUsersPublic() []*apigen.User {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListUsersPublic: %v", err))
	}
	out := make([]*apigen.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, &apigen.User{ID: int32(row.ID), Name: row.Name})
	}
	return out
}

func (s *Service) SubscribeUserUpdates() (*pubsubu.Sub[apigen.User], func()) {
	sub := s.userSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifyBackupStatusUpdate(status apigen.BackupStatus) {
	s.backupStatusMu.Lock()
	s.backupStatus = status
	s.backupStatusMu.Unlock()
	s.backupStatusSubs.Notify(status)
}

func (s *Service) CurrentBackupStatus() apigen.BackupStatus {
	s.backupStatusMu.RLock()
	defer s.backupStatusMu.RUnlock()
	return s.backupStatus
}

func (s *Service) SubscribeBackupStatusUpdates() (*pubsubu.Sub[apigen.BackupStatus], func()) {
	sub := s.backupStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifySecretsStatusUpdate(status apigen.SecretsStatusResponse) {
	s.secretStatusSubs.Notify(status)
}

func (s *Service) SubscribeSecretsStatusUpdates() (*pubsubu.Sub[apigen.SecretsStatusResponse], func()) {
	sub := s.secretStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifySecretMetaUpdate(meta apigen.SecretMeta) {
	s.secretMetaSubs.Notify(meta)
}

func (s *Service) SubscribeSecretMetaUpdates() (*pubsubu.Sub[apigen.SecretMeta], func()) {
	sub := s.secretMetaSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifyConfigMetaUpdate(meta apigen.ConfigMeta) {
	s.userConfigSubs.Notify(meta)
}

func (s *Service) SubscribeConfigMetaUpdates() (*pubsubu.Sub[apigen.ConfigMeta], func()) {
	sub := s.userConfigSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifyValueDirectoryUpdate(dir apigen.ValueDirectory) {
	s.valueDirSubs.Notify(dir)
}

func (s *Service) SubscribeValueDirectoryUpdates() (*pubsubu.Sub[apigen.ValueDirectory], func()) {
	sub := s.valueDirSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) NotifyAssetDirectoryUpdate(dir apigen.AssetDirectory) {
	s.assetDirSubs.Notify(dir)
}

func (s *Service) SubscribeAssetDirectoryUpdates() (*pubsubu.Sub[apigen.AssetDirectory], func()) {
	sub := s.assetDirSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) ListSpaces() []*apigen.Space {
	rows, err := s.q.ListSpaces(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSpaces: %v", err))
	}
	out := make([]*apigen.Space, 0, len(rows))
	for _, row := range rows {
		out = append(out, spaceRowToProto(row))
	}
	return out
}

func (s *Service) CreateSpace(name string) (*apigen.Space, error) {
	row, err := s.q.CreateSpace(context.Background(), name)
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	// Open the new space on every node before announcing it, so nothing can
	// observe a space that exists but is placeable nowhere. A node allows every
	// space that existed when it was enrolled; this keeps that true for spaces
	// created afterwards, and leaves the allow list purely an opt-out tool.
	s.AllowSpaceOnAllNodes(space.ID)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *Service) UpdateSpace(id int32, name string) (*apigen.Space, error) {
	row, err := s.q.UpdateSpace(context.Background(), pq.UpdateSpaceParams{Name: name, ID: int64(id)})
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *Service) DeleteSpace(id int32) error {
	if err := s.q.DeleteSpace(context.Background(), int64(id)); err != nil {
		return err
	}
	// Otherwise ids of spaces that no longer exist accumulate in every node's
	// allow list, and a later space reusing the id would inherit them.
	s.RemoveSpaceFromAllNodes(id)
	s.spaceSubs.Notify(apigen.Space{ID: id, Deleted: true})
	return nil
}

func (s *Service) CountDeploymentsForSpace(id int32) (int64, error) {
	return s.q.CountDeploymentsForSpace(context.Background(), int64(id))
}

func (s *Service) SubscribeSpaceUpdates() (*pubsubu.Sub[apigen.Space], func()) {
	sub := s.spaceSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Service) FetchUserByID(id int32) (*apigen.InternalUser, error) {
	row, err := s.q.GetUser(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(row.DataBlob)
}

func (s *Service) FetchUserMatching(predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		u, err := apigen.DecodeInternalUser(row.DataBlob)
		if err != nil {
			continue
		}
		if predicate(u) {
			return u, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) UpdateUserMatching(predicate func(*apigen.InternalUser) bool, f func(*apigen.InternalUser)) {
	user, err := s.FetchUserMatching(predicate)
	if err != nil {
		panic(fmt.Sprintf("UpdateUserMatching: %v", err))
	}
	f(user)
	s.WriteUser(user)
}

// --- auth: agent sessions ---

// AgentSession is a stored agent session. TokenHash is the SHA-256 of the
// issued token; the plaintext is never persisted. A session that is still a
// pending request has no token at all: TokenHash, TokenPrefix, and ExpiresAt
// stay zero until it is approved and collected.
type AgentSessionRecord struct {
	ID                string
	UserID            int32
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         time.Time
	Scopes            []string
	Status            apigen.AgentSessionStatus
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        time.Time
}

// Collected reports whether this session's token has been minted. Approval on
// its own does not mint one, so this is what separates a session waiting to be
// picked up from a live one.
func (r AgentSessionRecord) Collected() bool { return len(r.TokenHash) > 0 }

func (s *Service) InsertAgentSession(rec AgentSessionRecord) error {
	// A pending request has no token yet, and a nil slice would land as NULL
	// against a NOT NULL column.
	tokenHash := rec.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	err := s.q.InsertAgentSession(context.Background(), pq.InsertAgentSessionParams{
		ID:                rec.ID,
		UserID:            int64(rec.UserID),
		CreatedAt:         rec.CreatedAt.Unix(),
		ExpiresAt:         unixOrZero(rec.ExpiresAt),
		TokenHash:         tokenHash,
		TokenPrefix:       rec.TokenPrefix,
		Scopes:            strings.Join(rec.Scopes, ","),
		Status:            int64(rec.Status),
		RequestingAddress: rec.RequestingAddress,
		ApprovalCode:      rec.ApprovalCode,
		ApprovedAt:        unixOrZero(rec.ApprovedAt),
	})
	if err != nil {
		return err
	}
	s.agentSessionSubs.Notify(rec)
	return nil
}

// FetchAgentSession returns ErrNotFound when no session carries the id, which
// is the normal case for a token minted before this table existed.
func (s *Service) FetchAgentSession(id string) (AgentSessionRecord, error) {
	row, err := s.q.GetAgentSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSessionRecord{}, ErrNotFound
	}
	if err != nil {
		return AgentSessionRecord{}, err
	}
	return agentSessionRowToRecord(row), nil
}

func (s *Service) ListAgentSessionsForUser(userID int32) ([]AgentSessionRecord, error) {
	rows, err := s.q.ListAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]AgentSessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// ListPendingAgentSessionsForUser returns the user's open session requests,
// newest first. There is normally at most one, but stale requests are only
// closed lazily, so a caller has to be prepared for several.
func (s *Service) ListPendingAgentSessionsForUser(userID int32) ([]AgentSessionRecord, error) {
	rows, err := s.q.ListPendingAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]AgentSessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// SetAgentSessionStatus moves a session to a new state. approvedAt and revokedAt
// are written together with it so the row can never claim a state whose
// timestamp is missing.
func (s *Service) SetAgentSessionStatus(id string, status apigen.AgentSessionStatus, approvedAt, revokedAt time.Time) error {
	err := s.q.SetAgentSessionStatus(context.Background(), pq.SetAgentSessionStatusParams{
		Status:     int64(status),
		ApprovedAt: unixOrZero(approvedAt),
		RevokedAt:  unixOrZero(revokedAt),
		ID:         id,
	})
	if err != nil {
		return err
	}
	s.notifyAgentSession(id)
	return nil
}

// ApproveAgentSession approves a pending request and records the approver's
// scopes in the same statement. It reports false when the row was not pending,
// which is how a second approval of the same request is rejected.
func (s *Service) ApproveAgentSession(id string, userID int32, scopes []string, at time.Time) (bool, error) {
	rows, err := s.q.ApproveAgentSession(context.Background(), pq.ApproveAgentSessionParams{
		ApprovedAt: at.Unix(),
		Scopes:     strings.Join(scopes, ","),
		ID:         id,
		UserID:     int64(userID),
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	s.notifyAgentSession(id)
	return true, nil
}

// ClaimAgentSessionToken records a freshly minted token against an approved
// session and reports whether this caller was the one that claimed it. A false
// return means another request got there first; the caller must discard the
// token it minted rather than hand out a second working credential.
func (s *Service) ClaimAgentSessionToken(id string, tokenHash []byte, tokenPrefix string, expiresAt time.Time, scopes []string) (bool, error) {
	rows, err := s.q.ClaimAgentSessionToken(context.Background(), pq.ClaimAgentSessionTokenParams{
		TokenHash:   tokenHash,
		TokenPrefix: tokenPrefix,
		ExpiresAt:   unixOrZero(expiresAt),
		Scopes:      strings.Join(scopes, ","),
		ID:          id,
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	s.notifyAgentSession(id)
	return true, nil
}

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id.
func (s *Service) RevokeAgentSession(id string, userID int32, status apigen.AgentSessionStatus, at time.Time) error {
	err := s.q.RevokeAgentSession(context.Background(), pq.RevokeAgentSessionParams{
		RevokedAt: at.Unix(),
		Status:    int64(status),
		ID:        id,
		UserID:    int64(userID),
	})
	if err != nil {
		return err
	}
	s.notifyAgentSession(id)
	return nil
}

// SubscribeAgentSessionUpdates streams every agent session change. Records are
// not filtered here: subscribers must drop the ones whose UserID is not theirs.
func (s *Service) SubscribeAgentSessionUpdates() (*pubsubu.Sub[AgentSessionRecord], func()) {
	sub := s.agentSessionSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

// notifyAgentSession re-reads the row so subscribers always see the persisted
// state rather than a caller's idea of it. A failed read is dropped: a missed
// update degrades the live list, which the next reconnect repairs, and is not
// worth failing the write that just succeeded.
func (s *Service) notifyAgentSession(id string) {
	rec, err := s.FetchAgentSession(id)
	if err != nil {
		return
	}
	s.agentSessionSubs.Notify(rec)
}

func (s *Service) UserCount() int {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("UserCount: %v", err))
	}
	return len(rows)
}

func (s *Service) FetchLatestOpenDeployConfig() (SystemConfigRevision, error) {
	r, err := s.q.GetLatestConfig(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return SystemConfigRevision{}, ErrNotFound
	}
	return r, err
}

func (s *Service) AppendOpenDeploySettings(blob []byte) (int64, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.q.InsertSystemConfigRevision(context.Background(), time.Now().UnixMilli(), blob)
}

// --- auth: public keys ---
func (s *Service) WritePublicKey(rec *apigen.PublicKeyRecord) {
	ctx := context.Background()
	if err := s.q.UpsertPublicKey(ctx, pq.UpsertPublicKeyParams{
		Kid:      rec.Kid,
		KeyBytes: rec.KeyBytes,
	}); err != nil {
		panic(fmt.Sprintf("UpsertPublicKey: %v", err))
	}
}

func (s *Service) FetchPublicKey(kid string) (*apigen.PublicKeyRecord, error) {
	row, err := s.q.GetPublicKey(context.Background(), kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &apigen.PublicKeyRecord{Kid: row.Kid, KeyBytes: row.KeyBytes}, nil
}
