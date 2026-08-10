package webuihandler

import "github.com/jptrs93/opsagent/backend/apigen"

var ReferenceInUseErr = apigen.NewApiErr("Referenced value is still in use", "reference_in_use", 400)

func (h *Handler) deploymentUsesSecretID(ids map[int32]struct{}) bool {
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.Deleted {
			continue
		}
		container := cfg.Spec.Container()
		if container == nil {
			continue
		}
		for _, value := range container.Runtime.EnvVars {
			if value != nil && value.SecretVersionID != nil {
				if _, ok := ids[*value.SecretVersionID]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) deploymentUsesConfigID(ids map[int32]struct{}) bool {
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.Deleted {
			continue
		}
		container := cfg.Spec.Container()
		if container == nil {
			continue
		}
		for _, value := range container.Runtime.EnvVars {
			if value != nil && value.ConfigVersionID != nil {
				if _, ok := ids[*value.ConfigVersionID]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) deploymentUsesAddressID(ids map[int32]struct{}) bool {
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.Deleted {
			continue
		}
		container := cfg.Spec.Container()
		if container == nil {
			continue
		}
		for _, value := range container.Runtime.EnvVars {
			if value == nil || value.AddressDeploymentID == nil {
				continue
			}
			if _, ok := ids[*value.AddressDeploymentID]; ok {
				return true
			}
		}
	}
	return false
}

func (h *Handler) settingsUseSecretID(ids map[int32]struct{}) bool {
	if h.ConfigService == nil {
		return false
	}
	for _, settings := range h.settingsForReferenceChecks() {
		for _, id := range []int32{
			settings.HttpsWeb.TlsCertPem.VersionID,
			settings.Repo.GithubToken.VersionID,
			settings.Backup.S3SecretAccessKey.VersionID,
			settings.LargeAssets.S3SecretAccessKey.VersionID,
		} {
			if _, ok := ids[id]; ok {
				return true
			}
		}
	}
	return false
}

func (h *Handler) settingsUseConfigID(ids map[int32]struct{}) bool {
	if h.ConfigService == nil {
		return false
	}
	for _, settings := range h.settingsForReferenceChecks() {
		refs := []apigen.ConfigRef{
			settings.HttpWeb.Enabled.ConfigRef,
			settings.HttpWeb.Listen.ConfigRef,
			settings.HttpsWeb.Enabled.ConfigRef,
			settings.HttpsWeb.Listen.ConfigRef,
			settings.HttpsWeb.TlsSelfManaged.ConfigRef,
			settings.HttpsWeb.AcmeHosts.ConfigRef,
			settings.HttpsWeb.AcmeEmail.ConfigRef,
			settings.Cluster.Listen.ConfigRef,
			settings.Cluster.EnrollmentListen.ConfigRef,
			settings.Backup.Enabled.ConfigRef,
			settings.Backup.S3AccessKeyID.ConfigRef,
			settings.Backup.S3Bucket.ConfigRef,
			settings.Backup.S3Path.ConfigRef,
			settings.Backup.S3Region.ConfigRef,
			settings.Backup.S3Endpoint.ConfigRef,
			settings.LargeAssets.UseSeparateS3.ConfigRef,
			settings.LargeAssets.S3AccessKeyID.ConfigRef,
			settings.LargeAssets.S3Bucket.ConfigRef,
			settings.LargeAssets.S3Path.ConfigRef,
			settings.LargeAssets.S3Region.ConfigRef,
			settings.LargeAssets.S3Endpoint.ConfigRef,
		}
		for _, ref := range refs {
			if _, ok := ids[ref.VersionID]; ok {
				return true
			}
		}
	}
	return false
}

func (h *Handler) settingsForReferenceChecks() []apigen.ClusterSettings {
	settings := []apigen.ClusterSettings{h.ConfigService.Snapshot().Settings}
	migration, ok := h.Store.GetUnfinishedAssetMigration()
	if !ok {
		return settings
	}
	for _, versionID := range []int64{migration.OldConfigVersionID, migration.NewConfigVersionID} {
		row, err := h.Store.FetchOpenDeployConfigByID(versionID)
		if err != nil {
			continue
		}
		cfg, err := apigen.DecodePrimaryConfig(row.ConfigBlob)
		if err == nil {
			settings = append(settings, cfg.Settings)
		}
	}
	return settings
}

func int32Set(ids []int32) map[int32]struct{} {
	out := make(map[int32]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}
