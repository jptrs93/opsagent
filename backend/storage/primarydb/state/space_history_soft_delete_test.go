package state

import (
	"path/filepath"
	"testing"
)

func TestSpaceAssignmentHistory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	sha := store.MustPutInlineAssetContent([]byte("content"))
	v, err := store.CreateAssetWithVersion("app.conf", DefaultSpaceID, 0, 5, sha, 7)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := store.MoveAssetSpaceLocked(v.ID, 2, 0, 9); err != nil {
		t.Fatalf("move asset space: %v", err)
	}
	// A directory-only move through the space endpoint must not log.
	if err := store.MoveAssetSpaceLocked(v.ID, 2, 0, 9); err != nil {
		t.Fatalf("repeat asset space move: %v", err)
	}
	moved, ok := store.GetAsset(v.ID)
	if !ok {
		t.Fatal("asset not found after move")
	}
	if len(moved.SpaceVersions) != 2 {
		t.Fatalf("asset space log rows = %d, want 2", len(moved.SpaceVersions))
	}
	if moved.SpaceVersions[1].SpaceID != DefaultSpaceID || moved.SpaceVersions[1].Author != 5 {
		t.Fatalf("initial assignment = space %d author %d, want space %d author 5",
			moved.SpaceVersions[1].SpaceID, moved.SpaceVersions[1].Author, DefaultSpaceID)
	}
	if moved.SpaceVersions[0].SpaceID != 2 || moved.SpaceVersions[0].Author != 9 {
		t.Fatalf("move row = space %d author %d, want space 2 author 9",
			moved.SpaceVersions[0].SpaceID, moved.SpaceVersions[0].Author)
	}
	if a, ok := store.GetAssetRow(v.ID); !ok || a.SpaceID != 2 {
		t.Fatalf("asset row after move = %+v ok=%v, want current space 2", a, ok)
	}

	sec, err := store.CreateSecretWithVersionLocked("token", DefaultSpaceID, 0, 5, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := store.MoveSecretSpaceLocked(sec.SecretID, 2, 0, 9); err != nil {
		t.Fatalf("move secret space: %v", err)
	}
	movedSec, ok := store.GetSecret(sec.SecretID)
	if !ok {
		t.Fatal("secret not found after move")
	}
	if len(movedSec.SpaceVersions) != 2 ||
		movedSec.SpaceVersions[1].SpaceID != DefaultSpaceID || movedSec.SpaceVersions[1].Author != 5 ||
		movedSec.SpaceVersions[0].SpaceID != 2 || movedSec.SpaceVersions[0].Author != 9 {
		t.Fatalf("secret space log = %+v, want initial space %d author 5 then space 2 author 9", movedSec.SpaceVersions, DefaultSpaceID)
	}

	cfg, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 5, "on")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if err := store.MoveConfigSpaceLocked(cfg.ID, 2, 0, 9); err != nil {
		t.Fatalf("move config space: %v", err)
	}
	movedCfg, ok := store.GetConfig(cfg.ID)
	if !ok {
		t.Fatal("config not found after move")
	}
	if len(movedCfg.SpaceVersions) != 2 ||
		movedCfg.SpaceVersions[1].SpaceID != DefaultSpaceID || movedCfg.SpaceVersions[1].Author != 5 ||
		movedCfg.SpaceVersions[0].SpaceID != 2 || movedCfg.SpaceVersions[0].Author != 9 {
		t.Fatalf("config space log = %+v, want initial space %d author 5 then space 2 author 9", movedCfg.SpaceVersions, DefaultSpaceID)
	}
	if c, ok := store.GetConfig(cfg.ID); !ok || c.SpaceID() != 2 {
		t.Fatalf("config after move = %+v ok=%v, want current space 2", c, ok)
	}
}

func TestSoftDeleteHidesRowAndFreesName(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	v := store.SetAssetByKey("app.conf", []byte("v1"))
	store.DeleteAssetLocked(v.ID)
	if _, ok := store.GetAssetRow(v.ID); ok {
		t.Fatal("deleted asset still resolves by id")
	}
	if _, ok := getAssetInRootByKey(store, DefaultSpaceID, "app.conf"); ok {
		t.Fatal("deleted asset still resolves by key")
	}
	if got := store.ListAssets(); len(got) != 0 {
		t.Fatalf("ListAssets after delete = %d items, want 0", len(got))
	}
	// The name is reusable, and the old asset's version rows survive.
	replacement := store.SetAssetByKey("app.conf", []byte("v2"))
	if replacement.ID == v.ID {
		t.Fatal("recreated asset reused the deleted identity")
	}
	if versions := store.ListAssetVersionsJoinedOfAsset(v.ID); len(versions) != 1 {
		t.Fatalf("deleted asset version rows = %d, want 1 retained", len(versions))
	}

	sec, err := store.CreateSecretWithVersionLocked("token", DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := store.DeleteSecretLocked(sec.SecretID); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	if _, ok := store.GetSecretInRootByName(DefaultSpaceID, "token"); ok {
		t.Fatal("deleted secret still resolves by name")
	}
	if got := store.ListSecretVersionRecords(); len(got) != 0 {
		t.Fatalf("deleted secret leaked %d records into the manager load", len(got))
	}
	if _, err := store.CreateSecretWithVersionLocked("token", DefaultSpaceID, 0, 1, testSealFunc(2)); err != nil {
		t.Fatalf("recreate secret with freed name: %v", err)
	}

	cfg, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 1, "on")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if c, ok := store.DeleteConfigLocked(cfg.ID); !ok || c.DeletedAt.IsZero() {
		t.Fatalf("delete config = %+v ok=%v", c, ok)
	}
	if _, ok := store.GetConfig(cfg.ID); ok {
		t.Fatal("deleted config still resolves by id")
	}
	if got := store.ListConfigs(); len(got) != 0 {
		t.Fatalf("ListConfigs after delete = %d items, want 0", len(got))
	}
	recreated, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 1, "off")
	if err != nil {
		t.Fatalf("recreate config with freed name: %v", err)
	}
	if recreated.ID == cfg.ID {
		t.Fatal("recreated config reused the deleted identity")
	}
	// Pinned version rows of the deleted config still resolve.
	if ref, ok := store.GetConfigVersionByID(cfg.ValueVersions[0].ID); !ok || ref.Value != "on" {
		t.Fatalf("pinned version of deleted config = %+v ok=%v", ref, ok)
	}
}
