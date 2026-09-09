package systemconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type Service struct {
	Storage                *state.Service
	Subs                   *pubsubu.PubSub[apigen.SystemConfig]
	VersionedSubs          *pubsubu.PubSub[apigen.SystemConfigVersion]
	AssetOperationMu       sync.Locker
	ValidateSettingsUpdate func(current, next apigen.ClusterSettings) error
	mu                     sync.Mutex
	versionID              int64
	migrationWake          chan struct{}
}

type Loader interface {
	MustLoadStringSetting(v apigen.StringSetting) string
	MustLoadBoolSetting(v apigen.BoolSetting) bool
}

type Initial struct {
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
	// PasswordLoginEnabled opts the Web UI into username/password login next
	// to passkeys. Off by default; local and evaluation installs turn it on.
	PasswordLoginEnabled bool
}

func DefaultInitial() Initial {
	return Initial{
		WebHTTPListen:    ":8080",
		WebHTTPSEnabled:  true,
		WebHTTPSListen:   ":443",
		AcmeHosts:        []string{"opendeploy.example.com"},
		ClusterListen:    ":9443",
		EnrollmentListen: ":9444",
	}
}

func DefaultSettings(initial Initial) *apigen.ClusterSettings {
	return &apigen.ClusterSettings{
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
		Cluster: apigen.ClusterListenSettings{
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
		Auth: apigen.AuthSettings{
			PasswordLoginEnabled: apigen.BoolSetting{Value: initial.PasswordLoginEnabled},
		},
	}
}

func NormalizeSettings(settings apigen.ClusterSettings) apigen.ClusterSettings {
	return settings
}

func normalizeConfig(cfg apigen.SystemConfig) apigen.SystemConfig {
	cfg.Settings = NormalizeSettings(cfg.Settings)
	return cfg
}

func Default(initial Initial) *apigen.SystemConfig {
	return &apigen.SystemConfig{
		Settings:           *DefaultSettings(initial),
		MasterPasswordHash: initial.MasterPasswordHash,
	}
}

func NewService(store *state.Service) (*Service, error) {
	s := &Service{
		Storage:       store,
		Subs:          &pubsubu.PubSub[apigen.SystemConfig]{},
		VersionedSubs: &pubsubu.PubSub[apigen.SystemConfigVersion]{},
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
func InitializeService(store *state.Service, cfg apigen.SystemConfig) (*Service, error) {
	if _, err := LatestRevision(store.Queries()); err == nil {
		return nil, fmt.Errorf("primary config is already initialized")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("checking existing primary config: %w", err)
	}
	if len(cfg.NetworkUlaPrefix) == 0 {
		cfg.NetworkUlaPrefix = network.GeneratePrefix().Bytes()
	}
	if _, err := network.ParsePrefix(cfg.NetworkUlaPrefix); err != nil {
		return nil, fmt.Errorf("initial network ULA prefix is invalid: %w", err)
	}
	if _, err := AppendRevision(store, cfg.Encode()); err != nil {
		return nil, fmt.Errorf("persisting initial primary config: %w", err)
	}
	return NewService(store)
}

// NetworkPrefix returns the cluster's ULA /48 prefix.
func (s *Service) NetworkPrefix() network.Prefix {
	p, err := network.ParsePrefix(s.Snapshot().NetworkUlaPrefix)
	if err != nil {
		panic(fmt.Sprintf("stored network ULA prefix is invalid: %v", err))
	}
	return p
}

func (s *Service) SnapshotAndSubscribe(filter func(a, b apigen.SystemConfig) bool) *pubsubu.Sub[apigen.SystemConfig] {
	return s.Subs.Subscribe(filter)
}

func (s *Service) VersionedSnapshotAndSubscribe() *pubsubu.Sub[apigen.SystemConfigVersion] {
	return s.VersionedSubs.Subscribe(nil)
}

func (s *Service) Snapshot() apigen.SystemConfig {
	return s.Subs.Value()
}

func (s *Service) loadConfig() (apigen.SystemConfig, pq.SystemConfigRevision, error) {
	var res apigen.SystemConfig
	r, err := LatestRevision(s.Storage.Queries())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, pq.SystemConfigRevision{}, fmt.Errorf("primary config is not initialized")
		} else {
			return res, pq.SystemConfigRevision{}, fmt.Errorf("LatestRevision: %w", err)
		}
	}
	cfg, err := apigen.DecodeSystemConfig(r.ConfigBlob)
	if err != nil {
		return res, pq.SystemConfigRevision{}, fmt.Errorf("DecodeConfig: %w", err)
	}
	return normalizeConfig(*cfg), r, nil
}

func (s *Service) publishConfig(cfg apigen.SystemConfig, version int64, updatedAt time.Time) {
	s.Subs.Notify(cfg)
	cfg.MasterPasswordHash = ""
	s.VersionedSubs.Notify(apigen.SystemConfigVersion{
		Version:   version,
		UpdatedAt: updatedAt,
		Config:    cfg,
	})
}

func (s *Service) UpdateSettings(settings apigen.ClusterSettings, inlockValidate pq.Validator) error {
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
	oldBackupEnabled, err := s.LoadBoolSetting(cfg.Settings.Backup.Enabled)
	if err != nil {
		return fmt.Errorf("load current Backup.Enabled: %w", err)
	}
	newBackupEnabled, err := s.LoadBoolSetting(settings.Backup.Enabled)
	if err != nil {
		return fmt.Errorf("load new Backup.Enabled: %w", err)
	}
	cfg.Settings = settings
	cfg = normalizeConfig(cfg)
	versionID, migration, err := AppendRevisionWithAssetMigration(s.Storage, cfg.Encode(), oldBackupEnabled != newBackupEnabled, inlockValidate)
	if err != nil {
		return err
	}
	s.versionID = versionID
	row, err := s.Storage.Queries().GetConfigByID(context.Background(), versionID)
	if err != nil {
		panic(fmt.Sprintf("GetConfigByID after settings update: %v", err))
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

func (s *Service) saveAndNotifyLocked(cfg apigen.SystemConfig) error {
	cfg = normalizeConfig(cfg)
	versionID, err := AppendRevision(s.Storage, cfg.Encode())
	if err != nil {
		return fmt.Errorf("AppendRevision: %w", err)
	}
	s.versionID = versionID
	row, err := s.Storage.Queries().GetConfigByID(context.Background(), versionID)
	if err != nil {
		panic(fmt.Sprintf("GetConfigByID after config update: %v", err))
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

func (s *Service) UpdateSettingsInternal(settings apigen.ClusterSettings) error {
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

func (s *Service) MustLoadStringSetting(v apigen.StringSetting) string {
	return erru.Must(s.LoadStringSetting(v))
}

func (s *Service) MustLoadBoolSetting(v apigen.BoolSetting) bool {
	return erru.Must(s.LoadBoolSetting(v))
}

func (s *Service) LoadStringSetting(v apigen.StringSetting) (string, error) {
	if v.ConfigRef.VersionID == 0 {
		return v.Value, nil
	}
	if s == nil || s.Storage == nil {
		return "", fmt.Errorf("config storage is not configured")
	}
	ref, ok := values.GetConfigVersion(s.Storage.Queries(), v.ConfigRef.VersionID)
	if !ok {
		return "", fmt.Errorf("config ref id %d was not found", v.ConfigRef.VersionID)
	}
	return ref.Value, nil
}

func (s *Service) LoadBoolSetting(v apigen.BoolSetting) (bool, error) {
	if v.ConfigRef.VersionID == 0 {
		return v.Value, nil
	}
	value, err := s.LoadStringSetting(apigen.StringSetting{ConfigRef: v.ConfigRef})
	if err != nil {
		return false, err
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("config ref id %d must resolve to true or false", v.ConfigRef.VersionID)
	}
	return parsed, nil
}
