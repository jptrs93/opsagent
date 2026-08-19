package state

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCreateValueDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	root, err := store.CreateValueDirectory(0, 0, "db", 7)
	if err != nil {
		t.Fatalf("create root directory: %v", err)
	}
	if root.ID == 0 || root.SpaceID != int64(DefaultSpaceID) || root.ParentID != 0 || root.Name != "db" || root.Author != 7 {
		t.Fatalf("root directory = %+v", root)
	}

	child, err := store.CreateValueDirectory(0, int32(root.ID), "postgres", 7)
	if err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if child.ParentID != root.ID || child.SpaceID != root.SpaceID {
		t.Fatalf("nested directory = %+v, want parent %d", child, root.ID)
	}

	if _, err := store.CreateValueDirectory(0, 0, "bad/name", 0); !errors.Is(err, ErrValueNameInvalid) {
		t.Fatalf("invalid name err = %v, want ErrValueNameInvalid", err)
	}
	if _, err := store.CreateValueDirectory(0, 0, "db", 0); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("duplicate directory err = %v, want ErrValueAlreadyExists", err)
	}
	if _, err := store.CreateValueDirectory(0, 999, "orphan", 0); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("missing parent err = %v, want ErrValueDirectoryNotFound", err)
	}

	// The sibling namespace spans all three tables: a secret and a config each
	// block a directory of the same name.
	if _, err := store.CreateSecretWithVersion("api-token", DefaultSpaceID, 0, 0, testSealFunc(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateValueDirectory(0, 0, "api-token", 0); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("directory over secret name err = %v, want ErrValueAlreadyExists", err)
	}
	if _, err := store.CreateConfigWithVersion("log-level", DefaultSpaceID, 0, 0, "info"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateValueDirectory(0, 0, "log-level", 0); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("directory over config name err = %v, want ErrValueAlreadyExists", err)
	}
	// And a directory blocks a secret or config of the same name.
	if _, err := store.CreateSecretWithVersion("db", DefaultSpaceID, 0, 0, testSealFunc(1)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("secret over directory name err = %v, want ErrValueAlreadyExists", err)
	}
	if _, err := store.CreateConfigWithVersion("db", DefaultSpaceID, 0, 0, "x"); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("config over directory name err = %v, want ErrValueAlreadyExists", err)
	}

	// The same name is free under a different parent and in a different space.
	if _, err := store.CreateValueDirectory(0, int32(root.ID), "db", 0); err != nil {
		t.Fatalf("same name under other parent: %v", err)
	}
	other, err := store.CreateValueDirectory(2, 0, "db", 0)
	if err != nil {
		t.Fatalf("same name in other space: %v", err)
	}
	// A parent from another space does not exist from this space's viewpoint.
	if _, err := store.CreateValueDirectory(0, int32(other.ID), "sub", 0); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("cross-space parent err = %v, want ErrValueDirectoryNotFound", err)
	}
}

func TestMoveValueDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	a, _ := store.CreateValueDirectory(0, 0, "a", 0)
	b, _ := store.CreateValueDirectory(0, int32(a.ID), "b", 0)
	c, _ := store.CreateValueDirectory(0, int32(b.ID), "c", 0)

	// No-op: already under that parent.
	if moved, err := store.MoveValueDirectory(int32(b.ID), int32(a.ID)); err != nil || moved.ParentID != a.ID {
		t.Fatalf("no-op move = %+v, %v", moved, err)
	}

	// Cycles: into itself, and into its own descendant.
	if _, err := store.MoveValueDirectory(int32(a.ID), int32(a.ID)); !errors.Is(err, ErrValueDirectoryCycle) {
		t.Fatalf("move into itself err = %v, want ErrValueDirectoryCycle", err)
	}
	if _, err := store.MoveValueDirectory(int32(a.ID), int32(c.ID)); !errors.Is(err, ErrValueDirectoryCycle) {
		t.Fatalf("move into descendant err = %v, want ErrValueDirectoryCycle", err)
	}

	if _, err := store.MoveValueDirectory(999, 0); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("move missing directory err = %v, want ErrValueDirectoryNotFound", err)
	}
	if _, err := store.MoveValueDirectory(int32(c.ID), 999); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("move to missing parent err = %v, want ErrValueDirectoryNotFound", err)
	}

	// Destination name conflicts against each sibling kind: directory, secret,
	// and config.
	if _, err := store.CreateValueDirectory(0, 0, "c", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveValueDirectory(int32(c.ID), 0); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto sibling directory err = %v, want ErrValueAlreadyExists", err)
	}
	sec, err := store.CreateSecretWithVersion("s", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveSecretDirectory(sec.SecretID, int32(b.ID)); err != nil {
		t.Fatal(err)
	}
	dirS, _ := store.CreateValueDirectory(0, 0, "s", 0)
	if _, err := store.MoveValueDirectory(int32(dirS.ID), int32(b.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto sibling secret err = %v, want ErrValueAlreadyExists", err)
	}
	cfg, err := store.CreateConfigWithVersion("k", DefaultSpaceID, 0, 0, "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveConfigDirectory(cfg.ID, int32(b.ID)); err != nil {
		t.Fatal(err)
	}
	dirK, _ := store.CreateValueDirectory(0, 0, "k", 0)
	if _, err := store.MoveValueDirectory(int32(dirK.ID), int32(b.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto sibling config err = %v, want ErrValueAlreadyExists", err)
	}

	// A real move: b (with its subtree and contents) leaves a for the root.
	movedB, err := store.MoveValueDirectory(int32(b.ID), 0)
	if err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if movedB.ParentID != 0 {
		t.Fatalf("moved.ParentID = %d, want 0", movedB.ParentID)
	}
	// The old slot is free again and the new one is claimed.
	if _, err := store.CreateValueDirectory(0, int32(a.ID), "b", 0); err != nil {
		t.Fatalf("recreate at vacated slot: %v", err)
	}
	if _, err := store.CreateValueDirectory(0, 0, "b", 0); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("create at claimed slot err = %v, want ErrValueAlreadyExists", err)
	}
	// The subtree and contents moved with it, so b is not empty.
	if err := store.DeleteValueDirectory(int32(b.ID)); !errors.Is(err, ErrValueDirectoryNotEmpty) {
		t.Fatalf("delete moved parent err = %v, want ErrValueDirectoryNotEmpty", err)
	}
}

func TestDeleteValueDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	parent, _ := store.CreateValueDirectory(0, 0, "parent", 0)
	child, _ := store.CreateValueDirectory(0, int32(parent.ID), "child", 0)

	if err := store.DeleteValueDirectory(999); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("delete missing err = %v, want ErrValueDirectoryNotFound", err)
	}
	if err := store.DeleteValueDirectory(int32(parent.ID)); !errors.Is(err, ErrValueDirectoryNotEmpty) {
		t.Fatalf("delete with child directory err = %v, want ErrValueDirectoryNotEmpty", err)
	}

	// A resident secret blocks deletion, then a resident config.
	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveSecretDirectory(sec.SecretID, int32(child.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteValueDirectory(int32(child.ID)); !errors.Is(err, ErrValueDirectoryNotEmpty) {
		t.Fatalf("delete with secret err = %v, want ErrValueDirectoryNotEmpty", err)
	}
	if _, err := store.MoveSecretDirectory(sec.SecretID, 0); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.CreateConfigWithVersion("level", DefaultSpaceID, 0, 0, "info")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveConfigDirectory(cfg.ID, int32(child.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteValueDirectory(int32(child.ID)); !errors.Is(err, ErrValueDirectoryNotEmpty) {
		t.Fatalf("delete with config err = %v, want ErrValueDirectoryNotEmpty", err)
	}

	// Empty it out and both deletes succeed, bottom up.
	if _, err := store.MoveConfigDirectory(cfg.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteValueDirectory(int32(child.ID)); err != nil {
		t.Fatalf("delete emptied child: %v", err)
	}
	if err := store.DeleteValueDirectory(int32(parent.ID)); err != nil {
		t.Fatalf("delete emptied parent: %v", err)
	}
	if err := store.DeleteValueDirectory(int32(parent.ID)); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("second delete err = %v, want ErrValueDirectoryNotFound", err)
	}
}

func TestMoveSecretDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	dir, _ := store.CreateValueDirectory(0, 0, "creds", 0)
	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}

	moved, err := store.MoveSecretDirectory(sec.SecretID, int32(dir.ID))
	if err != nil {
		t.Fatalf("move secret: %v", err)
	}
	if moved.ValueDirectoryID != dir.ID || moved.Name != "token" {
		t.Fatalf("moved secret = %+v", moved)
	}

	// The sealed version rows are untouched by the move.
	records := store.ListSecretVersionRecords()
	if len(records) != 1 || records[0].SecretID != sec.SecretID || records[0].Ciphertext[0] != 1 {
		t.Fatalf("records after move = %+v", records)
	}

	// No-op move to the current directory.
	if again, err := store.MoveSecretDirectory(sec.SecretID, int32(dir.ID)); err != nil || again.ValueDirectoryID != dir.ID {
		t.Fatalf("no-op move = %+v, %v", again, err)
	}

	// The root name is free again; the destination name is now taken — by the
	// moved secret against a second secret, and by a config already in place.
	sec2, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(2))
	if err != nil {
		t.Fatalf("recreate at vacated name: %v", err)
	}
	if _, err := store.MoveSecretDirectory(sec2.SecretID, int32(dir.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto taken name err = %v, want ErrValueAlreadyExists", err)
	}
	cfg, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 0, "on")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveConfigDirectory(cfg.ID, int32(dir.ID)); err != nil {
		t.Fatal(err)
	}
	sec3, err := store.CreateSecretWithVersion("mode", DefaultSpaceID, 0, 0, testSealFunc(3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveSecretDirectory(sec3.SecretID, int32(dir.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto config name err = %v, want ErrValueAlreadyExists", err)
	}

	if _, err := store.MoveSecretDirectory(sec2.SecretID, 999); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("move to missing directory err = %v, want ErrValueDirectoryNotFound", err)
	}
	if _, err := store.MoveSecretDirectory(999, int32(dir.ID)); !errors.Is(err, ErrValueNotFound) {
		t.Fatalf("move missing secret err = %v, want ErrValueNotFound", err)
	}

	foreign, _ := store.CreateValueDirectory(2, 0, "other", 0)
	if _, err := store.MoveSecretDirectory(sec2.SecretID, int32(foreign.ID)); !errors.Is(err, ErrSpaceMoveUnsupported) {
		t.Fatalf("cross-space move err = %v, want ErrSpaceMoveUnsupported", err)
	}
}

func TestMoveConfigDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	dir, _ := store.CreateValueDirectory(0, 0, "app", 0)
	cfg, err := store.CreateConfigWithVersion("level", DefaultSpaceID, 0, 0, "info")
	if err != nil {
		t.Fatal(err)
	}

	moved, err := store.MoveConfigDirectory(cfg.ID, int32(dir.ID))
	if err != nil {
		t.Fatalf("move config: %v", err)
	}
	if moved.ValueDirectoryID != dir.ID || moved.Name != "level" {
		t.Fatalf("moved config = %+v", moved)
	}

	// Version rows are untouched: the pinned version id still resolves.
	versionID := cfg.ValueVersions[0].ID
	ref, ok := store.GetConfigVersionByID(versionID)
	if !ok || ref.ConfigID != cfg.ID || ref.Value != "info" {
		t.Fatalf("version after move = %+v ok=%v", ref, ok)
	}

	// No-op move, then conflicts against a config and a secret at the
	// destination.
	if again, err := store.MoveConfigDirectory(cfg.ID, int32(dir.ID)); err != nil || again.ValueDirectoryID != dir.ID {
		t.Fatalf("no-op move = %+v, %v", again, err)
	}
	cfg2, err := store.CreateConfigWithVersion("level", DefaultSpaceID, 0, 0, "debug")
	if err != nil {
		t.Fatalf("recreate at vacated name: %v", err)
	}
	if _, err := store.MoveConfigDirectory(cfg2.ID, int32(dir.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto taken name err = %v, want ErrValueAlreadyExists", err)
	}
	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveSecretDirectory(sec.SecretID, int32(dir.ID)); err != nil {
		t.Fatal(err)
	}
	cfg3, err := store.CreateConfigWithVersion("token", DefaultSpaceID, 0, 0, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveConfigDirectory(cfg3.ID, int32(dir.ID)); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto secret name err = %v, want ErrValueAlreadyExists", err)
	}

	if _, err := store.MoveConfigDirectory(cfg2.ID, 999); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("move to missing directory err = %v, want ErrValueDirectoryNotFound", err)
	}
	if _, err := store.MoveConfigDirectory(999, int32(dir.ID)); !errors.Is(err, ErrValueNotFound) {
		t.Fatalf("move missing config err = %v, want ErrValueNotFound", err)
	}

	foreign, _ := store.CreateValueDirectory(2, 0, "other", 0)
	if _, err := store.MoveConfigDirectory(cfg2.ID, int32(foreign.ID)); !errors.Is(err, ErrSpaceMoveUnsupported) {
		t.Fatalf("cross-space move err = %v, want ErrSpaceMoveUnsupported", err)
	}
}

func TestMoveSecretSpace(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveSecretSpace(sec.SecretID, DefaultSpaceID, 0, 1); err != nil {
		t.Fatalf("same-space no-op err = %v", err)
	}
	if err := store.MoveSecretSpace(999, 2, 0, 1); !errors.Is(err, ErrValueNotFound) {
		t.Fatalf("missing secret err = %v, want ErrValueNotFound", err)
	}

	if err := store.MoveSecretSpace(sec.SecretID, 2, 0, 1); err != nil {
		t.Fatalf("space move: %v", err)
	}
	moved, ok := store.GetSecret(sec.SecretID)
	if !ok || moved.SpaceID() != 2 || moved.Fs.DirectoryID != 0 {
		t.Fatalf("secret after move = %+v ok=%v", moved, ok)
	}
	// Version rows are untouched: the pinned version id still resolves.
	if got := store.SecretVersionIDs(sec.SecretID); len(got) != 1 || got[0] != sec.ID {
		t.Fatalf("version ids after move = %v, want [%d]", got, sec.ID)
	}

	// The vacated name is reusable at the origin, and the occupied one blocks a
	// move back.
	dup, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatalf("recreate at vacated name: %v", err)
	}
	if err := store.MoveSecretSpace(sec.SecretID, DefaultSpaceID, 0, 1); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto taken name err = %v, want ErrValueAlreadyExists", err)
	}

	// A destination directory must live in the destination space; the origin's
	// directory reads as absent there.
	originDir, _ := store.CreateValueDirectory(int32(DefaultSpaceID), 0, "app", 0)
	if err := store.MoveSecretSpace(dup.SecretID, 2, int32(originDir.ID), 1); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("foreign destination dir err = %v, want ErrValueDirectoryNotFound", err)
	}
	destDir, _ := store.CreateValueDirectory(2, 0, "app", 0)
	if err := store.MoveSecretSpace(dup.SecretID, 2, int32(destDir.ID), 1); err != nil {
		t.Fatalf("space move into directory: %v", err)
	}
	moved, ok = store.GetSecret(dup.SecretID)
	if !ok || moved.SpaceID() != 2 || moved.Fs.DirectoryID != int32(destDir.ID) {
		t.Fatalf("secret after directory move = %+v ok=%v", moved, ok)
	}
}

