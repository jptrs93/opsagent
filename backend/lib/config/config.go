package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Service struct {
	Storage                *sqlite.PrimaryStorage
	Subs                   *pubsubu.PubSub[apigen.Config]
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

type InitialConfigHook func(*apigen.Config) error

func DefaultSettings(static ainit.StaticConfiguration) *apigen.Settings {
	return &apigen.Settings{
		HttpWeb: apigen.HttpWebSettings{
			Enabled: apigen.BoolSetting{Value: static.InitialWebHTTPEnabled},
			Listen:  apigen.StringSetting{Value: static.InitialWebHTTPListen},
		},
		HttpsWeb: apigen.HttpsWebSettings{
			Enabled:        apigen.BoolSetting{Value: static.InitialWebHTTPSEnabled},
			Listen:         apigen.StringSetting{Value: static.InitialWebHTTPSListen},
			TlsSelfManaged: apigen.BoolSetting{Value: static.InitialWebTLSSelfManaged},
			TlsCertPem:     apigen.SecretRef{},
			AcmeHosts:      apigen.StringSetting{Value: strings.Join(static.InitialAcmeHosts, ",")},
			AcmeEmail:      apigen.StringSetting{Value: static.InitialAcmeEmail},
		},
		Cluster: apigen.ClusterSettings{
			Listen:           apigen.StringSetting{Value: static.InitialClusterListen},
			EnrollmentListen: apigen.StringSetting{Value: static.InitialEnrollmentListen},
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

func DefaultConfig(static ainit.StaticConfiguration) *apigen.Config {
	return &apigen.Config{
		Settings:           *DefaultSettings(static),
		MasterPasswordHash: static.InitialMasterPasswordHash,
	}
}

func NewService(store *sqlite.PrimaryStorage) (*Service, error) {
	return NewServiceWithInitialConfigHook(store, nil)
}

func NewServiceWithInitialConfigHook(store *sqlite.PrimaryStorage, hook InitialConfigHook) (*Service, error) {
	s := &Service{
		Storage:       store,
		Subs:          &pubsubu.PubSub[apigen.Config]{},
		migrationWake: make(chan struct{}, 1),
	}
	cfg, versionID, err := s.loadOrInitConfig(hook)
	if err != nil {
		return nil, err
	}
	// The virtual network ULA prefix is generated exactly once per cluster and
	// is immutable thereafter (addresses are pure functions of it).
	if len(cfg.NetworkUlaPrefix) == 0 {
		cfg.NetworkUlaPrefix = network.GeneratePrefix().Bytes()
		versionID, err = s.Storage.AppendOpenDeploySettings(cfg.Encode())
		if err != nil {
			return nil, fmt.Errorf("persisting generated network ULA prefix: %w", err)
		}
	}
	s.versionID = versionID
	s.Subs.Notify(cfg)
	return s, nil
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

func (s *Service) Snapshot() apigen.Config {
	return s.Subs.Value()
}

func (s *Service) loadOrInitConfig(hook InitialConfigHook) (apigen.Config, int64, error) {
	var res apigen.Config
	r, err := s.Storage.FetchLatestOpenDeployConfig()
	if err != nil {
		if errors.Is(err, sqlite.ErrNotFound) {
			cfg := DefaultConfig(ainit.StaticConfig)
			if hook != nil {
				if hookErr := hook(cfg); hookErr != nil {
					return res, 0, fmt.Errorf("initial config hook: %w", hookErr)
				}
			}
			id, appendErr := s.Storage.AppendOpenDeploySettings(cfg.Encode())
			if appendErr != nil {
				return res, 0, fmt.Errorf("AppendOpenDeploySettings: %w", appendErr)
			}
			return normalizeConfig(*cfg), id, nil
		} else {
			return res, 0, fmt.Errorf("FetchLatestOpenDeployConfig: %w", err)
		}
	}
	cfg, err := apigen.DecodeConfig(r.ConfigBlob)
	if err != nil {
		return res, 0, fmt.Errorf("DecodeConfig: %w", err)
	}
	return normalizeConfig(*cfg), r.ID, nil
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
	s.Subs.Notify(cfg)
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
	s.Subs.Notify(cfg)
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
