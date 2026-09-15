package clusterhandler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func acceptTestSecondary(t *testing.T, store *state.Service, identifier string) *nodes.Node {
	t.Helper()
	req, version, err := nodes.UpsertEnrollmentRequest(store, "127.0.0.1", "v0.0.1", apigen.NodeReported{Identifier: identifier, UnderlayAddress: "10.0.0.2", WgPublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.AcceptEnrollmentRequest(store, req.ID, identifier, req.RequestingMachineID, version); err != nil {
		t.Fatal(err)
	}
	for _, member := range nodes.ListNodes(store.Queries()) {
		if member.Identifier == identifier {
			return member
		}
	}
	t.Fatalf("node %q not listed", identifier)
	return nil
}

func TestEvictedNodeIsForbiddenAndItsSessionEnds(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	nodes.EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptTestSecondary(t, store, "secondary-id")
	handler := New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil, nil)
	peerCtx := context.WithValue(context.Background(), machineCtxKey{}, node.Identifier)
	if _, err := handler.requireScheduledInstancePredicate(peerCtx); err != nil {
		t.Fatalf("member predicate: %v", err)
	}

	watchCtx, stopWatch := context.WithCancel(context.Background())
	t.Cleanup(stopWatch)
	go handler.RunEvictionWatch(watchCtx)
	sessCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess := newSession(sessCtx, cancel, node.ID, node.Identifier, scheduledInstancePredicateForNode(node.ID), store, nil)
	handler.registerSession(node.ID, node.Identifier, sess)
	time.Sleep(50 * time.Millisecond)

	if _, err := nodes.EvictNode(apigen.Context{Ctx: context.Background()}, store, node.Identifier, node.Version, true); err != nil {
		t.Fatalf("EvictNode: %v", err)
	}
	select {
	case msg := <-sess.outbox:
		if !msg.Evicted {
			t.Fatalf("session frame = %+v, want evicted", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("evicted node's session never received the evicted frame")
	}
	if _, err := handler.requireScheduledInstancePredicate(peerCtx); !errors.Is(err, clusterForbiddenErr) {
		t.Fatalf("evicted predicate: got %v, want clusterForbiddenErr", err)
	}
	if !nodes.IsEvictedIdentifier(store.Queries(), node.Identifier) {
		t.Fatal("identifier should classify as evicted for the connect rejection")
	}
}
