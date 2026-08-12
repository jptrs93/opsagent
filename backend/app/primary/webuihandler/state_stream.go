package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
)

// PostV1GlobalStateStream delivers the current scheduled instance snapshot to the UI,
// then forwards per-instance updates as they happen, with periodic heartbeats to
// keep the HTTP connection alive.
func (h *Handler) PostV1GlobalStateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
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
		secretMetaSub, secretMetaUnsub := h.Store.SubscribeSecretMetaUpdates()
		defer secretMetaUnsub()
		userConfigSub, userConfigUnsub := h.Store.SubscribeConfigMetaUpdates()
		defer userConfigUnsub()
		valueDirSub, valueDirUnsub := h.Store.SubscribeValueDirectoryUpdates()
		defer valueDirUnsub()
		spaceSub, spaceUnsub := h.Store.SubscribeSpaceUpdates()
		defer spaceUnsub()
		assetSub, assetUnsub := h.Store.SubscribeAssetUpdates()
		defer assetUnsub()
		assetDirSub, assetDirUnsub := h.Store.SubscribeAssetDirectoryUpdates()
		defer assetDirUnsub()
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
		agentSessionSub, agentSessionUnsub := h.Store.SubscribeAgentSessionUpdates()
		defer agentSessionUnsub()
		authzSub, authzUnsub := h.Authz.SubscribeChanges()
		defer authzUnsub()

		// Agent sessions are the one thing here that belongs to a single
		// operator, so they are filtered to the connected user rather than
		// broadcast like everything else.
		agentSessions := &apigen.AgentSessionList{}
		if ctx.User != nil {
			records, err := h.Store.ListAgentSessionsForUser(ctx.User.ID)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, rec := range records {
				agentSessions.Items = append(agentSessions.Items, agentSessionToProto(rec))
			}
		}

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
			SecretsStatusSnapshot:      &secretStatus,
			SecretMetasSnapshot:        &apigen.SecretList{Items: h.Store.ListSecretMetas()},
			UserConfigValuesSnapshot:   &apigen.ConfigList{Items: h.Store.ListConfigMetas()},
			ValueDirectoriesSnapshot:   &apigen.ValueDirectoryList{Items: h.Store.ListValueDirectories()},
			SpacesSnapshot:             &apigen.SpaceList{Items: h.Store.ListSpaces()},
			AssetsSnapshot:             &apigen.AssetList{Items: h.Store.ListAssets()},
			AssetDirectoriesSnapshot:   &apigen.AssetDirectoryList{Items: h.Store.ListAssetDirectories()},
			NodesSnapshot:              &apigen.ClusterNodeList{Items: h.Store.ListClusterNodes()},
			NodeStatusesSnapshot:       &apigen.ClusterNodeStatusList{Items: h.Store.ListNodeStatuses()},
			BackupStatusSnapshot:       &backupStatus,
			ConfigSnapshot:             configSub.InitialValue,
			AgentSessionsSnapshot:      agentSessions,
			AuthzRuleTemplatesSnapshot: &apigen.AuthzRuleTemplateList{Items: h.Authz.RuleTemplates()},
			AuthzGrantsSnapshot:        &apigen.AuthzGrantList{Items: h.Authz.Grants()},
			AuthzGlobalRulesSnapshot:   &apigen.AuthzGlobalRuleList{Items: h.Authz.GlobalRules()},
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
				if !yield(&apigen.State{UserConfigValueUpdate: &userConfig}, nil) {
					return
				}
			case valueDir, ok := <-valueDirSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{ValueDirectoryUpdate: &valueDir}, nil) {
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
			case assetDir, ok := <-assetDirSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{AssetDirectoryUpdate: &assetDir}, nil) {
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
			case rec, ok := <-agentSessionSub.Ch:
				if !ok {
					return
				}
				// Dropped rather than yielded when it belongs to someone else:
				// this is the filter that keeps one operator's session requests
				// off another's screen.
				if ctx.User == nil || rec.UserID != ctx.User.ID {
					continue
				}
				if !yield(&apigen.State{AgentSessionUpdate: agentSessionToProto(rec)}, nil) {
					return
				}
			case kind, ok := <-authzSub.Ch:
				if !ok {
					return
				}
				update := &apigen.State{}
				switch kind {
				case authz.ChangeRuleTemplates:
					update.AuthzRuleTemplatesSnapshot = &apigen.AuthzRuleTemplateList{Items: h.Authz.RuleTemplates()}
				case authz.ChangeGrants:
					update.AuthzGrantsSnapshot = &apigen.AuthzGrantList{Items: h.Authz.Grants()}
				case authz.ChangeGlobalRules:
					update.AuthzGlobalRulesSnapshot = &apigen.AuthzGlobalRuleList{Items: h.Authz.GlobalRules()}
				}
				if !yield(update, nil) {
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
