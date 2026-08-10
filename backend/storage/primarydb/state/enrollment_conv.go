package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func enrollmentRequestRowToProto(r pq.EnrollmentRequest) *apigen.EnrollmentRequestStatus {
	return &apigen.EnrollmentRequestStatus{
		ID:                  int32(r.ID),
		CreatedAt:           millisToTime(r.CreatedAt),
		UpdatedAt:           millisToTime(r.UpdatedAt),
		RequestingIpAddress: r.RequestingIpAddress,
		RequestingMachineID: r.RequestingMachineID,
		OpendeployVersion:   r.OpendeployVersion,
		UnderlayAddress:     r.UnderlayAddress,
		Status:              r.Status,
	}
}
