package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func enrollmentRequestFromRow(r pq.NodeCurrentRow) *apigen.EnrollmentRequestStatus {
	status := apigen.NodeLifecycleStatus(r.Status)
	if isMemberStatus(status) && r.EnrollmentPending == 1 {
		status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED
	}
	underlay := ""
	if addresses := parseNodeAddresses(r.Addresses); len(addresses) > 0 {
		underlay = addresses[0]
	}
	return &apigen.EnrollmentRequestStatus{
		ID:                  int32(r.ID),
		CreatedAt:           millisToTime(r.CreatedAt),
		RequestingIpAddress: r.RemoteAddress,
		RequestingMachineID: r.Identifier,
		OpendeployVersion:   r.OpendeployVersion,
		UnderlayAddress:     underlay,
		Status:              status,
		IsConnected:         r.IsConnected != 0,
	}
}
