package configmigration

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type ConfigKey string

const (
	WebListen                   ConfigKey = "WEB_LISTEN"
	WebHTTPOnly                 ConfigKey = "WEB_HTTP_ONLY"
	WebHTTPEnabled              ConfigKey = "WEB_HTTP_ENABLED"
	WebHTTPListen               ConfigKey = "WEB_HTTP_LISTEN"
	WebHTTPSEnabled             ConfigKey = "WEB_HTTPS_ENABLED"
	WebHTTPSListen              ConfigKey = "WEB_HTTPS_LISTEN"
	WebTLSSelfManaged           ConfigKey = "WEB_TLS_SELF_MANAGED"
	WebTLSCertPEM               ConfigKey = "WEB_TLS_CERT_PEM"
	ClusterListen               ConfigKey = "CLUSTER_LISTEN"
	EnrollmentListen            ConfigKey = "ENROLLMENT_LISTEN"
	AcmeHosts                   ConfigKey = "ACME_HOSTS"
	AcmeEmail                   ConfigKey = "ACME_EMAIL"
	MasterPasswordHash          ConfigKey = "MASTER_PASSWORD_HASH"
	GithubToken                 ConfigKey = "GITHUB_TOKEN"
	BackupEnabled               ConfigKey = "BACKUP_ENABLED"
	BackupS3AccessKeyID         ConfigKey = "BACKUP_S3_ACCESS_KEY_ID"
	BackupS3SecretAccessKey     ConfigKey = "BACKUP_S3_SECRET_ACCESS_KEY"
	BackupS3Bucket              ConfigKey = "BACKUP_S3_BUCKET"
	BackupS3Path                ConfigKey = "BACKUP_S3_PATH"
	BackupS3Region              ConfigKey = "BACKUP_S3_REGION"
	BackupS3Endpoint            ConfigKey = "BACKUP_S3_ENDPOINT"
	LargeAssetS3Enabled         ConfigKey = "LARGE_ASSET_S3_ENABLED"
	LargeAssetS3AccessKeyID     ConfigKey = "LARGE_ASSET_S3_ACCESS_KEY_ID"
	LargeAssetS3SecretAccessKey ConfigKey = "LARGE_ASSET_S3_SECRET_ACCESS_KEY"
	LargeAssetS3Bucket          ConfigKey = "LARGE_ASSET_S3_BUCKET"
	LargeAssetS3Path            ConfigKey = "LARGE_ASSET_S3_PATH"
	LargeAssetS3Region          ConfigKey = "LARGE_ASSET_S3_REGION"
	LargeAssetS3Endpoint        ConfigKey = "LARGE_ASSET_S3_ENDPOINT"
)

type StoredValue struct {
	Value     string            `json:"value"`
	ConfigRef *apigen.ConfigRef `json:"config_ref,omitempty"`
}

type Update struct {
	Key   ConfigKey
	Value StoredValue
}

func LiteralValue(value string) StoredValue {
	return StoredValue{Value: value}
}

func ConfigRefValue(key string, version int) StoredValue {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return StoredValue{}
	}
	return StoredValue{ConfigRef: &apigen.ConfigRef{Key: trimmed, Version: int32(version)}}
}

func MigrateOldConfig(store *sqlite.PrimaryStorage) error {
	if _, err := store.FetchLatestOpenDeployConfig(); err == nil {
		return nil
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return fmt.Errorf("FetchLatestOpenDeployConfig: %w", err)
	}
	cfg := config.DefaultConfig(ainit.StaticConfig)
	updates, found, err := legacyUpdates(store)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	for _, update := range updates {
		if update.Key == MasterPasswordHash {
			cfg.MasterPasswordHash = update.Value.Value
			continue
		}
		applyUpdate(&cfg.Settings, update)
	}
	if _, err := store.AppendOpenDeploySettings(cfg.Encode()); err != nil {
		return fmt.Errorf("AppendOpenDeploySettings: %w", err)
	}
	return nil
}

func ApplyUpdates(settings *apigen.Settings, updates []Update) {
	for _, update := range updates {
		applyUpdate(settings, update)
	}
}

