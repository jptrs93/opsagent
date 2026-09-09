package assets

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestCreateAssetNotifiesSubscribers(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	s := &Store{DB: store}

	sub, unsub := store.SubscribeUpdates()
	defer unsub()

	if _, err := s.CreateAsset(context.Background(), "notify-check.txt", 1, 0, 1, []byte("hello")); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	select {
	case update := <-sub:
		if len(update.AssetEvents) != 1 {
			t.Fatalf("asset transaction = %+v", update)
		}
		asset := update.AssetEvents[0]
		if asset.Value.Fs == nil || asset.Value.Fs.Key != "notify-check.txt" {
			t.Fatalf("asset.Value.Fs = %+v", asset.Value.Fs)
		}
		if asset.EventID == 0 || asset.ValueVersion != 1 {
			t.Fatalf("asset = %+v, want a first content version with an id", asset)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no asset update was published within 2s of CreateAsset")
	}
}
