package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Service struct {
	Storage                *sqlite.PrimaryStorage
	Subs                   *pubsubu.PubSub[apigen.Config]
	VersionedSubs          *pubsubu.PubSub[apigen.ConfigVersion]
	AssetOperationMu       sync.Locker
	ValidateSettingsUpdate func(current, next apigen.Settings) error
	mu                     sync.Mutex
	referenceMu            sync.Mutex
	versionID              int64
	migrationWake          chan struct{}
}

type Loader interface {
	MustLoadConfigStringValue(v apigen.StringSetting) string
	MustLoadConfigBoolValue(v apigen.BoolSetting) bool
}

type InitialConfig struct {
	WebHTTPEnabled     bool
	WebHTTPListen      string
	WebHTTPSEnabled    bool
	WebHTTPSListen     string
	WebTLSSelfManaged  bool
	AcmeHosts          []string
	AcmeEmail          string
	ClusterListen      string
	EnrollmentListen   string
	MasterPasswordHash string
}

func DefaultInitialConfig() InitialConfig {
	return InitialConfig{
		WebHTTPListen:    ":8080",
		WebHTTPSEnabled:  true,
		WebHTTPSListen:   ":443",
		AcmeHosts:        []string{"opendeploy.example.com"},
		ClusterListen:    ":9443",
		EnrollmentListen: ":9444",
	}
}

func DefaultSettings(initial InitialConfig) *apigen.Settings {
	return &apigen.Settings{
		HttpWeb: apigen.HttpWebSettings{
			Enabled: apigen.BoolSetting{Value: initial.WebHTTPEnabled},
			Listen:  apigen.StringSetting{Value: initial.WebHTTPListen},
		},
		HttpsWeb: apigen.HttpsWebSettings{
			Enabled:        apigen.BoolSetting{Value: initial.WebHTTPSEnabled},
			Listen:         apigen.StringSetting{Value: initial.WebHTTPSListen},
			TlsSelfManaged: apigen.BoolSetting{Value: initial.WebTLSSelfManaged},
			TlsCertPem:     apigen.SecretRef{},
			AcmeHosts:      apigen.StringSetting{Value: strings.Join(initial.AcmeHosts, ",")},
			AcmeEmail:      apigen.StringSetting{Value: initial.AcmeEmail},
		},
		Cluster: apigen.ClusterSettings{
			Listen:           apigen.StringSetting{Value: initial.ClusterListen},
			EnrollmentListen: apigen.StringSetting{Value: initial.EnrollmentListen},
		},
		Repo: apigen.RepoSettings{
			GithubToken: apigen.SecretRef{},
		},
		Backup: apigen.BackupSettings{
			Enabled:           apigen.BoolSetting{Value: false},
			S3AccessKeyID:     apigen.StringSetting{Value: ""},
			S3SecretAccessKey: apigen.SecretRef{},
			S3Bucket:          apigen.StringSetting{Value: ""},
			S3Path:            apigen.StringSetting{Value: "opendeploy/primary"},
			S3Region:          apigen.StringSetting{Value: "us-east-1"},
			S3Endpoint:        apigen.StringSetting{Value: ""},
		},
		LargeAssets: apigen.LargeAssetsSettings{
			UseSeparateS3:     apigen.BoolSetting{Value: false},
			S3AccessKeyID:     apigen.StringSetting{Value: ""},
			S3SecretAccessKey: apigen.SecretRef{},
			S3Bucket:          apigen.StringSetting{Value: ""},
			S3Path:            apigen.StringSetting{Value: "opendeploy/assets"},
			S3Region:          apigen.StringSetting{Value: "us-east-1"},
			S3Endpoint:        apigen.StringSetting{Value: ""},
		},
	}
}

func NormalizeSettings(settings apigen.Settings) apigen.Settings {
	return settings
}

func normalizeConfig(cfg apigen.Config) apigen.Config {
	cfg.Settings = NormalizeSettings(cfg.Settings)
	return cfg
}

func DefaultConfig(initial InitialConfig) *apigen.Config {
	return &apigen.Config{
		Settings:           *DefaultSettings(initial),
		MasterPasswordHash: initial.MasterPasswordHash,
	}
}

