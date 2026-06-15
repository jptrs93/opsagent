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
	WebListen               ConfigKey = "WEB_LISTEN"
	WebHTTPOnly             ConfigKey = "WEB_HTTP_ONLY"
	ClusterListen           ConfigKey = "CLUSTER_LISTEN"
	EnrollmentListen        ConfigKey = "ENROLLMENT_LISTEN"
	AcmeHosts               ConfigKey = "ACME_HOSTS"
	AcmeEmail               ConfigKey = "ACME_EMAIL"
	MasterPasswordHash      ConfigKey = "MASTER_PASSWORD_HASH"
	GithubToken             ConfigKey = "GITHUB_TOKEN"
	BackupS3AccessKeyID     ConfigKey = "BACKUP_S3_ACCESS_KEY_ID"
	BackupS3SecretAccessKey ConfigKey = "BACKUP_S3_SECRET_ACCESS_KEY"
	BackupS3Bucket          ConfigKey = "BACKUP_S3_BUCKET"
	BackupS3Path            ConfigKey = "BACKUP_S3_PATH"
	BackupS3Region          ConfigKey = "BACKUP_S3_REGION"
	BackupS3Endpoint        ConfigKey = "BACKUP_S3_ENDPOINT"
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
	configSecretGroup                 = "config"
	githubTokenSecretName             = "config.github_token"
	backupS3SecretAccessKeySecretName = "config.backup_s3_secret_access_key"
)

func (s *Service) SnapshotAndSubscribe() *pubsubu.Sub[ainit.DynamicConfiguration] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = &pubsubu.PubSub[ainit.DynamicConfiguration]{}
	}
	s.subs.Notify(s.snapshot())
	return s.subs.Subscribe(nil)
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

func (s *Service) SetMasterPasswordHash(hash string) error {
	if err := s.Storage.SetConfigValue(string(MasterPasswordHash), hash); err != nil {
		return fmt.Errorf("SetConfigValue %s: %w", MasterPasswordHash, err)
	}
	return nil
}

func (s *Service) UpdateValues(updates []Update) error {
	for _, update := range updates {
		if isSecretConfigKey(update.Key) && update.Value != "" {
			unlocked, _ := s.Secrets.Status()
			if !unlocked {
				return secrets.ErrLocked
			}
		}
	}
	for _, update := range updates {
		if err := s.updateValueWithoutNotify(update.Key, update.Value); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

func (s *Service) updateValueWithoutNotify(key ConfigKey, value string) error {
	switch key {
	case GithubToken:
		return s.updateConfigSecretValue(githubTokenSecretName, value)
	case BackupS3SecretAccessKey:
		return s.updateConfigSecretValue(backupS3SecretAccessKeySecretName, value)
	}
	if err := s.Storage.SetConfigValue(string(key), value); err != nil {
		return fmt.Errorf("SetConfigValue %s: %w", key, err)
	}
	return nil
}

func isSecretConfigKey(key ConfigKey) bool {
	return key == GithubToken || key == BackupS3SecretAccessKey
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
		WebListen:               ptru.NonNil(s.mustLoadValue(WebListen), static.InitialWebListen),
		WebHTTPOnly:             parseBool(ptru.NonNil(s.mustLoadValue(WebHTTPOnly), strconv.FormatBool(static.InitialWebHTTPOnly)), WebHTTPOnly),
		ClusterListen:           ptru.NonNil(s.mustLoadValue(ClusterListen), static.InitialClusterListen),
		EnrollmentListen:        ptru.NonNil(s.mustLoadValue(EnrollmentListen), static.InitialEnrollmentListen),
		AcmeHosts:               parseStringList(ptru.NonNil(s.mustLoadValue(AcmeHosts), strings.Join(static.InitialAcmeHosts, ","))),
		AcmeEmail:               ptru.NonNil(s.mustLoadValue(AcmeEmail), static.InitialAcmeEmail),
		GithubToken:             s.loadGithubToken(),
		BackupS3AccessKeyID:     ptru.SafeDref(s.mustLoadValue(BackupS3AccessKeyID)),
		BackupS3SecretAccessKey: s.loadBackupS3SecretAccessKey(),
		BackupS3Bucket:          ptru.SafeDref(s.mustLoadValue(BackupS3Bucket)),
		BackupS3Path:            ptru.NonNil(s.mustLoadValue(BackupS3Path), "opendeploy/primary"),
		BackupS3Region:          ptru.NonNil(s.mustLoadValue(BackupS3Region), "us-east-1"),
		BackupS3Endpoint:        ptru.SafeDref(s.mustLoadValue(BackupS3Endpoint)),
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

func (s *Service) updateConfigSecretValue(secretName, value string) error {
	if value == "" {
		if err := s.Secrets.Delete(secretName); err != nil {
			return fmt.Errorf("DeleteSecret %s: %w", secretName, err)
		}
		return nil
	}
	if _, err := s.Secrets.Set(secretName, configSecretGroup, []byte(value), 0); err != nil {
		return fmt.Errorf("SetSecret %s: %w", secretName, err)
	}
	return nil
}

func (s *Service) loadGithubToken() secretu.SecretValue {
	if ok, t := s.Secrets.HasSecret(githubTokenSecretName); !ok {
		return secretu.PlainSecretValue{}
	} else {
		return secretu.StoredSecretValue{K: githubTokenSecretName, Revealer: s.Secrets, UpdatedAt: t}
	}
}

func (s *Service) loadBackupS3SecretAccessKey() secretu.SecretValue {
	if ok, t := s.Secrets.HasSecret(backupS3SecretAccessKeySecretName); !ok {
		return secretu.PlainSecretValue{}
	} else {
		return secretu.StoredSecretValue{K: backupS3SecretAccessKeySecretName, Revealer: s.Secrets, UpdatedAt: t}
	}
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
