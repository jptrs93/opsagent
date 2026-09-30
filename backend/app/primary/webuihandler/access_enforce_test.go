package webuihandler

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
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
	configService, err := systemconfig.InitializeService(store, apigen.SystemConfig{})
	if err != nil {
		t.Fatalf("systemconfig.InitializeService: %v", err)
	}
	authzService, err := authz.Open(store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	for id, name := range map[int32]string{1: "admin", 2: "spaceop", 3: "viewer"} {
		users.Write(store, &apigen.InternalUser{ID: id, Name: name})
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
			{ArgumentID: 1, Values: []int64{int64(nodes.DefaultSpaceID)}},
		}},
	}); err != nil {
		t.Fatalf("seed space_admin grant: %v", err)
	}
	if _, err := authzService.CreateGrant(&apigen.AuthzGrantRecord{
		UserID: 3,
		Grant: &apigen.AuthzGrant{Rule: &apigen.AuthzRule{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{Include: []int64{int64(nodes.DefaultSpaceID)}},
			EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_CONFIG)}},
			EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
		}},
	}); err != nil {
		t.Fatalf("seed viewer grant: %v", err)
	}
	staging, err := nodes.CreateSpace(store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	h := &Handler{Store: store, Queries: store.Queries(), Secrets: secretManager, SystemConfig: configService, Authz: authzService}
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
	granted, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app.conf", SpaceID: nodes.DefaultSpaceID, Value: "b"})
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

	listed, err := h.PostV1ConfigsList(spaceop)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, item := range listed.Items {
		if item.SpaceID() != nodes.DefaultSpaceID {
			t.Fatalf("space-limited list leaked config %q from space %d", item.Value.Fs.Name, item.SpaceID())
		}
	}
	if len(listed.Items) != 2 {
		t.Fatalf("expected the 2 default-space configs, got %d", len(listed.Items))
	}

	// Entities outside the caller's view read as absent, not forbidden.
	if _, err := h.PostV1ConfigsRename(spaceop, &apigen.ConfigRenameRequest{ConfigID: hidden.ConfigID, NewName: "sneaky.conf"}); !errors.Is(err, UserConfigNotFoundErr) {
		t.Fatalf("rename of hidden config: got %v, want UserConfigNotFoundErr", err)
	}
	if _, err := h.PostV1ConfigsRename(spaceop, &apigen.ConfigRenameRequest{ConfigID: granted.ConfigID, NewName: "renamed.conf"}); err != nil {
		t.Fatalf("rename in granted space: %v", err)
	}
	// A viewable entity without the requested verb reads as forbidden.
	if _, err := h.PostV1ConfigsRename(viewer, &apigen.ConfigRenameRequest{ConfigID: granted.ConfigID, NewName: "viewer.conf"}); !errors.Is(err, AccessDeniedErr) {
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
	if _, err := h.PostV1ConfigsCreate(agent, &apigen.ConfigCreateRequest{Name: "agent.conf", SpaceID: nodes.DefaultSpaceID, Value: "x"}); err != nil {
		t.Fatalf("delegated create in default space: %v", err)
	}

	// Secrets sit in their own delegable rule that grants view and create and
	// nothing else. An operator's secret is therefore visible to the agent, but
	// touching its value comes back forbidden.
	meta, err := h.Secrets.Create("db_password", []byte("hunter2"), 1, nodes.DefaultSpaceID, 0)
	if err != nil {
		t.Fatalf("Secrets.Create: %v", err)
	}
	stored, ok := secrets.Get(h.Store.Queries(), meta.SecretID)
	if !ok || len(statetest.ValueVersions(h.Store, stored)) == 0 {
		t.Fatalf("secret missing after create: %+v", stored)
	}
	agentList, err := h.PostV1SecretsList(agent)
	if err != nil {
		t.Fatalf("delegated list: %v", err)
	}
	if len(agentList.Items) != 1 || agentList.Items[0].SecretID != meta.SecretID {
		t.Fatalf("delegated list should show the operator's secret meta, got %+v", agentList.Items)
	}
	first := statetest.ValueVersions(h.Store, stored)[len(statetest.ValueVersions(h.Store, stored))-1].Ref
	if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{SecretID: first.ID, Version: first.Version}); !errors.Is(err, AccessDeniedErr) {
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
	if _, err := h.PostV1SecretsCreate(agent, &apigen.SecretCreateRequest{Name: "agent_supplied", SpaceID: nodes.DefaultSpaceID, Value: []byte("known")}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated value-supplied create: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1SecretsCreate(admin, &apigen.SecretCreateRequest{Name: "admin_supplied", SpaceID: nodes.DefaultSpaceID, Value: []byte("known")}); err != nil {
		t.Fatalf("admin value-supplied create: %v", err)
	}
	revealed, err := h.PostV1SecretsReveal(admin, &apigen.SecretRevealRequest{SecretID: first.ID, Version: first.Version})
	if err != nil {
		t.Fatalf("admin reveal: %v", err)
	}
	if string(revealed.Value) != "hunter2" {
		t.Fatalf("revealed value = %q", revealed.Value)
	}

	// What the agent can do is mint one, which is a value it never sees either.
	minted, err := h.PostV1SecretsGenerate(agent, &apigen.SecretGenerateRequest{
		Name:     "agent_minted",
		SpaceID:  nodes.DefaultSpaceID,
		Password: &apigen.SecretPasswordSpec{Length: 32},
	})
	if err != nil {
		t.Fatalf("delegated generate: %v", err)
	}
	if len(statetest.ValueVersions(h.Store, minted)) != 1 {
		t.Fatalf("generate returned %d versions, want 1", len(statetest.ValueVersions(h.Store, minted)))
	}
	if _, err := h.PostV1SecretsReveal(agent, &apigen.SecretRevealRequest{SecretID: minted.SecretID, Version: statetest.ValueVersions(h.Store, minted)[0].Version}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("delegated reveal of its own secret: got %v, want AccessDeniedErr", err)
	}
}

// The node's system log (deployment_id = 0) is the opendeploy system
// deployment's log, gated on view_logs for that deployment in the system
// space; a permission on the node entity does not open it.
func TestEnforcementSystemLogIsTheSystemDeploymentsLog(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	deployments.EnsureSystem(h.Store, node.ID, "v1.2.3")
	self := findSystemDeployment(t, h.Store, node.ID)
	if self.Value.SpaceID != 0 {
		t.Fatalf("system deployment lives in space %d, want 0", self.Value.SpaceID)
	}
	grantRule := func(userID int64, entity apigen.AuthzEntity) {
		t.Helper()
		if _, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{
			UserID: userID,
			Grant: &apigen.AuthzGrant{Rule: &apigen.AuthzRule{
				Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW), int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS)}},
				Spaces:      &apigen.AuthzSelector{Include: []int64{0}},
				EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(entity)}},
				EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
			}},
		}); err != nil {
			t.Fatalf("grant %v to user %d: %v", entity, userID, err)
		}
	}
	users.Write(h.Store, &apigen.InternalUser{ID: 4, Name: "deploylogs"})
	users.Write(h.Store, &apigen.InternalUser{ID: 5, Name: "nodelogs"})
	grantRule(4, apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT)
	grantRule(5, apigen.AuthzEntity_AUTHZ_ENTITY_NODE)

	if got, err := h.logQueryTargetNode(enforceCtx(1, false), 0, node.ID, 0); err != nil || got != node.ID {
		t.Fatalf("admin system log: got node %d, %v", got, err)
	}
	if got, err := h.logQueryTargetNode(enforceCtx(4, false), 0, node.ID, 0); err != nil || got != node.ID {
		t.Fatalf("view_logs on system-space deployments: got node %d, %v; want allowed", got, err)
	}
	if _, err := h.logQueryTargetNode(enforceCtx(5, false), 0, node.ID, 0); !errors.Is(err, deployments.NotFoundErr) {
		t.Fatalf("view_logs on nodes only: got %v, want NotFoundErr", err)
	}
	if _, err := h.logQueryTargetNode(enforceCtx(2, false), 0, node.ID, 0); !errors.Is(err, deployments.NotFoundErr) {
		t.Fatalf("space_admin of the default space: got %v, want NotFoundErr", err)
	}
	if _, err := h.logQueryTargetNode(enforceCtx(1, false), 0, node.ID+1, 0); !errors.Is(err, deployments.NotFoundErr) {
		t.Fatalf("node without a system deployment: got %v, want NotFoundErr", err)
	}
	// The deployment's own id resolves to the same node under the same check.
	if got, err := h.logQueryTargetNode(enforceCtx(4, false), self.DeploymentID, 0, 0); err != nil || got != node.ID {
		t.Fatalf("system deployment by id: got node %d, %v", got, err)
	}
}

