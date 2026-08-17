package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
)

var ReferenceInUseErr = apigen.NewApiErr("Referenced value is still in use", "reference_in_use", 400)

// MoveReferencesOutsideSpaceErr refuses a cross-space move while the value is
// pinned from outside the destination space — by a deployment in another
// space, or by cluster settings (which pin the value to the global space).
var MoveReferencesOutsideSpaceErr = apigen.NewApiErr("Value is referenced from outside the destination space", "move_references_outside_space", 400)

// The deployment-side scans below extract pinned version ids with the same
// collectors the engine uses to fetch runtime inputs (plus addressRefIDs,
// which has no engine collector because addresses are not fetched). Delete and
// move protection therefore cannot lag behind what a runner would actually
// resolve — the env-only scan this replaced missed ingress cert secrets.

func assetRefIDs(cfg *apigen.DeploymentConfig) []int32 {
	refs := runtimeinputs.RequiredAssetRefs(cfg)
	ids := make([]int32, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.AssetVersionID)
	}
	return ids
}

func addressRefIDs(cfg *apigen.DeploymentConfig) []int32 {
	container := cfg.Spec.Container()
	if container == nil {
		return nil
	}
	var ids []int32
	for _, value := range container.Runtime.EnvVars {
		if value != nil && value.AddressDeploymentID != nil {
			ids = append(ids, *value.AddressDeploymentID)
		}
	}
	return ids
}

// referencingDeployments returns the non-deleted deployments pinning any of
// ids, with refs extracting one kind's version ids from a config.
func (h *Handler) referencingDeployments(ids map[int32]struct{}, refs func(*apigen.DeploymentConfig) []int32) []apigen.DeploymentConfig {
	var out []apigen.DeploymentConfig
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.Deleted {
			continue
		}
		for _, id := range refs(&cfg) {
			if _, ok := ids[id]; ok {
				out = append(out, cfg)
				break
			}
		}
	}
	return out
}

// referencesOutsideSpace reports whether any non-deleted deployment outside
// spaceID pins one of ids — the veto for cross-space moves.
func (h *Handler) referencesOutsideSpace(ids map[int32]struct{}, refs func(*apigen.DeploymentConfig) []int32, spaceID int32) bool {
	for _, cfg := range h.referencingDeployments(ids, refs) {
		if cfg.SpaceID != spaceID {
			return true
		}
	}
	return false
}

func (h *Handler) deploymentUsesSecretID(ids map[int32]struct{}) bool {
	return len(h.referencingDeployments(ids, runtimeinputs.SecretRefs)) > 0
}

func (h *Handler) deploymentUsesConfigID(ids map[int32]struct{}) bool {
	return len(h.referencingDeployments(ids, runtimeinputs.ConfigRefs)) > 0
}

func (h *Handler) deploymentUsesAssetID(ids map[int32]struct{}) bool {
	return len(h.referencingDeployments(ids, assetRefIDs)) > 0
}

func (h *Handler) deploymentUsesAddressID(ids map[int32]struct{}) bool {
	return len(h.referencingDeployments(ids, addressRefIDs)) > 0
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
