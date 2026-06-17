package handler

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/util/secretu"
)

func (h *Handler) GetV1Config(ctx apigen.Context) (*apigen.DynamicConfiguration, error) {
	return dynamicConfigToProto(h.ConfigService.Snapshot()), nil
}

func (h *Handler) PostV1ConfigUpdate(ctx apigen.Context, req *apigen.ConfigUpdateRequest) (*apigen.DynamicConfiguration, error) {
	updates, err := validateConfigUpdates(req)
	if err != nil {
		return nil, apigen.NewApiErr(err.Error(), "config_invalid_update", http.StatusBadRequest)
	}
	for _, update := range updates {
		if config.IsSecretConfigKey(update.Key) && strings.TrimSpace(update.Value) != "" {
			if ok, _ := h.Secrets.HasSecret(strings.TrimSpace(update.Value)); !ok {
				return nil, SecretNotFoundErr
			}
		}
	}
	if err := h.ConfigService.UpdateValues(updates); err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return nil, SecretsLockedErr
		}
		return nil, err
	}
	return dynamicConfigToProto(h.ConfigService.Snapshot()), nil
}

func validateConfigUpdates(req *apigen.ConfigUpdateRequest) ([]config.Update, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	seen := map[string]bool{}
	updates := make([]config.Update, 0, len(req.Values))
	for _, value := range req.Values {
		if value == nil {
			return nil, fmt.Errorf("config update contains an empty item")
		}
		key := strings.TrimSpace(value.Key)
		if key == "" {
			return nil, fmt.Errorf("config key is required")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate config key %q", key)
		}
		seen[key] = true
		update, err := validateConfigUpdate(key, value.Value)
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, nil
}

func validateConfigUpdate(key, value string) (config.Update, error) {
	switch key {
	case "WEB_LISTEN":
		return validateListenConfig(config.WebListen, value)
	case "WEB_HTTP_ONLY":
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil {
			return config.Update{}, fmt.Errorf("WEB_HTTP_ONLY must be true or false")
		}
		return config.Update{Key: config.WebHTTPOnly, Value: strings.TrimSpace(value)}, nil
	case "CLUSTER_LISTEN":
		return validateListenConfig(config.ClusterListen, value)
	case "ENROLLMENT_LISTEN":
		return validateListenConfig(config.EnrollmentListen, value)
	case "ACME_HOSTS":
		return config.Update{Key: config.AcmeHosts, Value: strings.Join(parseConfigList(value), ",")}, nil
	case "ACME_EMAIL":
		return config.Update{Key: config.AcmeEmail, Value: strings.TrimSpace(value)}, nil
	case "GITHUB_TOKEN":
		return config.Update{Key: config.GithubToken, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_ENABLED":
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil {
			return config.Update{}, fmt.Errorf("BACKUP_ENABLED must be true or false")
		}
		return config.Update{Key: config.BackupEnabled, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_ACCESS_KEY_ID":
		return config.Update{Key: config.BackupS3AccessKeyID, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_SECRET_ACCESS_KEY":
		return config.Update{Key: config.BackupS3SecretAccessKey, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_BUCKET":
		return config.Update{Key: config.BackupS3Bucket, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_PATH":
		return config.Update{Key: config.BackupS3Path, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_REGION":
		return config.Update{Key: config.BackupS3Region, Value: strings.TrimSpace(value)}, nil
	case "BACKUP_S3_ENDPOINT":
		return config.Update{Key: config.BackupS3Endpoint, Value: strings.TrimSpace(value)}, nil
	default:
		return config.Update{}, fmt.Errorf("unknown config key %q", key)
	}
}

func validateListenConfig(key config.ConfigKey, value string) (config.Update, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return config.Update{}, fmt.Errorf("%s is required", key)
	}
	if _, port, err := net.SplitHostPort(value); err != nil || port == "" {
		return config.Update{}, fmt.Errorf("%s must be a listen address like :8080", key)
	}
	return config.Update{Key: key, Value: value}, nil
}

func parseConfigList(value string) []string {
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

func dynamicConfigToProto(cfg ainit.DynamicConfiguration) *apigen.DynamicConfiguration {
	return &apigen.DynamicConfiguration{
		WebListen:               cfg.WebListen,
		WebHttpOnly:             cfg.WebHTTPOnly,
		ClusterListen:           cfg.ClusterListen,
		EnrollmentListen:        cfg.EnrollmentListen,
		AcmeHosts:               cfg.AcmeHosts,
		AcmeEmail:               cfg.AcmeEmail,
		GithubToken:             secretValueToProto(cfg.GithubToken),
		BackupEnabled:           cfg.BackupEnabled,
		BackupS3AccessKeyID:     cfg.BackupS3AccessKeyID,
		BackupS3SecretAccessKey: secretValueToProto(cfg.BackupS3SecretAccessKey),
		BackupS3Bucket:          cfg.BackupS3Bucket,
		BackupS3Path:            cfg.BackupS3Path,
		BackupS3Region:          cfg.BackupS3Region,
		BackupS3Endpoint:        cfg.BackupS3Endpoint,
	}
}

func secretValueToProto(value secretu.SecretValue) *apigen.SecretValue {
	if value == nil || value.Key() == "" {
		return nil
	}
	return &apigen.SecretValue{Key: value.Key()}
}
