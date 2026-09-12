package nodes

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func testNode(store *state.Service, identifier string) *Node {
	return EnsurePrimaryNode(store, identifier, identifier)
}

func TestEnsurePrimaryNodeCreatesPrimaryRole(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))

	EnsurePrimaryNode(store, "primary", "primary-id")

	nodes := ListNodes(store.Queries())
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %+v", len(nodes), nodes)
	}
	node := nodes[0]
	if node.Name != "primary" || node.Identifier != "primary-id" {
		t.Fatalf("node identity = name %q identifier %q, want primary/primary-id", node.Name, node.Identifier)
	}
	if node.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("primary status = %v, want member normal", node.Status)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRolePrimary {
		t.Fatalf("primary roles = %+v, want primary", node.Roles)
	}
}

func TestEnsurePrimaryNodeUsesCertificateIdentifier(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	defer store.Close()
	seedDB := sqlitedb.MustOpen(dbPath)
	for i, seed := range []struct{ name, roles string }{
		{"coflip-prod", "[1]"},
		{"primary", "[0]"},
	} {
		if _, err := seedDB.Exec(`
			INSERT INTO node_event_log (global_seq, event_time, created_time, author, node_id, version,
				name, identifier, enrolled_time, status, roles, addresses, wg_public_key, allowed_spaces, event_type)
			VALUES (0, 0, 0, 0, ?1, 1, ?2, ?2, 0, 4, ?3, '[]', '', '[0]', 1)`, i+1, seed.name, seed.roles); err != nil {
			t.Fatalf("seed node %s: %v", seed.name, err)
		}
	}
	if err := seedDB.Close(); err != nil {
		t.Fatal(err)
	}

	node := EnsurePrimaryNode(store, "primary", "primary")
	if node.Name != "primary" || node.Identifier != "primary" {
		t.Fatalf("primary node = %+v, want primary certificate identity", node)
	}
}

func TestAcceptEnrollmentRequestCreatesNode(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	const wgPublicKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	req, expectedVersion := mustUpsertEnrollmentRequest(t, store, "127.0.0.1", "v0.0.200", apigen.NodeReported{Identifier: "requesting-id", UnderlayAddress: "10.0.0.2", WgPublicKey: wgPublicKey})
	if req.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED || !req.IsConnected {
		t.Fatalf("request = %+v, want connected enrollment-requested", req)
	}
	if req.RequestingIpAddress != "127.0.0.1" || req.OpendeployVersion != "v0.0.200" {
		t.Fatalf("request observed meta = %+v, want 127.0.0.1/v0.0.200", req)
	}
	if members := ListNodes(store.Queries()); len(members) != 0 {
		t.Fatalf("requested node listed as member: %+v", members)
	}

	status, err := AcceptEnrollmentRequest(store, req.ID, "secondary-1", req.RequestingMachineID, expectedVersion)
	if err != nil {
		t.Fatalf("AcceptEnrollmentRequest: %v", err)
	}
	if status.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("status = %v, want member normal", status.Status)
	}
	if status.UnderlayAddress != "10.0.0.2" {
		t.Fatalf("underlay address = %q, want 10.0.0.2", status.UnderlayAddress)
	}
	nodes := ListNodes(store.Queries())
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %+v", len(nodes), nodes)
	}
	node := nodes[0]
	if node.Name != "secondary-1" || node.Identifier != "requesting-id" {
		t.Fatalf("node identity = name %q identifier %q, want secondary-1/requesting-id", node.Name, node.Identifier)
	}
	if node.ID != req.ID {
		t.Fatalf("node id = %d, want request id %d", node.ID, req.ID)
	}
	if node.EnrolledAt.IsZero() || node.CreatedAt.IsZero() {
		t.Fatalf("node timestamps = %+v, want created and enrolled set", node)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRoleSecondary {
		t.Fatalf("node roles = %+v, want secondary", node.Roles)
	}
	if len(node.Addresses) != 1 || node.Addresses[0] != "10.0.0.2" {
		t.Fatalf("node addresses = %v, want [10.0.0.2]", node.Addresses)
	}
	if node.WGPublicKey != wgPublicKey {
		t.Fatalf("node wg public key = %q, want %q", node.WGPublicKey, wgPublicKey)
	}
}

