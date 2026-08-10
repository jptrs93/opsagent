package webuihandler

import (
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
)

// newAssetTestHandler extends the auth test handler with an asset store.
// Inline-sized blobs never leave the database, so no S3 or filesystem wiring
// is needed.
func newAssetTestHandler(t *testing.T) (*Handler, *apigen.InternalUser) {
	t.Helper()
	h, user := newAuthTestHandler(t)
	h.Assets = &assetstore.Store{DB: h.Store}
	return h, user
}

func mustCreateAssetDir(t *testing.T, h *Handler, user *apigen.InternalUser, spaceID, parentID int32, key string) *apigen.AssetDirectory {
	t.Helper()
	dir, err := h.PostV1AssetDirectoriesCreate(testCtx(user), &apigen.AssetDirectoryCreateRequest{
		SpaceID: spaceID, ParentID: parentID, Key: key,
	})
	if err != nil {
		t.Fatalf("PostV1AssetDirectoriesCreate(%q): %v", key, err)
	}
	return dir
}

func TestCreateAssetInsideDirectory(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "nginx")
	if dir.SpaceID != 1 || dir.ParentID != 0 || dir.CreatedBy != user.ID {
		t.Fatalf("dir = %+v, want a root directory in space 1 created by %d", dir, user.ID)
	}

	asset, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "site.conf", SpaceID: 1, Blob: []byte("server {}"), AssetDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1AssetsCreate into directory: %v", err)
	}
	meta, ok := h.Store.GetAssetMeta(asset.AssetID)
	if !ok || meta.AssetDirectoryID != dir.ID {
		t.Fatalf("created asset meta = %+v, want directory %d", meta, dir.ID)
	}

	// The same key is free in the root: the sibling namespace is per directory.
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "site.conf", SpaceID: 1, Blob: []byte("other"),
	}); err != nil {
		t.Fatalf("PostV1AssetsCreate same key at root: %v", err)
	}
	// But taken inside the directory, and folders share the namespace too.
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "site.conf", SpaceID: 1, Blob: []byte("x"), AssetDirectoryID: dir.ID,
	}); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("create over sibling asset err = %v, want AssetAlreadyExistsErr", err)
	}
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "nginx", SpaceID: 1, Blob: []byte("x"),
	}); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("create over sibling folder err = %v, want AssetAlreadyExistsErr", err)
	}
}

func TestCreateAssetIntoMissingOrForeignDirectoryIsNotFound(t *testing.T) {
	h, user := newAssetTestHandler(t)
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "app.yaml", SpaceID: 1, Blob: []byte("x"), AssetDirectoryID: 999,
	}); !errors.Is(err, AssetDirectoryNotFoundErr) {
		t.Fatalf("create into missing directory err = %v, want AssetDirectoryNotFoundErr", err)
	}

	space, err := h.PostV1SpacesCreate(testCtx(user), &apigen.SpaceSetRequest{Name: "staging"})
	if err != nil {
		t.Fatalf("PostV1SpacesCreate: %v", err)
	}
	foreign := mustCreateAssetDir(t, h, user, space.ID, 0, "tls")
	// A directory in another space does not exist from this space's viewpoint.
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "cert.pem", SpaceID: 1, Blob: []byte("pem"), AssetDirectoryID: foreign.ID,
	}); !errors.Is(err, AssetDirectoryNotFoundErr) {
		t.Fatalf("create into foreign-space directory err = %v, want AssetDirectoryNotFoundErr", err)
	}
}

func TestUploadAssetIntoDirectory(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "bundles")

	upload := func(query string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/assets/upload?"+query, strings.NewReader("payload"))
		req.Header.Set("Accept", "application/json")
		if err := h.PostV1AssetsUpload(testCtx(user), req, httptest.NewRecorder()); err != nil {
			t.Fatalf("PostV1AssetsUpload(%s): %v", query, err)
		}
	}

	upload("name=app.tar&space_id=1&directory_id=" + strconv.Itoa(int(dir.ID)))
	if _, ok := h.Store.GetAssetInDirectory(1, dir.ID, "app.tar"); !ok {
		t.Fatalf("uploaded asset not found in directory %d", dir.ID)
	}

	// A taken name is suffixed within the same directory, not the root.
	upload("name=app.tar&space_id=1&directory_id=" + strconv.Itoa(int(dir.ID)))
	if _, ok := h.Store.GetAssetInDirectory(1, dir.ID, "app.tar1"); !ok {
		t.Fatalf("second upload did not suffix within the directory")
	}
	if _, ok := h.Store.GetAssetInDirectory(1, 0, "app.tar"); ok {
		t.Fatalf("upload leaked into the space root")
	}
}

