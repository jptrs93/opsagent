package config

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/secretu"
)

type ConfigKey string

const (
	WebListen                   ConfigKey = "WEB_LISTEN"
	WebHTTPOnly                 ConfigKey = "WEB_HTTP_ONLY"
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

type Service struct {
	Storage *sqlite.PrimaryStorage
	Secrets *secrets.Manager

	mu   sync.Mutex
	subs *pubsubu.PubSub[ainit.DynamicConfiguration]
}

type Update struct {
	Key   ConfigKey
	Value string
}

const (
	githubTokenSecretName                 = "opendeploy.config.github_token"
	backupS3SecretAccessKeySecretName     = "opendeploy.config.backup_s3_secret_access_key"
	largeAssetS3SecretAccessKeySecretName = "opendeploy.config.large_asset_s3_secret_access_key"
	legacyGithubTokenSecretName           = "config.github_token"
	legacyBackupS3SecretAccessKeyName     = "config.backup_s3_secret_access_key"
)

func (s *Service) SnapshotAndSubscribe(filter func(ainit.DynamicConfiguration) bool) *pubsubu.Sub[ainit.DynamicConfiguration] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = &pubsubu.PubSub[ainit.DynamicConfiguration]{}
	}
	s.subs.Notify(s.snapshot())
	return s.subs.Subscribe(filter)
}

func (s *Service) Snapshot() ainit.DynamicConfiguration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Service) UpdateValue(key ConfigKey, value string) error {
	return s.UpdateValues([]Update{{Key: key, Value: value}})
}

func (s *Service) GetMasterPasswordHash() (string, error) {
	value, configured, err := s.Storage.FetchConfigValue(string(MasterPasswordHash))
	if err != nil {
		return "", fmt.Errorf("FetchConfigValue %s: %w", MasterPasswordHash, err)
	}
	if configured {
		return value, nil
	}
	return ainit.StaticConfig.InitialMasterPasswordHash, nil
}

func (s *Service) EnsureInitialMasterPasswordHashPersisted() error {
	if ainit.StaticConfig.InitialMasterPasswordHash == "" {
		return nil
	}
	_, configured, err := s.Storage.FetchConfigValue(string(MasterPasswordHash))
	if err != nil {
		return fmt.Errorf("FetchConfigValue %s: %w", MasterPasswordHash, err)
	}
	if configured {
		return nil
	}
	if err := s.Storage.SetConfigValue(string(MasterPasswordHash), ainit.StaticConfig.InitialMasterPasswordHash); err != nil {
		return fmt.Errorf("SetConfigValue %s: %w", MasterPasswordHash, err)
	}
	return nil
}

func (s *Service) SetMasterPasswordHash(hash string) error {
	if err := s.Storage.SetConfigValue(string(MasterPasswordHash), hash); err != nil {
		return fmt.Errorf("SetConfigValue %s: %w", MasterPasswordHash, err)
	}
	return nil
}

