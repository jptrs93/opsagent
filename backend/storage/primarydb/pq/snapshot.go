package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Snapshot is the materialised state as a stream opening: every live entity
// with its meta, in type then id order. A deployment or value appears once
// per retained version, in version order, each entry carrying that version's
// document with the identity's current key, directory, and space; a
// retained version of a deleted deployment carries deleted with the
// envelope of the delete.
func (q *Queries) Snapshot(ctx context.Context) ([]*apigen.MaterialisedEntity, error) {
	var out []*apigen.MaterialisedEntity
	for _, t := range reducedTypes {
		entries, err := q.snapshotOf(ctx, t)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func entry(t apigen.CoreEntityType, id uint64, e apigen.CoreEntity, meta apigen.EntityMeta) *apigen.MaterialisedEntity {
	return &apigen.MaterialisedEntity{EntityType: t, EntityID: id, Entity: e, Meta: meta}
}

// valueVersionMeta is the meta of one version of a value: the identity's
// creation, the version's write, or the identity's own later write for the
// newest version when a rename or move followed the last value change.
func valueVersionMeta(created, identitySeq, identityTime, identityAuthor, versionSeq, versionTime, versionAuthor int64, version uint32, newest bool) apigen.EntityMeta {
	meta := rowMeta(created, versionSeq, versionTime, versionAuthor)
	if newest && identitySeq > versionSeq {
		meta = rowMeta(created, identitySeq, identityTime, identityAuthor)
	}
	meta.ValueVersion = version
	return meta
}

func (q *Queries) snapshotOf(ctx context.Context, t apigen.CoreEntityType) ([]*apigen.MaterialisedEntity, error) {
	var out []*apigen.MaterialisedEntity
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		versions, err := q.ListRetainedDeploymentVersions(ctx)
		if err != nil {
			return nil, err
		}
		live, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return nil, err
		}
		current := map[uint64]bool{}
		for _, r := range live {
			current[r.Deployment.ID] = true
		}
		deletions := map[uint64]rowEnvelope{}
		for _, r := range versions {
			id := r.Deployment.ID
			meta := r.Meta
			if !current[id] {
				env, ok := deletions[id]
				if !ok {
					if env, err = q.deletionEnvelope(ctx, t, id); err != nil {
						return nil, err
					}
					deletions[id] = env
				}
				meta.UpdatedSeq, meta.UpdatedTime, meta.UpdatedActor, meta.Deleted = env.Seq, env.EventTime, env.Author, true
			}
			out = append(out, entry(t, id, DeploymentMutationOf(apigen.AuthzVerb_AUTHZ_VERB_CREATE, r).Entity, meta))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		rows, err := q.ListRetainedScheduledInstances(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range rows {
			out = append(out, entry(t, e.ScheduledInstanceID, ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, e).Entity, rowMeta(e.CreatedTime, e.Seq, e.EventTime, e.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		rows, err := q.ListNodeRows(ctx, AllNodeStatuses)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			e := &rows[i].Event
			out = append(out, entry(t, e.NodeID, NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, e).Entity, rowMeta(e.CreatedTime, e.Seq, e.EventTime, e.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		rows, err := q.listScheduledInstanceStatusRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.Status.ScheduledInstanceID, entityOf(apigen.CoreEntityValueOneof{ScheduledInstanceStatus: r.Status}), r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		rows, err := q.listNodeStatusRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.Status.NodeID, entityOf(apigen.CoreEntityValueOneof{NodeStatus: r.Status}), r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		rows, err := q.ListSecretRows(ctx)
		if err != nil {
			return nil, err
		}
		versions, err := q.ListSecretVersions(ctx)
		if err != nil {
			return nil, err
		}
		byID := make(map[uint64][]SecretVersionRow, len(rows))
		for _, v := range versions {
			byID[v.SecretID] = append(byID[v.SecretID], v)
		}
		for _, s := range rows {
			vs := byID[s.ID]
			for i, v := range vs {
				meta := valueVersionMeta(s.CreatedTime, s.Seq, s.EventTime, s.Author, v.Seq, v.EventTime, v.Author, v.ValueVersion, i == len(vs)-1)
				out = append(out, entry(t, s.ID, SecretMutation(EventMeta{}, s.ID, SecretEntity(s, v)).Entity, meta))
			}
		}
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		rows, err := q.ListConfigRows(ctx)
		if err != nil {
			return nil, err
		}
		versions, err := q.ListConfigVersions(ctx)
		if err != nil {
			return nil, err
		}
		byID := make(map[uint64][]ConfigVersionRow, len(rows))
		for _, v := range versions {
			byID[v.ConfigID] = append(byID[v.ConfigID], v)
		}
		for _, c := range rows {
			vs := byID[c.ID]
			for i, v := range vs {
				meta := valueVersionMeta(c.CreatedTime, c.Seq, c.EventTime, c.Author, v.Seq, v.EventTime, v.Author, v.ValueVersion, i == len(vs)-1)
				out = append(out, entry(t, c.ID, ConfigMutation(EventMeta{}, c.ID, ConfigEntity(c, v)).Entity, meta))
			}
		}
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		rows, err := q.ListAssetRows(ctx)
		if err != nil {
			return nil, err
		}
		versions, err := q.ListAssetVersions(ctx)
		if err != nil {
			return nil, err
		}
		byID := make(map[uint64][]AssetVersionRow, len(rows))
		for _, v := range versions {
			byID[v.AssetID] = append(byID[v.AssetID], v)
		}
		for _, a := range rows {
			vs := byID[a.ID]
			for i, v := range vs {
				meta := valueVersionMeta(a.CreatedTime, a.Seq, a.EventTime, a.Author, v.Seq, v.EventTime, v.Author, v.ValueVersion, i == len(vs)-1)
				out = append(out, entry(t, a.ID, AssetMutation(EventMeta{}, a.ID, AssetEntity(a, v)).Entity, meta))
			}
		}
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		rows, err := q.listValueDirectoryRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.Directory.ID, ValueDirectoryMutation(EventMeta{}, r.Directory).Entity, r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		rows, err := q.listAssetDirectoryRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.Directory.ID, AssetDirectoryMutation(EventMeta{}, r.Directory).Entity, r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		rows, err := q.listSpaceRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.Space.ID, SpaceMutation(EventMeta{}, r.Space).Entity, r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		rows, err := q.ListUserRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			u, err := UserEntity(r)
			if err != nil {
				return nil, err
			}
			out = append(out, entry(t, r.ID, UserMutation(EventMeta{}, u).Entity, rowMeta(r.CreatedTime, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		rows, err := q.ListNetworkPolicies(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range rows {
			out = append(out, entry(t, e.NetworkPolicyID, NetworkPolicyMutation(EventMeta{}, e.NetworkPolicyID, e.Value).Entity, rowMeta(e.CreatedTime, e.Seq, e.EventTime, e.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE:
		rows, err := q.ListAuthzGrantTemplates(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			record, err := AuthzGrantTemplateEntity(r)
			if err != nil {
				return nil, err
			}
			out = append(out, entry(t, r.ID, AuthzGrantTemplateMutation(EventMeta{}, record).Entity, rowMeta(r.CreatedTime, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		rows, err := q.ListAuthzGrants(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			value, err := AuthzGrantEntity(r)
			if err != nil {
				return nil, err
			}
			out = append(out, entry(t, r.ID, AuthzGrantMutation(EventMeta{}, r.ID, value).Entity, rowMeta(r.CreatedTime, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		rows, err := q.ListAuthzGlobalRules(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			record, err := AuthzGlobalRuleEntity(r)
			if err != nil {
				return nil, err
			}
			out = append(out, entry(t, r.ID, AuthzGlobalRuleMutation(EventMeta{}, record).Entity, rowMeta(r.CreatedTime, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		rows, err := q.ListAllAgentSessions(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.ID, entityOf(apigen.CoreEntityValueOneof{AgentSession: r.Entity()}), rowMeta(r.CreatedAt, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		rows, err := q.ListAllUserSessions(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.ID, entityOf(apigen.CoreEntityValueOneof{UserSession: r.Entity()}), rowMeta(r.CreatedAt, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		rows, err := q.ListNixStoreResetRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, entry(t, r.ID, entityOf(apigen.CoreEntityValueOneof{NixStoreReset: r.Entity()}), rowMeta(r.CreatedTime, r.Seq, r.EventTime, r.Author)))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:
		rows, err := q.listSecretKeyslotRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			k := r.Keyslot
			out = append(out, entry(t, k.ID, entityOf(apigen.CoreEntityValueOneof{SecretKeyslot: &k}), r.meta()))
		}
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:
		r, err := q.GetSystemConfig(ctx)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		cfg, err := apigen.DecodeSystemConfig(r.ConfigBlob)
		if err != nil {
			return nil, err
		}
		cfg.ID = uint32(SystemConfigEntityID)
		out = append(out, entry(t, SystemConfigEntityID, entityOf(apigen.CoreEntityValueOneof{SystemConfig: cfg}), rowMeta(r.CreatedTime, r.Seq, r.UpdatedAt, r.Author)))
	}
	return out, nil
}
