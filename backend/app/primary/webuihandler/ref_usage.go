package webuihandler

import "github.com/jptrs93/opsagent/backend/apigen"

var ReferenceInUseErr = apigen.NewApiErr("Secret or config is still in use", "reference_in_use", 400)

func (h *Handler) deploymentUsesSecretID(ids map[int32]struct{}) bool {
	for _, item := range h.Store.FetchDeploymentSnapshot("") {
		if item.Config.Deleted {
			continue
		}
		for _, value := range item.Config.Spec.Runner.Container.EnvVars {
			if value != nil && value.SecretID != nil {
				if _, ok := ids[*value.SecretID]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) deploymentUsesConfigID(ids map[int32]struct{}) bool {
	for _, item := range h.Store.FetchDeploymentSnapshot("") {
		if item.Config.Deleted {
			continue
		}
		for _, value := range item.Config.Spec.Runner.Container.EnvVars {
			if value != nil && value.ConfigID != nil {
				if _, ok := ids[*value.ConfigID]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) settingsUseSecretID(ids map[int32]struct{}) bool {
	if h.ConfigService == nil {
		return false
	}
	settings := h.ConfigService.Snapshot().Settings
	for _, id := range []int32{
		settings.HttpsWeb.TlsCertPem.ID,
		settings.Repo.GithubToken.ID,
		settings.Backup.S3SecretAccessKey.ID,
		settings.LargeAssets.S3SecretAccessKey.ID,
	} {
		if _, ok := ids[id]; ok {
			return true
		}
	}
	return false
}

func (h *Handler) settingsUseConfigID(ids map[int32]struct{}) bool {
	if h.ConfigService == nil {
		return false
	}
	settings := h.ConfigService.Snapshot().Settings
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
		settings.LargeAssets.S3Enabled.ConfigRef,
		settings.LargeAssets.S3AccessKeyID.ConfigRef,
		settings.LargeAssets.S3Bucket.ConfigRef,
		settings.LargeAssets.S3Path.ConfigRef,
		settings.LargeAssets.S3Region.ConfigRef,
		settings.LargeAssets.S3Endpoint.ConfigRef,
	}
	for _, ref := range refs {
		if _, ok := ids[ref.ID]; ok {
			return true
		}
	}
	return false
}

func int32Set(ids []int32) map[int32]struct{} {
	out := make(map[int32]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}
