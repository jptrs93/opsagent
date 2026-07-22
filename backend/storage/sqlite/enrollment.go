package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	EnrollmentStatusWaiting      = "waiting"
	EnrollmentStatusDisconnected = "disconnected"
	EnrollmentStatusAccepted     = "accepted"
)

var ErrEnrollmentRequestChanged = errors.New("enrollment request changed")

func (s *PrimaryStorage) MustUpsertEnrollmentRequest(requestingIPAddress, requestingMachineID, opendeployVersion, underlayAddress string) *apigen.EnrollmentRequestStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	row, err := scanEnrollmentRequest(s.db.QueryRowContext(context.Background(), `
		INSERT INTO enrollment_requests (created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(requesting_machine_id) DO UPDATE SET
			updated_at = MAX(enrollment_requests.updated_at + 1, excluded.updated_at),
			requesting_ip_address = excluded.requesting_ip_address,
			opendeploy_version = excluded.opendeploy_version,
			underlay_address = excluded.underlay_address,
			status = excluded.status
		RETURNING id, created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status`,
		now, now, requestingIPAddress, requestingMachineID, opendeployVersion, underlayAddress, EnrollmentStatusWaiting,
	))
	if err != nil {
		panic(fmt.Sprintf("upsert enrollment request: %v", err))
	}
	s.enrollmentSubs.Notify(*row)
	return row
}

func (s *PrimaryStorage) MustMarkEnrollmentDisconnected(id int32, requestingMachineID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	row, err := scanEnrollmentRequest(s.db.QueryRowContext(context.Background(), `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ? AND requesting_machine_id = ? AND status = ?
		RETURNING id, created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status`,
		now, EnrollmentStatusDisconnected, int64(id), requestingMachineID, EnrollmentStatusWaiting,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		panic(fmt.Sprintf("mark enrollment disconnected: %v", err))
	}
	s.enrollmentSubs.Notify(*row)
}

func (s *PrimaryStorage) ListEnrollmentRequests() ([]*apigen.EnrollmentRequestStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listEnrollmentRequestsLocked()
}

func (s *PrimaryStorage) MustFetchEnrollmentSnapshotAndSubscribe() ([]*apigen.EnrollmentRequestStatus, chan apigen.EnrollmentRequestStatus, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.listEnrollmentRequestsLocked()
	if err != nil {
		return nil, nil, nil, err
	}
	sub := s.enrollmentSubs.Subscribe(nil)
	return items, sub.Ch, sub.UnsubscribeFunc, nil
}

func (s *PrimaryStorage) listEnrollmentRequestsLocked() ([]*apigen.EnrollmentRequestStatus, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status
		FROM enrollment_requests
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*apigen.EnrollmentRequestStatus
	for rows.Next() {
		item, err := scanEnrollmentRequestRows(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *PrimaryStorage) AcceptEnrollmentRequest(id int32, workerName, requestingMachineID, underlayAddress string, expectedUpdatedAt time.Time) (*apigen.EnrollmentRequestStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin enrollment accept tx: %v", err))
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	row, err := scanEnrollmentRequest(tx.QueryRowContext(ctx, `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ? AND requesting_machine_id = ? AND underlay_address = ? AND updated_at = ? AND status = ?
		RETURNING id, created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status`,
		now, EnrollmentStatusAccepted, int64(id), requestingMachineID, underlayAddress, expectedUpdatedAt.UnixMilli(), EnrollmentStatusWaiting,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEnrollmentRequestChanged
	}
	if err != nil {
		return row, err
	}
	node, err := upsertEnrolledNode(ctx, tx, int64(id), workerName, row.RequestingMachineID, []int32{NodeRoleSecondary}, []string{row.UnderlayAddress})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit enrollment accept tx: %v", err))
	}
	s.enrollmentSubs.Notify(*row)
	s.nodeSubs.Notify(*nodeToAPI(node))
	return row, nil
}

func scanEnrollmentRequest(row *sql.Row) (*apigen.EnrollmentRequestStatus, error) {
	return scanEnrollmentRequestScanner(row)
}

func scanEnrollmentRequestRows(rows *sql.Rows) (*apigen.EnrollmentRequestStatus, error) {
	return scanEnrollmentRequestScanner(rows)
}

type enrollmentRequestScanner interface {
	Scan(dest ...any) error
}

func scanEnrollmentRequestScanner(scanner enrollmentRequestScanner) (*apigen.EnrollmentRequestStatus, error) {
	var id int64
	var createdAt int64
	var updatedAt int64
	var requestingIPAddress string
	var requestingMachineID string
	var opendeployVersion string
	var underlayAddress string
	var status string
	if err := scanner.Scan(&id, &createdAt, &updatedAt, &requestingIPAddress, &requestingMachineID, &opendeployVersion, &underlayAddress, &status); err != nil {
		return nil, err
	}
	return &apigen.EnrollmentRequestStatus{
		ID:                  int32(id),
		CreatedAt:           millisToTime(createdAt),
		UpdatedAt:           millisToTime(updatedAt),
		RequestingIpAddress: requestingIPAddress,
		RequestingMachineID: requestingMachineID,
		OpendeployVersion:   opendeployVersion,
		UnderlayAddress:     underlayAddress,
		Status:              status,
	}, nil
}
