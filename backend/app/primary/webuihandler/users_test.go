package webuihandler

import (
	"context"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestUserWritersPublishSequencedPersistedState(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func() {
		t.Helper()
		update := <-sub
		statetest.AssertUpdateMatchesRows(t, s, update)
		snapshot := s.BuildSnapshot(context.Background())
		if len(update.Users) != 1 || !reflect.DeepEqual(update.Users, snapshot.Users) {
			t.Fatal("user publication differs from persisted public state")
		}
	}
	users.Write(s, &apigen.InternalUser{ID: 1, Name: "user"})
	check()
	users.TouchLastLogin(s, 1)
	check()
	users.UpdateMatching(s, func(u *apigen.InternalUser) bool { return u.ID == 1 }, func(u *apigen.InternalUser) { u.Name = "renamed" })
	check()
	if user, err := users.ByID(s.Queries(), 1); err != nil || user.Name != "renamed" {
		t.Fatalf("user after update = %+v, %v", user, err)
	}
}
