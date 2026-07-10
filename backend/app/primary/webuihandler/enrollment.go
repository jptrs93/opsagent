package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) GetV1EnrollmentInfo(ctx apigen.Context) (*apigen.EnrollmentInfo, error) {
	return h.Enrollment.GetV1EnrollmentInfo(ctx)
}

func (h *Handler) PostV1EnrollmentList(ctx apigen.Context) (*apigen.EnrollmentRequestList, error) {
	return h.Enrollment.PostV1EnrollmentList(ctx)
}

func (h *Handler) PostV1EnrollmentAccept(ctx apigen.Context, req *apigen.EnrollmentAcceptRequest) (*apigen.EnrollmentRequestStatus, error) {
	return h.Enrollment.PostV1EnrollmentAccept(ctx, req)
}
