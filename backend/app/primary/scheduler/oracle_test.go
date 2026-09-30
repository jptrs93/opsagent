package scheduler

import (
	"context"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const (
	deploymentType     = apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT
	instanceType       = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE
	instanceStatusType = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS
	nodeStatusType     = apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS
)

func globalSeq(t testing.TB, store *state.Service) int64 {
	t.Helper()
	return erru.Must(store.Queries().GetGlobalSeq(context.Background()))
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

func hasCore(update state.Update) bool {
	return update.Has(deploymentType) || update.Has(instanceType)
}

func hasObserved(update state.Update) bool {
	return update.Has(instanceStatusType) || update.Has(nodeStatusType)
}

func fingerprint(t testing.TB, store *state.Service) []byte {
	t.Helper()
	out := []byte{byte(globalSeq(t, store))}
	for _, event := range statetest.Bootstrap(t, store.Queries()) {
		out = append(out, statetest.Canonical(*event)...)
	}
	return out
}

func liveEntity(t testing.TB, store *state.Service, typ apigen.CoreEntityType, id int32) *apigen.CoreEntity {
	t.Helper()
	entity := statetest.Live(t, store.Queries(), typ)[int64(id)]
	if entity == nil {
		t.Fatalf("bootstrap has no live %v %d", typ, id)
	}
	return entity
}
