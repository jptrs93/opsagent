package handler

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
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/secrets"
)

func (h *Handler) GetV1Settings(ctx apigen.Context) (*apigen.Settings, error) {
	return ptru.To(h.ConfigService.Snapshot().Settings), nil
}

func (h *Handler) PutV1Settings(ctx apigen.Context, req *apigen.Settings) (*apigen.Settings, error) {
	stored, resolved, err := validateSettings(req, func(ref *apigen.ConfigRef) (string, bool, error) {
		if ref == nil {
			return "", false, nil
		}
		value, ok := h.Store.ResolveConfigByName(strings.TrimSpace(ref.Key))
		return value, ok, nil
	})
	if err != nil {
		return nil, apigen.NewApiErr(err.Error(), "settings_invalid", http.StatusBadRequest)
	}
	for _, key := range []string{
		stored.HttpsWeb.TlsCertPem.Key,
		stored.Repo.GithubToken.Key,
		stored.Backup.S3SecretAccessKey.Key,
		stored.LargeAssets.S3SecretAccessKey.Key,
	} {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if ok, _ := h.Secrets.HasSecret(strings.TrimSpace(key)); !ok {
			return nil, SecretNotFoundErr
		}
	}
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
		return nil, err
	}
	return stored, nil
}

func validateSettings(req *apigen.Settings, resolveRef func(*apigen.ConfigRef) (string, bool, error)) (*apigen.Settings, *apigen.Settings, error) {
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
	if err := resolveBoolInPlace(&stored.LargeAssets.S3Enabled, &resolved.LargeAssets.S3Enabled, "large_assets.s3_enabled", resolveRef); err != nil {
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
	if strings.TrimSpace(stored.ConfigRef.Key) == "" {
		return nil
	}
	value, ok, err := resolveRef(&stored.ConfigRef)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s config ref %q was not found", field, stored.ConfigRef.Key)
	}
	resolved.Value = value
	return nil
}

func resolveBoolInPlace(stored, resolved *apigen.BoolSetting, field string, resolveRef func(*apigen.ConfigRef) (string, bool, error)) error {
	if stored == nil || resolved == nil {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(stored.ConfigRef.Key) == "" {
		return nil
	}
	value, ok, err := resolveRef(&stored.ConfigRef)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s config ref %q was not found", field, stored.ConfigRef.Key)
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s referenced config must be true or false", field)
	}
	resolved.Value = parsed
	return nil
}

func validateResolvedSettings(settings *apigen.Settings) error {
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

func (h *Handler) validateWebTLSCert(settings *apigen.Settings) error {
	tlsSelfManaged := settings.HttpsWeb.TlsSelfManaged.Value
	key := settings.HttpsWeb.TlsCertPem.Key
	if !tlsSelfManaged || strings.TrimSpace(key) == "" {
		return nil
	}
	bundle, err := h.Secrets.Reveal(key)
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
		return fmt.Errorf("https_web.tls_cert_pem must contain a PEM certificate chain and private key: %w", err)
	}
	return nil
}
