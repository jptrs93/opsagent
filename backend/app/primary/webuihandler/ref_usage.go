package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) settingsUseSecretID(ids map[uint64]struct{}) bool {
	return len(h.settingsSecretRefDetails(ids)) > 0
}

func (h *Handler) settingsUseConfigID(ids map[uint64]struct{}) bool {
	return len(h.settingsConfigRefDetails(ids)) > 0
}

// settingsSecretRefDetails renders "cluster settings (<field>)" lines for
// every settings field pinning a value of one of the secret ids, deduped
// across the live settings and any unfinished asset-migration snapshots.
func (h *Handler) settingsSecretRefDetails(ids map[uint64]struct{}) []string {
	if h.SystemConfig == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, settings := range []apigen.ClusterSettings{h.SystemConfig.Snapshot().Settings} {
		refs := []struct {
			field string
			ref   apigen.Maybe[apigen.SecretRef]
		}{
			{"HTTPS web TLS certificate", settings.HttpsWeb.TlsCertPem},
			{"GitHub token", settings.Repo.GithubToken},
			{"backup S3 secret access key", settings.Backup.S3SecretAccessKey},
			{"large-assets S3 secret access key", settings.LargeAssets.S3SecretAccessKey},
		}
		for _, ref := range refs {
			if !ref.ref.Present || !ref.ref.Value.Valid() {
				continue
			}
			if _, ok := ids[ref.ref.Value.SecretID]; ok && !seen[ref.field] {
				seen[ref.field] = true
				out = append(out, "cluster settings ("+ref.field+")")
			}
		}
	}
	return out
}

// settingsConfigRefDetails is settingsSecretRefDetails for config references.
func (h *Handler) settingsConfigRefDetails(ids map[uint64]struct{}) []string {
	if h.SystemConfig == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, settings := range []apigen.ClusterSettings{h.SystemConfig.Snapshot().Settings} {
		refs := []struct {
			field string
			ref   *apigen.ConfigRef
		}{
			{"HTTP web enabled", settings.HttpWeb.Enabled.Value.Value.ConfigRef},
			{"HTTP web listen", settings.HttpWeb.Listen.Value.Value.ConfigRef},
			{"HTTPS web enabled", settings.HttpsWeb.Enabled.Value.Value.ConfigRef},
			{"HTTPS web listen", settings.HttpsWeb.Listen.Value.Value.ConfigRef},
			{"HTTPS web self-managed TLS", settings.HttpsWeb.TlsSelfManaged.Value.Value.ConfigRef},
			{"HTTPS web ACME hosts", settings.HttpsWeb.AcmeHosts.Value.Value.ConfigRef},
			{"HTTPS web ACME email", settings.HttpsWeb.AcmeEmail.Value.Value.ConfigRef},
			{"password login enabled", settings.Auth.PasswordLoginEnabled.Value.Value.ConfigRef},
			{"cluster listen", settings.Cluster.Listen.Value.Value.ConfigRef},
			{"cluster enrollment listen", settings.Cluster.EnrollmentListen.Value.Value.ConfigRef},
			{"backup enabled", settings.Backup.Enabled.Value.Value.ConfigRef},
			{"backup S3 access key id", settings.Backup.S3AccessKeyID.Value.Value.ConfigRef},
			{"backup S3 bucket", settings.Backup.S3Bucket.Value.Value.ConfigRef},
			{"backup S3 path", settings.Backup.S3Path.Value.Value.ConfigRef},
			{"backup S3 region", settings.Backup.S3Region.Value.Value.ConfigRef},
			{"backup S3 endpoint", settings.Backup.S3Endpoint.Value.Value.ConfigRef},
			{"large-assets separate S3", settings.LargeAssets.UseSeparateS3.Value.Value.ConfigRef},
			{"large-assets S3 access key id", settings.LargeAssets.S3AccessKeyID.Value.Value.ConfigRef},
			{"large-assets S3 bucket", settings.LargeAssets.S3Bucket.Value.Value.ConfigRef},
			{"large-assets S3 path", settings.LargeAssets.S3Path.Value.Value.ConfigRef},
			{"large-assets S3 region", settings.LargeAssets.S3Region.Value.Value.ConfigRef},
			{"large-assets S3 endpoint", settings.LargeAssets.S3Endpoint.Value.Value.ConfigRef},
			{"large-assets keep local copy", settings.LargeAssets.KeepLocalCopy.Value.Value.ConfigRef},
		}
		for _, ref := range refs {
			if ref.ref == nil || !ref.ref.Valid() {
				continue
			}
			if _, ok := ids[ref.ref.ConfigID]; ok && !seen[ref.field] {
				seen[ref.field] = true
				out = append(out, "cluster settings ("+ref.field+")")
			}
		}
	}
	return out
}
