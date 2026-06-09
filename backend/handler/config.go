package handler

import (
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/util/secretu"
)

func (h *Handler) GetV1Config(ctx apigen.Context) (*apigen.DynamicConfiguration, error) {
	return dynamicConfigToProto(h.ConfigService.Snapshot()), nil
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
