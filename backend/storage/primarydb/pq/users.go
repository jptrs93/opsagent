package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type UserRow struct {
	EventMeta
	ID        int64
	UserID    int64
	Name      string
	DataBlob  []byte
	CreatedAt int64
}

func (r UserRow) Public() apigen.User {
	return apigen.User{ID: int32(r.UserID), Name: r.Name, CreatedAt: r.CreatedAt}
}

const userColumns = `id, global_seq, event_time, author, user_id, event_type, name, data_blob, created_at`

func scanUserRow(row scanner) (UserRow, error) {
	var r UserRow
	err := row.Scan(&r.ID, &r.GlobalSeq, &r.EventTime, &r.Author, &r.UserID, &r.EventType, &r.Name, &r.DataBlob, &r.CreatedAt)
	return r, err
}

const liveUsers = `FROM user_event_log WHERE id IN (SELECT MAX(id) FROM user_event_log GROUP BY user_id) AND event_type != 3`

func (q *Queries) listUserRows(ctx context.Context, where string, args ...any) ([]UserRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+userColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		r, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetUserRow returns the newest row for the user, deleted or not.
func (q *Queries) GetUserRow(ctx context.Context, userID int64) (UserRow, error) {
	return scanUserRow(q.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM user_event_log WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID))
}

func (q *Queries) getLiveUserRow(ctx context.Context, userID int64) (UserRow, error) {
	r, err := q.GetUserRow(ctx, userID)
	if err == nil && r.deleted() {
		return UserRow{}, sql.ErrNoRows
	}
	return r, err
}

func (q *Queries) GetUser(ctx context.Context, userID int64) (apigen.User, error) {
	r, err := q.getLiveUserRow(ctx, userID)
	return r.Public(), err
}

func (q *Queries) ListUsers(ctx context.Context) ([]apigen.User, error) {
	rows, err := q.listUserRows(ctx, liveUsers+` ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Public())
	}
	return out, nil
}

func (q *Queries) ListUsersAtSeq(ctx context.Context, seq int64) ([]apigen.User, error) {
	rows, err := q.listUserRows(ctx, `FROM user_event_log WHERE global_seq = ? ORDER BY id`, seq)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Public())
	}
	return out, nil
}

func (q *Queries) GetInternalUser(ctx context.Context, userID int64) (*apigen.InternalUser, error) {
	r, err := q.getLiveUserRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(r.DataBlob)
}

func (q *Queries) ListInternalUsers(ctx context.Context) ([]*apigen.InternalUser, error) {
	rows, err := q.listUserRows(ctx, liveUsers+` ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	var out []*apigen.InternalUser
	for _, r := range rows {
		u, err := apigen.DecodeInternalUser(r.DataBlob)
		if err != nil {
			// Preserve matching semantics: an invalid credential record cannot match.
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func (q *Queries) NextUserID(ctx context.Context) (int64, error) {
	return q.nextEntityID(ctx, "user_event_log", "user_id")
}

type UserEventParams struct {
	EventMeta
	UserID    int64
	Name      string
	DataBlob  []byte
	CreatedAt int64
}

func (q *Queries) InsertUserEvent(ctx context.Context, arg UserEventParams) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO user_event_log (global_seq, event_time, author, user_id, event_type, name, data_blob, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, arg.GlobalSeq, arg.EventTime, arg.Author, arg.UserID, arg.EventType, arg.Name, arg.DataBlob, arg.CreatedAt)
	return err
}
