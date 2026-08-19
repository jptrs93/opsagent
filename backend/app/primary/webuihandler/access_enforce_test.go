package webuihandler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// newEnforcementTestHandler wires enough of a Handler for authz enforcement to
// be live, with three users: 1 = cluster_admin, 2 = space_admin of the default
// space, 3 = view-only on configs in the default space. A "staging" space is
// created so tests have somewhere users 2 and 3 cannot see.
func newEnforcementTestHandler(t *testing.T) (*Handler, *apigen.Space) {
	t.Helper()
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	t.Cleanup(func() { store.Close() })
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	configService, err := config.InitializeService(store, apigen.PrimaryConfig{})
	if err != nil {
		t.Fatalf("config.InitializeService: %v", err)
	}
	authzService, err := authz.Open(store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	for id, name := range map[int32]string{1: "admin", 2: "spaceop", 3: "viewer"} {
		store.WriteUser(&apigen.InternalUser{ID: id, Name: name})
	}
	if _, err := authzService.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     1,
		TemplateID: authz.ClusterAdminTemplateID,
		Grant:      &apigen.AuthzGrant{},
	}); err != nil {
		t.Fatalf("seed admin grant: %v", err)
	}
	if _, err := authzService.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     2,
		TemplateID: authz.SpaceAdminTemplateID,
		Grant: &apigen.AuthzGrant{Args: []*apigen.AuthzArgumentBinding{
			{ArgumentID: 1, Values: []int64{int64(state.DefaultSpaceID)}},
		}},
	}); err != nil {
		t.Fatalf("seed space_admin grant: %v", err)
	}
	if _, err := authzService.CreateGrant(&apigen.AuthzGrantRecord{
		UserID: 3,
		Grant: &apigen.AuthzGrant{Rule: &apigen.AuthzRule{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{Include: []int64{int64(state.DefaultSpaceID)}},
			EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_CONFIG)}},
			EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
		}},
	}); err != nil {
		t.Fatalf("seed viewer grant: %v", err)
	}
	staging, err := store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	h := &Handler{Store: store, Secrets: secretManager, ConfigService: configService, Authz: authzService}
	return h, staging
}

func enforceCtx(userID int32, delegated bool) apigen.Context {
	return apigen.Context{Ctx: context.Background(), User: &apigen.InternalUser{ID: userID, Delegated: delegated}}
}

func TestEnforcementConfigsBySpace(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin, spaceop, viewer := enforceCtx(1, false), enforceCtx(2, false), enforceCtx(3, false)

	hidden, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "staging.conf", SpaceID: staging.ID, Value: "a"})
	if err != nil {
		t.Fatalf("admin create in staging: %v", err)
	}
	granted, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app.conf", SpaceID: state.DefaultSpaceID, Value: "b"})
	if err != nil {
		t.Fatalf("admin create in default space: %v", err)
	}

	if _, err := h.PostV1ConfigsCreate(spaceop, &apigen.ConfigCreateRequest{Name: "denied.conf", SpaceID: staging.ID, Value: "x"}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("space-limited create in staging: got %v, want AccessDeniedErr", err)
	}
	// Space id 0 normalizes to the default space for values, so the check must
	// pass for a user whose rights cover the effective space.
	if _, err := h.PostV1ConfigsCreate(spaceop, &apigen.ConfigCreateRequest{Name: "allowed.conf", SpaceID: 0, Value: "y"}); err != nil {
		t.Fatalf("space-limited create with space id 0: %v", err)
	}

	listed, err := h.PostV1ConfigsList(spaceop, &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, item := range listed.Items {
		if item.SpaceID() != state.DefaultSpaceID {
			t.Fatalf("space-limited list leaked config %q from space %d", item.Fs.Name, item.SpaceID())
		}
	}
	if len(listed.Items) != 2 {
		t.Fatalf("expected the 2 default-space configs, got %d", len(listed.Items))
	}

	// Entities outside the caller's view read as absent, not forbidden.
	if _, err := h.PostV1ConfigsRename(spaceop, &apigen.ConfigRenameRequest{ConfigID: hidden.ID, NewName: "sneaky.conf"}); !errors.Is(err, UserConfigNotFoundErr) {
		t.Fatalf("rename of hidden config: got %v, want UserConfigNotFoundErr", err)
	}
	if _, err := h.PostV1ConfigsRename(spaceop, &apigen.ConfigRenameRequest{ConfigID: granted.ID, NewName: "renamed.conf"}); err != nil {
		t.Fatalf("rename in granted space: %v", err)
	}
	// A viewable entity without the requested verb reads as forbidden.
	if _, err := h.PostV1ConfigsRename(viewer, &apigen.ConfigRenameRequest{ConfigID: granted.ID, NewName: "viewer.conf"}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("view-only rename: got %v, want AccessDeniedErr", err)
	}
}

