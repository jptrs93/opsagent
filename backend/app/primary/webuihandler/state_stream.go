package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// PostV1StateStream delivers the current scheduled instance snapshot to the UI,
// then forwards per-instance updates as they happen, with periodic heartbeats to
// keep the HTTP connection alive.
func (h *Handler) PostV1StateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
	return func(yield func(*apigen.State, error) bool) {
		configs, configUpdatesCh, configUpdatesUnsub := h.Store.MustFetchDeploymentConfigSnapshotAndSubscribe(nil)
		defer configUpdatesUnsub()
		snapshot, updatesCh, updatesUnsub := h.Store.MustFetchScheduledSnapshotWithLatestFinalAndSubscribe(nil)
		defer updatesUnsub()
		userSub, userUnsub := h.Store.SubscribeUserUpdates()
		defer userUnsub()
		backupStatusSub, backupStatusUnsub := h.Store.SubscribeBackupStatusUpdates()
		defer backupStatusUnsub()
		secretStatusSub, secretStatusUnsub := h.Store.SubscribeSecretsStatusUpdates()
		defer secretStatusUnsub()
		configSub := h.ConfigService.VersionedSnapshotAndSubscribe()
		defer configSub.UnsubscribeFunc()
		secretSub, secretUnsub := h.Store.SubscribeSecretReferenceUpdates()
		defer secretUnsub()
		secretMetaSub, secretMetaUnsub := h.Store.SubscribeSecretMetaUpdates()
		defer secretMetaUnsub()
		userConfigSub, userConfigUnsub := h.Store.SubscribeUserConfigReferenceUpdates()
		defer userConfigUnsub()
		userConfigValueSub, userConfigValueUnsub := h.Store.SubscribeUserConfigValueUpdates()
		defer userConfigValueUnsub()
		spaceSub, spaceUnsub := h.Store.SubscribeSpaceUpdates()
		defer spaceUnsub()
		assetSub, assetUnsub := h.Store.SubscribeAssetUpdates()
		defer assetUnsub()
		nodeSub, nodeUnsub := h.Store.SubscribeNodeUpdates()
		defer nodeUnsub()
		nodeStatusSub, nodeStatusUnsub := h.Store.SubscribeNodeStatusUpdates()
		defer nodeStatusUnsub()
		enrollments, enrollmentCh, enrollmentUnsub, err := h.Store.MustFetchEnrollmentSnapshotAndSubscribe()
		if err != nil {
			yield(nil, err)
			return
		}
		defer enrollmentUnsub()

		configItems := make([]*apigen.DeploymentConfig, 0, len(configs))
		for i := range configs {
			configItems = append(configItems, redactDeploymentConfig(&configs[i]))
		}
		items := make([]*apigen.ScheduledInstanceState, 0, len(snapshot))
		for i := range snapshot {
			items = append(items, redactScheduledInstanceState(&snapshot[i]))
		}
		secretStatus := h.secretsStatus()
		backupStatus := h.Store.CurrentBackupStatus()
		initial := &apigen.State{
			DeploymentConfigsSnapshot:  &apigen.DeploymentConfigSnapshot{Items: configItems},
			ScheduledInstancesSnapshot: &apigen.ScheduledInstanceSnapshot{Items: items},
			UsersSnapshot:              h.Store.ListUsersPublic(),
			EnrollmentsSnapshot:        &apigen.EnrollmentRequestList{Items: enrollments},
			SecretsSnapshot:            &apigen.SecretReferenceList{Items: h.Store.ListSecretReferences()},
			UserConfigsSnapshot:        &apigen.UserConfigReferenceList{Items: h.Store.ListUserConfigReferences()},
			SecretsStatusSnapshot:      &secretStatus,
			SecretMetasSnapshot:        &apigen.SecretList{Items: h.listAllSecretMetas()},
			UserConfigValuesSnapshot:   &apigen.UserConfigList{Items: h.Store.ListAllUserConfigs()},
			SpacesSnapshot:             &apigen.SpaceList{Items: h.Store.ListSpaces()},
			AssetsSnapshot:             &apigen.AssetList{Items: h.Store.ListAllAssetVersions()},
			NodesSnapshot:              &apigen.ClusterNodeList{Items: h.Store.ListClusterNodes()},
			NodeStatusesSnapshot:       &apigen.ClusterNodeStatusList{Items: h.Store.ListNodeStatuses()},
			BackupStatusSnapshot:       &backupStatus,
			ConfigSnapshot:             configSub.InitialValue,
		}
		if !yield(initial, nil) {
			return
		}

		heartbeatTicker := time.NewTicker(5 * time.Second)
		defer heartbeatTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case state, ok := <-updatesCh:
				if !ok {
					return
				}
				update := redactScheduledInstanceState(&state)
				if !yield(&apigen.State{ScheduledInstanceUpdate: update}, nil) {
					return
				}
			case cfg, ok := <-configUpdatesCh:
				if !ok {
					return
				}
				if !yield(&apigen.State{DeploymentConfigUpdate: redactDeploymentConfig(&cfg)}, nil) {
					return
				}
			case u, ok := <-userSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserUpdate: &u}, nil) {
					return
				}
			case backupStatus, ok := <-backupStatusSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{BackupStatusUpdate: &backupStatus}, nil) {
					return
				}
			case status, ok := <-secretStatusSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{SecretsStatusSnapshot: &status}, nil) {
					return
				}
			case config, ok := <-configSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{ConfigSnapshot: config}, nil) {
					return
				}
			case secret, ok := <-secretSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{SecretUpdate: &secret}, nil) {
					return
				}
			case secretMeta, ok := <-secretMetaSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{SecretMetaUpdate: &secretMeta}, nil) {
					return
				}
			case userConfig, ok := <-userConfigSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserConfigUpdate: &userConfig}, nil) {
					return
				}
			case userConfigValue, ok := <-userConfigValueSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserConfigValueUpdate: &userConfigValue}, nil) {
					return
				}
			case space, ok := <-spaceSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{SpaceUpdate: &space}, nil) {
					return
				}
			case asset, ok := <-assetSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{AssetUpdate: &asset}, nil) {
					return
				}
			case node, ok := <-nodeSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{NodeUpdate: &node}, nil) {
					return
				}
			case nodeStatus, ok := <-nodeStatusSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{NodeStatusUpdate: &nodeStatus}, nil) {
					return
				}
			case enrollment, ok := <-enrollmentCh:
				if !ok {
					return
				}
				if !yield(&apigen.State{EnrollmentUpdate: &enrollment}, nil) {
					return
				}
			case <-heartbeatTicker.C:
				if !yield(&apigen.State{Heartbeat: true}, nil) {
					return
				}
			}
		}
	}
}