func applyUpdate(settings *apigen.Settings, update Update) {
	value := normalizeStoredValue(update.Value)
	switch update.Key {
	case WebListen:
		settings.HttpWeb.Listen = toStringSetting(value)
		settings.HttpsWeb.Listen = toStringSetting(value)
	case WebHTTPOnly:
		httpOnly, _ := strconv.ParseBool(strings.TrimSpace(value.Value))
		settings.HttpWeb.Enabled = apigen.BoolSetting{Value: httpOnly}
		settings.HttpsWeb.Enabled = apigen.BoolSetting{Value: !httpOnly}
	case WebHTTPEnabled:
		settings.HttpWeb.Enabled = toBoolSetting(value)
	case WebHTTPListen:
		settings.HttpWeb.Listen = toStringSetting(value)
	case WebHTTPSEnabled:
		settings.HttpsWeb.Enabled = toBoolSetting(value)
	case WebHTTPSListen:
		settings.HttpsWeb.Listen = toStringSetting(value)
	case WebTLSSelfManaged:
		settings.HttpsWeb.TlsSelfManaged = toBoolSetting(value)
	case WebTLSCertPEM:
		settings.HttpsWeb.TlsCertPem = secretRef(value.Value)
	case ClusterListen:
		settings.Cluster.Listen = toStringSetting(value)
	case EnrollmentListen:
		settings.Cluster.EnrollmentListen = toStringSetting(value)
	case AcmeHosts:
		settings.HttpsWeb.AcmeHosts = toStringSetting(value)
	case AcmeEmail:
		settings.HttpsWeb.AcmeEmail = toStringSetting(value)
	case GithubToken:
		settings.Repo.GithubToken = secretRef(value.Value)
	case BackupEnabled:
		settings.Backup.Enabled = toBoolSetting(value)
	case BackupS3AccessKeyID:
		settings.Backup.S3AccessKeyID = toStringSetting(value)
	case BackupS3SecretAccessKey:
		settings.Backup.S3SecretAccessKey = secretRef(value.Value)
	case BackupS3Bucket:
		settings.Backup.S3Bucket = toStringSetting(value)
	case BackupS3Path:
		settings.Backup.S3Path = toStringSetting(value)
	case BackupS3Region:
		settings.Backup.S3Region = toStringSetting(value)
	case BackupS3Endpoint:
		settings.Backup.S3Endpoint = toStringSetting(value)
	case LargeAssetS3Enabled:
		settings.LargeAssets.S3Enabled = toBoolSetting(value)
	case LargeAssetS3AccessKeyID:
		settings.LargeAssets.S3AccessKeyID = toStringSetting(value)
	case LargeAssetS3SecretAccessKey:
		settings.LargeAssets.S3SecretAccessKey = secretRef(value.Value)
	case LargeAssetS3Bucket:
		settings.LargeAssets.S3Bucket = toStringSetting(value)
	case LargeAssetS3Path:
		settings.LargeAssets.S3Path = toStringSetting(value)
	case LargeAssetS3Region:
		settings.LargeAssets.S3Region = toStringSetting(value)
	case LargeAssetS3Endpoint:
		settings.LargeAssets.S3Endpoint = toStringSetting(value)
	}
}

func legacyUpdates(store *sqlite.PrimaryStorage) ([]Update, bool, error) {
	legacyKeys := []struct {
		key       ConfigKey
		legacyKey string
	}{
		{WebListen, string(WebListen)},
		{WebHTTPOnly, string(WebHTTPOnly)},
		{WebHTTPEnabled, string(WebHTTPEnabled)},
		{WebHTTPListen, string(WebHTTPListen)},
		{WebHTTPSEnabled, string(WebHTTPSEnabled)},
		{WebHTTPSListen, string(WebHTTPSListen)},
		{WebTLSSelfManaged, string(WebTLSSelfManaged)},
		{WebTLSCertPEM, string(WebTLSCertPEM)},
		{ClusterListen, string(ClusterListen)},
		{EnrollmentListen, string(EnrollmentListen)},
		{AcmeHosts, string(AcmeHosts)},
		{AcmeEmail, string(AcmeEmail)},
		{MasterPasswordHash, string(MasterPasswordHash)},
		{GithubToken, string(GithubToken)},
		{BackupEnabled, string(BackupEnabled)},
		{BackupS3AccessKeyID, string(BackupS3AccessKeyID)},
		{BackupS3SecretAccessKey, string(BackupS3SecretAccessKey)},
		{BackupS3Bucket, string(BackupS3Bucket)},
		{BackupS3Path, string(BackupS3Path)},
		{BackupS3Region, string(BackupS3Region)},
		{BackupS3Endpoint, string(BackupS3Endpoint)},
		{LargeAssetS3Enabled, string(LargeAssetS3Enabled)},
		{LargeAssetS3AccessKeyID, string(LargeAssetS3AccessKeyID)},
		{LargeAssetS3SecretAccessKey, string(LargeAssetS3SecretAccessKey)},
		{LargeAssetS3Bucket, string(LargeAssetS3Bucket)},
		{LargeAssetS3Path, string(LargeAssetS3Path)},
		{LargeAssetS3Region, string(LargeAssetS3Region)},
		{LargeAssetS3Endpoint, string(LargeAssetS3Endpoint)},
	}
	updates := make([]Update, 0, len(legacyKeys))
	found := false
	for _, item := range legacyKeys {
		value, configured, err := store.FetchLegacySystemConfigValue(item.legacyKey)
		if err != nil {
			return nil, false, fmt.Errorf("FetchLegacySystemConfigValue %s: %w", item.legacyKey, err)
		}
		if !configured {
			continue
		}
		updates = append(updates, Update{Key: item.key, Value: LiteralValue(value)})
		found = true
	}
	return updates, found, nil
}

func normalizeStoredValue(value StoredValue) StoredValue {
	if value.ConfigRef == nil || strings.TrimSpace(value.ConfigRef.Key) == "" {
		return LiteralValue(value.Value)
	}
	return ConfigRefValue(strings.TrimSpace(value.ConfigRef.Key), int(value.ConfigRef.Version))
}

func toStringSetting(value StoredValue) apigen.StringSetting {
	if value.ConfigRef != nil {
		return apigen.StringSetting{ConfigRef: apigen.ConfigRef{Key: value.ConfigRef.Key, Version: value.ConfigRef.Version}}
	}
	return apigen.StringSetting{Value: value.Value}
}

func toBoolSetting(value StoredValue) apigen.BoolSetting {
	if value.ConfigRef != nil {
		return apigen.BoolSetting{ConfigRef: apigen.ConfigRef{Key: value.ConfigRef.Key, Version: value.ConfigRef.Version}}
	}
	parsed, _ := strconv.ParseBool(strings.TrimSpace(value.Value))
	return apigen.BoolSetting{Value: parsed}
}

func secretRef(key string) apigen.SecretRef {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return apigen.SecretRef{}
	}
	return apigen.SecretRef{Key: trimmed}
}