func (s *Service) UpdateValues(updates []Update) error {
	for _, update := range updates {
		if err := s.updateValueWithoutNotify(update.Key, update.Value); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

func (s *Service) updateValueWithoutNotify(key ConfigKey, value string) error {
	if err := s.Storage.SetConfigValue(string(key), value); err != nil {
		return fmt.Errorf("SetConfigValue %s: %w", key, err)
	}
	return nil
}

func IsSecretConfigKey(key ConfigKey) bool {
	return key == GithubToken || key == BackupS3SecretAccessKey || key == LargeAssetS3SecretAccessKey
}

func (s *Service) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs != nil {
		s.subs.Notify(s.snapshot())
	}
}

func (s *Service) snapshot() ainit.DynamicConfiguration {
	static := ainit.StaticConfig
	return ainit.DynamicConfiguration{
		WebListen:                   ptru.NonNil(s.mustLoadValue(WebListen), static.InitialWebListen),
		WebHTTPOnly:                 parseBool(ptru.NonNil(s.mustLoadValue(WebHTTPOnly), strconv.FormatBool(static.InitialWebHTTPOnly)), WebHTTPOnly),
		ClusterListen:               ptru.NonNil(s.mustLoadValue(ClusterListen), static.InitialClusterListen),
		EnrollmentListen:            ptru.NonNil(s.mustLoadValue(EnrollmentListen), static.InitialEnrollmentListen),
		AcmeHosts:                   parseStringList(ptru.NonNil(s.mustLoadValue(AcmeHosts), strings.Join(static.InitialAcmeHosts, ","))),
		AcmeEmail:                   ptru.NonNil(s.mustLoadValue(AcmeEmail), static.InitialAcmeEmail),
		GithubToken:                 s.loadGithubToken(),
		BackupEnabled:               parseBool(ptru.NonNil(s.mustLoadValue(BackupEnabled), "false"), BackupEnabled),
		BackupS3AccessKeyID:         ptru.SafeDref(s.mustLoadValue(BackupS3AccessKeyID)),
		BackupS3SecretAccessKey:     s.loadBackupS3SecretAccessKey(),
		BackupS3Bucket:              ptru.SafeDref(s.mustLoadValue(BackupS3Bucket)),
		BackupS3Path:                ptru.NonNil(s.mustLoadValue(BackupS3Path), "opendeploy/primary"),
		BackupS3Region:              ptru.NonNil(s.mustLoadValue(BackupS3Region), "us-east-1"),
		BackupS3Endpoint:            ptru.SafeDref(s.mustLoadValue(BackupS3Endpoint)),
		LargeAssetS3Enabled:         parseBool(ptru.NonNil(s.mustLoadValue(LargeAssetS3Enabled), "false"), LargeAssetS3Enabled),
		LargeAssetS3AccessKeyID:     ptru.SafeDref(s.mustLoadValue(LargeAssetS3AccessKeyID)),
		LargeAssetS3SecretAccessKey: s.loadLargeAssetS3SecretAccessKey(),
		LargeAssetS3Bucket:          ptru.SafeDref(s.mustLoadValue(LargeAssetS3Bucket)),
		LargeAssetS3Path:            ptru.NonNil(s.mustLoadValue(LargeAssetS3Path), "opendeploy/assets"),
		LargeAssetS3Region:          ptru.NonNil(s.mustLoadValue(LargeAssetS3Region), "us-east-1"),
		LargeAssetS3Endpoint:        ptru.SafeDref(s.mustLoadValue(LargeAssetS3Endpoint)),
	}
}

func (s *Service) mustLoadValue(key ConfigKey) *string {
	value, configured, err := s.Storage.FetchConfigValue(string(key))
	if err != nil {
		panic(fmt.Sprintf("FetchConfigValue %s: %v", key, err))
	}
	if !configured {
		return nil
	}
	return &value
}

func (s *Service) loadGithubToken() secretu.SecretValue {
	return s.loadConfigSecretRef(GithubToken, legacyGithubTokenSecretName)
}

func (s *Service) loadBackupS3SecretAccessKey() secretu.SecretValue {
	return s.loadConfigSecretRef(BackupS3SecretAccessKey, legacyBackupS3SecretAccessKeyName)
}

func (s *Service) loadLargeAssetS3SecretAccessKey() secretu.SecretValue {
	return s.loadConfigSecretRef(LargeAssetS3SecretAccessKey, largeAssetS3SecretAccessKeySecretName)
}

func (s *Service) loadConfigSecretRef(key ConfigKey, legacySecretName string) secretu.SecretValue {
	if s.Secrets == nil {
		return secretu.PlainSecretValue{}
	}
	if ref := strings.TrimSpace(ptru.SafeDref(s.mustLoadValue(key))); ref != "" {
		_, t := s.Secrets.HasSecret(ref)
		return secretu.StoredSecretValue{K: ref, Revealer: s.Secrets, UpdatedAt: t}
	}
	if ok, t := s.Secrets.HasSecret(legacySecretName); ok {
		return secretu.StoredSecretValue{K: legacySecretName, Revealer: s.Secrets, UpdatedAt: t}
	}
	return secretu.PlainSecretValue{}
}

func parseBool(value string, key ConfigKey) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		panic(fmt.Sprintf("parse config value %s as bool: %v", key, err))
	}
	return parsed
}

func parseStringList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
