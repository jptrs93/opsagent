package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// PostV1GlobalStateStream delivers the current scheduled instance snapshot to the UI,
// then forwards per-instance updates as they happen, with periodic heartbeats to
// keep the HTTP connection alive.
//
// Everything sent is filtered to what the connected user may view. Snapshot
// fields are full replacements on the client, so when the user's grants change
// the whole filtered state is simply re-emitted rather than diffed.
func (h *Handler) PostV1GlobalStateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
	return func(yield func(*apigen.State, error) bool) {
		_, configUpdatesCh, configUpdatesUnsub := h.Store.MustFetchDeploymentSnapshotAndSubscribe(nil)
		defer configUpdatesUnsub()
		_, updatesCh, updatesUnsub := h.Store.MustFetchScheduledSnapshotWithLatestFinalAndSubscribe(nil)
		defer updatesUnsub()
		userSub, userUnsub := h.Store.SubscribeUserUpdates()
		defer userUnsub()
		backupStatusSub, backupStatusUnsub := h.Store.SubscribeBackupStatusUpdates()
		defer backupStatusUnsub()
		secretStatusSub, secretStatusUnsub := h.Store.SubscribeSecretsStatusUpdates()
		defer secretStatusUnsub()
		configSub := h.ConfigService.VersionedSnapshotAndSubscribe()
		defer configSub.Unsubscribe()
		secretMetaSub, secretMetaUnsub := h.Store.SubscribeSecretUpdates()
		defer secretMetaUnsub()
		userConfigSub, userConfigUnsub := h.Store.SubscribeConfigUpdates()
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
		networkPolicySub, networkPolicyUnsub := h.Store.SubscribeNetworkPolicyUpdates()
		defer networkPolicyUnsub()

		latestConfig := configSub.InitialValue

		// depSpace maps deployment id -> space id so scheduled-instance updates
		// (which do not carry a space) can be filtered.
		depSpace := map[int32]int32{}
		spaceOfDeployment := func(deploymentID int32) (int32, bool) {
			if spaceID, ok := depSpace[deploymentID]; ok {
				return spaceID, true
			}
			if cfg := h.findConfigByID(deploymentID); cfg != nil {
				depSpace[deploymentID] = cfg.SpaceID
				return cfg.SpaceID, true
			}
			return 0, false
		}

		seesCluster := func() bool { return h.canAccess(ctx, vView, eCluster, 0, 0) }
		seesNodes := func() bool { return h.canAccess(ctx, vView, eNode, 0, 0) }

		// Authz collections are all-or-nothing on access:view, except that every
		// user always receives the template catalogue and their own grants so the
		// UI can describe what they hold.
		applyAuthzState := func(state *apigen.State) {
			state.AuthzRuleTemplatesSnapshot = &apigen.AuthzRuleTemplateList{Items: h.Authz.RuleTemplates()}
			if h.canAccess(ctx, vView, eAccess, 0, 0) {
				state.AuthzGrantsSnapshot = &apigen.AuthzGrantList{Items: h.Authz.Grants()}
				state.AuthzGlobalRulesSnapshot = &apigen.AuthzGlobalRuleList{Items: h.Authz.GlobalRules()}
				return
			}
			var own []*apigen.AuthzGrantRecord
			if ctx.User != nil {
				own = h.Authz.GrantsForUser(int64(ctx.User.ID))
			}
			state.AuthzGrantsSnapshot = &apigen.AuthzGrantList{Items: own}
			state.AuthzGlobalRulesSnapshot = &apigen.AuthzGlobalRuleList{Items: []*apigen.AuthzGlobalRuleRecord{}}
		}

		buildState := func() (*apigen.State, error) {
			for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
				depSpace[cfg.ID] = cfg.SpaceID
			}
			configs := h.filterDeployments(ctx, h.Store.ListActiveDeployments())
			configItems := make([]*apigen.Deployment, 0, len(configs))
			for _, cfg := range configs {
				configItems = append(configItems, cfg)
			}
			snapshot := h.Store.FetchScheduledSnapshotWithLatestFinal(nil)
			items := make([]*apigen.ScheduledInstanceState, 0, len(snapshot))
			for i := range snapshot {
				spaceID, ok := spaceOfDeployment(snapshot[i].Instance.DeploymentID)
				if !ok || !h.canAccess(ctx, vView, eDeployment, int64(spaceID), int64(snapshot[i].Instance.DeploymentID)) {
					continue
				}
				items = append(items, &snapshot[i])
			}
			agentSessions := &apigen.AgentSessionList{}
			if ctx.User != nil {
				records, err := h.Store.ListAgentSessionsForUser(ctx.User.ID)
				if err != nil {
					return nil, err
				}
				for _, rec := range records {
					agentSessions.Items = append(agentSessions.Items, agentSessionToProto(rec))
				}
			}
			visibleEnrollments := &apigen.EnrollmentRequestList{Items: []*apigen.EnrollmentRequestStatus{}}
			if seesNodes() {
				visibleEnrollments.Items = enrollments
			}
			secretStatus := h.secretsStatus()
			state := &apigen.State{
				DeploymentsSnapshot:        &apigen.DeploymentSnapshot{Items: configItems},
				ScheduledInstancesSnapshot: &apigen.ScheduledInstanceSnapshot{Items: items},
				UsersSnapshot:              h.filterUsers(ctx, h.Store.ListUsersPublic()),
				EnrollmentsSnapshot:        visibleEnrollments,
				SecretsStatusSnapshot:      &secretStatus,
				SecretMetasSnapshot:        &apigen.SecretList{Items: h.filterSecrets(ctx, h.Store.ListSecrets())},
				UserConfigValuesSnapshot:   &apigen.ConfigList{Items: h.filterConfigs(ctx, h.Store.ListConfigs())},
				ValueDirectoriesSnapshot:   &apigen.ValueDirectoryList{Items: h.filterValueDirectories(ctx, h.Store.ListValueDirectories())},
				SpacesSnapshot:             &apigen.SpaceList{Items: h.filterSpaces(ctx, h.Store.ListSpaces())},
				AssetsSnapshot:             &apigen.AssetList{Items: h.filterAssets(ctx, h.Store.ListAssets())},
				AssetDirectoriesSnapshot:   &apigen.AssetDirectoryList{Items: h.filterAssetDirectories(ctx, h.Store.ListAssetDirectories())},
				NodesSnapshot:              &apigen.ClusterNodeList{Items: h.filterNodes(ctx, h.Store.ListClusterNodes())},
				NodeStatusesSnapshot:       &apigen.ClusterNodeStatusList{Items: h.filterNodeStatuses(ctx, h.Store.ListNodeStatuses())},
				AgentSessionsSnapshot:      agentSessions,
				NetworkPoliciesSnapshot:    &apigen.NetworkPolicyList{Items: h.visibleNetworkPolicies(ctx)},
			}
			if seesCluster() {
				backupStatus := h.Store.CurrentBackupStatus()
				state.BackupStatusSnapshot = &backupStatus
				state.ConfigSnapshot = latestConfig
			}
			applyAuthzState(state)
			return state, nil
		}

		initial, err := buildState()
		if err != nil {
			yield(nil, err)
			return
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
				spaceID, known := spaceOfDeployment(state.Instance.DeploymentID)
				if !known || !h.canAccess(ctx, vView, eDeployment, int64(spaceID), int64(state.Instance.DeploymentID)) {
					continue
				}
				if !yield(&apigen.State{ScheduledInstanceUpdate: &state}, nil) {
					return
				}
			case cfg, ok := <-configUpdatesCh:
				if !ok {
					return
				}
				depSpace[cfg.ID] = cfg.SpaceID
				if !h.canAccess(ctx, vView, eDeployment, int64(cfg.SpaceID), int64(cfg.ID)) {
					continue
				}
				if !yield(&apigen.State{DeploymentUpdate: &cfg}, nil) {
					return
				}
			case u, ok := <-userSub.Ch:
				if !ok {
					return
				}
				self := ctx.User != nil && u.ID == ctx.User.ID
				if !self && !h.canAccess(ctx, vView, eUser, 0, int64(u.ID)) {
					continue
				}
				if !yield(&apigen.State{UserUpdate: &u}, nil) {
					return
				}
			case backupStatus, ok := <-backupStatusSub.Ch:
				if !ok {
					return
				}
				if !seesCluster() {
					continue
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
				latestConfig = config
				if !seesCluster() {
					continue
				}
				if !yield(&apigen.State{ConfigSnapshot: config}, nil) {
					return
				}
			case secretMeta, ok := <-secretMetaSub.Ch:
				if !ok {
					return
				}
				if !h.canAccess(ctx, vView, eSecret, int64(secretMeta.SpaceID()), int64(secretMeta.ID)) {
					continue
				}
				if !yield(&apigen.State{SecretMetaUpdate: &secretMeta}, nil) {
					return
				}
			case userConfig, ok := <-userConfigSub.Ch:
				if !ok {
					return
				}
				if !h.canAccess(ctx, vView, eConfig, int64(userConfig.SpaceID()), int64(userConfig.ID)) {
					continue
				}
				if !yield(&apigen.State{UserConfigValueUpdate: &userConfig}, nil) {
					return
				}
			case valueDir, ok := <-valueDirSub.Ch:
				if !ok {
					return
				}
				// Deletion tombstones carry only {id, deleted} and pass through so
				// the folder disappears for everyone who could see it.
				if !valueDir.Deleted && !h.canAccessAny(ctx, vView, eValues, int64(valueDir.SpaceID), 0) {
					continue
				}
				if !yield(&apigen.State{ValueDirectoryUpdate: &valueDir}, nil) {
					return
				}
			case space, ok := <-spaceSub.Ch:
				if !ok {
					return
				}
				if !space.Deleted && !h.spaceVisible(ctx, int64(space.ID)) {
					continue
				}
				if !yield(&apigen.State{SpaceUpdate: &space}, nil) {
					return
				}
			case asset, ok := <-assetSub.Ch:
				if !ok {
					return
				}
				if !h.canAccess(ctx, vView, eAsset, int64(asset.SpaceID()), int64(asset.ID)) {
					continue
				}
				if !yield(&apigen.State{AssetUpdate: &asset}, nil) {
					return
				}
			case assetDir, ok := <-assetDirSub.Ch:
				if !ok {
					return
				}
				if !assetDir.Deleted && !h.canAccess(ctx, vView, eAsset, int64(assetDir.SpaceID), 0) {
					continue
				}
				if !yield(&apigen.State{AssetDirectoryUpdate: &assetDir}, nil) {
					return
				}
			case node, ok := <-nodeSub.Ch:
				if !ok {
					return
				}
				if !h.nodeVisible(ctx, int64(node.ID), node.AllowedSpaces) {
					continue
				}
				if !yield(&apigen.State{NodeUpdate: &node}, nil) {
					return
				}
			case nodeStatus, ok := <-nodeStatusSub.Ch:
				if !ok {
					return
				}
				if !h.nodeVisible(ctx, int64(nodeStatus.NodeID), h.nodeAllowedSpaces()[nodeStatus.NodeID]) {
					continue
				}
				if !yield(&apigen.State{NodeStatusUpdate: &nodeStatus}, nil) {
					return
				}
			case enrollment, ok := <-enrollmentCh:
				if !ok {
					return
				}
				enrollments = applyEnrollmentUpdate(enrollments, &enrollment)
				if !seesNodes() {
					continue
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
			case _, ok := <-networkPolicySub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{NetworkPoliciesSnapshot: &apigen.NetworkPolicyList{Items: h.visibleNetworkPolicies(ctx)}}, nil) {
					return
				}
			case _, ok := <-authzSub.Ch:
				if !ok {
					return
				}
				// A grant, template, or global-rule change can alter what this
				// user may see, and previously hidden items have no pending
				// updates to reveal them. Snapshot fields replace wholesale on
				// the client, so re-emit the full filtered state.
				state, err := buildState()
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(state, nil) {
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

func applyEnrollmentUpdate(enrollments []*apigen.EnrollmentRequestStatus, update *apigen.EnrollmentRequestStatus) []*apigen.EnrollmentRequestStatus {
	for i, e := range enrollments {
		if e != nil && e.ID == update.ID {
			enrollments[i] = update
			return enrollments
		}
	}
	return append(enrollments, update)
}
