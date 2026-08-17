package assetstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type testLoader struct{}

func (testLoader) MustLoadConfigStringValue(v apigen.StringSetting) string { return v.Value }
func (testLoader) MustLoadConfigBoolValue(v apigen.BoolSetting) bool       { return v.Value }

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

func storeRowFor(t *testing.T, store *Store, blob []byte) state.AssetStore {
	t.Helper()
	row, ok := store.DB.GetAssetStoreRowBySha(hashBlob(blob))
	if !ok {
		t.Fatal("content store row not found")
	}
	return row
}

func createMigration(t *testing.T, store *Store, oldSettings, newSettings *apigen.ClusterSettings) {
	t.Helper()
	if _, err := store.DB.FetchLatestOpenDeployConfig(); err != nil {
		oldConfig := apigen.PrimaryConfig{Settings: *oldSettings}
		if _, err := store.DB.AppendOpenDeploySettings(oldConfig.Encode()); err != nil {
			t.Fatalf("store old config: %v", err)
		}
	}
	newConfig := apigen.PrimaryConfig{Settings: *newSettings}
	if _, migration, err := store.DB.AppendOpenDeploySettingsWithAssetMigration(newConfig.Encode(), true); err != nil {
		t.Fatalf("create migration: %v", err)
	} else if migration == nil {
		t.Fatal("migration was not created")
	}
}

