package assets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type testLoader struct{}

func (testLoader) MustLoadStringSetting(v apigen.StringSetting) string { return v.Value }
func (testLoader) MustLoadBoolSetting(v apigen.BoolSetting) bool       { return v.Value }

type testSecrets map[int32]string

func (s testSecrets) RevealByID(id int32) ([]byte, error) {
	value, ok := s[id]
	if !ok {
		return nil, fmt.Errorf("secret %d not found", id)
	}
	return []byte(value), nil
}

func newTestStore(t *testing.T, settings **apigen.ClusterSettings) *Store {
	t.Helper()
	dir := t.TempDir()
	db := state.Open(filepath.Join(dir, "primary.db"))
	t.Cleanup(func() { _ = db.Close() })
	root := filepath.Join(dir, "large-assets")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatalf("create large asset root: %v", err)
	}
	return &Store{
		DB:      db,
		Config:  func() *apigen.ClusterSettings { return *settings },
		Loader:  testLoader{},
		Secrets: testSecrets{1: "shared-secret", 2: "separate-secret"},
	}
}

func largeTestBlob() []byte {
	return bytes.Repeat([]byte("a"), InlineThresholdBytes+1)
}

func storeRowFor(t *testing.T, store *Store, blob []byte) pq.AssetStore {
	t.Helper()
	row, ok := GetAssetStoreRowBySha(store.DB.Queries(), hashBlob(blob))
	if !ok {
		t.Fatal("content store row not found")
	}
	return row
}

func createMigration(t *testing.T, store *Store, oldSettings, newSettings *apigen.ClusterSettings) {
	t.Helper()
	if _, err := systemconfig.LatestRevision(store.DB.Queries()); err != nil {
		oldConfig := apigen.SystemConfig{Settings: *oldSettings}
		if _, err := systemconfig.AppendRevision(store.DB, oldConfig.Encode()); err != nil {
			t.Fatalf("store old config: %v", err)
		}
	}
	newConfig := apigen.SystemConfig{Settings: *newSettings}
	if _, migration, err := systemconfig.AppendRevisionWithAssetMigration(store.DB, newConfig.Encode(), true, nil); err != nil {
		t.Fatalf("create migration: %v", err)
	} else if migration == nil {
		t.Fatal("migration was not created")
	}
}