func TestEnforcementSpaces(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)

	if _, err := h.PostV1SpacesCreate(spaceop, &apigen.SpaceSetRequest{Name: "rogue"}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("space-limited space create: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1SpacesUpdate(spaceop, &apigen.SpaceSetRequest{ID: staging.ID, Name: "hidden"}); !errors.Is(err, SpaceNotFoundErr) {
		t.Fatalf("update of hidden space: got %v, want SpaceNotFoundErr", err)
	}
	if err := h.PostV1SpacesDelete(spaceop, &apigen.SpaceDeleteRequest{ID: staging.ID}); !errors.Is(err, SpaceNotFoundErr) {
		t.Fatalf("delete of hidden space: got %v, want SpaceNotFoundErr", err)
	}
	if _, err := h.PostV1SpacesUpdate(admin, &apigen.SpaceSetRequest{ID: staging.ID, Name: "staging2"}); err != nil {
		t.Fatalf("admin space update: %v", err)
	}
}

func TestEnforcementDelegated(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	admin, agent := enforceCtx(1, false), enforceCtx(1, true)

	// cluster_admin's delegable rule excludes space 0 entirely, so cluster-level
	// operations are human-only even for a fully privileged agent.
	if _, err := h.PostV1ClusterSettingsUpdate(agent, &apigen.ClusterSettings{}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated cluster settings update: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1ConfigsCreate(agent, &apigen.ConfigCreateRequest{Name: "agent.conf", SpaceID: state.DefaultSpaceID, Value: "x"}); err != nil {
		t.Fatalf("delegated create in default space: %v", err)
	}

	// Secrets sit in their own delegable rule that grants view and create and
	// nothing else. An operator's secret is therefore visible to the agent, but
	// touching its value comes back forbidden.
	meta, err := h.Secrets.Create("db_password", []byte("hunter2"), 1, state.DefaultSpaceID, 0)
	if err != nil {
		t.Fatalf("Secrets.Create: %v", err)
	}
	stored, ok := h.Store.GetSecret(meta.SecretID)
	if !ok || len(stored.Versions) == 0 {
		t.Fatalf("secret missing after create: %+v", stored)
	}
	agentList, err := h.PostV1SecretsList(agent, &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("delegated list: %v", err)
	}
	if len(agentList.Items) != 1 || agentList.Items[0].ID != meta.SecretID {
		t.Fatalf("delegated list should show the operator's secret meta, got %+v", agentList.Items)
	}
	versionID := stored.Versions[len(stored.Versions)-1].ID
	if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{ID: versionID}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated reveal: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1SecretsSet(agent, &apigen.SecretSetRequest{SecretID: meta.SecretID, Value: []byte("x")}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated set: got %v, want AccessDeniedErr", err)
	}
	if err := h.PostV1SecretsDelete(agent, &apigen.SecretDeleteRequest{SecretID: meta.SecretID}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated delete: got %v, want AccessDeniedErr", err)
	}
	// Supplying the value on create is a read in disguise — it needs reveal on
	// top of create, which the delegable rule withholds.
	if _, err := h.PostV1SecretsCreate(agent, &apigen.SecretCreateRequest{Name: "agent_supplied", SpaceID: state.DefaultSpaceID, Value: []byte("known")}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated value-supplied create: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1SecretsCreate(admin, &apigen.SecretCreateRequest{Name: "admin_supplied", SpaceID: state.DefaultSpaceID, Value: []byte("known")}); err != nil {
		t.Fatalf("admin value-supplied create: %v", err)
	}
	revealed, err := h.PostV1SecretsReveal(admin, &apigen.SecretRevealRequest{ID: versionID})
	if err != nil {
		t.Fatalf("admin reveal: %v", err)
	}
	if string(revealed.Value) != "hunter2" {
		t.Fatalf("revealed value = %q", revealed.Value)
	}

	// What the agent can do is mint one, which is a value it never sees either.
	minted, err := h.PostV1SecretsGenerate(agent, &apigen.SecretGenerateRequest{
		Name:     "agent_minted",
		SpaceID:  state.DefaultSpaceID,
		Password: &apigen.SecretPasswordSpec{Length: 32},
	})
	if err != nil {
		t.Fatalf("delegated generate: %v", err)
	}
	if len(minted.Versions) != 1 {
		t.Fatalf("generate returned %d versions, want 1", len(minted.Versions))
	}
	if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{ID: minted.Versions[0].ID}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated reveal of its own secret: got %v, want AccessDeniedErr", err)
	}
}

func recvState(t *testing.T, states <-chan *apigen.State) *apigen.State {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s, ok := <-states:
			if !ok {
				t.Fatal("stream closed unexpectedly")
			}
			if s.Heartbeat {
				continue
			}
			return s
		case <-deadline:
			t.Fatal("timed out waiting for a state message")
		}
	}
}

