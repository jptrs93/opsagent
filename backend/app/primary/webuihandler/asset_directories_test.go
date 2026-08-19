package webuihandler

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
)

func createTestAsset(h *Handler, ctx apigen.Context, key string, spaceID, directoryID int32, blob []byte) (*apigen.Asset, error) {
	query := url.Values{"key": {key}, "space_id": {strconv.Itoa(int(spaceID))}}
	if directoryID != 0 {
		query.Set("directory_id", strconv.Itoa(int(directoryID)))
	}
	req := httptest.NewRequest("POST", "/v1/assets/upload?"+query.Encode(), bytes.NewReader(blob))
	return h.uploadAsset(ctx, req)
}

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

// Content leaves the server only through the raw content route; metadata
// travels on the asset shapes.
func TestGetAssetContentStreamsRawBytes(t *testing.T) {
	h, user := newAssetTestHandler(t)
	asset, err := createTestAsset(h, testCtx(user), "app.conf", 1, 0, []byte("listen 8080;"))
	if err != nil {
		t.Fatalf("createTestAsset: %v", err)
	}
	versionID := asset.ContentVersions[0].ID

	req := httptest.NewRequest("GET", "/v1/assets/content?content_version_id="+strconv.Itoa(int(versionID)), nil)
	rec := httptest.NewRecorder()
	if err := h.GetV1AssetsContent(testCtx(user), req, rec); err != nil {
		t.Fatalf("GetV1AssetsContent: %v", err)
	}
	if got := rec.Body.String(); got != "listen 8080;" {
		t.Fatalf("streamed content = %q, want the uploaded blob", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len("listen 8080;")) {
		t.Fatalf("Content-Length = %q, want %d", cl, len("listen 8080;"))
	}

	// A version id that resolves to nothing is a not-found, not a stream.
	req = httptest.NewRequest("GET", "/v1/assets/content?content_version_id=999999", nil)
	if err := h.GetV1AssetsContent(testCtx(user), req, httptest.NewRecorder()); !errors.Is(err, AssetNotFoundErr) {
		t.Fatalf("missing version err = %v, want AssetNotFoundErr", err)
	}
	// The id is required.
	req = httptest.NewRequest("GET", "/v1/assets/content", nil)
	if err := h.GetV1AssetsContent(testCtx(user), req, httptest.NewRecorder()); err == nil {
		t.Fatal("missing content_version_id param did not error")
	}
}

func TestCreateAssetInsideDirectory(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "nginx")
	if dir.SpaceID != 1 || dir.ParentID != 0 || dir.Author != user.ID {
		t.Fatalf("dir = %+v, want a root directory in space 1 created by %d", dir, user.ID)
	}

	asset, err := createTestAsset(h, testCtx(user), "site.conf", 1, dir.ID, []byte("server {}"))
	if err != nil {
		t.Fatalf("createTestAsset into directory: %v", err)
	}
	stored, ok := h.Store.GetAsset(asset.ID)
	if !ok || stored.Fs.DirectoryID != dir.ID {
		t.Fatalf("created asset = %+v, want directory %d", stored, dir.ID)
	}
	// The acting user is recorded on the version row; the UI's author display
	// depends on it.
	if stored.ContentVersions[0].Author != user.ID {
		t.Fatalf("author = %d, want %d", stored.ContentVersions[0].Author, user.ID)
	}

	// The same key is free in the root: the sibling namespace is per directory.
	if _, err := createTestAsset(h, testCtx(user), "site.conf", 1, 0, []byte("other")); err != nil {
		t.Fatalf("createTestAsset same key at root: %v", err)
	}
	// But taken inside the directory, and folders share the namespace too.
	if _, err := createTestAsset(h, testCtx(user), "site.conf", 1, dir.ID, []byte("x")); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("create over sibling asset err = %v, want AssetAlreadyExistsErr", err)
	}
	if _, err := createTestAsset(h, testCtx(user), "nginx", 1, 0, []byte("x")); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("create over sibling folder err = %v, want AssetAlreadyExistsErr", err)
	}
}

func TestCreateAssetIntoMissingOrForeignDirectoryIsNotFound(t *testing.T) {
	h, user := newAssetTestHandler(t)
	if _, err := createTestAsset(h, testCtx(user), "app.yaml", 1, 999, []byte("x")); !errors.Is(err, AssetDirectoryNotFoundErr) {
		t.Fatalf("create into missing directory err = %v, want AssetDirectoryNotFoundErr", err)
	}

	space, err := h.PostV1SpacesCreate(testCtx(user), &apigen.SpaceSetRequest{Name: "staging"})
	if err != nil {
		t.Fatalf("PostV1SpacesCreate: %v", err)
	}
	foreign := mustCreateAssetDir(t, h, user, space.ID, 0, "tls")
	// A directory in another space does not exist from this space's viewpoint.
	if _, err := createTestAsset(h, testCtx(user), "cert.pem", 1, foreign.ID, []byte("pem")); !errors.Is(err, AssetDirectoryNotFoundErr) {
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

	upload("key=app.tar&unique_key=1&space_id=1&directory_id=" + strconv.Itoa(int(dir.ID)))
	uploaded, ok := h.Store.GetAssetInDirectory(1, dir.ID, "app.tar")
	if !ok {
		t.Fatalf("uploaded asset not found in directory %d", dir.ID)
	}
	if stored, ok := h.Store.GetAsset(int32(uploaded.ID)); !ok || stored.ContentVersions[0].Author != user.ID {
		t.Fatalf("uploaded version created-by = %+v, want user %d", stored, user.ID)
	}

	// A taken key is suffixed within the same directory, not the root.
	upload("key=app.tar&unique_key=1&space_id=1&directory_id=" + strconv.Itoa(int(dir.ID)))
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

	asset, err := createTestAsset(h, testCtx(user), "config.yaml", 1, 0, []byte("a: 1"))
	if err != nil {
		t.Fatalf("createTestAsset: %v", err)
	}
	moved, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, AssetDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1AssetsMove: %v", err)
	}
	if moved.Fs.DirectoryID != dir.ID {
		t.Fatalf("moved asset directory = %d, want %d", moved.Fs.DirectoryID, dir.ID)
	}
	// The version index survives the move untouched: deployment specs pin
	// version row ids.
	if len(moved.ContentVersions) != 1 || moved.ContentVersions[0].ID != asset.ContentVersions[0].ID {
		t.Fatalf("content versions changed across the move: %+v, want version id %d", moved.ContentVersions, asset.ContentVersions[0].ID)
	}

	// A sibling with the same key blocks the move back out.
	if _, err := createTestAsset(h, testCtx(user), "config.yaml", 1, 0, []byte("root")); err != nil {
		t.Fatalf("createTestAsset at root: %v", err)
	}
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, AssetDirectoryID: 0,
	}); !errors.Is(err, AssetAlreadyExistsErr) {
		t.Fatalf("move onto taken key err = %v, want AssetAlreadyExistsErr", err)
	}
}

