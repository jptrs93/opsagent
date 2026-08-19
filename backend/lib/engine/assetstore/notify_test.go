package assetstore

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

	sub, unsub := store.SubscribeAssetUpdates()
	defer unsub()

	if _, err := s.CreateAsset(context.Background(), "notify-check.txt", 1, 0, 1, []byte("hello")); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	select {
	case asset := <-sub.Ch:
		if asset.Fs == nil || asset.Fs.Key != "notify-check.txt" {
			t.Fatalf("asset.Fs = %+v", asset.Fs)
		}
		if len(asset.ContentVersions) == 0 || asset.ContentVersions[0].ID == 0 {
			t.Fatalf("asset.ContentVersions = %+v, want a first content version with an id", asset.ContentVersions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no asset update was published within 2s of CreateAsset")
	}
}