func TestEnforcementStreamFiltering(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)

	if _, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "staging.conf", SpaceID: staging.ID, Value: "a"}); err != nil {
		t.Fatalf("create in staging: %v", err)
	}
	visible, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app.conf", SpaceID: nodes.DefaultSpaceID, Value: "b"})
	if err != nil {
		t.Fatalf("create in default space: %v", err)
	}

	states, initial := openTestEventStream(t, h, 2, 0)
	if !initial.Reset {
		t.Fatalf("a fresh stream must bootstrap: %+v", initial)
	}
	fold := statetest.Fold(initial.Events)
	if configs := fold[apigen.CoreEntityType_CORE_ENTITY_CONFIG]; len(configs) != 1 || configs[int64(visible.ConfigID)] == nil {
		t.Fatalf("initial configs = %+v, want only the default-space config", configs)
	}
	if fold[apigen.CoreEntityType_CORE_ENTITY_SPACE][int64(staging.ID)] != nil {
		t.Fatal("staging space leaked into a space-limited stream")
	}
	if rules := fold[apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE]; len(rules) != 0 {
		t.Fatalf("global rules leaked to a non-admin: %+v", rules)
	}
	for _, g := range fold[apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT] {
		if g.AuthzGrant.UserID != 2 {
			t.Fatalf("grant for user %d leaked to user 2", g.AuthzGrant.UserID)
		}
	}
	if len(fold[apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE]) == 0 {
		t.Fatal("template catalogue should always be sent")
	}
	if side, err := h.PostV1GlobalEvents(enforceCtx(2, false), &apigen.EventStreamRequest{}); err != nil || side.BackupStatus == nil || *side.BackupStatus != (apigen.BackupStatus{}) {
		t.Fatalf("backup status is cluster-scoped and should be withheld: %+v %v", side, err)
	}

	// A hidden-space update must be dropped; the next visible one still flows.
	if _, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "staging2.conf", SpaceID: staging.ID, Value: "c"}); err != nil {
		t.Fatalf("create in staging: %v", err)
	}
	visible2, err := h.PostV1ConfigsCreate(admin, &apigen.ConfigCreateRequest{Name: "app2.conf", SpaceID: nodes.DefaultSpaceID, Value: "d"})
	if err != nil {
		t.Fatalf("create in default space: %v", err)
	}
	update := recvMsg(t, states)
	configs := mutationsOf(update, apigen.CoreEntityType_CORE_ENTITY_CONFIG)
	if update.Reset || len(update.Events) != 1 || len(configs) != 1 || configs[0].EntityID() != int64(visible2.ConfigID) || update.Seq != visible2.Seq {
		t.Fatalf("update = %+v, want the default-space config update only", update)
	}

	// A grant change re-sends the compacted history so newly visible or
	// hidden items are reconciled.
	if _, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     2,
		TemplateID: authz.ClusterAdminTemplateID,
		Grant:      &apigen.AuthzGrant{},
	}); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	reEmit := recvMsg(t, states)
	if !reEmit.Reset || !reEmit.Synced {
		t.Fatalf("authz change should re-bootstrap with reset, got %+v", reEmit)
	}
	if configs := statetest.Fold(reEmit.Events)[apigen.CoreEntityType_CORE_ENTITY_CONFIG]; len(configs) != 4 {
		t.Fatalf("re-emitted bootstrap has %d configs, want 4 after expanded access", len(configs))
	}
}

