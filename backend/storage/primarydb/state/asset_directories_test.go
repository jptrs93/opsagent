package state

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCreateDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	root, err := store.CreateDirectory(0, 0, "configs", 7)
	if err != nil {
		t.Fatalf("create root directory: %v", err)
	}
	if root.ID == 0 || root.SpaceID != int64(DefaultSpaceID) || root.ParentID != 0 || root.Key != "configs" || root.Author != 7 {
		t.Fatalf("root directory = %+v", root)
	}

	child, err := store.CreateDirectory(0, int32(root.ID), "nginx", 7)
	if err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if child.ParentID != root.ID || child.SpaceID != root.SpaceID {
		t.Fatalf("nested directory = %+v, want parent %d", child, root.ID)
	}

	if _, err := store.CreateDirectory(0, 0, "bad/key", 0); !errors.Is(err, ErrAssetKeyInvalid) {
		t.Fatalf("invalid key err = %v, want ErrAssetKeyInvalid", err)
	}
	if _, err := store.CreateDirectory(0, 0, "configs", 0); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("duplicate directory err = %v, want ErrAssetAlreadyExists", err)
	}
	if _, err := store.CreateDirectory(0, 999, "orphan", 0); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("missing parent err = %v, want ErrDirectoryNotFound", err)
	}

	// The sibling namespace is shared with assets.
	store.SetAssetByKey("app.conf", []byte("x"))
	if _, err := store.CreateDirectory(0, 0, "app.conf", 0); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("directory over asset key err = %v, want ErrAssetAlreadyExists", err)
	}

	// The same key is free under a different parent and in a different space.
	if _, err := store.CreateDirectory(0, int32(root.ID), "configs", 0); err != nil {
		t.Fatalf("same key under other parent: %v", err)
	}
	other, err := store.CreateDirectory(2, 0, "configs", 0)
	if err != nil {
		t.Fatalf("same key in other space: %v", err)
	}
	// A parent from another space does not exist from this space's viewpoint.
	if _, err := store.CreateDirectory(0, int32(other.ID), "sub", 0); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("cross-space parent err = %v, want ErrDirectoryNotFound", err)
	}
}

func TestMoveDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	a, _ := store.CreateDirectory(0, 0, "a", 0)
	b, _ := store.CreateDirectory(0, int32(a.ID), "b", 0)
	c, _ := store.CreateDirectory(0, int32(b.ID), "c", 0)

	// No-op: already under that parent.
	if moved, err := store.MoveDirectory(int32(b.ID), int32(a.ID)); err != nil || moved.ParentID != a.ID {
		t.Fatalf("no-op move = %+v, %v", moved, err)
	}

	// Cycles: into itself, and into its own descendant.
	if _, err := store.MoveDirectory(int32(a.ID), int32(a.ID)); !errors.Is(err, ErrDirectoryCycle) {
		t.Fatalf("move into itself err = %v, want ErrDirectoryCycle", err)
	}
	if _, err := store.MoveDirectory(int32(a.ID), int32(c.ID)); !errors.Is(err, ErrDirectoryCycle) {
		t.Fatalf("move into descendant err = %v, want ErrDirectoryCycle", err)
	}

	if _, err := store.MoveDirectory(999, 0); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("move missing directory err = %v, want ErrDirectoryNotFound", err)
	}
	if _, err := store.MoveDirectory(int32(c.ID), 999); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("move to missing parent err = %v, want ErrDirectoryNotFound", err)
	}

	// Destination key conflicts, against both a directory and an asset.
	if _, err := store.CreateDirectory(0, 0, "c", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveDirectory(int32(c.ID), 0); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("move onto sibling directory err = %v, want ErrAssetAlreadyExists", err)
	}
	v := store.SetAssetByKey("c.txt", []byte("x"))
	asset, err := store.MoveAssetDirectory(v.ID, int32(b.ID))
	if err != nil {
		t.Fatal(err)
	}
	conflict, _ := store.CreateDirectory(0, 0, "c.txt", 0)
	if _, err := store.MoveDirectory(int32(conflict.ID), int32(b.ID)); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("move onto sibling asset err = %v, want ErrAssetAlreadyExists", err)
	}
	_ = asset

	// A real move: b (with its subtree) leaves a for the root.
	movedB, err := store.MoveDirectory(int32(b.ID), 0)
	if err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if movedB.ParentID != 0 {
		t.Fatalf("moved.ParentID = %d, want 0", movedB.ParentID)
	}
	// The old slot is free again and the new one is claimed.
	if _, err := store.CreateDirectory(0, int32(a.ID), "b", 0); err != nil {
		t.Fatalf("recreate at vacated slot: %v", err)
	}
	if _, err := store.CreateDirectory(0, 0, "b", 0); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("create at claimed slot err = %v, want ErrAssetAlreadyExists", err)
	}
	// The subtree moved with it: c is still b's child, so b is not empty.
	if err := store.DeleteDirectory(int32(b.ID)); !errors.Is(err, ErrDirectoryNotEmpty) {
		t.Fatalf("delete moved parent err = %v, want ErrDirectoryNotEmpty", err)
	}
}

func TestDeleteDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	parent, _ := store.CreateDirectory(0, 0, "parent", 0)
	child, _ := store.CreateDirectory(0, int32(parent.ID), "child", 0)

	if err := store.DeleteDirectory(999); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("delete missing err = %v, want ErrDirectoryNotFound", err)
	}
	if err := store.DeleteDirectory(int32(parent.ID)); !errors.Is(err, ErrDirectoryNotEmpty) {
		t.Fatalf("delete with child directory err = %v, want ErrDirectoryNotEmpty", err)
	}

	v := store.SetAssetByKey("f.txt", []byte("x"))
	if _, err := store.MoveAssetDirectory(v.ID, int32(child.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDirectory(int32(child.ID)); !errors.Is(err, ErrDirectoryNotEmpty) {
		t.Fatalf("delete with asset err = %v, want ErrDirectoryNotEmpty", err)
	}

	// Empty it out and both deletes succeed, bottom up.
	if _, err := store.MoveAssetDirectory(v.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDirectory(int32(child.ID)); err != nil {
		t.Fatalf("delete emptied child: %v", err)
	}
	if err := store.DeleteDirectory(int32(parent.ID)); err != nil {
		t.Fatalf("delete emptied parent: %v", err)
	}
	if err := store.DeleteDirectory(int32(parent.ID)); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("second delete err = %v, want ErrDirectoryNotFound", err)
	}
}

func TestMoveAssetDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	dir, _ := store.CreateDirectory(0, 0, "confs", 0)
	v := store.SetAssetByKey("nginx.conf", []byte("events {}\n"))

	moved, err := store.MoveAssetDirectory(v.ID, int32(dir.ID))
	if err != nil {
		t.Fatalf("move asset: %v", err)
	}
	if moved.AssetDirectoryID != dir.ID || moved.Key != "nginx.conf" {
		t.Fatalf("moved asset = %+v", moved)
	}

	// Version rows are untouched by the move.
	versionID := v.LatestContentVersion().ID
	ref, ok := store.GetAssetVersionRef(versionID)
	if !ok || ref.AssetID != v.ID || ref.Key != "nginx.conf" {
		t.Fatalf("version ref after move = %+v ok=%v", ref, ok)
	}
	if joined, ok := store.GetAssetVersionJoined(versionID); !ok || joined.Version.Version != 1 || string(joined.Store.InlineBlob) != "events {}\n" {
		t.Fatalf("version after move = %+v ok=%v", joined, ok)
	}

	// No-op move to the current directory.
	if again, err := store.MoveAssetDirectory(v.ID, int32(dir.ID)); err != nil || again.AssetDirectoryID != dir.ID {
		t.Fatalf("no-op move = %+v, %v", again, err)
	}

	// The root key is free again, and the destination key is now taken.
	v2 := store.SetAssetByKey("nginx.conf", []byte("http {}\n"))
	if v2.ID == v.ID {
		t.Fatal("expected a new asset identity in the root")
	}
	if _, err := store.MoveAssetDirectory(v2.ID, int32(dir.ID)); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("move onto taken key err = %v, want ErrAssetAlreadyExists", err)
	}
	// A directory with the asset's key also blocks the move.
	sub, _ := store.CreateDirectory(0, int32(dir.ID), "sub", 0)
	v3 := store.SetAssetByKey("sub", []byte("x"))
	if _, err := store.MoveAssetDirectory(v3.ID, int32(dir.ID)); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("move onto directory key err = %v, want ErrAssetAlreadyExists", err)
	}
	_ = sub

	if _, err := store.MoveAssetDirectory(v2.ID, 999); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("move to missing directory err = %v, want ErrDirectoryNotFound", err)
	}
	if _, err := store.MoveAssetDirectory(999, int32(dir.ID)); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("move missing asset err = %v, want ErrAssetNotFound", err)
	}

	// A directory in another space is a space move, which is unsupported.
	foreign, _ := store.CreateDirectory(2, 0, "other", 0)
	if _, err := store.MoveAssetDirectory(v2.ID, int32(foreign.ID)); !errors.Is(err, ErrSpaceMoveUnsupported) {
		t.Fatalf("cross-space move err = %v, want ErrSpaceMoveUnsupported", err)
	}
}

