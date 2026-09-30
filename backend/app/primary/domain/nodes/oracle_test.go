package nodes

import (
	"context"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func globalSeq(t testing.TB, store *state.Service) int64 {
	t.Helper()
	return erru.Must(store.Queries().GetGlobalSeq(context.Background()))
}

func fingerprint(t testing.TB, store *state.Service) []byte {
	t.Helper()
	out := []byte{byte(globalSeq(t, store))}
	for _, event := range statetest.Bootstrap(t, store.Queries()) {
		out = append(out, statetest.Canonical(*event)...)
	}
	return out
}

func latestNodeEvent(t testing.TB, store *state.Service, identifier string) apigen.NodeEvent {
	t.Helper()
	row, err := store.Queries().GetNodeRowByIdentifier(context.Background(), identifier)
	if err != nil {
		t.Fatalf("GetNodeRowByIdentifier(%q): %v", identifier, err)
	}
	return row.Event
}

func mutationsOf(update state.Update, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for _, m := range update.Mutations {
		if m.Type() == typ {
			out = append(out, m)
		}
	}
	return out
}