func TestEnforcementDerivedNodeVisibility(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)

	node := nodes.EnsurePrimaryNode(h.Store, "secondary", "secondary-id")

	// A new node allows every space, so the space-limited operator sees it
	// through the derived path — no node:view grant exists below cluster_admin.
	if nodes := h.filterNodes(spaceop, nodes.ListClusterNodes(h.Store.Queries())); len(nodes) != 1 || nodes[0].NodeID != node.ID {
		t.Fatalf("space-limited operator should see the node via its allowed spaces, got %+v", nodes)
	}
	agent := enforceCtx(2, true)
	if nodes := h.filterNodes(agent, nodes.ListClusterNodes(h.Store.Queries())); len(nodes) != 1 {
		t.Fatalf("the delegated session should inherit derived node visibility, got %+v", nodes)
	}

	// Seeing a node is not editing it: a visible node without node:edit reads
	// as forbidden, not absent.
	if _, err := h.PostV1NodesRename(spaceop, &apigen.NodeRenameRequest{Identifier: "secondary-id", Name: "sneaky"}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("derived-visible rename: got %v, want AccessDeniedErr", err)
	}

	// Narrowing the node to staging removes it from the operator's world.
	if _, err := h.PostV1NodesAllowedSpaces(admin, &apigen.NodeAllowedSpacesRequest{Identifier: "secondary-id", SpaceIds: []int32{staging.ID}}); err != nil {
		t.Fatalf("narrow allowed spaces: %v", err)
	}
	if nodes := h.filterNodes(spaceop, nodes.ListClusterNodes(h.Store.Queries())); len(nodes) != 0 {
		t.Fatalf("node narrowed to staging should be hidden, got %+v", nodes)
	}
	if statuses := h.filterNodeStatuses(spaceop, erru.Must(h.Queries.ListLatestNodeStatuses(context.Background()))); len(statuses) != 0 {
		t.Fatalf("node statuses should filter with the node, got %+v", statuses)
	}
	if _, err := h.PostV1NodesRename(spaceop, &apigen.NodeRenameRequest{Identifier: "secondary-id", Name: "sneaky"}); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("hidden-node rename: got %v, want NodeNotFoundErr", err)
	}
	if nodes := h.filterNodes(admin, nodes.ListClusterNodes(h.Store.Queries())); len(nodes) != 1 {
		t.Fatalf("cluster_admin keeps explicit node visibility, got %+v", nodes)
	}
}

