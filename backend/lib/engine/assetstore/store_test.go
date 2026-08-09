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

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
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
	db := sqlite.NewPrimaryStorage(filepath.Join(dir, "primary.db"))
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

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	if got, want := asset.Location, localLocation(asset.ID); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if _, err := os.Stat(localPath(asset.ID)); err != nil {
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
	if _, err := os.Stat(localPath(asset.ID)); !os.IsNotExist(err) {
		t.Fatalf("local file still exists after delete: %v", err)
	}
}

func TestReconcileFinishesInterruptedLocalUpload(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	store := newTestStore(t, &settings)
	stagedName := ".upload-interrupted"
	if err := os.WriteFile(filepath.Join(ainit.StaticConfig.LargeAssetsDir, stagedName), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	asset, err := store.DB.CreateAssetWithVersion("interrupted.bin", 1, 0, pendingLocation(stagedName), 4, nil)
	if err != nil {
		t.Fatalf("stage pending upload: %v", err)
	}
	if _, ok := store.DB.GetAssetVersionByID(asset.ID); ok {
		t.Fatal("pending upload was visible through the public asset reader")
	}

	if err := store.recoverPendingUploads(context.Background()); err != nil {
		t.Fatalf("recoverPendingUploads: %v", err)
	}
	stored, ok := store.DB.GetAssetVersionByID(asset.ID)
	if !ok {
		t.Fatal("asset disappeared during reconciliation")
	}
	if got, want := stored.Location, localLocation(asset.ID); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if got, err := os.ReadFile(localPath(asset.ID)); err != nil || string(got) != "data" {
		t.Fatalf("local file = %q, err=%v", got, err)
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
	settings.Backup.S3SecretAccessKey = apigen.SecretRef{ID: 1}
	settings.Backup.S3Bucket.Value = "bucket"
	settings.Backup.S3Region.Value = "us-east-1"
	settings.Backup.S3Endpoint.Value = server.URL
	settings.LargeAssets.S3Path.Value = "asset-prefix"
	store := newTestStore(t, &settings)
	blob := largeTestBlob()
	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("local upload: %v", err)
	}

	oldSettings := *settings
	newSettings := *settings
	newSettings.Backup.Enabled.Value = true
	createMigration(t, store, &oldSettings, &newSettings)
	settings = &newSettings
	if pending, err := store.Reconcile(context.Background()); err != nil || pending != 0 {
		t.Fatalf("reconcile to S3: pending=%d err=%v", pending, err)
	}
	remote, _ := store.DB.GetAssetVersionByID(asset.ID)
	if got, want := remote.Location, "s3://bucket/asset-prefix/"+fmt.Sprint(asset.ID); got != want {
		t.Fatalf("S3 location = %q, want %q", got, want)
	}
	if _, err := os.Stat(localPath(asset.ID)); !os.IsNotExist(err) {
		t.Fatalf("local file remains after S3 migration: %v", err)
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
	if got, want := local.Location, localLocation(asset.ID); got != want {
		t.Fatalf("local location = %q, want %q", got, want)
	}
	mu.Lock()
	_, retained := objects["/bucket/asset-prefix/"+fmt.Sprint(asset.ID)]
	mu.Unlock()
	if !retained {
		t.Fatal("S3 object was not retained after migration to local storage")
	}
	if err := store.DeleteAsset(context.Background(), asset.AssetID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	mu.Lock()
	_, retained = objects["/bucket/asset-prefix/"+fmt.Sprint(asset.ID)]
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
	settings.Backup.S3SecretAccessKey = apigen.SecretRef{ID: 1}
	settings.Backup.S3Bucket.Value = "shared-bucket"
	settings.Backup.S3Region.Value = "us-east-1"
	settings.LargeAssets.UseSeparateS3.Value = true
	settings.LargeAssets.S3AccessKeyID.Value = "separate-key"
	settings.LargeAssets.S3SecretAccessKey = apigen.SecretRef{ID: 2}
	settings.LargeAssets.S3Bucket.Value = "separate-bucket"
	settings.LargeAssets.S3Region.Value = "us-east-1"
	settings.LargeAssets.S3Endpoint.Value = server.URL
	store := newTestStore(t, &settings)
	blob := largeTestBlob()

	asset, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, int64(len(blob)), bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("CreateAssetFromReader: %v", err)
	}
	if !strings.HasPrefix(asset.Location, "s3://separate-bucket/") {
		t.Fatalf("Location = %q, want separate bucket", asset.Location)
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

	_, err := store.CreateAssetFromReader(context.Background(), "large.bin", 1, 0, int64(len(blob)), bytes.NewReader(blob))
	if err == nil || !strings.Contains(err.Error(), ErrLargeAssetS3Config.Error()) {
		t.Fatalf("CreateAssetFromReader error = %v, want S3 configuration error", err)
	}
	if got := store.DB.ListAssets(); len(got) != 0 {
		t.Fatalf("stored %d asset rows after failed S3 upload, want 0", len(got))
	}
	if got := store.DB.ListAllAssetVersions(); len(got) != 0 {
		t.Fatalf("stored %d asset rows after failed S3 upload, want 0", len(got))
	}
}

func TestS3ConfigurationChangeRequiresAssetsToBeLocal(t *testing.T) {
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	store := newTestStore(t, &settings)
	if _, err := store.DB.CreateAssetWithVersion("large.bin", 1, 0, "s3://bucket/path/1", InlineThresholdBytes+1, nil); err != nil {
		t.Fatalf("create s3 asset: %v", err)
	}
	next := *settings
	next.LargeAssets.S3Path.Value = "different-path"

	if err := store.ValidateSettingsUpdate(*settings, next); !errors.Is(err, ErrAssetS3ConfigChangeRequiresLocal) {
		t.Fatalf("ValidateSettingsUpdate error = %v, want ErrAssetS3ConfigChangeRequiresLocal", err)
	}
	store.DB.UpdateAssetVersionLocation(1, localLocation(1))
	if err := store.ValidateSettingsUpdate(*settings, next); err != nil {
		t.Fatalf("ValidateSettingsUpdate with local assets: %v", err)
	}
}
