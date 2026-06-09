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

func (s *PrimaryStorage) MustUpsertEnrollmentRequest(requestingIPAddress, requestingMachineID string) *apigen.EnrollmentRequestStatus {
	now := time.Now().UnixMilli()
	row, err := scanEnrollmentRequest(s.db.QueryRowContext(context.Background(), `
		INSERT INTO enrollment_requests (created_at, updated_at, requesting_ip_address, requesting_machine_id, status)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(requesting_machine_id) DO UPDATE SET
			updated_at = excluded.updated_at,
			requesting_ip_address = excluded.requesting_ip_address,
			status = excluded.status
		RETURNING id, created_at, updated_at, requesting_ip_address, requesting_machine_id, status`,
		now, now, requestingIPAddress, requestingMachineID, EnrollmentStatusWaiting,
	))
	if err != nil {
		panic(fmt.Sprintf("upsert enrollment request: %v", err))
	}
	return row
}

func (s *PrimaryStorage) MustMarkEnrollmentDisconnected(id int32, requestingMachineID string) {
	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(context.Background(), `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ? AND requesting_machine_id = ? AND status = ?`,
		now, EnrollmentStatusDisconnected, int64(id), requestingMachineID, EnrollmentStatusWaiting,
	); err != nil {
		panic(fmt.Sprintf("mark enrollment disconnected: %v", err))
	}
}

func (s *PrimaryStorage) ListEnrollmentRequests() ([]*apigen.EnrollmentRequestStatus, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, created_at, updated_at, requesting_ip_address, requesting_machine_id, status
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

func (s *PrimaryStorage) AcceptEnrollmentRequest(id int32) (*apigen.EnrollmentRequestStatus, error) {
	now := time.Now().UnixMilli()
	row, err := scanEnrollmentRequest(s.db.QueryRowContext(context.Background(), `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ?
		RETURNING id, created_at, updated_at, requesting_ip_address, requesting_machine_id, status`,
		now, EnrollmentStatusAccepted, int64(id),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return row, err
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
	var status string
	if err := scanner.Scan(&id, &createdAt, &updatedAt, &requestingIPAddress, &requestingMachineID, &status); err != nil {
		return nil, err
	}
	return &apigen.EnrollmentRequestStatus{
		ID:                  int32(id),
		CreatedAt:           millisToTime(createdAt),
		UpdatedAt:           millisToTime(updatedAt),
		RequestingIpAddress: requestingIPAddress,
		RequestingMachineID: requestingMachineID,
		Status:              status,
	}, nil
}
