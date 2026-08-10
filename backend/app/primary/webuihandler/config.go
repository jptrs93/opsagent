package webuihandler

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func (h *Handler) PostV1ClusterSettingsGet(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.ClusterSettings, error) {
	return ptru.To(h.ConfigService.Snapshot().Settings), nil
}

func (h *Handler) PostV1ClusterSettingsUpdate(ctx apigen.Context, req *apigen.ClusterSettings) (*apigen.ClusterSettings, error) {
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	stored, resolved, err := validateSettings(req, func(ref *apigen.ConfigRef) (string, bool, error) {
		if ref == nil {
			return "", false, nil
		}
		if ref.VersionID == 0 {
			return "", false, nil
		}
		cfg, ok := h.Store.GetConfigVersionByID(ref.VersionID)
		if !ok {
			return "", false, nil
		}
		value := cfg.Value
		return value, ok, nil
	})
	if err != nil {
		return nil, apigen.NewApiErr(err.Error(), "settings_invalid", http.StatusBadRequest)
	}
	for _, ref := range settingsSecretRefs(stored) {
		if err := h.validateSecretRef(ref); err != nil {
			return nil, err
		}
	}
	resolved.HttpsWeb.TlsCertPem = stored.HttpsWeb.TlsCertPem
	if err := h.validateWebTLSCert(resolved); err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return nil, SecretsLockedErr
		}
		if errors.Is(err, secrets.ErrNotFound) {
			return nil, SecretNotFoundErr
		}
		return nil, apigen.NewApiErr(err.Error(), "settings_invalid", http.StatusBadRequest)
	}
	if err := h.ConfigService.UpdateSettings(*stored); err != nil {
		if errors.Is(err, sqlite.ErrAssetMigrationInProgress) {
			return nil, apigen.NewApiErr(
				"Wait for the current large asset migration to finish before changing settings",
				"asset_migration_in_progress",
				http.StatusConflict,
			)
		}
		if errors.Is(err, assetstore.ErrAssetS3ConfigChangeRequiresLocal) {
			return nil, apigen.NewApiErr(
				"Disable Backup and wait for large assets to migrate locally before changing the large asset S3 configuration",
				"settings_invalid",
				http.StatusBadRequest,
			)
		}
		return nil, err
	}
	return stored, nil
}