func TestLargeAssetStoredLocallyWhenBackupDisabled(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
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
	if got, want := asset.Location, "local://"+row.ID; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if got, want := asset.Sha256, hashBlob(blob); got != want {
		t.Fatalf("Sha256 = %q, want %q", got, want)
	}
	if _, err := os.Stat(localPath(row.ID)); err != nil {
		t.Fatalf("local asset file: %v", err)
	}

	_, body, err := store.OpenAsset(context.Background(), asset.ID)
	if err != nil {
		t.Fatalf("OpenAsset: %v", err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatal("opened asset did not match upload")
	}

	if err := store.DeleteAsset(context.Background(), asset.AssetID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if _, err := os.Stat(localPath(row.ID)); !os.IsNotExist(err) {
		t.Fatalf("local file still exists after delete: %v", err)
	}
	if _, ok := store.DB.GetAssetStoreRowBySha(hashBlob(blob)); ok {
		t.Fatal("content store row still exists after delete")
	}
}

func TestDuplicateContentSharesOneStoreRow(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
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
	if first.Sha256 != second.Sha256 {
		t.Fatalf("shas differ: %q vs %q", first.Sha256, second.Sha256)
	}
	if rows := store.DB.ListAssetStoreRowMetas(); len(rows) != 1 {
		t.Fatalf("store rows = %d, want 1", len(rows))
	}

	if err := store.DeleteAsset(context.Background(), first.AssetID); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	if _, ok := store.DB.GetAssetStoreRowBySha(first.Sha256); !ok {
		t.Fatal("shared content row deleted while still referenced")
	}
	if err := store.DeleteAsset(context.Background(), second.AssetID); err != nil {
		t.Fatalf("delete second: %v", err)
	}
	if _, ok := store.DB.GetAssetStoreRowBySha(first.Sha256); ok {
		t.Fatal("content row still exists after last reference deleted")
	}
}

func TestSweepReclaimsInterruptedStagedUpload(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	store := newTestStore(t, &settings)
	staged := newStoreID()
	store.DB.InsertAssetStoreRow(staged, "", 4, nil, 0, 0)
	if err := os.WriteFile(localPath(staged), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.SweepUnreferencedStoreRows(time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("graced sweep: %v", err)
	}
	if _, ok := store.DB.GetAssetStoreRowByID(staged); !ok {
		t.Fatal("staging row inside the grace period was reclaimed")
	}

	if err := store.SweepUnreferencedStoreRows(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("startup sweep: %v", err)
	}
	if _, ok := store.DB.GetAssetStoreRowByID(staged); ok {
		t.Fatal("staging row survived the startup sweep")
	}
	if _, err := os.Stat(localPath(staged)); !os.IsNotExist(err) {
		t.Fatalf("staged file survived the startup sweep: %v", err)
	}
}

func TestMigrationRemainsActiveUntilReconcileFinishesIt(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	store := newTestStore(t, &settings)
	oldSettings := *settings
	newSettings := *settings
	newSettings.Backup.Enabled.Value = true
	createMigration(t, store, &oldSettings, &newSettings)
	settings = &newSettings

	status := store.ReconcileStatus()
	if !status.Running || !status.TargetS3 || status.Pending != 0 {
		t.Fatalf("status before reconcile = %+v", status)
	}
	if ready, _ := store.ReadyForDatabaseBackup(); ready {
		t.Fatal("database backup was ready before the migration row finished")
	}
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("Reconcile: pending=%d err=%v", pending, err)
	}
	if _, ok := store.DB.GetUnfinishedAssetMigration(); ok {
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

	settings := config.DefaultSettings(config.DefaultInitialConfig())
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
	remote, _ := store.DB.GetAssetVersionByID(asset.ID)
	if got, want := remote.Location, "s3://"+row.ID; got != want {
		t.Fatalf("S3 location = %q, want %q", got, want)
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
	local, _ := store.DB.GetAssetVersionByID(asset.ID)
	if got, want := local.Location, "local://"+row.ID; got != want {
		t.Fatalf("local location = %q, want %q", got, want)
	}
	mu.Lock()
	_, retained := objects[objectPath]
	mu.Unlock()
	if !retained {
		t.Fatal("S3 object was not retained after migration to local storage")
	}
	if err := store.DeleteAsset(context.Background(), asset.AssetID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	mu.Lock()
	_, retained = objects[objectPath]
	mu.Unlock()
	if !retained {
		t.Fatal("S3 object was not retained after asset deletion")
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

	settings := config.DefaultSettings(config.DefaultInitialConfig())
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
	if got, want := asset.Location, "s3://"+row.ID; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if !strings.Contains(auth, "Credential=separate-key/") {
		t.Fatalf("S3 request did not use separate credentials: %q", auth)
	}
}

func TestLargeAssetUploadDoesNotFallBackWhenBackupS3IsInvalid(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	settings.Backup.Enabled.Value = true
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	_, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, 0, int64(len(blob)), bytes.NewReader(blob))
	if err == nil || !strings.Contains(err.Error(), ErrLargeAssetS3Config.Error()) {
		t.Fatalf("CreateAssetFromReader error = %v, want S3 configuration error", err)
	}
	if got := store.DB.ListAssets(); len(got) != 0 {
		t.Fatalf("stored %d asset rows after failed S3 upload, want 0", len(got))
	}
	if got := store.DB.ListAssetStoreRowMetas(); len(got) != 0 {
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
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	store := newTestStore(t, &settings)
	sha := hashBlob([]byte("remote content"))
	store.DB.InsertAssetStoreRow("remote-row", sha, InlineThresholdBytes+1, nil, 0, 1)
	next := *settings
	next.LargeAssets.S3Path.Value = "different-path"

	if err := store.ValidateSettingsUpdate(*settings, next); !errors.Is(err, ErrAssetS3ConfigChangeRequiresLocal) {
		t.Fatalf("ValidateSettingsUpdate error = %v, want ErrAssetS3ConfigChangeRequiresLocal", err)
	}
	store.DB.SetAssetStoreRemoteStatus("remote-row", 0)
	store.DB.SetAssetStoreLocalStatus("remote-row", 1)
	if err := store.ValidateSettingsUpdate(*settings, next); err != nil {
		t.Fatalf("ValidateSettingsUpdate with local assets: %v", err)
	}

	store.DB.InsertAssetStoreRow("staging-row", "", 4, nil, 0, 0)
	if err := store.ValidateSettingsUpdate(*settings, next); !errors.Is(err, ErrAssetS3ConfigChangeRequiresLocal) {
		t.Fatalf("ValidateSettingsUpdate with staging row error = %v, want ErrAssetS3ConfigChangeRequiresLocal", err)
	}
}
