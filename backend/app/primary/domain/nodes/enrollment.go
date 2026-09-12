package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ErrEnrollmentRequestChanged = errors.New("enrollment request changed")
var ErrEnrollmentIdentifierEnrolled = errors.New("identifier is already enrolled")

func UpsertEnrollmentRequest(store *state.Service, remoteAddress, opendeployVersion string, reported apigen.NodeReported) (*apigen.EnrollmentRequestStatus, int64, error) {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	requestingMachineID := reported.Identifier
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, requestingMachineID)
		isNew := errors.Is(err, sql.ErrNoRows)
		if err != nil && !isNew {
			return nil, err
		}
		if !isNew && isMemberStatus(current.Event.Value.Status) {
			return nil, ErrEnrollmentIdentifierEnrolled
		}
		spec := nodeEventSpecOf(current)
		spec.Status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED
		if !reported.HostAddressesUnknown {
			spec.HostAddressesJSON = nodeAddressesJSON(canonicalHostAddresses(reported.HostAddresses))
		}
		spec.AddressesJSON = nodeAddressesJSON([]string{reported.UnderlayAddress})
		spec.WGPublicKey = reported.WgPublicKey
		changed := isNew || current.Event.Value.EnrollmentRequestedAt == 0 || spec != nodeEventSpecOf(current)
		var update state.Update
		row = current
		if changed {
			if isNew {
				row, err = q.InsertNodeRow(ctx, pq.InsertNodeParams{
					CreatedAt: now, EnrollmentRequestedAt: now,
					HostAddressesJSON: nodeAddressesJSON(canonicalHostAddresses(reported.HostAddresses)),
					Name:              requestingMachineID, Identifier: requestingMachineID,
					Status:    int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED),
					RolesJSON: nodeRolesJSON([]int32{NodeRoleSecondary}), AddressesJSON: spec.AddressesJSON,
					WgPublicKey: reported.WgPublicKey, GlobalSeq: seq,
				})
			} else {
				if spec.EnrollmentRequestedAt == 0 {
					spec.EnrollmentRequestedAt, err = q.NextEnrollmentRequestedAt(ctx, int64(current.Event.NodeID), now)
					if err != nil {
						return nil, err
					}
				}
				row, _, err = appendNodeVersion(ctx, q, seq, current, 0, func(next *nodeEventSpec) { *next = spec })
			}
			if err != nil {
				return nil, err
			}
			update.NodeEvents = []*apigen.NodeEvent{&row.Event}
		}
		if _, err := q.UpsertNodeObservedMeta(ctx, seq, row.Event.NodeID, time.UnixMilli(now), opendeployVersion, remoteAddress); err != nil {
			return nil, err
		}
		row, err = q.GetNodeRowByID(ctx, int64(row.Event.NodeID))
		if err != nil {
			return nil, err
		}
		update.NodeStatuses = []*apigen.NodeStatus{&row.Status}
		return &update, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return enrollmentRequestFromRow(row), int64(row.Event.Version), nil
}

func MarkEnrollmentDisconnected(store *state.Service, id int32, requestingMachineID string) {
	ctx := context.Background()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		row, err = q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if row.Event.Value.Reported.Identifier != requestingMachineID {
			return nil, sql.ErrNoRows
		}
		if _, err := q.SetNodeConnectionStatus(ctx, seq, requestingMachineID, false, time.Time{}); err != nil {
			return nil, err
		}
		row, err = q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		return &state.Update{NodeStatuses: []*apigen.NodeStatus{&row.Status}}, nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		panic(fmt.Sprintf("mark enrollment disconnected: %v", err))
	}

}

func ListEnrollmentRequests(q *pq.Queries) ([]*apigen.EnrollmentRequestStatus, error) {
	rows, err := q.ListEnrollmentNodeRows(context.Background(), pq.EnrollmentNodeStatuses)
	if err != nil {
		return nil, err
	}
	var items []*apigen.EnrollmentRequestStatus
	for _, row := range rows {
		items = append(items, enrollmentRequestFromRow(row))
	}
	return items, nil
}

func AcceptEnrollmentRequest(store *state.Service, id int32, nodeName, requestingMachineID string, expectedVersion int64) (*apigen.EnrollmentRequestStatus, error) {

	ctx := context.Background()
	now := time.Now().UnixMilli()
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		current, err := q.GetNodeRowByID(ctx, int64(id))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEnrollmentRequestChanged
		}
		if err != nil {
			return nil, err
		}
		if int64(current.Event.Version) != expectedVersion ||
			current.Event.Value.Reported.Identifier != requestingMachineID ||
			current.Event.Value.EnrollmentRequestedAt == 0 {
			return nil, ErrEnrollmentRequestChanged
		}
		taken, err := q.CountNodesWithName(ctx, nodeName, int64(id))
		if err != nil {
			return nil, err
		}
		if taken > 0 {
			return nil, ErrDuplicateNodeName
		}
		row, _, err = appendNodeVersion(ctx, q, seq, current, 0, func(spec *nodeEventSpec) {
			spec.Name = nodeName
			if spec.EnrolledTime == 0 {
				spec.EnrolledTime = now
			}
			spec.Status = apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL
			spec.RolesJSON = nodeRolesJSON([]int32{NodeRoleSecondary})
			spec.EnrollmentRequestedAt = 0
		})
		if err != nil {
			return nil, err
		}
		return &state.Update{NodeEvents: []*apigen.NodeEvent{&row.Event}}, nil
	})
	if err != nil {
		return nil, err
	}
	status := enrollmentRequestFromRow(row)

	return status, nil
}
func EndEnrollmentRequest(store *state.Service, id int32, requestedAt int64, expired bool) error {
	ctx := context.Background()
	current, err := store.Queries().GetNodeRowByID(ctx, int64(id))
	if err != nil {
		return err
	}
	if current.Event.Value.EnrollmentRequestedAt == 0 ||
		current.Event.Value.EnrollmentRequestedAt != requestedAt {
		return nil
	}
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		row, _, err := appendNodeVersion(ctx, q, seq, current, 0, func(spec *nodeEventSpec) {
			spec.EnrollmentRequestedAt = 0
			if spec.EnrolledTime == 0 {
				spec.Status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED
				if expired {
					spec.Status = apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED
				}
			}
		})
		if err != nil {
			return nil, err
		}
		return &state.Update{NodeEvents: []*apigen.NodeEvent{&row.Event}}, nil
	})
}
