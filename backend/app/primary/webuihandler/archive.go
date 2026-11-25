package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) PostV1ArchiveStatus(ctx apigen.Context, _ *apigen.ArchiveStatusRequest) (*apigen.ArchiveStatusResponse, error) {
	if err := h.requireAccess(ctx, vView, eNode, 0, 0); err != nil {
		return nil, err
	}
	if h.Chunks == nil {
		return &apigen.ArchiveStatusResponse{}, nil
	}
	return h.Chunks.Status(ctx)
}
