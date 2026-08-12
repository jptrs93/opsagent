package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) GetV1NodesEnrollmentsInfo(ctx apigen.Context) (*apigen.NodeEnrollmentInfo, error) {
	if err := h.requireAccess(ctx, vView, eNode, 0, 0); err != nil {
		return nil, err
	}
	return h.Enrollment.GetV1NodesEnrollmentsInfo(ctx)
}

func (h *Handler) PostV1NodesEnrollmentsList(ctx apigen.Context) (*apigen.EnrollmentRequestList, error) {
	if err := h.requireAccess(ctx, vView, eNode, 0, 0); err != nil {
		return nil, err
	}
	return h.Enrollment.PostV1NodesEnrollmentsList(ctx)
}

func (h *Handler) PostV1NodesEnrollmentsAccept(ctx apigen.Context, req *apigen.EnrollmentAcceptRequest) (*apigen.EnrollmentRequestStatus, error) {
	// Accepting an enrollment adds a node to the cluster.
	if err := h.requireAccess(ctx, vCreate, eNode, 0, 0); err != nil {
		return nil, err
	}
	return h.Enrollment.PostV1NodesEnrollmentsAccept(ctx, req)
}
