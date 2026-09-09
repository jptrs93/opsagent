package webuihandler

import (
	"context"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) settingsUseSecretID(ids map[int32]struct{}) bool {
	return len(h.settingsSecretRefDetails(ids)) > 0
}

func (h *Handler) settingsUseConfigID(ids map[int32]struct{}) bool {
	return len(h.settingsConfigRefDetails(ids)) > 0
}

// settingsSecretRefDetails renders "cluster settings (<field>)" lines for
// every settings field pinning one of ids, deduped across the live settings
// and any unfinished asset-migration snapshots.
func (h *Handler) settingsSecretRefDetails(ids map[int32]struct{}) []string {
	if h.SystemConfig == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, settings := range h.settingsForReferenceChecks() {
		refs := []struct {
			field     string
			versionID int32
		}{
			{"HTTPS web TLS certificate", settings.HttpsWeb.TlsCertPem.VersionID},
			{"GitHub token", settings.Repo.GithubToken.VersionID},
			{"backup S3 secret access key", settings.Backup.S3SecretAccessKey.VersionID},
			{"large-assets S3 secret access key", settings.LargeAssets.S3SecretAccessKey.VersionID},
		}
		for _, ref := range refs {
			if _, ok := ids[ref.versionID]; ok && !seen[ref.field] {
				seen[ref.field] = true
				out = append(out, "cluster settings ("+ref.field+")")
			}
		}
	}
	return out
}

// settingsConfigRefDetails is settingsSecretRefDetails for config references.
func (h *Handler) settingsConfigRefDetails(ids map[int32]struct{}) []string {
	if h.SystemConfig == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, settings := range h.settingsForReferenceChecks() {
		refs := []struct {
			field string
			ref   apigen.ConfigRef
		}{
			{"HTTP web enabled", settings.HttpWeb.Enabled.ConfigRef},
			{"HTTP web listen", settings.HttpWeb.Listen.ConfigRef},
			{"HTTPS web enabled", settings.HttpsWeb.Enabled.ConfigRef},
			{"HTTPS web listen", settings.HttpsWeb.Listen.ConfigRef},
			{"HTTPS web self-managed TLS", settings.HttpsWeb.TlsSelfManaged.ConfigRef},
			{"HTTPS web ACME hosts", settings.HttpsWeb.AcmeHosts.ConfigRef},
			{"HTTPS web ACME email", settings.HttpsWeb.AcmeEmail.ConfigRef},
			{"password login enabled", settings.Auth.PasswordLoginEnabled.ConfigRef},
			{"cluster listen", settings.Cluster.Listen.ConfigRef},
			{"cluster enrollment listen", settings.Cluster.EnrollmentListen.ConfigRef},
			{"backup enabled", settings.Backup.Enabled.ConfigRef},
			{"backup S3 access key id", settings.Backup.S3AccessKeyID.ConfigRef},
			{"backup S3 bucket", settings.Backup.S3Bucket.ConfigRef},
			{"backup S3 path", settings.Backup.S3Path.ConfigRef},
			{"backup S3 region", settings.Backup.S3Region.ConfigRef},
			{"backup S3 endpoint", settings.Backup.S3Endpoint.ConfigRef},
			{"large-assets separate S3", settings.LargeAssets.UseSeparateS3.ConfigRef},
			{"large-assets S3 access key id", settings.LargeAssets.S3AccessKeyID.ConfigRef},
			{"large-assets S3 bucket", settings.LargeAssets.S3Bucket.ConfigRef},
			{"large-assets S3 path", settings.LargeAssets.S3Path.ConfigRef},
			{"large-assets S3 region", settings.LargeAssets.S3Region.ConfigRef},
			{"large-assets S3 endpoint", settings.LargeAssets.S3Endpoint.ConfigRef},
		}
		for _, ref := range refs {
			if _, ok := ids[ref.ref.VersionID]; ok && !seen[ref.field] {
				seen[ref.field] = true
				out = append(out, "cluster settings ("+ref.field+")")
			}
		}
	}
	return out
}

func (h *Handler) settingsForReferenceChecks() []apigen.ClusterSettings {
	settings := []apigen.ClusterSettings{h.SystemConfig.Snapshot().Settings}
	migration, ok := systemconfig.UnfinishedAssetMigration(h.Queries)
	if !ok {
		return settings
	}
	for _, versionID := range []int64{migration.OldConfigVersionID, migration.NewConfigVersionID} {
		row, err := h.Queries.GetConfigByID(context.Background(), versionID)
		if err != nil {
			continue
		}
		cfg, err := apigen.DecodeSystemConfig(row.ConfigBlob)
		if err == nil {
			settings = append(settings, cfg.Settings)
		}
	}
	return settings
}