func TestMoveConfigSpace(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	cfg, err := store.CreateConfigWithVersion("level", DefaultSpaceID, 0, 0, "info")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveConfigSpace(cfg.ID, DefaultSpaceID, 0, 1); err != nil {
		t.Fatalf("same-space no-op err = %v", err)
	}
	if err := store.MoveConfigSpace(999, 2, 0, 1); !errors.Is(err, ErrValueNotFound) {
		t.Fatalf("missing config err = %v, want ErrValueNotFound", err)
	}

	if err := store.MoveConfigSpace(cfg.ID, 2, 0, 1); err != nil {
		t.Fatalf("space move: %v", err)
	}
	moved, ok := store.GetConfig(cfg.ID)
	if !ok || moved.SpaceID() != 2 || moved.Fs.DirectoryID != 0 {
		t.Fatalf("config after move = %+v ok=%v", moved, ok)
	}
	versionID := cfg.ValueVersions[0].ID
	if ref, ok := store.GetConfigVersionByID(versionID); !ok || ref.Value != "info" {
		t.Fatalf("version after move = %+v ok=%v", ref, ok)
	}

	// The shared secrets/configs namespace holds at the destination: a secret
	// with the same name there blocks the move.
	if _, err := store.CreateSecretWithVersion("blocker", 2, 0, 0, testSealFunc(1)); err != nil {
		t.Fatal(err)
	}
	cfg2, err := store.CreateConfigWithVersion("blocker", DefaultSpaceID, 0, 0, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveConfigSpace(cfg2.ID, 2, 0, 1); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("move onto secret name err = %v, want ErrValueAlreadyExists", err)
	}
}

func TestRenameValueDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	a, _ := store.CreateValueDirectory(0, 0, "a", 0)
	child, _ := store.CreateValueDirectory(0, int32(a.ID), "sub", 0)

	renamed, err := store.RenameValueDirectory(int32(a.ID), "app")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Name != "app" || renamed.ParentID != 0 {
		t.Fatalf("renamed = %+v, want name app at root", renamed)
	}
	// The contents keep their directory id, so nothing inside moves.
	if got, _ := store.GetValueDirectoryMeta(int32(child.ID)); got.ParentID != int32(a.ID) {
		t.Fatalf("child parent = %d, want %d", got.ParentID, a.ID)
	}

	// Same-name rename is a no-op, not a conflict with itself.
	if _, err := store.RenameValueDirectory(int32(a.ID), "app"); err != nil {
		t.Fatalf("same-name rename err = %v", err)
	}

	if _, err := store.RenameValueDirectory(int32(a.ID), "bad/name"); !errors.Is(err, ErrValueNameInvalid) {
		t.Fatalf("invalid name err = %v, want ErrValueNameInvalid", err)
	}
	if _, err := store.RenameValueDirectory(999, "x"); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("missing directory err = %v, want ErrValueDirectoryNotFound", err)
	}

	// The target namespace spans directories, secrets, and configs.
	if _, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameValueDirectory(int32(a.ID), "token"); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("rename onto secret name err = %v, want ErrValueAlreadyExists", err)
	}
}

