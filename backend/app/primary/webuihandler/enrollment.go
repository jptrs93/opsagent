package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) GetV1NodesEnrollmentsInfo(ctx apigen.Context) (*apigen.NodeEnrollmentInfo, error) {
	return h.Enrollment.GetV1NodesEnrollmentsInfo(ctx)
}

func (h *Handler) PostV1NodesEnrollmentsList(ctx apigen.Context) (*apigen.EnrollmentRequestList, error) {
	return h.Enrollment.PostV1NodesEnrollmentsList(ctx)
}

func (h *Handler) PostV1NodesEnrollmentsAccept(ctx apigen.Context, req *apigen.EnrollmentAcceptRequest) (*apigen.EnrollmentRequestStatus, error) {
	return h.Enrollment.PostV1NodesEnrollmentsAccept(ctx, req)
}