func TestEnforcementUserRoster(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)

	// The seeded default_user_visibility rule shows the roster to everyone,
	// including agents and users with no grants at all.
	for _, ctx := range []apigen.Context{enforceCtx(3, false), enforceCtx(3, true), enforceCtx(9, false)} {
		if got := h.filterUsers(ctx, users.ListPublic(h.Store.Queries())); len(got) != 3 {
			t.Fatalf("expected the full 3-user roster, got %+v", got)
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
	if got := h.filterUsers(enforceCtx(3, false), users.ListPublic(h.Store.Queries())); len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("without the rule a viewer should see only themself, got %+v", got)
	}
	if got := h.filterUsers(enforceCtx(1, false), users.ListPublic(h.Store.Queries())); len(got) != 3 {
		t.Fatalf("cluster_admin should keep the roster, got %+v", got)
	}
}

func TestEnforcementSystemSpaceValuesAreUnreachable(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	if err := h.Secrets.SetInternal("opendeploy.cluster.ca.key", []byte("ca-key")); err != nil {
		t.Fatalf("SetInternal: %v", err)
	}
	var system *apigen.SecretEvent
	for _, e := range secrets.List(h.Store.Queries()) {
		if e.Value.SpaceID == 0 {
			system = e
		}
	}
	if system == nil {
		t.Fatal("system secret missing from the event log")
	}
	admin := enforceCtx(1, false)
	for _, e := range h.filterSecrets(admin, secrets.List(h.Store.Queries())) {
		if e.Value.SpaceID == 0 {
			t.Fatalf("cluster admin sees a space 0 secret: %+v", e)
		}
	}
	if _, err := h.PostV1SecretsReveal(admin, &apigen.SecretRevealRequest{SecretID: system.SecretID, Version: system.ValueVersion}); err == nil {
		t.Fatal("cluster admin revealed a space 0 secret")
	}
	if err := h.PostV1SecretsDelete(admin, &apigen.SecretDeleteRequest{SecretID: system.SecretID}); err == nil {
		t.Fatal("cluster admin deleted a space 0 secret")
	}
	if _, err := h.PostV1DeploymentsCreate(admin, &apigen.DeploymentCreateRequest{SpaceID: 0, Name: "rogue", Scheduling: apigen.DedicatedScheduling(false, 1)}); err == nil || !strings.Contains(err.Error(), "spaceId must be between 1") {
		t.Fatalf("deployment create in space 0 err = %v, want space 0 rejection", err)
	}
	if got, err := h.Secrets.RevealInternal("opendeploy.cluster.ca.key"); err != nil || string(got) != "ca-key" {
		t.Fatalf("RevealInternal = %q, %v", got, err)
	}
}