func NewService(store *sqlite.PrimaryStorage) (*Service, error) {
	s := &Service{
		Storage:       store,
		Subs:          &pubsubu.PubSub[apigen.Config]{},
		VersionedSubs: &pubsubu.PubSub[apigen.ConfigVersion]{},
		migrationWake: make(chan struct{}, 1),
	}
	cfg, row, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	if _, err := network.ParsePrefix(cfg.NetworkUlaPrefix); err != nil {
		return nil, fmt.Errorf("stored network ULA prefix is invalid: %w", err)
	}
	s.versionID = row.ID
	s.publishConfig(cfg, row.ID, time.UnixMilli(row.UpdatedAt))
	return s, nil
}

// InitializeService persists the first primary config. Normal primary startup
// uses NewService and therefore never invents missing cluster configuration.
func InitializeService(store *sqlite.PrimaryStorage, cfg apigen.Config) (*Service, error) {
	if _, err := store.FetchLatestOpenDeployConfig(); err == nil {
		return nil, fmt.Errorf("primary config is already initialized")
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return nil, fmt.Errorf("checking existing primary config: %w", err)
	}
	if len(cfg.NetworkUlaPrefix) == 0 {
		cfg.NetworkUlaPrefix = network.GeneratePrefix().Bytes()
	}
	if _, err := network.ParsePrefix(cfg.NetworkUlaPrefix); err != nil {
		return nil, fmt.Errorf("initial network ULA prefix is invalid: %w", err)
	}
	if _, err := store.AppendOpenDeploySettings(cfg.Encode()); err != nil {
		return nil, fmt.Errorf("persisting initial primary config: %w", err)
	}
	return NewService(store)
}

// MigrateLegacyInitialConfig applies bootstrap-era defaults that older primary
// databases may not contain. It is called by installer upgrade/restore, never
// by normal primary startup.
func MigrateLegacyInitialConfig(store *sqlite.PrimaryStorage) error {
	r, err := store.FetchLatestOpenDeployConfig()
	if err != nil {
		return err
	}
	cfg, err := apigen.DecodeConfig(r.ConfigBlob)
	if err != nil {
		return fmt.Errorf("decoding primary config for migration: %w", err)
	}
	if len(cfg.NetworkUlaPrefix) != 0 {
		return nil
	}
	cfg.NetworkUlaPrefix = network.GeneratePrefix().Bytes()
	if _, err := store.AppendOpenDeploySettings(cfg.Encode()); err != nil {
		return fmt.Errorf("persisting migrated network ULA prefix: %w", err)
	}
	return nil
}

// NetworkPrefix returns the cluster's ULA /48 prefix.
func (s *Service) NetworkPrefix() network.Prefix {
	p, err := network.ParsePrefix(s.Snapshot().NetworkUlaPrefix)
	if err != nil {
		panic(fmt.Sprintf("stored network ULA prefix is invalid: %v", err))
	}
	return p
}

func (s *Service) SnapshotAndSubscribe(filter func(a, b apigen.Config) bool) *pubsubu.Sub[apigen.Config] {
	return s.Subs.Subscribe(filter)
}

func (s *Service) VersionedSnapshotAndSubscribe() *pubsubu.Sub[apigen.ConfigVersion] {
	return s.VersionedSubs.Subscribe(nil)
}

func (s *Service) Snapshot() apigen.Config {
	return s.Subs.Value()
}

func (s *Service) loadConfig() (apigen.Config, sqlite.SystemConfigRevision, error) {
	var res apigen.Config
	r, err := s.Storage.FetchLatestOpenDeployConfig()
	if err != nil {
		if errors.Is(err, sqlite.ErrNotFound) {
			return res, sqlite.SystemConfigRevision{}, fmt.Errorf("primary config is not initialized")
		} else {
			return res, sqlite.SystemConfigRevision{}, fmt.Errorf("FetchLatestOpenDeployConfig: %w", err)
		}
	}
	cfg, err := apigen.DecodeConfig(r.ConfigBlob)
	if err != nil {
		return res, sqlite.SystemConfigRevision{}, fmt.Errorf("DecodeConfig: %w", err)
	}
	return normalizeConfig(*cfg), r, nil
}

func (s *Service) publishConfig(cfg apigen.Config, version int64, updatedAt time.Time) {
	s.Subs.Notify(cfg)
	cfg.MasterPasswordHash = ""
	s.VersionedSubs.Notify(apigen.ConfigVersion{
		Version:   version,
		UpdatedAt: updatedAt,
		Config:    cfg,
	})
}

