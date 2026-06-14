package sqlite

import (
	"path/filepath"
	"testing"
)

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))

	v1 := store.SetAsset("nginx.conf", "nginx", []byte("events {}\n"))
	if v1.ID == 0 || v1.Version != 1 {
		t.Fatalf("first asset = id %d version %d, want nonzero id version 1", v1.ID, v1.Version)
	}
	v2 := store.SetAsset("nginx.conf", "nginx", []byte("events {}\nhttp {}\n"))
	if v2.ID == 0 || v2.ID == v1.ID || v2.Version != 2 {
		t.Fatalf("second asset = id %d version %d, want new id version 2", v2.ID, v2.Version)
	}

	latest, ok := store.GetAsset("nginx.conf", 0)
	if !ok {
		t.Fatal("latest asset not found")
	}
	if latest.ID != v2.ID || latest.Version != 2 || string(latest.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("latest asset = v%d %q", latest.Version, latest.Blob)
	}
	byID, ok := store.GetAssetByIDVersion(v2.ID, v2.Version)
	if !ok || byID.Key != "nginx.conf" || string(byID.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("asset by id/version = %+v ok=%v", byID, ok)
	}

	old, ok := store.GetAsset("nginx.conf", 1)
	if !ok {
		t.Fatal("version 1 asset not found")
	}
	if old.Version != 1 || string(old.Blob) != "events {}\n" {
		t.Fatalf("old asset = v%d %q", old.Version, old.Blob)
	}

	items := store.ListAssets()
	if len(items) != 1 {
		t.Fatalf("asset list length = %d, want 1", len(items))
	}
	if items[0].ID != v2.ID || items[0].Key != "nginx.conf" || items[0].Version != 2 || items[0].SizeBytes != int32(len("events {}\nhttp {}\n")) {
		t.Fatalf("asset meta = %+v", items[0])
	}

	store.DeleteAsset("nginx.conf")
	if _, ok := store.GetAsset("nginx.conf", 0); ok {
		t.Fatal("asset still found after delete")
	}
}
