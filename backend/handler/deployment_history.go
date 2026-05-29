package handler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) PostV1DeploymentStatusHistory(ctx apigen.Context, req *apigen.DeploymentHistoryRequest) (*apigen.DeploymentStatusHistoryResponse, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	return &apigen.DeploymentStatusHistoryResponse{
		Statuses: h.Store.MustFetchDeploymentStatusHistory(req.DeploymentID),
	}, nil
}

func (h *Handler) PostV1DeploymentConfigHistory(ctx apigen.Context, req *apigen.DeploymentHistoryRequest) (*apigen.DeploymentConfigHistoryResponse, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	return &apigen.DeploymentConfigHistoryResponse{
		Configs: h.Store.MustFetchDeploymentHistory(req.DeploymentID),
	}, nil
}
