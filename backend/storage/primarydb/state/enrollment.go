package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const (
	EnrollmentStatusWaiting      = "waiting"
	EnrollmentStatusDisconnected = "disconnected"
	EnrollmentStatusAccepted     = "accepted"
)

var ErrEnrollmentRequestChanged = errors.New("enrollment request changed")

func (s *Service) MustUpsertEnrollmentRequest(requestingIPAddress, requestingMachineID, opendeployVersion, underlayAddress string) *apigen.EnrollmentRequestStatus {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.UpsertEnrollmentRequest(context.Background(), pq.UpsertEnrollmentRequestParams{
		Now:                 time.Now().UnixMilli(),
		RequestingIPAddress: requestingIPAddress,
		RequestingMachineID: requestingMachineID,
		OpendeployVersion:   opendeployVersion,
		UnderlayAddress:     underlayAddress,
		Status:              EnrollmentStatusWaiting,
	})
	if err != nil {
		panic(fmt.Sprintf("upsert enrollment request: %v", err))
	}
	status := enrollmentRequestRowToProto(row)
	s.enrollmentSubs.Notify(*status)
	return status
}

func (s *Service) MustMarkEnrollmentDisconnected(id int32, requestingMachineID string) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.TransitionEnrollmentStatus(context.Background(), pq.TransitionEnrollmentStatusParams{
		Now:                 time.Now().UnixMilli(),
		ID:                  int64(id),
		RequestingMachineID: requestingMachineID,
		FromStatus:          EnrollmentStatusWaiting,
		ToStatus:            EnrollmentStatusDisconnected,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		panic(fmt.Sprintf("mark enrollment disconnected: %v", err))
	}
	s.enrollmentSubs.Notify(*enrollmentRequestRowToProto(row))
}

func (s *Service) ListEnrollmentRequests() ([]*apigen.EnrollmentRequestStatus, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.listEnrollmentRequestsLocked()
}

func (s *Service) MustFetchEnrollmentSnapshotAndSubscribe() ([]*apigen.EnrollmentRequestStatus, chan apigen.EnrollmentRequestStatus, func(), error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	items, err := s.listEnrollmentRequestsLocked()
	if err != nil {
		return nil, nil, nil, err
	}
	sub := s.enrollmentSubs.Subscribe(nil)
	return items, sub.Ch, sub.UnsubscribeFunc, nil
}

func (s *Service) listEnrollmentRequestsLocked() ([]*apigen.EnrollmentRequestStatus, error) {
	rows, err := s.q.ListEnrollmentRequestRows(context.Background())
	if err != nil {
		return nil, err
	}
	var items []*apigen.EnrollmentRequestStatus
	for _, row := range rows {
		items = append(items, enrollmentRequestRowToProto(row))
	}
	return items, nil
}

func (s *Service) AcceptEnrollmentRequest(id int32, workerName, requestingMachineID, underlayAddress string, expectedUpdatedAt time.Time) (*apigen.EnrollmentRequestStatus, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	ctx := context.Background()
	now := time.Now().UnixMilli()
	var request pq.EnrollmentRequest
	var node *Node
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		request, err = q.AcceptEnrollmentRequestRow(ctx, pq.AcceptEnrollmentRequestParams{
			Now:                 now,
			ID:                  int64(id),
			RequestingMachineID: requestingMachineID,
			UnderlayAddress:     underlayAddress,
			ExpectedUpdatedAt:   expectedUpdatedAt.UnixMilli(),
			FromStatus:          EnrollmentStatusWaiting,
			ToStatus:            EnrollmentStatusAccepted,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEnrollmentRequestChanged
		}
		if err != nil {
			return err
		}
		nodeRow, err := q.UpsertEnrolledNodeRow(ctx, pq.UpsertEnrolledNodeRowParams{
			EnrollmentID:  int64(id),
			EnrolledAt:    time.Now().UnixMilli(),
			Name:          workerName,
			Identifier:    request.RequestingMachineID,
			RolesJSON:     nodeRolesJSON([]int32{NodeRoleSecondary}),
			AddressesJSON: nodeAddressesJSON([]string{request.UnderlayAddress}),
		})
		if err != nil {
			return err
		}
		node = nodeRowToNode(nodeRow)
		return nil
	})
	if err != nil {
		return nil, err
	}
	status := enrollmentRequestRowToProto(request)
	s.enrollmentSubs.Notify(*status)
	s.nodeSubs.Notify(*nodeToAPI(node))
	return status, nil
}
