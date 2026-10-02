package webuihandler

import (
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// writtenMutation returns the first create or update of a type in a
// receipt. A receipt's meta is stamped from the rows, so it must be present.
func writtenMutation(u *apigen.CoreWriteUpdate, t apigen.CoreEntityType) *apigen.CoreMutation {
	if u == nil {
		return nil
	}
	for _, m := range u.Mutations {
		if m.Delete != nil || m.Type() != t {
			continue
		}
		if m.Meta() == nil || m.Meta().CreatedTime == 0 {
			panic(fmt.Sprintf("receipt for %v %d carries no meta: %+v", t, m.EntityID(), m))
		}
		return m
	}
	return nil
}

func writtenVerb(m *apigen.CoreMutation) apigen.EventType {
	if m.Create != nil {
		return apigen.EventType_EVENT_TYPE_CREATE
	}
	return apigen.EventType_EVENT_TYPE_UPDATE
}

func deploymentOf(u *apigen.CoreWriteUpdate) *apigen.DeploymentEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
	if m == nil || m.Entity().Deployment == nil {
		return nil
	}
	d, meta := *m.Entity().Deployment, m.Meta()
	return &apigen.DeploymentEvent{DeploymentID: int32(m.EntityID()), Version: meta.Version, Seq: u.Seq, Author: u.Actor, EventType: writtenVerb(m),
		CreatedTime: time.UnixMilli(meta.CreatedTime), EventTime: time.UnixMilli(u.Time), SpecVersion: meta.SpecVersion, Value: d}
}

func secretOf(u *apigen.CoreWriteUpdate) *pq.SecretEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_SECRET)
	if m == nil || m.Entity().Secret == nil {
		return nil
	}
	s, meta := *m.Entity().Secret, m.Meta()
	return &pq.SecretEvent{SecretID: int32(m.EntityID()), Seq: u.Seq, Author: u.Actor, CreatedTime: meta.CreatedTime, EventTime: u.Time, ValueVersion: meta.ValueVersion, Value: s}
}

func configOf(u *apigen.CoreWriteUpdate) *pq.ConfigEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_CONFIG)
	if m == nil || m.Entity().Config == nil {
		return nil
	}
	c, meta := *m.Entity().Config, m.Meta()
	return &pq.ConfigEvent{ConfigID: int32(m.EntityID()), Seq: u.Seq, Author: u.Actor, CreatedTime: meta.CreatedTime, EventTime: u.Time, ValueVersion: meta.ValueVersion, Value: c}
}

func assetOf(u *apigen.CoreWriteUpdate) *pq.AssetEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_ASSET)
	if m == nil || m.Entity().Asset == nil {
		return nil
	}
	a, meta := *m.Entity().Asset, m.Meta()
	return &pq.AssetEvent{AssetID: int32(m.EntityID()), Seq: u.Seq, Author: u.Actor, CreatedTime: meta.CreatedTime, EventTime: u.Time, ValueVersion: meta.ValueVersion, Value: a}
}

func nodeOf(u *apigen.CoreWriteUpdate) *pq.NodeEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_NODE)
	if m == nil || m.Entity().Node == nil {
		return nil
	}
	n := *m.Entity().Node
	return &pq.NodeEvent{NodeID: int32(m.EntityID()), Seq: u.Seq, Author: u.Actor, CreatedTime: m.Meta().CreatedTime, EventTime: u.Time, Value: n}
}

func networkPolicyOf(u *apigen.CoreWriteUpdate) *pq.NetworkPolicyEvent {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)
	if m == nil || m.Entity().NetworkPolicy == nil {
		return nil
	}
	p := *m.Entity().NetworkPolicy
	return &pq.NetworkPolicyEvent{NetworkPolicyID: int32(m.EntityID()), Seq: u.Seq, Author: u.Actor, CreatedTime: m.Meta().CreatedTime, EventTime: u.Time, Value: p}
}

func ruleTemplateOf(u *apigen.CoreWriteUpdate) *apigen.AuthzRuleTemplate {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE)
	if m == nil {
		return nil
	}
	return m.Entity().AuthzRuleTemplate
}

func grantOf(u *apigen.CoreWriteUpdate) *apigen.AuthzGrant {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)
	if m == nil {
		return nil
	}
	return m.Entity().AuthzGrant
}

func globalRuleOf(u *apigen.CoreWriteUpdate) *apigen.AuthzGlobalRule {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE)
	if m == nil {
		return nil
	}
	return m.Entity().AuthzGlobalRule
}

func (h *Handler) deploymentsCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.DeploymentEvent, error) {
	u, err := h.PostV1DeploymentsCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return deploymentOf(u), nil
}

func (h *Handler) deploymentsUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequestV2) (*apigen.DeploymentEvent, error) {
	u, err := h.PostV2DeploymentsUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	return deploymentOf(u), nil
}

