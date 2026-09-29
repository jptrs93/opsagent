package systemconfig

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func LatestRevision(q *pq.Queries) (pq.SystemConfigRevision, error) {
	return q.GetLatestConfig(context.Background())
}

func AppendRevision(store *state.Service, author int32, blob []byte, inlockValidate func(*pq.Queries) error) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		eventType := apigen.AuthzVerb_AUTHZ_VERB_UPDATE
		if _, err := q.GetLatestConfig(ctx); errors.Is(err, sql.ErrNoRows) {
			eventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
		} else if err != nil {
			return nil, err
		}
		var err error
		id, err = q.InsertSystemConfigRevision(ctx, pq.SystemConfigRevisionParams{
			EventMeta:  pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(author), EventType: eventType},
			ConfigBlob: blob,
		})
		if err != nil {
			return nil, err
		}
		row, err := q.GetSystemConfigByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return &apigen.CoreUpdate{SystemConfig: row}, nil
	})
	return id, err
}
