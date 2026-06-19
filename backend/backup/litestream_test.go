package backup

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/util/secretu"
)

func TestBackupConfigFilterOnlyAllowsBackupChanges(t *testing.T) {
	filter := newBackupConfigFilter()
	initial := ainit.DynamicConfiguration{
		WebListen:                   ":8080",
		BackupEnabled:               true,
		BackupS3AccessKeyID:         "access-key",
		BackupS3SecretAccessKey:     secretu.PlainSecretValue{K: "backup-secret", UpdatedAt: time.Unix(1, 0)},
		BackupS3Bucket:              "bucket",
		BackupS3Path:                "path",
		BackupS3Region:              "region",
		BackupS3Endpoint:            "endpoint",
		LargeAssetS3Enabled:         false,
		LargeAssetS3AccessKeyID:     "unrelated",
		LargeAssetS3SecretAccessKey: secretu.PlainSecretValue{K: "large-asset-secret", UpdatedAt: time.Unix(1, 0)},
	}
	filter.SetInitial(initial)

	unrelated := initial
	unrelated.WebListen = ":9090"
	unrelated.LargeAssetS3Enabled = true
	unrelated.LargeAssetS3AccessKeyID = "changed"
	if filter.Filter(unrelated) {
		t.Fatal("unrelated config change passed backup filter")
	}

	backupChanged := unrelated
	backupChanged.BackupS3Bucket = "new-bucket"
	if !filter.Filter(backupChanged) {
		t.Fatal("backup config change did not pass backup filter")
	}

	secretChanged := backupChanged
	secretChanged.BackupS3SecretAccessKey = secretu.PlainSecretValue{K: "backup-secret", UpdatedAt: time.Unix(2, 0)}
	if !filter.Filter(secretChanged) {
		t.Fatal("backup secret change did not pass backup filter")
	}
}

func TestBackupConfigFilterIgnoresBackupSettingsWhileDisabled(t *testing.T) {
	filter := newBackupConfigFilter()
	initial := ainit.DynamicConfiguration{
		BackupEnabled:       false,
		BackupS3AccessKeyID: "access-key",
		BackupS3Bucket:      "bucket",
	}
	filter.SetInitial(initial)

	changedWhileDisabled := initial
	changedWhileDisabled.BackupS3AccessKeyID = "new-access-key"
	changedWhileDisabled.BackupS3Bucket = "new-bucket"
	if filter.Filter(changedWhileDisabled) {
		t.Fatal("disabled backup settings change passed backup filter")
	}

	enabled := changedWhileDisabled
	enabled.BackupEnabled = true
	if !filter.Filter(enabled) {
		t.Fatal("backup enable did not pass backup filter")
	}
}
