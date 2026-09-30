package authz

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func notNullBlob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func templateUpdate(event pq.AuthzRuleTemplateEvent) (*state.Update, error) {
	m, err := pq.AuthzRuleTemplateMutation(event)
	if err != nil {
		return nil, err
	}
	return pq.NewUpdate(m), nil
}

func globalRuleUpdate(event pq.GlobalAccessRuleEvent) (*state.Update, error) {
	m, err := pq.GlobalAccessRuleMutation(event)
	if err != nil {
		return nil, err
	}
	return pq.NewUpdate(m), nil
}

func listRuleTemplates(q *pq.Queries) ([]RuleTemplateRow, error) {
	rows, err := q.ListLatestAuthzRuleTemplateEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]RuleTemplateRow, 0, len(rows))
	for _, row := range rows {
		if row.EventType == pq.EventDelete {
			continue
		}
		out = append(out, RuleTemplateRow{
			ID: row.TemplateID, Name: row.Name, Builtin: row.Builtin != 0,
			Author: row.Author, CreatedAt: row.CreatedTime, Blob: row.DataBlob,
		})
	}
	return out, nil
}

func insertRuleTemplate(store *state.Service, row RuleTemplateRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		id, err = q.NextAuthzRuleTemplateID(ctx)
		if err != nil {
			return nil, err
		}
		event := pq.AuthzRuleTemplateEvent{
			GlobalSeq: seq, EventTime: row.CreatedAt, CreatedTime: row.CreatedAt, Author: row.Author, TemplateID: id,
			Version: 1, Name: row.Name, DataBlob: notNullBlob(row.Blob), EventType: pq.EventCreate,
		}
		if err := q.InsertAuthzRuleTemplateEvent(ctx, event); err != nil {
			return nil, err
		}
		return templateUpdate(event)
	})
	return id, err
}

func updateRuleTemplate(store *state.Service, id int64, name string, blob []byte, author, updatedAt int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := pq.AuthzRuleTemplateEvent{
			GlobalSeq: seq, EventTime: updatedAt, CreatedTime: prev.CreatedTime, Author: author, TemplateID: id,
			Version: prev.Version + 1, Name: name, Builtin: prev.Builtin, DataBlob: notNullBlob(blob), EventType: pq.EventUpdate,
		}
		if err := q.InsertAuthzRuleTemplateEvent(ctx, event); err != nil {
			return nil, err
		}
		return templateUpdate(event)
	})
}

func deleteRuleTemplate(store *state.Service, id int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := pq.AuthzRuleTemplateEvent{
			GlobalSeq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, TemplateID: id,
			Version: prev.Version + 1, Name: prev.Name, Builtin: prev.Builtin, DataBlob: notNullBlob(prev.DataBlob), EventType: pq.EventDelete,
		}
		if err := q.InsertAuthzRuleTemplateEvent(ctx, event); err != nil {
			return nil, err
		}
		return templateUpdate(event)
	})
}

func upsertBuiltinRuleTemplate(store *state.Service, id int64, name string, blob []byte) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		now := time.Now().UnixMilli()
		event := pq.AuthzRuleTemplateEvent{TemplateID: id, Version: 1, Name: name, Builtin: 1, DataBlob: notNullBlob(blob), EventType: pq.EventCreate, EventTime: now, CreatedTime: now}
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case prev.EventType == pq.EventDelete:
			event.CreatedTime = prev.CreatedTime
			event.Version = prev.Version + 1
		case prev.Name != name || prev.Builtin != 1 || !bytes.Equal(prev.DataBlob, blob):
			event.CreatedTime = prev.CreatedTime
			event.Version = prev.Version + 1
			event.EventType = pq.EventUpdate
		default:
			return nil, nil
		}
		event.GlobalSeq = seq
		if err := q.InsertAuthzRuleTemplateEvent(ctx, event); err != nil {
			return nil, err
		}
		return templateUpdate(event)
	})
}