func TestLargeAssetStoredLocallyWhenBackupDisabled(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	row := storeRowFor(t, store, blob)
	if row.LocalStatus != 1 || row.RemoteStatus != 0 {
		t.Fatalf("store row statuses = local %d remote %d, want durable local", row.LocalStatus, row.RemoteStatus)
	}
	if got, want := statetest.LatestValue(store.DB, asset).Sha256, hashBlob(blob); got != want {
		t.Fatalf("Sha256 = %q, want %q", got, want)
	}
	if _, err := os.Stat(localPath(row.ID)); err != nil {
		t.Fatalf("local asset file: %v", err)
	}

	sizeBytes, body, err := store.OpenAsset(context.Background(), statetest.LatestValue(store.DB, asset).ID)
	if err != nil {
		t.Fatalf("OpenAsset: %v", err)
	}
	if sizeBytes != int64(len(blob)) {
		t.Fatalf("OpenAsset size = %d, want %d", sizeBytes, len(blob))
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatal("opened asset did not match upload")
	}

	if err := store.DeleteAssetLocked(context.Background(), asset.AssetID, nil); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	// Deletes are soft: surviving version rows keep the content referenced.
	if _, err := os.Stat(localPath(row.ID)); err != nil {
		t.Fatalf("local file gone after soft delete: %v", err)
	}
	if _, ok := GetAssetStoreRowBySha(store.DB.Queries(), hashBlob(blob)); !ok {
		t.Fatal("content store row gone after soft delete")
	}
	// The large-assets dir is shared process state; leave it clean.
	if err := os.Remove(localPath(row.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateContentSharesOneStoreRow(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	blob := []byte("shared config contents")

	first, err := store.CreateAsset(context.Background(), "a.conf", 1, 0, 0, blob)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := store.CreateAsset(context.Background(), "b.conf", 1, 0, 0, blob)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	firstSha := statetest.LatestValue(store.DB, first).Sha256
	if firstSha != statetest.LatestValue(store.DB, second).Sha256 {
		t.Fatalf("shas differ: %q vs %q", firstSha, statetest.LatestValue(store.DB, second).Sha256)
	}
	if rows := ListAssetStoreRowMetas(store.DB.Queries()); len(rows) != 1 {
		t.Fatalf("store rows = %d, want 1", len(rows))
	}

	if err := store.DeleteAssetLocked(context.Background(), first.AssetID, nil); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	if _, ok := GetAssetStoreRowBySha(store.DB.Queries(), firstSha); !ok {
		t.Fatal("shared content row deleted while still referenced")
	}
	if err := store.DeleteAssetLocked(context.Background(), second.AssetID, nil); err != nil {
		t.Fatalf("delete second: %v", err)
	}
	// Deletes are soft: the surviving version rows keep the content referenced.
	if _, ok := GetAssetStoreRowBySha(store.DB.Queries(), firstSha); !ok {
		t.Fatal("content row gone after soft deletes")
	}
}

func TestSweepReclaimsInterruptedStagedUpload(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	staged := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), staged, "", 4, nil, 0, 0)
	if err := os.WriteFile(localPath(staged), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.SweepUnreferencedStoreRows(time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("graced sweep: %v", err)
	}
	if _, ok := GetAssetStoreRowByID(store.DB.Queries(), staged); !ok {
		t.Fatal("staging row inside the grace period was reclaimed")
	}

	if err := store.SweepUnreferencedStoreRows(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("startup sweep: %v", err)
	}
	if _, ok := GetAssetStoreRowByID(store.DB.Queries(), staged); ok {
		t.Fatal("staging row survived the startup sweep")
	}
	if _, err := os.Stat(localPath(staged)); !os.IsNotExist(err) {
		t.Fatalf("staged file survived the startup sweep: %v", err)
	}
}

func TestMigrationRemainsActiveUntilReconcileFinishesIt(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	oldSettings := *settings
	newSettings := *settings
	newSettings.Backup.Enabled.Value = true
	createMigration(t, store, &oldSettings, &newSettings)
	settings = &newSettings

	status := store.ReconcileStatus()
	if !status.Running || status.Target != systemconfig.AssetStorageS3 || status.Pending != 0 {
		t.Fatalf("status before reconcile = %+v", status)
	}
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("Reconcile: pending=%d err=%v", pending, err)
	}
	if _, ok := systemconfig.UnfinishedAssetMigration(store.DB.Queries()); ok {
		t.Fatal("migration remained unfinished after reconciliation")
	}
}

func TestLargeAssetReconcilesBetweenLocalAndSharedS3(t *testing.T) {
	var (
		mu      sync.Mutex
		objects = map[string][]byte{}
		auth    string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			objects[r.URL.Path] = body
			w.Header().Set("ETag", `"test"`)
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	settings.Backup.S3AccessKeyID.Value = "shared-key"
	settings.Backup.S3SecretAccessKey = apigen.SecretRef{VersionID: 1}
	settings.Backup.S3Bucket.Value = "bucket"
	settings.Backup.S3Region.Value = "us-east-1"
	settings.Backup.S3Endpoint.Value = server.URL
	settings.LargeAssets.S3Path.Value = "asset-prefix"
	store := newTestStore(t, &settings)
	blob := largeTestBlob()
	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("local upload: %v", err)
	}
	row := storeRowFor(t, store, blob)
	objectPath := "/bucket/asset-prefix/" + row.ID

	oldSettings := *settings
	newSettings := *settings
	newSettings.Backup.Enabled.Value = true
	createMigration(t, store, &oldSettings, &newSettings)
	settings = &newSettings
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("reconcile to S3: pending=%d err=%v", pending, err)
	}
	if remote := storeRowFor(t, store, blob); remote.RemoteStatus != 1 || remote.LocalStatus != 0 {
		t.Fatalf("store row after S3 migration = local %d remote %d, want durable remote", remote.LocalStatus, remote.RemoteStatus)
	}
	if _, err := os.Stat(localPath(row.ID)); !os.IsNotExist(err) {
		t.Fatalf("local file remains after S3 migration: %v", err)
	}
	mu.Lock()
	_, uploaded := objects[objectPath]
	mu.Unlock()
	if !uploaded {
		t.Fatalf("object %q was not uploaded", objectPath)
	}
	if !strings.Contains(auth, "Credential=shared-key/") {
		t.Fatalf("S3 request did not use shared credentials: %q", auth)
	}

	oldSettings = *settings
	newSettings = *settings
	newSettings.Backup.Enabled.Value = false
	createMigration(t, store, &oldSettings, &newSettings)
	settings = &newSettings
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("reconcile to local: pending=%d err=%v", pending, err)
	}
	if local := storeRowFor(t, store, blob); local.LocalStatus != 1 || local.RemoteStatus != 0 {
		t.Fatalf("store row after local migration = local %d remote %d, want durable local", local.LocalStatus, local.RemoteStatus)
	}
	mu.Lock()
	_, retained := objects[objectPath]
	mu.Unlock()
	if !retained {
		t.Fatal("S3 object was not retained after migration to local storage")
	}
	if err := store.DeleteAssetLocked(context.Background(), asset.AssetID, nil); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	mu.Lock()
	_, retained = objects[objectPath]
	mu.Unlock()
	if !retained {
		t.Fatal("S3 object was not retained after asset deletion")
	}
	// Deletes are soft, so the local content survives; the large-assets dir is
	// shared process state, so leave it clean.
	if err := os.Remove(localPath(row.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestLargeAssetSeparateS3OverridesSharedCredentials(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("ETag", `"test"`)
	}))
	defer server.Close()

	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	settings.Backup.Enabled.Value = true
	settings.Backup.S3AccessKeyID.Value = "shared-key"
	settings.Backup.S3SecretAccessKey = apigen.SecretRef{VersionID: 1}
	settings.Backup.S3Bucket.Value = "shared-bucket"
	settings.Backup.S3Region.Value = "us-east-1"
	settings.LargeAssets.UseSeparateS3.Value = true
	settings.LargeAssets.S3AccessKeyID.Value = "separate-key"
	settings.LargeAssets.S3SecretAccessKey = apigen.SecretRef{VersionID: 2}
	settings.LargeAssets.S3Bucket.Value = "separate-bucket"
	settings.LargeAssets.S3Region.Value = "us-east-1"
	settings.LargeAssets.S3Endpoint.Value = server.URL
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	row := storeRowFor(t, store, blob)
	if row.RemoteStatus != 1 || row.LocalStatus != 0 {
		t.Fatalf("store row statuses = local %d remote %d, want durable remote", row.LocalStatus, row.RemoteStatus)
	}
	if got, want := statetest.LatestValue(store.DB, asset).Sha256, hashBlob(blob); got != want {
		t.Fatalf("Sha256 = %q, want %q", got, want)
	}
	if !strings.Contains(auth, "Credential=separate-key/") {
		t.Fatalf("S3 request did not use separate credentials: %q", auth)
	}
}