func TestEnforcementStreamFiltering(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)

	if _, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "staging.conf", SpaceID: staging.ID, Value: "a"}); err != nil {
		t.Fatalf("create in staging: %v", err)
	}
	visible, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app.conf", SpaceID: state.DefaultSpaceID, Value: "b"})
	if err != nil {
		t.Fatalf("create in default space: %v", err)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states := make(chan *apigen.State, 32)
	go func() {
		defer close(states)
		for s, streamErr := range h.PostV1GlobalStateStream(apigen.Context{Ctx: streamCtx, User: &apigen.InternalUser{ID: 2}}) {
			if streamErr != nil {
				return
			}
			states <- s
		}
	}()

	initial := recvState(t, states)
	if initial.UserConfigValuesSnapshot == nil || len(initial.UserConfigValuesSnapshot.Items) != 1 ||
		initial.UserConfigValuesSnapshot.Items[0].ID != visible.ID {
		t.Fatalf("initial config snapshot = %+v, want only the default-space config", initial.UserConfigValuesSnapshot)
	}
	for _, space := range initial.SpacesSnapshot.Items {
		if space.ID == staging.ID {
			t.Fatal("staging space leaked into a space-limited stream")
		}
	}
	if len(initial.AuthzGlobalRulesSnapshot.Items) != 0 {
		t.Fatalf("global rules leaked to a non-admin: %+v", initial.AuthzGlobalRulesSnapshot.Items)
	}
	for _, g := range initial.AuthzGrantsSnapshot.Items {
		if g.UserID != 2 {
			t.Fatalf("grant for user %d leaked to user 2", g.UserID)
		}
	}
	if len(initial.AuthzRuleTemplatesSnapshot.Items) == 0 {
		t.Fatal("template catalogue should always be sent")
	}
	if initial.BackupStatusSnapshot != nil {
		t.Fatal("backup status is cluster-scoped and should be withheld")
	}

	// A hidden-space update must be dropped; the next visible one still flows.
	if _, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "staging2.conf", SpaceID: staging.ID, Value: "c"}); err != nil {
		t.Fatalf("create in staging: %v", err)
	}
	visible2, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app2.conf", SpaceID: state.DefaultSpaceID, Value: "d"})
	if err != nil {
		t.Fatalf("create in default space: %v", err)
	}
	update := recvState(t, states)
	if update.UserConfigValueUpdate == nil || update.UserConfigValueUpdate.ID != visible2.ID {
		t.Fatalf("update = %+v, want the default-space config update only", update)
	}

	// A grant change re-emits the full filtered snapshot so newly visible or
	// hidden items are reconciled.
	if _, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     9,
		TemplateID: authz.ClusterAdminTemplateID,
		Grant:      &apigen.AuthzGrant{},
	}); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	reEmit := recvState(t, states)
	if reEmit.UserConfigValuesSnapshot == nil || reEmit.SpacesSnapshot == nil || reEmit.AuthzGrantsSnapshot == nil {
		t.Fatalf("authz change should re-emit full snapshots, got %+v", reEmit)
	}
	if len(reEmit.UserConfigValuesSnapshot.Items) != 2 {
		t.Fatalf("re-emitted config snapshot has %d items, want 2", len(reEmit.UserConfigValuesSnapshot.Items))
	}
}