func listGrants(q *pq.Queries) ([]GrantRow, error) {
	events, err := q.ListLatestAuthzGrantEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]GrantRow, 0, len(events))
	for _, e := range events {
		if e.EventType == apigen.EventType_EVENT_TYPE_DELETE {
			continue
		}
		out = append(out, GrantRow{
			ID: e.AuthzGrantID, UserID: e.Value.UserID, TemplateID: e.Value.TemplateID, Author: e.Author, CreatedAt: e.CreatedTime, Blob: e.Value.Grant.Encode(),
		})
	}
	return out, nil
}

func insertGrant(store *state.Service, row GrantRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		id, err = q.NextAuthzGrantID(ctx)
		if err != nil {
			return nil, err
		}
		grant, err := apigen.DecodeAuthzGrant(row.Blob)
		if err != nil {
			return nil, err
		}
		event := apigen.AuthzGrantEvent{
			Seq: seq, EventTime: row.CreatedAt, CreatedTime: row.CreatedAt, Author: row.Author, AuthzGrantID: id, Version: 1,
			Value:     apigen.AuthzGrantValue{UserID: row.UserID, TemplateID: row.TemplateID, Grant: grant},
			EventType: apigen.EventType_EVENT_TYPE_CREATE,
		}
		if err := q.InsertAuthzGrantEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantMutation(&event)), nil
	})
	return id, err
}

func deleteGrant(store *state.Service, id int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := q.GetLatestAuthzGrantEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := apigen.AuthzGrantEvent{
			Seq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, AuthzGrantID: id, Version: prev.Version + 1,
			Value:     apigen.AuthzGrantValue{UserID: prev.Value.UserID, TemplateID: prev.Value.TemplateID, Grant: prev.Value.Grant},
			EventType: apigen.EventType_EVENT_TYPE_DELETE,
		}
		if err := q.InsertAuthzGrantEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantMutation(&event)), nil
	})
}

func listGlobalRules(q *pq.Queries) ([]GlobalRuleRow, error) {
	events, err := q.ListLatestGlobalAccessRuleEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]GlobalRuleRow, 0, len(events))
	for _, e := range events {
		if e.EventType == pq.EventDelete {
			continue
		}
		out = append(out, GlobalRuleRow{ID: e.RuleID, Name: e.Name, Author: e.Author, CreatedAt: e.CreatedTime, Blob: e.DataBlob})
	}
	return out, nil
}

func insertGlobalRule(store *state.Service, row GlobalRuleRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		id, err = q.NextGlobalAccessRuleID(ctx)
		if err != nil {
			return nil, err
		}
		event := pq.GlobalAccessRuleEvent{
			GlobalSeq: seq, EventTime: row.CreatedAt, CreatedTime: row.CreatedAt, Author: row.Author, RuleID: id,
			Version: 1, Name: row.Name, DataBlob: notNullBlob(row.Blob), EventType: pq.EventCreate,
		}
		if err := q.InsertGlobalAccessRuleEvent(ctx, event); err != nil {
			return nil, err
		}
		return globalRuleUpdate(event)
	})
	return id, err
}

func deleteGlobalRule(store *state.Service, id int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := q.GetLatestGlobalAccessRuleEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := pq.GlobalAccessRuleEvent{
			GlobalSeq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, RuleID: id,
			Version: prev.Version + 1, Name: prev.Name, DataBlob: notNullBlob(prev.DataBlob), EventType: pq.EventDelete,
		}
		if err := q.InsertGlobalAccessRuleEvent(ctx, event); err != nil {
			return nil, err
		}
		return globalRuleUpdate(event)
	})
}

func seedGlobalRule(store *state.Service, name string, blob []byte) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		count, err := q.CountGlobalAccessRuleEventsByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, nil
		}
		id, err := q.NextGlobalAccessRuleID(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		event := pq.GlobalAccessRuleEvent{GlobalSeq: seq, EventTime: now, CreatedTime: now, RuleID: id, Version: 1, Name: name, DataBlob: notNullBlob(blob), EventType: pq.EventCreate}
		if err := q.InsertGlobalAccessRuleEvent(ctx, event); err != nil {
			return nil, err
		}
		return globalRuleUpdate(event)
	})
}