func TestLargeAssetUploadDoesNotFallBackWhenBackupS3IsInvalid(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	settings.Backup.Enabled.Value = true
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	_, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err == nil || !strings.Contains(err.Error(), ErrLargeAssetS3Config.Error()) {
		t.Fatalf("CreateAssetFromReader error = %v, want S3 configuration error", err)
	}
	if got := ListAssets(store.DB.Queries()); len(got) != 0 {
		t.Fatalf("stored %d asset rows after failed S3 upload, want 0", len(got))
	}
	if got := ListAssetStoreRowMetas(store.DB.Queries()); len(got) != 0 {
		t.Fatalf("stored %d content rows after failed S3 upload, want 0", len(got))
	}
	entries, err := os.ReadDir(ainit.StaticConfig.LargeAssetsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staged files remain after failed S3 upload: %v", entries)
	}
}

func TestS3ConfigurationChangeRequiresAssetsToBeLocal(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	sha := hashBlob([]byte("remote content"))
	InsertAssetStoreRow(store.DB.Queries(), "remote-row", sha, InlineThresholdBytes+1, nil, 0, 1)
	next := *settings
	next.LargeAssets.S3Path.Value = "different-path"

	if err := store.ValidateSettingsUpdate(*settings, next); !errors.Is(err, ErrAssetS3ConfigChangeRequiresLocal) {
		t.Fatalf("ValidateSettingsUpdate error = %v, want ErrAssetS3ConfigChangeRequiresLocal", err)
	}
	SetAssetStoreRemoteStatus(store.DB.Queries(), "remote-row", 0)
	SetAssetStoreLocalStatus(store.DB.Queries(), "remote-row", 1)
	if err := store.ValidateSettingsUpdate(*settings, next); err != nil {
		t.Fatalf("ValidateSettingsUpdate with local assets: %v", err)
	}

	InsertAssetStoreRow(store.DB.Queries(), "staging-row", "", 4, nil, 0, 0)
	if err := store.ValidateSettingsUpdate(*settings, next); !errors.Is(err, ErrAssetS3ConfigChangeRequiresLocal) {
		t.Fatalf("ValidateSettingsUpdate with staging row error = %v, want ErrAssetS3ConfigChangeRequiresLocal", err)
	}
}

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	server  *httptest.Server
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			f.objects[r.URL.Path] = body
			w.Header().Set("ETag", `"test"`)
		case http.MethodGet:
			body, ok := f.objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[path]
	return ok
}