func validateSettings(req *apigen.ClusterSettings, resolveRef func(*apigen.ConfigRef) (string, bool, error)) (*apigen.ClusterSettings, *apigen.ClusterSettings, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("settings are required")
	}
	stored := config.NormalizeSettings(*req)
	resolved := *req
	resolved = stored
	if err := resolveStringInPlace(&stored.HttpsWeb.Listen, &resolved.HttpsWeb.Listen, "https_web.listen", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.HttpWeb.Listen, &resolved.HttpWeb.Listen, "http_web.listen", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveBoolInPlace(&stored.HttpsWeb.Enabled, &resolved.HttpsWeb.Enabled, "https_web.enabled", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveBoolInPlace(&stored.HttpWeb.Enabled, &resolved.HttpWeb.Enabled, "http_web.enabled", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveBoolInPlace(&stored.HttpsWeb.TlsSelfManaged, &resolved.HttpsWeb.TlsSelfManaged, "https_web.tls_self_managed", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.HttpsWeb.AcmeHosts, &resolved.HttpsWeb.AcmeHosts, "https_web.acme_hosts", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.HttpsWeb.AcmeEmail, &resolved.HttpsWeb.AcmeEmail, "https_web.acme_email", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Cluster.Listen, &resolved.Cluster.Listen, "cluster.listen", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Cluster.EnrollmentListen, &resolved.Cluster.EnrollmentListen, "cluster.enrollment_listen", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveBoolInPlace(&stored.Backup.Enabled, &resolved.Backup.Enabled, "backup.enabled", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Backup.S3AccessKeyID, &resolved.Backup.S3AccessKeyID, "backup.s3_access_key_id", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Backup.S3Bucket, &resolved.Backup.S3Bucket, "backup.s3_bucket", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Backup.S3Path, &resolved.Backup.S3Path, "backup.s3_path", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Backup.S3Region, &resolved.Backup.S3Region, "backup.s3_region", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.Backup.S3Endpoint, &resolved.Backup.S3Endpoint, "backup.s3_endpoint", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveBoolInPlace(&stored.LargeAssets.UseSeparateS3, &resolved.LargeAssets.UseSeparateS3, "large_assets.use_separate_s3", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.LargeAssets.S3AccessKeyID, &resolved.LargeAssets.S3AccessKeyID, "large_assets.s3_access_key_id", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.LargeAssets.S3Bucket, &resolved.LargeAssets.S3Bucket, "large_assets.s3_bucket", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.LargeAssets.S3Path, &resolved.LargeAssets.S3Path, "large_assets.s3_path", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.LargeAssets.S3Region, &resolved.LargeAssets.S3Region, "large_assets.s3_region", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := resolveStringInPlace(&stored.LargeAssets.S3Endpoint, &resolved.LargeAssets.S3Endpoint, "large_assets.s3_endpoint", resolveRef); err != nil {
		return nil, nil, err
	}
	if err := validateResolvedSettings(&resolved); err != nil {
		return nil, nil, err
	}
	return &stored, &resolved, nil
}

func resolveStringInPlace(stored, resolved *apigen.StringSetting, field string, resolveRef func(*apigen.ConfigRef) (string, bool, error)) error {
	if stored == nil || resolved == nil {
		return fmt.Errorf("%s is required", field)
	}
	if stored.ConfigRef.VersionID == 0 {
		return nil
	}
	value, ok, err := resolveRef(&stored.ConfigRef)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s config ref was not found", field)
	}
	resolved.Value = value
	return nil
}

func resolveBoolInPlace(stored, resolved *apigen.BoolSetting, field string, resolveRef func(*apigen.ConfigRef) (string, bool, error)) error {
	if stored == nil || resolved == nil {
		return fmt.Errorf("%s is required", field)
	}
	if stored.ConfigRef.VersionID == 0 {
		return nil
	}
	value, ok, err := resolveRef(&stored.ConfigRef)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s config ref was not found", field)
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s referenced config must be true or false", field)
	}
	resolved.Value = parsed
	return nil
}

func validateResolvedSettings(settings *apigen.ClusterSettings) error {
	httpEnabled := settings.HttpWeb.Enabled.Value
	httpsEnabled := settings.HttpsWeb.Enabled.Value
	httpListen := settings.HttpWeb.Listen.Value
	httpsListen := settings.HttpsWeb.Listen.Value
	if !httpEnabled && !httpsEnabled {
		return fmt.Errorf("at least one of http_web.enabled or https_web.enabled must be true")
	}
	if httpEnabled {
		if err := validateListenValue("http_web.listen", httpListen); err != nil {
			return err
		}
	}
	if httpsEnabled {
		if err := validateListenValue("https_web.listen", httpsListen); err != nil {
			return err
		}
	}
	if httpEnabled && httpsEnabled && strings.TrimSpace(httpListen) == strings.TrimSpace(httpsListen) {
		return fmt.Errorf("http_web.listen and https_web.listen must differ when both servers are enabled")
	}
	if err := validateListenValue("cluster.listen", settings.Cluster.Listen.Value); err != nil {
		return err
	}
	if err := validateListenValue("cluster.enrollment_listen", settings.Cluster.EnrollmentListen.Value); err != nil {
		return err
	}
	return nil
}

func validateListenValue(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if _, port, err := net.SplitHostPort(value); err != nil || port == "" {
		return fmt.Errorf("%s must be a listen address like :8080", field)
	}
	return nil
}

func (h *Handler) validateWebTLSCert(settings *apigen.ClusterSettings) error {
	tlsSelfManaged := settings.HttpsWeb.TlsSelfManaged.Value
	id := settings.HttpsWeb.TlsCertPem.VersionID
	if !tlsSelfManaged {
		return nil
	}
	if id == 0 {
		_, err := certu.BootstrapWebUISelfSigned(h.Secrets, certu.WebUISelfSignedNames(settings.HttpsWeb.AcmeHosts.Value, settings.HttpsWeb.Listen.Value))
		return err
	}
	bundle, err := h.Secrets.RevealByID(id)
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
		return fmt.Errorf("https_web.tls_cert_pem must contain a PEM certificate chain and private key: %w", err)
	}
	return nil
}

func settingsSecretRefs(settings *apigen.ClusterSettings) []*apigen.SecretRef {
	return []*apigen.SecretRef{
		&settings.HttpsWeb.TlsCertPem,
		&settings.Repo.GithubToken,
		&settings.Backup.S3SecretAccessKey,
		&settings.LargeAssets.S3SecretAccessKey,
	}
}

func (h *Handler) validateSecretRef(ref *apigen.SecretRef) error {
	if ref == nil || ref.VersionID == 0 {
		return nil
	}
	if _, ok := h.Secrets.MetaByID(ref.VersionID); !ok {
		return SecretNotFoundErr
	}
	return nil
}
