package nodes

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestUnknownHostInventoryPreservesLastReport(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2", HostAddresses: []string{"203.0.113.2"}}
	req, _ := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	before := globalSeq(t, store)
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
	mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", *wire)
	if node = ReportNode(store, reported.Identifier, *wire); nodeVersion(t, store, node.ID) != 1 || node.ID != req.ID {
		t.Fatal("unknown inventory changed a pending enrollment")
	}
	wire.HostAddressesUnknown = false
	node = ReportNode(store, reported.Identifier, *wire)
	if len(node.HostAddresses) != 0 || nodeVersion(t, store, node.ID) != 2 {
		t.Fatal("known empty inventory did not clear old addresses")
	}
}

func TestEnrollmentCleanupGuardsRequestInsteadOfNodeVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2"}
	req, _ := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	at := req.CreatedAt.UnixMilli()
	reported.HostAddresses = []string{"203.0.113.2"}
	ReportNode(store, reported.Identifier, reported)
	if err := EndEnrollmentRequest(store, req.ID, at, false); err != nil {
		t.Fatal(err)
	}
	if node := latestNodeEvent(t, store, reported.Identifier); node.Value.EnrollmentRequestedAt != 0 || nodeVersion(t, store, node.NodeID) != 3 {
		t.Fatal("an interleaved node event prevented cancellation")
	}
	fresh, _ := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	if fresh.CreatedAt.UnixMilli() <= at {
		t.Fatal("new request reused the old timestamp")
	}
	if err := EndEnrollmentRequest(store, req.ID, at, true); err != nil {
		t.Fatal(err)
	}
	if node := latestNodeEvent(t, store, reported.Identifier); node.Value.EnrollmentRequestedAt != fresh.CreatedAt.UnixMilli() {
		t.Fatal("old session expired a newer request")
	}
}

func TestEnrollmentReportsHaveNoTrailingEvents(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2", HostAddresses: []string{"203.0.113.2"}}
	req, version := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	if version != globalSeq(t, store) || nodeVersion(t, store, latestNodeEvent(t, store, reported.Identifier).NodeID) != 1 {
		t.Fatalf("request seq = %d, want the seq of the first node event", version)
	}
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	hello := ReportNode(store, reported.Identifier, reported)
	if v := nodeVersion(t, store, hello.ID); v != 2 {
		t.Fatalf("first cluster hello version = %d, want 2", v)
	}
	if node := ReportNode(store, reported.Identifier, reported); nodeVersion(t, store, node.ID) != 2 {
		t.Fatal("reconnect appended an event")
	}
	reported.HostAddresses = []string{"203.0.113.3", "203.0.113.2", "203.0.113.2"}
	node := ReportNode(store, reported.Identifier, reported)
	if v := nodeVersion(t, store, node.ID); v != 3 {
		t.Fatalf("address change version = %d, want 3", v)
	}
	reported.HostAddresses = []string{"203.0.113.2", "203.0.113.3"}
	if node := ReportNode(store, reported.Identifier, reported); nodeVersion(t, store, node.ID) != 3 {
		t.Fatal("equivalent address set appended an event")
	}
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	before := fingerprint(t, store)
	if _, _, err := UpsertEnrollmentRequest(store, "192.0.2.2", "v2", reported); !errors.Is(err, ErrEnrollmentIdentifierEnrolled) {
		t.Fatalf("member hello = %v, want ErrEnrollmentIdentifierEnrolled", err)
	}
	if !bytes.Equal(before, fingerprint(t, store)) {
		t.Fatal("rejected member hello changed state or sequence")
	}
	select {
	case <-sub:
		t.Fatal("rejected member hello published")
	default:
	}
	if node := ReportNode(store, reported.Identifier, reported); nodeVersion(t, store, node.ID) != 3 || node.EnrollmentRequestedAt != 0 {
		t.Fatalf("member after rejected hello = %+v", node)
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
	req, version := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	node := latestNodeEvent(t, store, reported.Identifier)
	if node.Value.EnrollmentRequestedAt != 0 || node.Value.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED {
		t.Fatalf("cancelled node: %+v", node)
	}
	req, version = mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), true); err != nil {
		t.Fatal(err)
	}
	node = latestNodeEvent(t, store, reported.Identifier)
	if node.Value.EnrollmentRequestedAt != 0 || node.Value.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED {
		t.Fatalf("expired node: %+v", node)
	}
	req, version = mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	requestedVersion := nodeVersion(t, store, latestNodeEvent(t, store, reported.Identifier).NodeID)
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	accepted := latestNodeEvent(t, store, reported.Identifier)
	acceptedVersion := nodeVersion(t, store, accepted.NodeID)
	if acceptedVersion != requestedVersion+1 {
		t.Fatalf("accept appended %d versions, want one", acceptedVersion-requestedVersion)
	}
	if err := EndEnrollmentRequest(store, req.ID, req.CreatedAt.UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	if node = latestNodeEvent(t, store, reported.Identifier); nodeVersion(t, store, node.NodeID) != acceptedVersion {
		t.Fatal("accepted session cleanup appended a trailing event")
	}
	if _, _, err := UpsertEnrollmentRequest(store, "192.0.2.2", "v1", reported); !errors.Is(err, ErrEnrollmentIdentifierEnrolled) {
		t.Fatalf("member hello = %v, want ErrEnrollmentIdentifierEnrolled", err)
	}
	node = latestNodeEvent(t, store, reported.Identifier)
	if node.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL || node.Value.EnrollmentRequestedAt != 0 || nodeVersion(t, store, node.NodeID) != acceptedVersion {
		t.Fatal("rejected member hello changed admitted node lifecycle")
	}
}

// nodeVersion counts the node's authored writes in the log: the version the
// node row used to carry.
func nodeVersion(t testing.TB, store *state.Service, id int32) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	for _, e := range erru.Must(store.Queries().WriteEventsInRange(ctx, -1, erru.Must(store.Queries().GetGlobalSeq(ctx)))) {
		for _, m := range e.Mutations {
			if m.Type() == apigen.CoreEntityType_CORE_ENTITY_NODE && m.EntityID() == int64(id) {
				count++
			}
		}
	}
	return count
}
