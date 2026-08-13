package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func filterVisible[T any](items []T, keep func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

func (h *Handler) filterSpaces(ctx apigen.Context, items []*apigen.Space) []*apigen.Space {
	return filterVisible(items, func(s *apigen.Space) bool {
		return h.spaceVisible(ctx, int64(s.ID))
	})
}

func (h *Handler) filterSecretMetas(ctx apigen.Context, items []*apigen.SecretMeta) []*apigen.SecretMeta {
	return filterVisible(items, func(m *apigen.SecretMeta) bool {
		return h.canAccess(ctx, vView, eSecret, int64(m.SpaceID), int64(m.ID))
	})
}

func (h *Handler) filterConfigMetas(ctx apigen.Context, items []*apigen.ConfigMeta) []*apigen.ConfigMeta {
	return filterVisible(items, func(m *apigen.ConfigMeta) bool {
		return h.canAccess(ctx, vView, eConfig, int64(m.SpaceID), int64(m.ID))
	})
}

func (h *Handler) filterAssetMetas(ctx apigen.Context, items []*apigen.AssetMeta) []*apigen.AssetMeta {
	return filterVisible(items, func(m *apigen.AssetMeta) bool {
		return h.canAccess(ctx, vView, eAsset, int64(m.SpaceID), int64(m.ID))
	})
}

func (h *Handler) filterValueDirectories(ctx apigen.Context, items []*apigen.ValueDirectory) []*apigen.ValueDirectory {
	return filterVisible(items, func(d *apigen.ValueDirectory) bool {
		return h.canAccessAny(ctx, vView, eValues, int64(d.SpaceID), 0)
	})
}

func (h *Handler) filterAssetDirectories(ctx apigen.Context, items []*apigen.AssetDirectory) []*apigen.AssetDirectory {
	return filterVisible(items, func(d *apigen.AssetDirectory) bool {
		return h.canAccess(ctx, vView, eAsset, int64(d.SpaceID), 0)
	})
}

func (h *Handler) filterDeploymentConfigs(ctx apigen.Context, items []*apigen.DeploymentConfig) []*apigen.DeploymentConfig {
	return filterVisible(items, func(cfg *apigen.DeploymentConfig) bool {
		return h.canAccess(ctx, vView, eDeployment, int64(cfg.Identity.SpaceID), int64(cfg.ID))
	})
}

func (h *Handler) filterNodes(ctx apigen.Context, items []*apigen.ClusterNode) []*apigen.ClusterNode {
	return filterVisible(items, func(n *apigen.ClusterNode) bool {
		return h.nodeVisible(ctx, int64(n.ID), n.AllowedSpaces)
	})
}

func (h *Handler) filterNodeStatuses(ctx apigen.Context, items []*apigen.ClusterNodeStatus) []*apigen.ClusterNodeStatus {
	allowed := h.nodeAllowedSpaces()
	return filterVisible(items, func(n *apigen.ClusterNodeStatus) bool {
		return h.nodeVisible(ctx, int64(n.NodeID), allowed[n.NodeID])
	})
}

func (h *Handler) filterUsers(ctx apigen.Context, items []*apigen.User) []*apigen.User {
	return filterVisible(items, func(u *apigen.User) bool {
		if ctx.User != nil && u.ID == ctx.User.ID {
			return true
		}
		return h.canAccess(ctx, vView, eUser, 0, int64(u.ID))
	})
}