func TestSetNodeWGPublicKeyIsDiffGatedAndVersioned(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := EnsurePrimaryNode(store, "primary", "primary-id")

	const keyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	const keyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="

	before := FetchNetworkMapInputs(store).Seq
	updated := ReportNode(store, node.Identifier, apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: node.Reported().UnderlayAddress, WgPublicKey: keyA, HostAddresses: node.HostAddresses})
	if updated.WGPublicKey != keyA {
		t.Fatalf("node wg key = %q, want %q", updated.WGPublicKey, keyA)
	}
	afterSet := FetchNetworkMapInputs(store).Seq
	if afterSet != before+1 {
		t.Fatalf("seq after set = %d, want %d", afterSet, before+1)
	}

	ReportNode(store, node.Identifier, apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: node.Reported().UnderlayAddress, WgPublicKey: keyA, HostAddresses: node.HostAddresses})
	if seq := FetchNetworkMapInputs(store).Seq; seq != afterSet {
		t.Fatalf("unchanged key advanced seq to %d", seq)
	}

	ReportNode(store, node.Identifier, apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: node.Reported().UnderlayAddress, WgPublicKey: keyB, HostAddresses: node.HostAddresses})
	if seq := FetchNetworkMapInputs(store).Seq; seq != afterSet+1 {
		t.Fatalf("seq after change = %d, want %d", seq, afterSet+1)
	}

}

func TestAcceptEnrollmentRequestRejectsReplacedSessionRevision(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	first, firstVersion := mustUpsertEnrollmentRequest(t, store, "127.0.0.1", "v1", apigen.NodeReported{Identifier: "requesting-id", UnderlayAddress: "10.0.0.2", WgPublicKey: ""})
	second, secondVersion := mustUpsertEnrollmentRequest(t, store, "127.0.0.2", "v2", apigen.NodeReported{Identifier: "requesting-id", UnderlayAddress: "10.0.0.3", WgPublicKey: ""})
	if second.ID != first.ID {
		t.Fatalf("replacement request id = %d, want %d", second.ID, first.ID)
	}
	if secondVersion <= firstVersion {
		t.Fatalf("replacement revision %d did not advance past %d", secondVersion, firstVersion)
	}
	_, err := AcceptEnrollmentRequest(store, first.ID, "secondary-1", first.RequestingMachineID, firstVersion)
	if !errors.Is(err, ErrEnrollmentRequestChanged) {
		t.Fatalf("accept error = %v, want ErrEnrollmentRequestChanged", err)
	}
	if nodes := ListNodes(store.Queries()); len(nodes) != 0 {
		t.Fatalf("stale enrollment created nodes: %+v", nodes)
	}
}

func TestRenameNodePreservesIdentifier(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primaryNode := EnsurePrimaryNode(store, "primary", "primary-id")
	statetest.MustCreateDeploymentForNode(store, apigen.Context{}, internaldeploy.SpaceID, internaldeploy.SelfName, primaryNode.ID, statetest.SpecWithState("v1", true))

	node, err := RenameNode(store, "primary-id", "control plane")
	if err != nil {
		t.Fatalf("RenameNode: %v", err)
	}
	if node.Value.Operator.Name != "control plane" || node.Value.Reported.Identifier != "primary-id" {
		t.Fatalf("renamed node = %+v", node)
	}
	configs := erru.Must(store.Queries().ListActiveDeployments(context.Background()))
	if len(configs) == 0 || configs[0].Value.NodeID != primaryNode.ID {
		t.Fatalf("deployment targets after rename = %+v", configs)
	}
}

func TestSpaceAndNodeChangesPublishTogether(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	EnsurePrimaryNode(s, "one", "one")
	EnsurePrimaryNode(s, "two", "two")
	before := s.BuildSnapshot(context.Background()).Seq
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	space, err := CreateSpace(s, "new")
	if err != nil {
		t.Fatal(err)
	}
	update := <-sub
	if update.Seq != before+1 || len(update.Spaces) != 1 || update.Spaces[0].ID != space.ID || len(update.NodeEvents) != 2 {
		t.Fatalf("space transaction: %+v", update)
	}
	for _, node := range update.NodeEvents {
		if node.Seq != update.Seq {
			t.Fatal("node used separate seq")
		}
	}
}
