package backup

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

type testSecretStore struct {
	updated map[int32]time.Time
}

type testConfigLoader struct{}

func (testConfigLoader) MustLoadConfigStringValue(v apigen.StringSetting) string { return v.Value }
func (testConfigLoader) MustLoadConfigBoolValue(v apigen.BoolSetting) bool       { return v.Value }

func (s testSecretStore) MetaByID(id int32) (secrets.Meta, bool) {
	updated, ok := s.updated[id]
	return secrets.Meta{ID: id, CreatedAt: updated}, ok
}

func (s testSecretStore) RevealByID(id int32) ([]byte, error) {
	return []byte("secret"), nil
}

func configWithSettings(settings *apigen.Settings) apigen.Config {
	return apigen.Config{Settings: *settings}
}

func TestBackupConfigFilterOnlyAllowsBackupChanges(t *testing.T) {
	secrets := testSecretStore{updated: map[int32]time.Time{10: time.Unix(1, 0)}}
	filter := newBackupConfigFilter(testConfigLoader{}, secrets)
	initial := config.DefaultSettings(ainit.StaticConfig)
	initial.HttpWeb.Listen = apigen.StringSetting{Value: ":8080"}
	initial.Backup.Enabled = apigen.BoolSetting{Value: true}
	initial.Backup.S3AccessKeyID = apigen.StringSetting{Value: "access-key"}
	initial.Backup.S3SecretAccessKey = apigen.SecretRef{ID: 10}
	initial.Backup.S3Bucket = apigen.StringSetting{Value: "bucket"}
	initial.Backup.S3Path = apigen.StringSetting{Value: "path"}
	initial.Backup.S3Region = apigen.StringSetting{Value: "region"}
	initial.Backup.S3Endpoint = apigen.StringSetting{Value: "endpoint"}
	initial.LargeAssets.S3Enabled = apigen.BoolSetting{Value: false}
	initial.LargeAssets.S3AccessKeyID = apigen.StringSetting{Value: "unrelated"}
	initial.LargeAssets.S3SecretAccessKey = apigen.SecretRef{ID: 11}
	filter.SetInitial(configWithSettings(initial))

	unrelatedValue := *initial
	unrelated := &unrelatedValue
	unrelated.HttpWeb.Listen.Value = ":9090"
	unrelated.LargeAssets.S3Enabled.Value = true
	unrelated.LargeAssets.S3AccessKeyID.Value = "changed"
	if filter.Filter(configWithSettings(initial), configWithSettings(unrelated)) {
		t.Fatal("unrelated config change passed backup filter")
	}

	backupChangedValue := *unrelated
	backupChanged := &backupChangedValue
	backupChanged.Backup.S3Bucket.Value = "new-bucket"
	if !filter.Filter(configWithSettings(unrelated), configWithSettings(backupChanged)) {
		t.Fatal("backup config change did not pass backup filter")
	}

	secretChangedValue := *backupChanged
	secretChanged := &secretChangedValue
	filter.secrets = testSecretStore{updated: map[int32]time.Time{10: time.Unix(2, 0)}}
	if !filter.Filter(configWithSettings(backupChanged), configWithSettings(secretChanged)) {
		t.Fatal("backup secret change did not pass backup filter")
	}
}

func TestBackupConfigFilterIgnoresBackupSettingsWhileDisabled(t *testing.T) {
	filter := newBackupConfigFilter(testConfigLoader{}, nil)
	initial := config.DefaultSettings(ainit.StaticConfig)
	initial.Backup.Enabled = apigen.BoolSetting{Value: false}
	initial.Backup.S3AccessKeyID = apigen.StringSetting{Value: "access-key"}
	initial.Backup.S3Bucket = apigen.StringSetting{Value: "bucket"}
	filter.SetInitial(configWithSettings(initial))

	changedWhileDisabledValue := *initial
	changedWhileDisabled := &changedWhileDisabledValue
	changedWhileDisabled.Backup.S3AccessKeyID.Value = "new-access-key"
	changedWhileDisabled.Backup.S3Bucket.Value = "new-bucket"
	if filter.Filter(configWithSettings(initial), configWithSettings(changedWhileDisabled)) {
		t.Fatal("disabled backup settings change passed backup filter")
	}

	enabledValue := *changedWhileDisabled
	enabled := &enabledValue
	enabled.Backup.Enabled.Value = true
	if !filter.Filter(configWithSettings(changedWhileDisabled), configWithSettings(enabled)) {
		t.Fatal("backup enable did not pass backup filter")
	}
}
