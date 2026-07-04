package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Service struct {
	Storage        *sqlite.PrimaryStorage
	SecretResolver SecretRefResolver
	Subs           *pubsubu.PubSub[apigen.Config]
}

type SecretRefResolver interface {
	LatestSecretIDByName(name string) (int32, bool)
}

type Loader interface {
	MustLoadConfigStringValue(v apigen.StringSetting) string
	MustLoadConfigBoolValue(v apigen.BoolSetting) bool
}

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
			S3Enabled:         apigen.BoolSetting{Value: false},
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

func NewService(store *sqlite.PrimaryStorage, secretResolvers ...SecretRefResolver) (*Service, error) {
	var secretResolver SecretRefResolver
	if len(secretResolvers) > 0 {
		secretResolver = secretResolvers[0]
	}
	s := &Service{Storage: store, SecretResolver: secretResolver, Subs: &pubsubu.PubSub[apigen.Config]{}}
	cfg, err := s.loadOrInitConfig()
	if err != nil {
		return nil, err
	}
	s.Subs.Notify(cfg)
	return s, nil
}

func (s *Service) SnapshotAndSubscribe(filter func(a, b apigen.Config) bool) *pubsubu.Sub[apigen.Config] {
	return s.Subs.Subscribe(filter)
}

func (s *Service) Snapshot() apigen.Config {
	return s.Subs.Value()
}

func (s *Service) loadOrInitConfig() (apigen.Config, error) {
	var res apigen.Config
	r, err := s.Storage.FetchLatestOpenDeployConfig()
	if err != nil {
		if errors.Is(err, sqlite.ErrNotFound) {
			cfg := DefaultConfig(ainit.StaticConfig)
			if _, err := s.Storage.AppendOpenDeploySettings(cfg.Encode()); err != nil {
				return res, fmt.Errorf("AppendOpenDeploySettings: %w", err)
			}
			return normalizeConfig(*cfg), nil
		} else {
			return res, fmt.Errorf("FetchLatestOpenDeployConfig: %w", err)
		}
	}
	cfg, err := apigen.DecodeConfig(r.ConfigBlob)
	if err != nil {
		return res, fmt.Errorf("DecodeConfig: %w", err)
	}
	cfg = ptrConfig(normalizeConfig(*cfg))
	changed, err := s.hydrateSettingsRefIDs(&cfg.Settings)
	if err != nil {
		return res, err
	}
	if changed {
		if _, err := s.Storage.AppendOpenDeploySettings(cfg.Encode()); err != nil {
			return res, fmt.Errorf("AppendOpenDeploySettings migrated ref IDs: %w", err)
		}
	}
	return *cfg, nil
}

func (s *Service) UpdateSettings(settings apigen.Settings) error {
	settings = NormalizeSettings(settings)
	if _, err := s.hydrateSettingsRefIDs(&settings); err != nil {
		return err
	}
	current := s.Snapshot()
	cfg := apigen.Config{Settings: settings, MasterPasswordHash: current.MasterPasswordHash}
	return s.saveAndNotify(cfg)
}

func (s *Service) saveAndNotify(cfg apigen.Config) error {
	cfg = normalizeConfig(cfg)
	if _, err := s.Storage.AppendOpenDeploySettings(cfg.Encode()); err != nil {
		return fmt.Errorf("AppendOpenDeploySettings: %w", err)
	}
	s.Subs.Notify(cfg)
	return nil
}

func (s *Service) GetMasterPasswordHash() (string, error) {
	return s.Snapshot().MasterPasswordHash, nil
}

func (s *Service) SetMasterPasswordHash(hash string) error {
	cfg := s.Snapshot()
	cfg.MasterPasswordHash = hash
	return s.saveAndNotify(cfg)
}

func (s *Service) MustLoadConfigStringValue(v apigen.StringSetting) string {
	return erru.Must(s.LoadConfigStringValue(v))
}

func (s *Service) MustLoadConfigBoolValue(v apigen.BoolSetting) bool {
	return erru.Must(s.LoadConfigBoolValue(v))
}

func (s *Service) LoadConfigStringValue(v apigen.StringSetting) (string, error) {
	if v.ConfigRef.ID == 0 {
		if strings.TrimSpace(v.ConfigRef.Key) != "" {
			return "", fmt.Errorf("config ref %q has no id", v.ConfigRef.Key)
		}
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
		if strings.TrimSpace(v.ConfigRef.Key) != "" {
			return false, fmt.Errorf("config ref %q has no id", v.ConfigRef.Key)
		}
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

func ptrConfig(cfg apigen.Config) *apigen.Config { return &cfg }

func (s *Service) hydrateSettingsRefIDs(settings *apigen.Settings) (bool, error) {
	if settings == nil {
		return false, nil
	}
	changed := false
	for _, ref := range configRefs(settings) {
		if ref == nil || ref.ID != 0 || strings.TrimSpace(ref.Key) == "" {
			continue
		}
		cfg, ok := s.Storage.GetLatestUserConfig(strings.TrimSpace(ref.Key))
		if !ok {
			return false, fmt.Errorf("config ref %q was not found", ref.Key)
		}
		ref.ID = cfg.ID
		ref.Key = cfg.Name
		changed = true
	}
	if s.SecretResolver != nil {
		for _, ref := range secretRefs(settings) {
			if ref == nil || ref.ID != 0 || strings.TrimSpace(ref.Key) == "" {
				continue
			}
			id, ok := s.SecretResolver.LatestSecretIDByName(strings.TrimSpace(ref.Key))
			if !ok {
				return false, fmt.Errorf("secret ref %q was not found", ref.Key)
			}
			ref.ID = id
			changed = true
		}
	}
	return changed, nil
}

func configRefs(settings *apigen.Settings) []*apigen.ConfigRef {
	return []*apigen.ConfigRef{
		&settings.HttpWeb.Enabled.ConfigRef,
		&settings.HttpWeb.Listen.ConfigRef,
		&settings.HttpsWeb.Enabled.ConfigRef,
		&settings.HttpsWeb.Listen.ConfigRef,
		&settings.HttpsWeb.TlsSelfManaged.ConfigRef,
		&settings.HttpsWeb.AcmeHosts.ConfigRef,
		&settings.HttpsWeb.AcmeEmail.ConfigRef,
		&settings.Cluster.Listen.ConfigRef,
		&settings.Cluster.EnrollmentListen.ConfigRef,
		&settings.Backup.Enabled.ConfigRef,
		&settings.Backup.S3AccessKeyID.ConfigRef,
		&settings.Backup.S3Bucket.ConfigRef,
		&settings.Backup.S3Path.ConfigRef,
		&settings.Backup.S3Region.ConfigRef,
		&settings.Backup.S3Endpoint.ConfigRef,
		&settings.LargeAssets.S3Enabled.ConfigRef,
		&settings.LargeAssets.S3AccessKeyID.ConfigRef,
		&settings.LargeAssets.S3Bucket.ConfigRef,
		&settings.LargeAssets.S3Path.ConfigRef,
		&settings.LargeAssets.S3Region.ConfigRef,
		&settings.LargeAssets.S3Endpoint.ConfigRef,
	}
}

func secretRefs(settings *apigen.Settings) []*apigen.SecretRef {
	return []*apigen.SecretRef{
		&settings.HttpsWeb.TlsCertPem,
		&settings.Repo.GithubToken,
		&settings.Backup.S3SecretAccessKey,
		&settings.LargeAssets.S3SecretAccessKey,
	}
}
