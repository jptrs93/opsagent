package webuihandler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ReferenceInUseErr = apigen.NewApiErr("Referenced value is still in use", "reference_in_use", 400)

// referenceInUseDetailErr is ReferenceInUseErr with a display message naming
// what still pins the value, e.g. "Secret still in use: referenced by
// deployment global / dev-machine / api". ApiErr.Is matches on the internal
// code, so errors.Is against the bare sentinel still holds.
func referenceInUseDetailErr(subject string, details []string) error {
	if len(details) == 0 {
		return ReferenceInUseErr
	}
	const maxShown = 5
	if len(details) > maxShown {
		details = append(details[:maxShown:maxShown], fmt.Sprintf("%d more", len(details)-maxShown))
	}
	return apigen.NewApiErr(
		subject+" still in use: referenced by "+strings.Join(details, "; "),
		ReferenceInUseErr.InternalErr,
		ReferenceInUseErr.Code,
	)
}

// MoveReferencesOutsideSpaceErr refuses a cross-space move while the value is
// pinned from outside the destination space — by a deployment in another
// space, or by cluster settings (which pin the value to the global space).
var MoveReferencesOutsideSpaceErr = apigen.NewApiErr("Value is referenced from outside the destination space", "move_references_outside_space", 400)

// The deployment-side scans below extract pinned version ids with the same
// collectors the engine uses to fetch runtime inputs (plus addressRefIDs,
// which has no engine collector because addresses are not fetched). Delete and
// move protection therefore cannot lag behind what a runner would actually
// resolve — the env-only scan this replaced missed ingress cert secrets.

func assetRefIDs(cfg *apigen.Deployment) []int32 {
	refs := runtimeinputs.RequiredAssetRefs(cfg)
	ids := make([]int32, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.AssetVersionID)
	}
	return ids
}

func addressRefIDs(cfg *apigen.Deployment) []int32 {
	container := cfg.Def.Spec.Container()
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

func crossDeploymentMountSourceIDs(cfg *apigen.Deployment) []int32 {
	container := cfg.Def.Spec.Container()
	if container == nil {
		return nil
	}
	var ids []int32
	for _, mount := range container.Runtime.CrossDeploymentMounts {
		if mount != nil {
			ids = append(ids, mount.DeploymentID)
		}
	}
	return ids
}

// referencingDeployments returns the non-deleted deployments pinning any of
// ids, with refs extracting one kind's version ids from a config.
func referencingDeployments(live state.LiveState, ids map[int32]struct{}, refs func(*apigen.Deployment) []int32) []*apigen.Deployment {
	var out []*apigen.Deployment
	for _, cfg := range live.Deployments {
		for _, id := range refs(cfg) {
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
func referencesOutsideSpace(live state.LiveState, ids map[int32]struct{}, refs func(*apigen.Deployment) []int32, spaceID int32) bool {
	for _, cfg := range referencingDeployments(live, ids, refs) {
		if cfg.Def.SpaceID != spaceID {
			return true
		}
	}
	return false
}

func deploymentUsesAddressID(live state.LiveState, ids map[int32]struct{}) bool {
	return len(referencingDeployments(live, ids, addressRefIDs)) > 0
}

// deploymentRefDetails renders "deployment <space> / <node> / <name>" lines
// for every deployment pinning one of ids — the human-readable half of the
// reference_in_use refusal.
func deploymentRefDetails(store *state.Service, live state.LiveState, ids map[int32]struct{}, refs func(*apigen.Deployment) []int32) []string {
	cfgs := referencingDeployments(live, ids, refs)
	if len(cfgs) == 0 {
		return nil
	}
	spaces := map[int32]string{}
	for _, space := range store.ListSpaces() {
		spaces[space.ID] = space.Name
	}
	nodes := map[int32]string{}
	for _, node := range live.Nodes {
		nodes[node.ID] = node.Name
	}
	details := make([]string, 0, len(cfgs))
	for _, cfg := range cfgs {
		space := spaces[cfg.Def.SpaceID]
		if space == "" {
			space = fmt.Sprintf("space %d", cfg.Def.SpaceID)
		}
		node := nodes[cfg.Def.NodeID]
		if node == "" {
			node = fmt.Sprintf("node %d", cfg.Def.NodeID)
		}
		details = append(details, "deployment "+space+" / "+node+" / "+cfg.Def.Name)
	}
	sort.Strings(details)
	return details
}

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
	if h.ConfigService == nil {
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
	if h.ConfigService == nil {
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
