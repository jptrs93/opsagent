package assets

import (
	"context"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
)

func TestCreateAssetNotifiesSubscribers(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	s := &Store{DB: openTestStore(t), Config: func() *apigen.ClusterSettings { return settings }, Loader: testLoader{}}
	store := s.DB

	sub, unsub := store.SubscribeUpdates()
	defer unsub()

	if _, err := s.CreateAsset(context.Background(), "notify-check.txt", 1, 0, 1, []byte("hello")); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	select {
	case update := <-sub:
		if len(update.Mutations) != 1 || update.Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_ASSET || update.Mutations[0].Kind() != apigen.AuthzVerb_AUTHZ_VERB_CREATE {
			t.Fatalf("asset transaction = %+v", update)
		}
		asset := update.Mutations[0].Entity().Asset
		if asset.Fs == nil || asset.Fs.Key != "notify-check.txt" {
			t.Fatalf("asset.Fs = %+v", asset.Fs)
		}
		if update.Mutations[0].EntityID() == 0 || update.Mutations[0].Meta() == nil || update.Mutations[0].Meta().ValueVersion != 1 {
			t.Fatalf("asset = %+v, want a first content version with an id", asset)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no asset update was published within 2s of CreateAsset")
	}
}
