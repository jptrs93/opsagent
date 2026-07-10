package handler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// PostV1StateStream delivers the current deployment snapshot to the UI,
// then forwards per-deployment updates as they happen, with periodic
// heartbeats to keep the HTTP connection alive.
func (h *Handler) PostV1StateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
	return func(yield func(*apigen.State, error) bool) {
		snapshot, updatesCh, updatesUnsub := h.Store.MustFetchSnapshotAndSubscribe("")
		defer updatesUnsub()
		userSub, userUnsub := h.Store.SubscribeUserUpdates()
		defer userUnsub()
		secretStatusSub, secretStatusUnsub := h.Store.SubscribeSecretsStatusUpdates()
		defer secretStatusUnsub()
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

		items := make([]*apigen.DeploymentWithStatus, 0, len(snapshot))
		for i := range snapshot {
			items = append(items, redactDeploymentWithStatus(&snapshot[i]))
		}
		secretStatus := h.secretsStatus()
		initial := &apigen.State{
			DeploymentsSnapshot:      &apigen.DeploymentWithStatusSnapshot{Items: items},
			UsersSnapshot:            h.Store.ListUsersPublic(),
			EnrollmentsSnapshot:      &apigen.EnrollmentRequestList{Items: enrollments},
			SecretsSnapshot:          &apigen.SecretReferenceList{Items: h.Store.ListSecretReferences()},
			UserConfigsSnapshot:      &apigen.UserConfigReferenceList{Items: h.Store.ListUserConfigReferences()},
			SecretsStatusSnapshot:    &secretStatus,
			SecretMetasSnapshot:      &apigen.SecretList{Items: h.listAllSecretMetas()},
			UserConfigValuesSnapshot: &apigen.UserConfigList{Items: h.Store.ListAllUserConfigs()},
			SpacesSnapshot:           &apigen.SpaceList{Items: h.Store.ListSpaces()},
			AssetsSnapshot:           &apigen.AssetList{Items: h.Store.ListAllAssetVersions()},
			NodesSnapshot:            &apigen.ClusterNodeList{Items: h.Store.ListClusterNodes()},
			NodeStatusesSnapshot:     &apigen.ClusterNodeStatusList{Items: h.Store.ListNodeStatuses()},
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
			case dws, ok := <-updatesCh:
				if !ok {
					return
				}
				update := redactDeploymentWithStatus(&dws)
				if !yield(&apigen.State{DeploymentUpdate: update}, nil) {
					return
				}
			case u, ok := <-userSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserUpdate: &u}, nil) {
					return
				}
			case status, ok := <-secretStatusSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{SecretsStatusSnapshot: &status}, nil) {
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