func (h *Handler) nodesRename(ctx apigen.Context, req *apigen.NodeRenameRequest) (*pq.NodeEvent, error) {
	u, err := h.PostV1NodesRename(ctx, req)
	if err != nil {
		return nil, err
	}
	return nodeOf(u), nil
}

func (h *Handler) nodesAllowedSpaces(ctx apigen.Context, req *apigen.NodeAllowedSpacesRequest) (*pq.NodeEvent, error) {
	u, err := h.PostV1NodesAllowedSpaces(ctx, req)
	if err != nil {
		return nil, err
	}
	return nodeOf(u), nil
}

func (h *Handler) nodesDrain(ctx apigen.Context, req *apigen.NodeDrainRequest) (*pq.NodeEvent, error) {
	u, err := h.PostV1NodesDrain(ctx, req)
	if err != nil {
		return nil, err
	}
	return nodeOf(u), nil
}

func (h *Handler) nodesEvict(ctx apigen.Context, req *apigen.NodeEvictRequest) (*pq.NodeEvent, error) {
	u, err := h.PostV1NodesEvict(ctx, req)
	if err != nil {
		return nil, err
	}
	return nodeOf(u), nil
}

func (h *Handler) networkPoliciesCreate(ctx apigen.Context, req *apigen.NetworkPolicyCreateRequest) (*pq.NetworkPolicyEvent, error) {
	u, err := h.PostV1NetworkPoliciesCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return networkPolicyOf(u), nil
}

func (h *Handler) networkPoliciesUpdate(ctx apigen.Context, req *apigen.NetworkPolicyUpdateRequest) (*pq.NetworkPolicyEvent, error) {
	u, err := h.PostV1NetworkPoliciesUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	return networkPolicyOf(u), nil
}

func (h *Handler) secretsCreate(ctx apigen.Context, req *apigen.SecretCreateRequest) (*pq.SecretEvent, error) {
	u, err := h.PostV1SecretsCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return secretOf(u), nil
}

func (h *Handler) secretsSet(ctx apigen.Context, req *apigen.SecretSetRequest) (*pq.SecretEvent, error) {
	u, err := h.PostV1SecretsSet(ctx, req)
	if err != nil {
		return nil, err
	}
	return secretOf(u), nil
}

func (h *Handler) secretsGenerate(ctx apigen.Context, req *apigen.SecretGenerateRequest) (*pq.SecretEvent, error) {
	u, err := h.PostV1SecretsGenerate(ctx, req)
	if err != nil {
		return nil, err
	}
	return secretOf(u), nil
}

func (h *Handler) secretsMove(ctx apigen.Context, req *apigen.SecretMoveRequest) (*pq.SecretEvent, error) {
	u, err := h.PostV1SecretsMove(ctx, req)
	if err != nil {
		return nil, err
	}
	return secretOf(u), nil
}

func (h *Handler) configsCreate(ctx apigen.Context, req *apigen.ConfigCreateRequest) (*pq.ConfigEvent, error) {
	u, err := h.PostV1ConfigsCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return configOf(u), nil
}

func (h *Handler) configsRename(ctx apigen.Context, req *apigen.ConfigRenameRequest) (*pq.ConfigEvent, error) {
	u, err := h.PostV1ConfigsRename(ctx, req)
	if err != nil {
		return nil, err
	}
	return configOf(u), nil
}

func (h *Handler) configsMove(ctx apigen.Context, req *apigen.ConfigMoveRequest) (*pq.ConfigEvent, error) {
	u, err := h.PostV1ConfigsMove(ctx, req)
	if err != nil {
		return nil, err
	}
	return configOf(u), nil
}

func (h *Handler) assetsMove(ctx apigen.Context, req *apigen.AssetMoveRequest) (*pq.AssetEvent, error) {
	u, err := h.PostV1AssetsMove(ctx, req)
	if err != nil {
		return nil, err
	}
	return assetOf(u), nil
}

func (h *Handler) accessRuleTemplatesCreate(ctx apigen.Context, req *apigen.AuthzRuleTemplateCreateRequest) (*apigen.AuthzRuleTemplate, error) {
	u, err := h.PostV1AccessRuleTemplatesCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return ruleTemplateOf(u), nil
}

func (h *Handler) accessRuleTemplatesUpdate(ctx apigen.Context, req *apigen.AuthzRuleTemplateUpdateRequest) (*apigen.AuthzRuleTemplate, error) {
	u, err := h.PostV1AccessRuleTemplatesUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	return ruleTemplateOf(u), nil
}

func (h *Handler) accessGrantsCreate(ctx apigen.Context, req *apigen.AuthzGrantCreateRequest) (*apigen.AuthzGrant, error) {
	u, err := h.PostV1AccessGrantsCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return grantOf(u), nil
}

func (h *Handler) accessGlobalRulesCreate(ctx apigen.Context, req *apigen.AuthzGlobalRuleCreateRequest) (*apigen.AuthzGlobalRule, error) {
	u, err := h.PostV1AccessGlobalRulesCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return globalRuleOf(u), nil
}
