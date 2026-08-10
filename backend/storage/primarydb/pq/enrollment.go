package pq

import (
	"context"
)

// Hand-written enrollment queries. Status strings and their transitions are
// the storage layer's domain; here they are opaque parameters.

const enrollmentColumns = `id, created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status`

type enrollmentScanner interface {
	Scan(dest ...any) error
}

func scanEnrollmentRequestRow(scanner enrollmentScanner) (EnrollmentRequest, error) {
	var r EnrollmentRequest
	err := scanner.Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt, &r.RequestingIpAddress, &r.RequestingMachineID, &r.OpendeployVersion, &r.UnderlayAddress, &r.Status)
	return r, err
}

type UpsertEnrollmentRequestParams struct {
	Now                 int64
	RequestingIPAddress string
	RequestingMachineID string
	OpendeployVersion   string
	UnderlayAddress     string
	Status              string
}

// UpsertEnrollmentRequest inserts a request or refreshes the one already open
// for the machine id, bumping updated_at monotonically.
func (q *Queries) UpsertEnrollmentRequest(ctx context.Context, p UpsertEnrollmentRequestParams) (EnrollmentRequest, error) {
	return scanEnrollmentRequestRow(q.db.QueryRowContext(ctx, `
		INSERT INTO enrollment_requests (created_at, updated_at, requesting_ip_address, requesting_machine_id, opendeploy_version, underlay_address, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(requesting_machine_id) DO UPDATE SET
			updated_at = MAX(enrollment_requests.updated_at + 1, excluded.updated_at),
			requesting_ip_address = excluded.requesting_ip_address,
			opendeploy_version = excluded.opendeploy_version,
			underlay_address = excluded.underlay_address,
			status = excluded.status
		RETURNING `+enrollmentColumns,
		p.Now, p.Now, p.RequestingIPAddress, p.RequestingMachineID, p.OpendeployVersion, p.UnderlayAddress, p.Status))
}

type TransitionEnrollmentStatusParams struct {
	Now                 int64
	ID                  int64
	RequestingMachineID string
	FromStatus          string
	ToStatus            string
}

// TransitionEnrollmentStatus moves a request from one status to another,
// returning sql.ErrNoRows when the row is not in FromStatus any more.
func (q *Queries) TransitionEnrollmentStatus(ctx context.Context, p TransitionEnrollmentStatusParams) (EnrollmentRequest, error) {
	return scanEnrollmentRequestRow(q.db.QueryRowContext(ctx, `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ? AND requesting_machine_id = ? AND status = ?
		RETURNING `+enrollmentColumns,
		p.Now, p.ToStatus, p.ID, p.RequestingMachineID, p.FromStatus))
}

type AcceptEnrollmentRequestParams struct {
	Now                 int64
	ID                  int64
	RequestingMachineID string
	UnderlayAddress     string
	ExpectedUpdatedAt   int64
	FromStatus          string
	ToStatus            string
}

// AcceptEnrollmentRequestRow is TransitionEnrollmentStatus with the accept
// path's optimistic-concurrency guards: the caller-observed underlay address
// and updated_at must still match.
func (q *Queries) AcceptEnrollmentRequestRow(ctx context.Context, p AcceptEnrollmentRequestParams) (EnrollmentRequest, error) {
	return scanEnrollmentRequestRow(q.db.QueryRowContext(ctx, `
		UPDATE enrollment_requests
		SET updated_at = ?, status = ?
		WHERE id = ? AND requesting_machine_id = ? AND underlay_address = ? AND updated_at = ? AND status = ?
		RETURNING `+enrollmentColumns,
		p.Now, p.ToStatus, p.ID, p.RequestingMachineID, p.UnderlayAddress, p.ExpectedUpdatedAt, p.FromStatus))
}

func (q *Queries) ListEnrollmentRequestRows(ctx context.Context) ([]EnrollmentRequest, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+enrollmentColumns+`
		FROM enrollment_requests
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnrollmentRequest
	for rows.Next() {
		r, err := scanEnrollmentRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
