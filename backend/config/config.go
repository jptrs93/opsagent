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

type configKey string

const (
	WebListen               configKey = "WEB_LISTEN"
	WebHTTPOnly             configKey = "WEB_HTTP_ONLY"
	ClusterListen           configKey = "CLUSTER_LISTEN"
	EnrollmentListen        configKey = "ENROLLMENT_LISTEN"
	AcmeHosts               configKey = "ACME_HOSTS"
	AcmeEmail               configKey = "ACME_EMAIL"
	GithubToken             configKey = "GITHUB_TOKEN"
	BackupS3AccessKeyID     configKey = "BACKUP_S3_ACCESS_KEY_ID"
	BackupS3SecretAccessKey configKey = "BACKUP_S3_SECRET_ACCESS_KEY"
	BackupS3Bucket          configKey = "BACKUP_S3_BUCKET"
	BackupS3Path            configKey = "BACKUP_S3_PATH"
	BackupS3Region          configKey = "BACKUP_S3_REGION"
	BackupS3Endpoint        configKey = "BACKUP_S3_ENDPOINT"
)

type Service struct {
	Storage *sqlite.PrimaryStorage
	Secrets *secrets.Manager

	mu   sync.Mutex
	subs *pubsubu.PubSub[ainit.DynamicConfiguration]
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

func (s *Service) UpdateValue(key configKey, value string) {
	switch key {
	case GithubToken:
		s.updateConfigSecretValue(githubTokenSecretName, value)
		s.notify()
		return
	case BackupS3SecretAccessKey:
		s.updateConfigSecretValue(backupS3SecretAccessKeySecretName, value)
		s.notify()
		return
	}
	if err := s.Storage.SetConfigValue(string(key), value); err != nil {
		panic(fmt.Sprintf("SetConfigValue %s: %v", key, err))
	}
	s.notify()
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

func (s *Service) mustLoadValue(key configKey) *string {
	value, configured, err := s.Storage.FetchConfigValue(string(key))
	if err != nil {
		panic(fmt.Sprintf("FetchConfigValue %s: %v", key, err))
	}
	if !configured {
		return nil
	}
	return &value
}

func (s *Service) updateConfigSecretValue(secretName, value string) {
	if value == "" {
		if err := s.Secrets.Delete(secretName); err != nil {
			panic(fmt.Sprintf("DeleteSecret %s: %v", secretName, err))
		}
		return
	}
	if _, err := s.Secrets.Set(secretName, configSecretGroup, []byte(value), 0); err != nil {
		panic(fmt.Sprintf("SetSecret %s: %v", secretName, err))
	}
}

func (s *Service) loadGithubToken() secretu.SecretValue {
	if !s.Secrets.HasSecret(githubTokenSecretName) {
		return secretu.PlainSecretValue{}
	}
	return secretu.StoredSecretValue{K: githubTokenSecretName, Revealer: s.Secrets}
}

func (s *Service) loadBackupS3SecretAccessKey() secretu.SecretValue {
	if !s.Secrets.HasSecret(backupS3SecretAccessKeySecretName) {
		return secretu.PlainSecretValue{}
	}
	return secretu.StoredSecretValue{K: backupS3SecretAccessKeySecretName, Revealer: s.Secrets}
}

func parseBool(value string, key configKey) bool {
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
