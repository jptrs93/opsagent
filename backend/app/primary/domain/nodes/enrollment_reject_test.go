package nodes

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const testEnrollmentWGKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="

func mustUpsertEnrollmentRequest(t *testing.T, store *state.Service, remoteAddress, opendeployVersion string, reported apigen.NodeReported) (*apigen.EnrollmentRequestStatus, int64) {
	t.Helper()
	req, version, err := UpsertEnrollmentRequest(store, remoteAddress, opendeployVersion, reported)
	if err != nil {
		t.Fatalf("UpsertEnrollmentRequest: %v", err)
	}
	return req, version
}

func TestEnrollmentHelloRejectsEnrolledIdentifiers(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	ctx := context.Background()
	primary := EnsurePrimaryNode(store, "primary", "primary-id")
	before := store.BuildSnapshot(ctx)
	_, _, err := UpsertEnrollmentRequest(store, "192.0.2.9", "v1", apigen.NodeReported{Identifier: primary.Identifier, UnderlayAddress: "192.0.2.9", WgPublicKey: testEnrollmentWGKey})
	if !errors.Is(err, ErrEnrollmentIdentifierEnrolled) {
		t.Fatalf("primary identifier hello = %v, want ErrEnrollmentIdentifierEnrolled", err)
	}
	if !reflect.DeepEqual(before, store.BuildSnapshot(ctx)) {
		t.Fatal("rejected primary hello changed state or sequence")
	}

	reported := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2"}
	req, version := mustUpsertEnrollmentRequest(t, store, "192.0.2.2", "v1", reported)
	if _, err := AcceptEnrollmentRequest(store, req.ID, "worker", reported.Identifier, version); err != nil {
		t.Fatal(err)
	}
	before = store.BuildSnapshot(ctx)
	hijack := apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.3", WgPublicKey: testEnrollmentWGKey}
	if _, _, err := UpsertEnrollmentRequest(store, "192.0.2.3", "v1", hijack); !errors.Is(err, ErrEnrollmentIdentifierEnrolled) {
		t.Fatalf("member hello = %v, want ErrEnrollmentIdentifierEnrolled", err)
	}
	if !reflect.DeepEqual(before, store.BuildSnapshot(ctx)) {
		t.Fatal("rejected member hello changed state or sequence")
	}
	row, err := store.Queries().GetNodeRowByIdentifier(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if row.Event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL || row.Event.Value.EnrollmentRequestedAt != 0 {
		t.Fatalf("member row after rejected hello = %+v", row)
	}
}
