package nodes

import (
	"context"
	"net/netip"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

var testUnderlay = netip.MustParseAddr("10.0.0.1")

func mustAddr(s string) apigen.IpAddress {
	return apigen.AddrOf(netip.MustParseAddr(s))
}

func addrs(items ...string) []apigen.IpAddress {
	out := make([]apigen.IpAddress, 0, len(items))
	for _, item := range items {
		out = append(out, mustAddr(item))
	}
	return out
}

func addrStrings(items []apigen.IpAddress) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.String())
	}
	return out
}

func globalSeq(t testing.TB, store *state.Service) int64 {
	t.Helper()
	return erru.Must(store.Queries().GetGlobalSeq(context.Background()))
}

func fingerprint(t testing.TB, store *state.Service) []byte {
	t.Helper()
	out := []byte{byte(globalSeq(t, store))}
	for _, entry := range statetest.Snapshot(t, store.Queries()) {
		out = append(out, entry.Encode()...)
	}
	return out
}

func latestNodeEvent(t testing.TB, store *state.Service, identifier string) pq.NodeEvent {
	t.Helper()
	row, err := store.Queries().GetNodeRowByIdentifier(context.Background(), identifier)
	if err != nil {
		t.Fatalf("GetNodeRowByIdentifier(%q): %v", identifier, err)
	}
	return row.Event
}

func mutationsOf(update state.WriteUpdate, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for i := range update.Mutations {
		if m := &update.Mutations[i]; m.Type() == typ {
			out = append(out, m)
		}
	}
	return out
}