func TestEnforcementDerivedNodeVisibility(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)

	node := h.Store.EnsurePrimaryNode("worker", "worker-id")

	// A new node allows every space, so the space-limited operator sees it
	// through the derived path — no node:view grant exists below cluster_admin.
	if nodes := h.filterNodes(spaceop, h.Store.ListClusterNodes()); len(nodes) != 1 || nodes[0].ID != node.ID {
		t.Fatalf("space-limited operator should see the node via its allowed spaces, got %+v", nodes)
	}
	agent := enforceCtx(2, true)
	if nodes := h.filterNodes(agent, h.Store.ListClusterNodes()); len(nodes) != 1 {
		t.Fatalf("the delegated session should inherit derived node visibility, got %+v", nodes)
	}

	// Seeing a node is not editing it: a visible node without node:edit reads
	// as forbidden, not absent.
	if _, err := h.PostV1NodesRename(spaceop, &apigen.NodeRenameRequest{Identifier: "worker-id", Name: "sneaky"}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("derived-visible rename: got %v, want AccessDeniedErr", err)
	}

	// Narrowing the node to staging removes it from the operator's world.
	if _, err := h.PostV1NodesAllowedSpaces(admin, &apigen.NodeAllowedSpacesRequest{Identifier: "worker-id", SpaceIds: []int32{staging.ID}}); err != nil {
		t.Fatalf("narrow allowed spaces: %v", err)
	}
	if nodes := h.filterNodes(spaceop, h.Store.ListClusterNodes()); len(nodes) != 0 {
		t.Fatalf("node narrowed to staging should be hidden, got %+v", nodes)
	}
	if statuses := h.filterNodeStatuses(spaceop, h.Store.ListNodeStatuses()); len(statuses) != 0 {
		t.Fatalf("node statuses should filter with the node, got %+v", statuses)
	}
	if _, err := h.PostV1NodesRename(spaceop, &apigen.NodeRenameRequest{Identifier: "worker-id", Name: "sneaky"}); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("hidden-node rename: got %v, want NodeNotFoundErr", err)
	}
	if nodes := h.filterNodes(admin, h.Store.ListClusterNodes()); len(nodes) != 1 {
		t.Fatalf("cluster_admin keeps explicit node visibility, got %+v", nodes)
	}
}

func TestEnforcementUserRoster(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)

	// The seeded default_user_visibility rule shows the roster to everyone,
	// including agents and users with no grants at all.
	for _, ctx := range []apigen.Context{enforceCtx(3, false), enforceCtx(3, true), enforceCtx(9, false)} {
		if users := h.filterUsers(ctx, h.Store.ListUsersPublic()); len(users) != 3 {
			t.Fatalf("expected the full 3-user roster, got %+v", users)
		}
	}

	// Deleting the seeded rule closes the roster to everyone but admins.
	for _, rule := range h.Authz.GlobalRules() {
		if rule.Name == authz.DefaultUserVisibilityRuleName {
			if err := h.Authz.DeleteGlobalRule(rule.ID); err != nil {
				t.Fatalf("DeleteGlobalRule: %v", err)
			}
		}
	}
	if users := h.filterUsers(enforceCtx(3, false), h.Store.ListUsersPublic()); len(users) != 1 || users[0].ID != 3 {
		t.Fatalf("without the rule a viewer should see only themself, got %+v", users)
	}
	if users := h.filterUsers(enforceCtx(1, false), h.Store.ListUsersPublic()); len(users) != 3 {
		t.Fatalf("cluster_admin should keep the roster, got %+v", users)
	}
}