func TestMoveAssetSpace(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	v := store.SetAssetByKey("a.txt", []byte("x"))

	if err := store.MoveAssetSpaceLocked(v.ID, DefaultSpaceID, 0, 1); err != nil {
		t.Fatalf("same-space no-op err = %v", err)
	}
	if err := store.MoveAssetSpaceLocked(999, 2, 0, 1); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
	}

	if err := store.MoveAssetSpaceLocked(v.ID, 2, 0, 1); err != nil {
		t.Fatalf("space move: %v", err)
	}
	asset, ok := store.GetAsset(v.ID)
	if !ok || asset.SpaceID() != 2 || asset.Fs.DirectoryID != 0 {
		t.Fatalf("asset after move = %+v ok=%v", asset, ok)
	}
	// Version rows are untouched: the pinned version id still resolves.
	if _, ok := store.GetAssetVersionRef(v.LatestContentVersion().ID); !ok {
		t.Fatalf("version %d missing after move", v.LatestContentVersion().ID)
	}

	// The vacated key is reusable at the origin, and the occupied one blocks a
	// move back.
	dup := store.SetAssetByKey("a.txt", []byte("y"))
	if err := store.MoveAssetSpaceLocked(v.ID, DefaultSpaceID, 0, 1); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("move onto taken key err = %v, want ErrAssetAlreadyExists", err)
	}

	// A destination directory must live in the destination space; the origin's
	// directory reads as absent there.
	originDir, _ := store.CreateDirectory(int32(DefaultSpaceID), 0, "app", 0)
	if err := store.MoveAssetSpaceLocked(dup.ID, 2, int32(originDir.ID), 1); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("foreign destination dir err = %v, want ErrDirectoryNotFound", err)
	}
	destDir, _ := store.CreateDirectory(2, 0, "app", 0)
	if err := store.MoveAssetSpaceLocked(dup.ID, 2, int32(destDir.ID), 1); err != nil {
		t.Fatalf("space move into directory: %v", err)
	}
	asset, ok = store.GetAsset(dup.ID)
	if !ok || asset.SpaceID() != 2 || asset.Fs.DirectoryID != int32(destDir.ID) {
		t.Fatalf("asset after directory move = %+v ok=%v", asset, ok)
	}
}

func TestRenameAssetDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	dir, err := store.CreateDirectory(0, 0, "configs", 0)
	if err != nil {
		t.Fatalf("create directory: %v", err)
	}

	renamed, err := store.RenameDirectory(int32(dir.ID), "conf")
	if err != nil {
		t.Fatalf("RenameDirectory: %v", err)
	}
	if renamed.Key != "conf" || renamed.ID != dir.ID || renamed.ParentID != dir.ParentID {
		t.Fatalf("renamed = %+v, want key conf in place", renamed)
	}

	// Renaming to the current key is a no-op, not a collision with itself.
	if _, err := store.RenameDirectory(int32(dir.ID), "conf"); err != nil {
		t.Fatalf("same-key rename err = %v", err)
	}

	// The sibling namespace spans assets too.
	store.SetAssetByKey("app.conf", []byte("x"))
	if _, err := store.RenameDirectory(int32(dir.ID), "app.conf"); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("rename onto asset key err = %v, want ErrAssetAlreadyExists", err)
	}
	if _, err := store.RenameDirectory(int32(dir.ID), "bad/key"); !errors.Is(err, ErrAssetKeyInvalid) {
		t.Fatalf("invalid key err = %v, want ErrAssetKeyInvalid", err)
	}
	if _, err := store.RenameDirectory(999, "x"); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("missing directory err = %v, want ErrDirectoryNotFound", err)
	}
}

func TestListAssetDirectoriesAndCreateInDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	dir, err := store.CreateDirectory(0, 0, "bundles", 3)
	if err != nil {
		t.Fatalf("create directory: %v", err)
	}

	listed := store.ListAssetDirectories()
	if len(listed) != 1 || listed[0].ID != int32(dir.ID) || listed[0].Key != "bundles" || listed[0].Author != 3 {
		t.Fatalf("ListAssetDirectories = %+v, want the one created", listed)
	}
	if meta, ok := store.GetAssetDirectoryMeta(int32(dir.ID)); !ok || meta.Key != "bundles" {
		t.Fatalf("GetAssetDirectoryMeta = %+v, %v", meta, ok)
	}

	v, err := store.CreateAssetWithVersion("app.tar", DefaultSpaceID, int32(dir.ID), 0, store.MustPutInlineAssetContent([]byte("x")), 1)
	if err != nil {
		t.Fatalf("create asset in directory: %v", err)
	}
	if a, ok := store.GetAssetRow(v.ID); !ok || a.AssetDirectoryID != dir.ID {
		t.Fatalf("asset row = %+v, want directory %d", a, dir.ID)
	}
	// The sibling check happens at the target directory, not the root.
	if _, err := store.CreateAssetWithVersion("app.tar", DefaultSpaceID, 0, 0, store.MustPutInlineAssetContent([]byte("x")), 1); err != nil {
		t.Fatalf("same key at root: %v", err)
	}
	if _, err := store.CreateAssetWithVersion("app.tar", DefaultSpaceID, int32(dir.ID), 0, store.MustPutInlineAssetContent([]byte("x")), 1); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("duplicate in directory err = %v, want ErrAssetAlreadyExists", err)
	}
	// Missing and cross-space directories do not exist for a create.
	if _, err := store.CreateAssetWithVersion("b.txt", DefaultSpaceID, 999, 0, store.MustPutInlineAssetContent([]byte("x")), 1); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("create into missing directory err = %v, want ErrDirectoryNotFound", err)
	}
	foreign, _ := store.CreateDirectory(2, 0, "other", 0)
	if _, err := store.CreateAssetWithVersion("b.txt", DefaultSpaceID, int32(foreign.ID), 0, store.MustPutInlineAssetContent([]byte("x")), 1); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("create into foreign directory err = %v, want ErrDirectoryNotFound", err)
	}
}
