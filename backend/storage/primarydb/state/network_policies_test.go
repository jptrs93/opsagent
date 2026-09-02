package state

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func testPolicy() *apigen.NetworkPolicy {
	return &apigen.NetworkPolicy{
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 2},
		Destination: &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT, ID: 7},
		Ports:       []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 8080}},
	}
}

func TestNetworkPolicyLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)

	created, err := store.CreateNetworkPolicy(testPolicy(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.Version != 1 || created.Deleted {
		t.Fatalf("created policy = %+v, want id 1 version 1", created)
	}
	if created.Source.Kind != apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE || created.Source.ID != 2 {
		t.Fatalf("created source = %+v", created.Source)
	}

	changed := testPolicy()
	changed.Ports = nil
	updated, err := store.UpdateNetworkPolicy(created.ID, 1, changed, 42)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || len(updated.Ports) != 0 {
		t.Fatalf("updated policy = %+v, want version 2 with no ports", updated)
	}

	if _, err := store.UpdateNetworkPolicy(created.ID, 1, changed, 42); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
	}
	if _, err := store.UpdateNetworkPolicy(99, 1, changed, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update error = %v, want ErrNotFound", err)
	}

	list := store.ListNetworkPolicies()
	if len(list) != 1 || list[0].ID != created.ID || list[0].Version != 2 {
		t.Fatalf("list = %+v, want one policy at version 2", list)
	}

	if err := store.DeleteNetworkPolicy(created.ID, 42); err != nil {
		t.Fatal(err)
	}
	if list := store.ListNetworkPolicies(); len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
	if err := store.DeleteNetworkPolicy(created.ID, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}

	inputs := store.FetchNetworkMapInputs()
	if len(inputs.Policies) != 0 {
		t.Fatalf("map inputs include deleted policy: %+v", inputs.Policies)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	rows, err := db.Query(`SELECT global_seq, event_type, author FROM network_policy_event_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int64
	var lastEventType, lastAuthor int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq, &lastEventType, &lastAuthor); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if len(seqs) != 3 || seqs[0] <= 0 || seqs[1] <= seqs[0] || seqs[2] <= seqs[1] {
		t.Fatalf("event seqs = %v, want three increasing positive values", seqs)
	}
	if lastEventType != 3 || lastAuthor != 42 {
		t.Fatalf("final event type/author = %d/%d, want authored delete event 3/42", lastEventType, lastAuthor)
	}
	var counter int64
	if err := db.QueryRow(`SELECT value FROM global_seq WHERE id = 1`).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if counter != seqs[2] {
		t.Fatalf("global_seq counter = %d, want %d (the delete event's own seq)", counter, seqs[2])
	}
}

func TestNetworkPolicyMapInputsIncludeActivePolicies(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	if _, err := store.CreateNetworkPolicy(testPolicy(), 1); err != nil {
		t.Fatal(err)
	}
	inputs := store.FetchNetworkMapInputs()
	if len(inputs.Policies) != 1 || inputs.Policies[0].ID != 1 {
		t.Fatalf("map input policies = %+v, want the created policy", inputs.Policies)
	}
	if inputs.Seq <= 0 {
		t.Fatalf("map input seq = %d, want positive after policy write", inputs.Seq)
	}
}