func (f *fakeS3) remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, path)
}

func dualStorageSettings(s3 *fakeS3) *apigen.ClusterSettings {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	settings.Backup.S3AccessKeyID.Value = "shared-key"
	settings.Backup.S3SecretAccessKey = apigen.SecretRef{VersionID: 1}
	settings.Backup.S3Bucket.Value = "bucket"
	settings.Backup.S3Region.Value = "us-east-1"
	settings.Backup.S3Endpoint.Value = s3.server.URL
	settings.LargeAssets.S3Path.Value = "asset-prefix"
	return settings
}

func readAssetContent(t *testing.T, store *Store, versionID int32) []byte {
	t.Helper()
	_, body, err := store.OpenAsset(context.Background(), versionID)
	if err != nil {
		t.Fatalf("OpenAsset: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	return got
}

func expectStatuses(t *testing.T, store *Store, blob []byte, local, remote int64) pq.AssetStore {
	t.Helper()
	row := storeRowFor(t, store, blob)
	if row.LocalStatus != local || row.RemoteStatus != remote {
		t.Fatalf("store row statuses = local %d remote %d, want local %d remote %d", row.LocalStatus, row.RemoteStatus, local, remote)
	}
	return row
}

func switchTarget(t *testing.T, store *Store, settings **apigen.ClusterSettings, mutate func(*apigen.ClusterSettings)) {
	t.Helper()
	oldSettings := **settings
	newSettings := **settings
	mutate(&newSettings)
	createMigration(t, store, &oldSettings, &newSettings)
	*settings = &newSettings
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("Reconcile to %s: pending=%d err=%v", systemconfig.LargeAssetStorageTarget(testLoader{}, newSettings), pending, err)
	}
	if _, ok := systemconfig.UnfinishedAssetMigration(store.DB.Queries()); ok {
		t.Fatal("migration remained unfinished after reconciliation")
	}
}

func TestLargeAssetStoredInBothWhenKeepLocalCopy(t *testing.T) {
	s3 := newFakeS3(t)
	settings := dualStorageSettings(s3)
	settings.Backup.Enabled.Value = true
	settings.LargeAssets.KeepLocalCopy.Value = true
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	row := expectStatuses(t, store, blob, 1, 1)
	t.Cleanup(func() { _ = os.Remove(localPath(row.ID)) })
	if _, err := os.Stat(localPath(row.ID)); err != nil {
		t.Fatalf("local copy: %v", err)
	}
	objectPath := "/bucket/asset-prefix/" + row.ID
	if !s3.has(objectPath) {
		t.Fatalf("object %q was not uploaded", objectPath)
	}
	if store.ReconcileStatus().Target != systemconfig.AssetStorageBoth {
		t.Fatalf("target = %s, want both", store.ReconcileStatus().Target)
	}

	s3.remove(objectPath)
	if got := readAssetContent(t, store, statetest.LatestValue(store.DB, asset).ID); !bytes.Equal(got, blob) {
		t.Fatal("content read with the S3 object gone did not match the upload")
	}
}

func TestOpenAssetFallsBackToS3WhenLocalCopyIsMissing(t *testing.T) {
	s3 := newFakeS3(t)
	settings := dualStorageSettings(s3)
	settings.Backup.Enabled.Value = true
	settings.LargeAssets.KeepLocalCopy.Value = true
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	row := expectStatuses(t, store, blob, 1, 1)
	t.Cleanup(func() { _ = os.Remove(localPath(row.ID)) })
	if err := os.Remove(localPath(row.ID)); err != nil {
		t.Fatal(err)
	}

	versionID := statetest.LatestValue(store.DB, asset).ID
	if got := readAssetContent(t, store, versionID); !bytes.Equal(got, blob) {
		t.Fatal("fallback read did not match the upload")
	}
	expectStatuses(t, store, blob, 0, 1)
	select {
	case <-store.wakeChan():
	default:
		t.Fatal("fallback read did not wake the reconciler")
	}
	if status := store.ReconcileStatus(); status.Pending != 1 || status.Running {
		t.Fatalf("status after fallback = %+v, want one pending row and no migration", status)
	}

	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("Reconcile: pending=%d err=%v", pending, err)
	}
	expectStatuses(t, store, blob, 1, 1)
	restored, err := os.ReadFile(localPath(row.ID))
	if err != nil {
		t.Fatalf("restored local copy: %v", err)
	}
	if !bytes.Equal(restored, blob) {
		t.Fatal("restored local copy did not match the upload")
	}
	if store.LastError() != "" {
		t.Fatalf("LastError = %q, want empty", store.LastError())
	}
}

