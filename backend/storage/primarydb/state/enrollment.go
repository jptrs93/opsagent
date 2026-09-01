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

var ErrEnrollmentRequestChanged = errors.New("enrollment request changed")

func (s *Service) MustUpsertEnrollmentRequest(remoteAddress, requestingMachineID, opendeployVersion, underlayAddress, wgPublicKey string) (*apigen.EnrollmentRequestStatus, int64) {
	s.Mu.Lock()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var row pq.NodeCurrentRow
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		row, err = q.GetNodeRowByIdentifier(ctx, requestingMachineID)
		if errors.Is(err, sql.ErrNoRows) {
			seq, err := q.NextGlobalSeq(ctx)
			if err != nil {
				return err
			}
			row, err = q.InsertNodeRow(ctx, pq.InsertNodeParams{
				CreatedAt:     now,
				Name:          requestingMachineID,
				Identifier:    requestingMachineID,
				Status:        int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED),
				RolesJSON:     nodeRolesJSON([]int32{NodeRoleSecondary}),
				AddressesJSON: nodeAddressesJSON([]string{underlayAddress}),
				WgPublicKey:   wgPublicKey,
				GlobalSeq:     seq,
			})
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if !isMemberStatus(apigen.NodeLifecycleStatus(row.Status)) {
			row, _, err = appendNodeVersion(ctx, q, row, 0, func(spec *nodeVersionSpec) {
				spec.Status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED
				spec.RolesJSON = nodeRolesJSON([]int32{NodeRoleSecondary})
				spec.AddressesJSON = nodeAddressesJSON([]string{underlayAddress})
				spec.WGPublicKey = wgPublicKey
			})
			if err != nil {
				return err
			}
		}
		if err := q.UpsertNodeObservedMeta(ctx, pq.UpsertNodeObservedMetaParams{
			NodeID:            row.ID,
			LastConnectedAt:   now,
			IsConnected:       1,
			OpendeployVersion: opendeployVersion,
			RemoteAddress:     remoteAddress,
			EnrollmentPending: 1,
		}); err != nil {
			return err
		}
		row, err = q.GetNodeRowByID(ctx, row.ID)
		return err
	})
	s.Mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("upsert enrollment request: %v", err))
	}
	status := enrollmentRequestFromRow(row)
	s.enrollmentSubs.Notify(*status)
	return status, row.Version
}

func (s *Service) MustMarkEnrollmentDisconnected(id int32, requestingMachineID string) {
	s.Mu.Lock()
	ctx := context.Background()
	var row pq.NodeCurrentRow
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		row, err = q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return err
		}
		if row.Identifier != requestingMachineID {
			return sql.ErrNoRows
		}
		if _, err := q.SetNodeConnectionStatus(ctx, pq.SetNodeConnectionStatusParams{
			Connected:  0,
			Identifier: requestingMachineID,
		}); err != nil {
			return err
		}
		row, err = q.GetNodeRowByID(ctx, int64(id))
		return err
	})
	s.Mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		panic(fmt.Sprintf("mark enrollment disconnected: %v", err))
	}
	s.enrollmentSubs.Notify(*enrollmentRequestFromRow(row))
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
	return items, sub.Ch, sub.Unsubscribe, nil
}

func (s *Service) listEnrollmentRequestsLocked() ([]*apigen.EnrollmentRequestStatus, error) {
	rows, err := s.q.ListEnrollmentNodeRows(context.Background(), enrollmentNodeStatuses)
	if err != nil {
		return nil, err
	}
	var items []*apigen.EnrollmentRequestStatus
	for _, row := range rows {
		items = append(items, enrollmentRequestFromRow(row))
	}
	return items, nil
}

func (s *Service) AcceptEnrollmentRequest(id int32, nodeName, requestingMachineID, underlayAddress, wgPublicKey string, expectedVersion int64) (*apigen.EnrollmentRequestStatus, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	ctx := context.Background()
	now := time.Now().UnixMilli()
	var row pq.NodeCurrentRow
	var node *Node
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		current, err := q.GetNodeRowByID(ctx, int64(id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEnrollmentRequestChanged
		}
		if err != nil {
			return err
		}
		if current.Identifier != requestingMachineID || current.Version != expectedVersion {
			return ErrEnrollmentRequestChanged
		}
		if err := q.UpdateNodeAccepted(ctx, nodeName, now, int64(id)); err != nil {
			return err
		}
		current, err = q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return err
		}
		row, _, err = appendNodeVersion(ctx, q, current, 0, func(spec *nodeVersionSpec) {
			spec.Status = apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL
			spec.RolesJSON = nodeRolesJSON([]int32{NodeRoleSecondary})
			spec.AddressesJSON = nodeAddressesJSON([]string{underlayAddress})
			spec.WGPublicKey = wgPublicKey
		})
		if err != nil {
			return err
		}
		if err := q.ClearNodeEnrollmentPending(ctx, int64(id)); err != nil {
			return err
		}
		row, err = q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return err
		}
		node = nodeRowToNode(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	status := enrollmentRequestFromRow(row)
	s.enrollmentSubs.Notify(*status)
	s.nodeSubs.Notify(*nodeToAPI(node))
	return status, nil
}