func TestListValueDirectoriesAndCreateInDirectory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	a, _ := store.CreateValueDirectory(0, 0, "a", 0)
	b, _ := store.CreateValueDirectory(2, 0, "b", 0)

	dirs := store.ListValueDirectories()
	if len(dirs) != 2 || dirs[0].ID != int32(a.ID) || dirs[1].ID != int32(b.ID) {
		t.Fatalf("list = %+v, want the two created directories in space order", dirs)
	}

	// Creates target the directory and validate its space.
	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, int32(a.ID), 0, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret in directory: %v", err)
	}
	if created, _ := store.GetSecret(sec.SecretID); created.Fs.DirectoryID != int32(a.ID) {
		t.Fatalf("secret directory = %d, want %d", created.Fs.DirectoryID, a.ID)
	}
	cfg, err := store.CreateConfigWithVersion("level", DefaultSpaceID, int32(a.ID), 0, "info")
	if err != nil {
		t.Fatalf("create config in directory: %v", err)
	}
	if cfg.Fs.DirectoryID != int32(a.ID) {
		t.Fatalf("config directory = %d, want %d", cfg.Fs.DirectoryID, a.ID)
	}
	// A directory in another space does not exist from this space's viewpoint.
	if _, err := store.CreateConfigWithVersion("x", DefaultSpaceID, int32(b.ID), 0, "v"); !errors.Is(err, ErrValueDirectoryNotFound) {
		t.Fatalf("create into foreign directory err = %v, want ErrValueDirectoryNotFound", err)
	}
	// Sibling uniqueness is checked at the target directory, not the root.
	if _, err := store.CreateConfigWithVersion("token", DefaultSpaceID, int32(a.ID), 0, "v"); !errors.Is(err, ErrValueAlreadyExists) {
		t.Fatalf("create over sibling secret err = %v, want ErrValueAlreadyExists", err)
	}
	if _, err := store.CreateConfigWithVersion("token", DefaultSpaceID, 0, 0, "v"); err != nil {
		t.Fatalf("same name at root: %v", err)
	}
}