func TestMoveAssetBetweenDirectories(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "app")

	asset, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "config.yaml", SpaceID: 1, Blob: []byte("a: 1"),
	})
	if err != nil {
		t.Fatalf("PostV1AssetsCreate: %v", err)
	}
	moved, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.AssetID, AssetDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1AssetsMove: %v", err)
	}
	if moved.AssetDirectoryID != dir.ID {
		t.Fatalf("moved asset directory = %d, want %d", moved.AssetDirectoryID, dir.ID)
	}
	// The version index survives the move untouched: deployment specs pin
	// version row ids.
	if len(moved.VersionRefs) != 1 || moved.VersionRefs[0].ID != asset.ID {
		t.Fatalf("version refs changed across the move: %+v, want version id %d", moved.VersionRefs, asset.ID)
	}

	// A sibling with the same key blocks the move back out.
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "config.yaml", SpaceID: 1, Blob: []byte("root"),
	}); err != nil {
		t.Fatalf("PostV1AssetsCreate at root: %v", err)
	}
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.AssetID, AssetDirectoryID: 0,
	}); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("move onto taken key err = %v, want AssetAlreadyExistsErr", err)
	}
}

func TestRenameAndMoveAssetDirectories(t *testing.T) {
	h, user := newAssetTestHandler(t)
	parent := mustCreateAssetDir(t, h, user, 1, 0, "a")
	child := mustCreateAssetDir(t, h, user, 1, parent.ID, "b")

	renamed, err := h.PostV1AssetDirectoriesRename(testCtx(user), &apigen.AssetDirectoryRenameRequest{
		DirectoryID: child.ID, NewKey: "c",
	})
	if err != nil {
		t.Fatalf("PostV1AssetDirectoriesRename: %v", err)
	}
	if renamed.Key != "c" || renamed.ParentID != parent.ID {
		t.Fatalf("renamed = %+v, want key c under parent %d", renamed, parent.ID)
	}

	// The rename target namespace spans assets and directories.
	if _, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "taken", SpaceID: 1, Blob: []byte("x"), AssetDirectoryID: parent.ID,
	}); err != nil {
		t.Fatalf("PostV1AssetsCreate: %v", err)
	}
	if _, err := h.PostV1AssetDirectoriesRename(testCtx(user), &apigen.AssetDirectoryRenameRequest{
		DirectoryID: child.ID, NewKey: "taken",
	}); !errors.Is(err, AssetDirectoryKeyTakenErr) {
		t.Fatalf("rename onto sibling asset err = %v, want AssetDirectoryKeyTakenErr", err)
	}

	// A directory cannot be moved inside its own subtree.
	if _, err := h.PostV1AssetDirectoriesMove(testCtx(user), &apigen.AssetDirectoryMoveRequest{
		DirectoryID: parent.ID, NewParentID: child.ID,
	}); !errors.Is(err, AssetDirectoryCycleErr) {
		t.Fatalf("cycle move err = %v, want AssetDirectoryCycleErr", err)
	}

	moved, err := h.PostV1AssetDirectoriesMove(testCtx(user), &apigen.AssetDirectoryMoveRequest{
		DirectoryID: child.ID, NewParentID: 0,
	})
	if err != nil {
		t.Fatalf("PostV1AssetDirectoriesMove to root: %v", err)
	}
	if moved.ParentID != 0 {
		t.Fatalf("moved parent = %d, want the root", moved.ParentID)
	}
}

func TestDeleteAssetDirectoryOnlyWhenEmpty(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "tmp")
	asset, err := h.PostV1AssetsCreate(testCtx(user), &apigen.AssetCreateRequest{
		Key: "k.txt", SpaceID: 1, Blob: []byte("v"), AssetDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1AssetsCreate: %v", err)
	}

	if err := h.PostV1AssetDirectoriesDelete(testCtx(user), &apigen.AssetDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); !errors.Is(err, AssetDirectoryNotEmptyErr) {
		t.Fatalf("delete of non-empty directory err = %v, want AssetDirectoryNotEmptyErr", err)
	}

	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{AssetID: asset.AssetID}); err != nil {
		t.Fatalf("PostV1AssetsMove to root: %v", err)
	}
	if err := h.PostV1AssetDirectoriesDelete(testCtx(user), &apigen.AssetDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); err != nil {
		t.Fatalf("delete of emptied directory: %v", err)
	}

	list, err := h.PostV1AssetDirectoriesList(testCtx(user), &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("PostV1AssetDirectoriesList: %v", err)
	}
	for _, d := range list.Items {
		if d.ID == dir.ID {
			t.Fatalf("deleted directory still listed: %+v", d)
		}
	}
}

func TestGlobalStateIncludesAssetDirectories(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "app")

	state, err := h.GetV1GlobalState(testCtx(user))
	if err != nil {
		t.Fatalf("GetV1GlobalState: %v", err)
	}
	if state.AssetDirectories == nil || len(state.AssetDirectories.Items) != 1 ||
		state.AssetDirectories.Items[0].ID != dir.ID {
		t.Fatalf("global state asset directories = %+v, want the one created", state.AssetDirectories)
	}
}