func (s *Service) UpdateSettings(settings apigen.Settings) error {
	if s.AssetOperationMu != nil {
		s.AssetOperationMu.Lock()
		defer s.AssetOperationMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings = NormalizeSettings(settings)
	cfg := s.Snapshot()
	if s.ValidateSettingsUpdate != nil {
		if err := s.ValidateSettingsUpdate(cfg.Settings, settings); err != nil {
			return err
		}
	}
	oldBackupEnabled, err := s.LoadConfigBoolValue(cfg.Settings.Backup.Enabled)
	if err != nil {
		return fmt.Errorf("load current Backup.Enabled: %w", err)
	}
	newBackupEnabled, err := s.LoadConfigBoolValue(settings.Backup.Enabled)
	if err != nil {
		return fmt.Errorf("load new Backup.Enabled: %w", err)
	}
	cfg.Settings = settings
	cfg = normalizeConfig(cfg)
	versionID, migration, err := s.Storage.AppendOpenDeploySettingsWithAssetMigration(cfg.Encode(), oldBackupEnabled != newBackupEnabled)
	if err != nil {
		return err
	}
	s.versionID = versionID
	row, err := s.Storage.FetchOpenDeployConfigByID(versionID)
	if err != nil {
		panic(fmt.Sprintf("FetchOpenDeployConfigByID after settings update: %v", err))
	}
	s.publishConfig(cfg, versionID, time.UnixMilli(row.UpdatedAt))
	if migration != nil {
		select {
		case s.migrationWake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *Service) saveAndNotifyLocked(cfg apigen.Config) error {
	cfg = normalizeConfig(cfg)
	versionID, err := s.Storage.AppendOpenDeploySettings(cfg.Encode())
	if err != nil {
		return fmt.Errorf("AppendOpenDeploySettings: %w", err)
	}
	s.versionID = versionID
	row, err := s.Storage.FetchOpenDeployConfigByID(versionID)
	if err != nil {
		panic(fmt.Sprintf("FetchOpenDeployConfigByID after config update: %v", err))
	}
	s.publishConfig(cfg, versionID, time.UnixMilli(row.UpdatedAt))
	return nil
}

func (s *Service) VersionID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.versionID
}

func (s *Service) AssetMigrationWake() <-chan struct{} {
	return s.migrationWake
}

func (s *Service) LockReferences() func() {
	s.referenceMu.Lock()
	return s.referenceMu.Unlock
}

func (s *Service) UpdateSettingsInternal(settings apigen.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.Snapshot()
	cfg.Settings = NormalizeSettings(settings)
	return s.saveAndNotifyLocked(cfg)
}

func (s *Service) GetMasterPasswordHash() (string, error) {
	return s.Snapshot().MasterPasswordHash, nil
}

func (s *Service) SetMasterPasswordHash(hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.Snapshot()
	cfg.MasterPasswordHash = hash
	return s.saveAndNotifyLocked(cfg)
}

func (s *Service) MustLoadConfigStringValue(v apigen.StringSetting) string {
	return erru.Must(s.LoadConfigStringValue(v))
}

func (s *Service) MustLoadConfigBoolValue(v apigen.BoolSetting) bool {
	return erru.Must(s.LoadConfigBoolValue(v))
}

func (s *Service) LoadConfigStringValue(v apigen.StringSetting) (string, error) {
	if v.ConfigRef.ID == 0 {
		return v.Value, nil
	}
	if s == nil || s.Storage == nil {
		return "", fmt.Errorf("config storage is not configured")
	}
	value, ok := s.Storage.ResolveConfig(v.ConfigRef.ID)
	if !ok {
		return "", fmt.Errorf("config ref id %d was not found", v.ConfigRef.ID)
	}
	return value, nil
}

func (s *Service) LoadConfigBoolValue(v apigen.BoolSetting) (bool, error) {
	if v.ConfigRef.ID == 0 {
		return v.Value, nil
	}
	value, err := s.LoadConfigStringValue(apigen.StringSetting{ConfigRef: v.ConfigRef})
	if err != nil {
		return false, err
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("config ref id %d must resolve to true or false", v.ConfigRef.ID)
	}
	return parsed, nil
}