// Assets move across spaces when no deployment outside the destination pins
// one of their versions. Directories still refuse: a subtree move needs
// per-item reference checks and stays unsupported.
func TestCrossSpaceAssetMove(t *testing.T) {
	h, user := newAssetTestHandler(t)
	dir := mustCreateAssetDir(t, h, user, 1, 0, "app")
	nested := mustCreateAssetDir(t, h, user, 1, dir.ID, "conf")

	asset, err := createTestAsset(h, testCtx(user), "config.yaml", 1, dir.ID, []byte("a: 1"))
	if err != nil {
		t.Fatalf("createTestAsset: %v", err)
	}

	moved, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, AssetDirectoryID: 0, SpaceID: 2,
	})
	if err != nil {
		t.Fatalf("cross-space asset move: %v", err)
	}
	if moved.SpaceID() != 2 || moved.Fs.DirectoryID != 0 {
		t.Fatalf("moved asset = space %d dir %d, want space 2 dir 0", moved.SpaceID(), moved.Fs.DirectoryID)
	}
	// The version index survives the move untouched: deployment specs pin
	// version row ids.
	if len(moved.ContentVersions) != 1 || moved.ContentVersions[0].ID != asset.ContentVersions[0].ID {
		t.Fatalf("content versions changed across the move: %+v, want version id %d", moved.ContentVersions, asset.ContentVersions[0].ID)
	}

	spec := remoteDeploymentSpec("registry/web", virtualNetworking())
	spec.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{
		AssetVersionID: asset.ContentVersions[0].ID, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_ONLY,
	}}
	createTestDeployment(h.Store, "node1", 2, "web", &spec)
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, SpaceID: 3,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("mounted asset move err = %v, want MoveReferencesOutsideSpaceErr", err)
	}
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, SpaceID: 1,
	}); err != nil {
		t.Fatalf("mounted asset move to global: %v", err)
	}
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, SpaceID: 2,
	}); err != nil {
		t.Fatalf("mounted asset move back to the mounting space: %v", err)
	}
	// And a pinned version blocks deletion of the whole asset.
	if err := h.PostV1AssetsDelete(testCtx(user), &apigen.AssetDeleteRequest{
		AssetID: asset.ID,
	}); !errors.Is(err, ReferenceInUseErr) {
		t.Fatalf("mounted asset delete err = %v, want ReferenceInUseErr", err)
	}

	if _, err := h.PostV1AssetDirectoriesMove(testCtx(user), &apigen.AssetDirectoryMoveRequest{
		DirectoryID: nested.ID, NewParentID: 0, SpaceID: 2,
	}); !errors.Is(err, AssetSpaceMoveUnsupportedErr) {
		t.Fatalf("cross-space directory move err = %v, want AssetSpaceMoveUnsupportedErr", err)
	}
	stayed, ok := h.Store.GetAssetDirectoryMeta(nested.ID)
	if !ok {
		t.Fatal("directory vanished after a rejected move")
	}
	if stayed.ParentID != dir.ID || stayed.SpaceID != 1 {
		t.Fatalf("directory = space %d parent %d after a rejected move, want space 1 parent %d",
			stayed.SpaceID, stayed.ParentID, dir.ID)
	}

	// Naming the row's own space is a no-op, not a rejection: the explorer sends
	// the target space on every drop, including same-space ones — and it must
	// not trip the reference check even though the asset is mounted.
	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{
		AssetID: asset.ID, AssetDirectoryID: 0, SpaceID: 2,
	}); err != nil {
		t.Fatalf("same-space move with an explicit space: %v", err)
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
	if _, err := createTestAsset(h, testCtx(user), "taken", 1, parent.ID, []byte("x")); err != nil {
		t.Fatalf("createTestAsset: %v", err)
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
	asset, err := createTestAsset(h, testCtx(user), "k.txt", 1, dir.ID, []byte("v"))
	if err != nil {
		t.Fatalf("createTestAsset: %v", err)
	}

	if err := h.PostV1AssetDirectoriesDelete(testCtx(user), &apigen.AssetDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); !errors.Is(err, AssetDirectoryNotEmptyErr) {
		t.Fatalf("delete of non-empty directory err = %v, want AssetDirectoryNotEmptyErr", err)
	}

	if _, err := h.PostV1AssetsMove(testCtx(user), &apigen.AssetMoveRequest{AssetID: asset.ID}); err != nil {
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