func TestReconcileConvergesAcrossStorageTargets(t *testing.T) {
	s3 := newFakeS3(t)
	settings := dualStorageSettings(s3)
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("local upload: %v", err)
	}
	row := expectStatuses(t, store, blob, 1, 0)
	t.Cleanup(func() { _ = os.Remove(localPath(row.ID)) })
	objectPath := "/bucket/asset-prefix/" + row.ID
	versionID := statetest.LatestValue(store.DB, asset).ID

	switchTarget(t, store, &settings, func(s *apigen.ClusterSettings) {
		s.Backup.Enabled.Value = true
		s.LargeAssets.KeepLocalCopy.Value = true
	})
	expectStatuses(t, store, blob, 1, 1)
	if _, err := os.Stat(localPath(row.ID)); err != nil {
		t.Fatalf("local copy after local→both: %v", err)
	}
	if !s3.has(objectPath) {
		t.Fatal("object missing after local→both")
	}

	switchTarget(t, store, &settings, func(s *apigen.ClusterSettings) {
		s.LargeAssets.KeepLocalCopy.Value = false
	})
	expectStatuses(t, store, blob, 0, 1)
	if _, err := os.Stat(localPath(row.ID)); !os.IsNotExist(err) {
		t.Fatalf("local copy remains after both→s3: %v", err)
	}

	switchTarget(t, store, &settings, func(s *apigen.ClusterSettings) {
		s.LargeAssets.KeepLocalCopy.Value = true
	})
	expectStatuses(t, store, blob, 1, 1)
	downloaded, err := os.ReadFile(localPath(row.ID))
	if err != nil {
		t.Fatalf("local copy after s3→both: %v", err)
	}
	if !bytes.Equal(downloaded, blob) {
		t.Fatal("downloaded local copy did not match the upload")
	}

	switchTarget(t, store, &settings, func(s *apigen.ClusterSettings) {
		s.Backup.Enabled.Value = false
	})
	expectStatuses(t, store, blob, 1, 0)
	if !s3.has(objectPath) {
		t.Fatal("S3 object was not retained after both→local")
	}
	if got := readAssetContent(t, store, versionID); !bytes.Equal(got, blob) {
		t.Fatal("content after both→local did not match the upload")
	}
}

