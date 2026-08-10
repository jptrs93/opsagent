package assetstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb"
)

func TestCreateAssetNotifiesSubscribers(t *testing.T) {
	store := primarydb.Open(filepath.Join(t.TempDir(), "primary.db"))
	s := &Store{DB: store}

	sub, unsub := store.SubscribeAssetUpdates()
	defer unsub()

	if _, err := s.CreateAsset(context.Background(), "notify-check.txt", 1, 1, []byte("hello")); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	select {
	case meta := <-sub.Ch:
		if meta.Key != "notify-check.txt" {
			t.Fatalf("meta.Key = %q", meta.Key)
		}
		if len(meta.VersionRefs) == 0 || meta.VersionRefs[0].ID == 0 {
			t.Fatalf("meta.VersionRefs = %+v, want a first version ref with an id", meta.VersionRefs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no asset update was published within 2s of CreateAsset")
	}
}
