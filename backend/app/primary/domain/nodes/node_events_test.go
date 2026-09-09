package nodes

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestUnknownHostInventoryPreservesLastReport(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2", HostAddresses: []string{"203.0.113.2"}}
	req, _ := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	before := store.BuildSnapshot(context.Background()).Seq
	reported.HostAddresses = nil
	reported.HostAddressesUnknown = true
	wire, err := apigen.DecodeNodeReported(reported.Encode())
	if err != nil {
		t.Fatal(err)
	}
	node := ReportNode(store, reported.Identifier, *wire)
	if !reflect.DeepEqual(node.HostAddresses, []string{"203.0.113.2"}) || node.Seq != before {
		t.Fatal("unknown inventory cleared addresses or appended an event")
	}
	UpsertEnrollmentRequest(store, "192.0.2.2", "v1", *wire)
	if node = ReportNode(store, reported.Identifier, *wire); node.Version != 1 || node.ID != req.ID {
		t.Fatal("unknown inventory changed a pending enrollment")
	}
	wire.HostAddressesUnknown = false
	node = ReportNode(store, reported.Identifier, *wire)
	if len(node.HostAddresses) != 0 || node.Version != 2 {
		t.Fatal("known empty inventory did not clear old addresses")
	}
}

func TestEnrollmentCleanupGuardsRequestInsteadOfNodeVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2"}
	req, _ := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	at := req.CreatedAt.UnixMilli()
	reported.HostAddresses = []string{"203.0.113.2"}
	ReportNode(store, reported.Identifier, reported)
	if err := EndEnrollmentRequest(store, req.ID, at, false); err != nil {
		t.Fatal(err)
	}
	if node := store.BuildSnapshot(context.Background()).NodeEvents[0]; node.Value.EnrollmentRequestedAt != 0 || node.Version != 3 {
		t.Fatal("an interleaved node event prevented cancellation")
	}
	fresh, _ := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if fresh.CreatedAt.UnixMilli() <= at {
		t.Fatal("new request reused the old timestamp")
	}
	if err := EndEnrollmentRequest(store, req.ID, at, true); err != nil {
		t.Fatal(err)
	}
	if node := store.BuildSnapshot(context.Background()).NodeEvents[0]; node.Value.EnrollmentRequestedAt != fresh.CreatedAt.UnixMilli() {
		t.Fatal("old session expired a newer request")
	}
}

func TestEnrollmentReportsHaveNoTrailingEvents(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2", HostAddresses: []string{"203.0.113.2"}}
	req, version := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if version != 1 {
		t.Fatalf("request version = %d, want 1", version)
	}
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	hello := ReportNode(store, reported.Identifier, reported)
	if hello.Version != 2 {
		t.Fatalf("first cluster hello version = %d, want 2", hello.Version)
	}
	if node := ReportNode(store, reported.Identifier, reported); node.Version != 2 {
		t.Fatal("reconnect appended an event")
	}
	reported.HostAddresses = []string{"203.0.113.3", "203.0.113.2", "203.0.113.2"}
	node := ReportNode(store, reported.Identifier, reported)
	if node.Version != 3 {
		t.Fatalf("address change version = %d, want 3", node.Version)
	}
	reported.HostAddresses = []string{"203.0.113.2", "203.0.113.3"}
	if node := ReportNode(store, reported.Identifier, reported); node.Version != 3 {
		t.Fatal("equivalent address set appended an event")
	}
	_, version = UpsertEnrollmentRequest(store, "192.0.2.2", "v2", reported)
	if version != 4 {
		t.Fatalf("member re-enrollment version = %d, want 4", version)
	}
	row, err := store.Queries().GetNodeRowByIdentifier(context.Background(), reported.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if int64(row.Event.Value.Status) != int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL) ||
		row.Event.Value.EnrollmentRequestedAt == 0 {
		t.Fatalf("pending member row = %+v", row)
	}
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	before := store.BuildSnapshot(context.Background())
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, 3); !errors.Is(err, ErrEnrollmentRequestChanged) {
		t.Fatalf("stale accept = %v", err)
	}
	if !reflect.DeepEqual(before, store.BuildSnapshot(context.Background())) {
		t.Fatal("stale enrollment changed state or sequence")
	}
	select {
	case <-sub:
		t.Fatal("stale enrollment published")
	default:
	}
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	statetest.AssertUpdateMatchesRows(t, store, <-sub)
	if node := ReportNode(store, reported.Identifier, reported); node.Version != 5 || node.EnrollmentRequestedAt != 0 {
		t.Fatalf("accepted member = %+v", node)
	}
}

func TestNodeObservedClockMonotonic(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := EnsurePrimaryNode(store, "primary", "primary")
	seq := FetchNetworkMapInputs(store).Seq
	SetNodeStatusByIdentifier(store, node.Identifier, true, node.CreatedAt)
	first := erru.Must(store.Queries().ListLatestNodeStatuses(context.Background()))[0]
	SetNodeStatusByIdentifier(store, node.Identifier, false, node.CreatedAt)
	second := erru.Must(store.Queries().ListLatestNodeStatuses(context.Background()))[0]
	if !second.UpdatedAt.After(first.UpdatedAt) || first.UpdatedAt.IsZero() {
		t.Fatalf("node clocks: %v, %v", first.UpdatedAt, second.UpdatedAt)
	}
	if FetchNetworkMapInputs(store).Seq != seq+2 {
		t.Fatal("observed writes did not each consume one sequence")
	}
}

func TestEnrollmentCancellationExpiryAndAcceptedSessionCleanup(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2"}
	req, version := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	node := store.BuildSnapshot(context.Background()).NodeEvents[0]
	if node.Value.EnrollmentRequestedAt != 0 || node.Value.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED {
		t.Fatalf("cancelled node: %+v", node)
	}
	req, version = UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), true); err != nil {
		t.Fatal(err)
	}
	node = store.BuildSnapshot(context.Background()).NodeEvents[0]
	if node.Value.EnrollmentRequestedAt != 0 || node.Value.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED {
		t.Fatalf("expired node: %+v", node)
	}
	req, version = UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	node = store.BuildSnapshot(context.Background()).NodeEvents[0]
	if node.Version != int32(version+1) {
		t.Fatal("accepted session cleanup appended a trailing event")
	}
	req, version = UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported)
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), true); err != nil {
		t.Fatal(err)
	}
	node = store.BuildSnapshot(context.Background()).NodeEvents[0]
	if node.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL || node.Value.EnrollmentRequestedAt != 0 {
		t.Fatal("expiry changed admitted node lifecycle")
	}
}