func TestReconcileVerifiesLocalClaimsAgainstTheFilesystem(t *testing.T) {
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	store := newTestStore(t, &settings)
	content := largeTestBlob()
	size := int64(len(content))

	missing := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), missing, hashBlob([]byte("missing")), size, nil, 1, 0)
	truncated := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), truncated, hashBlob([]byte("truncated")), size, nil, 1, 0)
	if err := os.WriteFile(localPath(truncated), content[:10], 0o600); err != nil {
		t.Fatal(err)
	}
	unclaimed := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), unclaimed, hashBlob(content), size, nil, 0, 0)
	if err := os.WriteFile(localPath(unclaimed), content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(localPath(unclaimed)) })
	corrupt := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), corrupt, hashBlob([]byte("corrupt")), size, nil, 0, 0)
	if err := os.WriteFile(localPath(corrupt), content, 0o600); err != nil {
		t.Fatal(err)
	}

	pending, err := store.Reconcile(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("Reconcile: pending=%d err=%v", pending, err)
	}
	for id, wantLocal := range map[string]int64{missing: 0, truncated: 0, unclaimed: 1, corrupt: 0} {
		row, ok := GetAssetStoreRowByID(store.DB.Queries(), id)
		if !ok {
			t.Fatalf("row %s missing", id)
		}
		if row.LocalStatus != wantLocal {
			t.Fatalf("row %s local_status = %d, want %d", id, row.LocalStatus, wantLocal)
		}
	}
	if _, err := os.Stat(localPath(truncated)); !os.IsNotExist(err) {
		t.Fatalf("truncated file survived cleanup: %v", err)
	}
	if _, err := os.Stat(localPath(corrupt)); !os.IsNotExist(err) {
		t.Fatalf("corrupt file survived cleanup: %v", err)
	}
	if _, err := os.Stat(localPath(unclaimed)); err != nil {
		t.Fatalf("adopted file: %v", err)
	}
	if !strings.Contains(store.LastError(), "3 large asset content row(s) have no durable copy") {
		t.Fatalf("LastError = %q, want the unavailable count", store.LastError())
	}
	if status := store.ReconcileStatus(); status.Pending != 0 || status.Error == "" {
		t.Fatalf("status = %+v, want no pending rows and the unavailable error", status)
	}
}

func TestDropLocalCopyClearsTheClaimBeforeRemovingTheFile(t *testing.T) {
	s3 := newFakeS3(t)
	settings := dualStorageSettings(s3)
	settings.Backup.Enabled.Value = true
	store := newTestStore(t, &settings)
	content := largeTestBlob()
	id := newStoreID()
	InsertAssetStoreRow(store.DB.Queries(), id, hashBlob(content), int64(len(content)), nil, 1, 1)
	if err := os.WriteFile(localPath(id), content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(localPath(id)) })
	if status := store.ReconcileStatus(); status.Pending != 1 {
		t.Fatalf("pending before drop = %d, want 1", status.Pending)
	}

	store.dropLocalCopy(context.Background(), id)
	row, _ := GetAssetStoreRowByID(store.DB.Queries(), id)
	if row.LocalStatus != 0 || row.RemoteStatus != 1 {
		t.Fatalf("statuses after drop = local %d remote %d", row.LocalStatus, row.RemoteStatus)
	}
	if _, err := os.Stat(localPath(id)); !os.IsNotExist(err) {
		t.Fatalf("file remains after drop: %v", err)
	}
	if status := store.ReconcileStatus(); status.Pending != 0 {
		t.Fatalf("pending after drop = %d, want 0", status.Pending)
	}
}
