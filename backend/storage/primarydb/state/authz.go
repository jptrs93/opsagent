package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func notNullBlob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func (s *Service) ListAuthzRuleTemplates() ([]authz.RuleTemplateRow, error) {
	rows, err := s.q.ListLatestAuthzRuleTemplateEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.RuleTemplateRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, authz.RuleTemplateRow{
			ID:        row.TemplateID,
			Name:      row.Name,
			Builtin:   row.Builtin != 0,
			Deleted:   row.EventType == pq.EventDelete,
			Author:    row.FirstAuthor,
			CreatedAt: row.CreatedTime,
			Blob:      row.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzRuleTemplate(row authz.RuleTemplateRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.NextAuthzRuleTemplateID(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertAuthzRuleTemplateEvent(ctx, pq.AuthzRuleTemplateEvent{
			GlobalSeq:   seq,
			EventTime:   row.CreatedAt,
			CreatedTime: row.CreatedAt,
			Author:      row.Author,
			TemplateID:  id,
			Version:     1,
			Name:        row.Name,
			DataBlob:    notNullBlob(row.Blob),
			EventType:   pq.EventCreate,
		})
	})
	return id, err
}

func (s *Service) UpdateAuthzRuleTemplate(id int64, name string, blob []byte, author, updatedAt int64) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertAuthzRuleTemplateEvent(ctx, pq.AuthzRuleTemplateEvent{
			GlobalSeq:   seq,
			EventTime:   updatedAt,
			CreatedTime: prev.CreatedTime,
			Author:      author,
			TemplateID:  id,
			Version:     prev.Version + 1,
			Name:        name,
			Builtin:     prev.Builtin,
			DataBlob:    notNullBlob(blob),
			EventType:   pq.EventUpdate,
		})
	})
}

func (s *Service) DeleteAuthzRuleTemplate(id int64) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertAuthzRuleTemplateEvent(ctx, pq.AuthzRuleTemplateEvent{
			GlobalSeq:   seq,
			EventTime:   time.Now().UnixMilli(),
			CreatedTime: prev.CreatedTime,
			TemplateID:  id,
			Version:     prev.Version + 1,
			Name:        prev.Name,
			Builtin:     prev.Builtin,
			DataBlob:    prev.DataBlob,
			EventType:   pq.EventDelete,
		})
	})
}

// UpsertAuthzRuleTemplate seeds a builtin template. It runs on every startup,
// so an event is appended only when the name or content actually changed.
func (s *Service) UpsertAuthzRuleTemplate(id int64, name string, blob []byte) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		prev, err := q.GetLatestAuthzRuleTemplateEvent(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		event := pq.AuthzRuleTemplateEvent{
			TemplateID: id,
			Version:    1,
			Name:       name,
			Builtin:    1,
			DataBlob:   notNullBlob(blob),
			EventType:  pq.EventCreate,
		}
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
			return nil
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		event.GlobalSeq = seq
		return q.InsertAuthzRuleTemplateEvent(ctx, event)
	})
}

func (s *Service) ListAuthzGrants() ([]authz.GrantRow, error) {
	events, err := s.q.ListLatestAuthzGrantEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.GrantRow, 0, len(events))
	for _, e := range events {
		if e.EventType == pq.EventDelete {
			continue
		}
		out = append(out, authz.GrantRow{
			ID:         e.GrantID,
			UserID:     e.UserID,
			TemplateID: e.TemplateID,
			Author:     e.Author,
			CreatedAt:  e.CreatedTime,
			Blob:       e.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzGrant(row authz.GrantRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.NextAuthzGrantID(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertAuthzGrantEvent(ctx, pq.AuthzGrantEvent{
			GlobalSeq:   seq,
			EventTime:   row.CreatedAt,
			CreatedTime: row.CreatedAt,
			Author:      row.Author,
			GrantID:     id,
			Version:     1,
			UserID:      row.UserID,
			TemplateID:  row.TemplateID,
			DataBlob:    notNullBlob(row.Blob),
			EventType:   pq.EventCreate,
		})
	})
	return id, err
}

func (s *Service) DeleteAuthzGrant(id int64) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		prev, err := q.GetLatestAuthzGrantEvent(ctx, id)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertAuthzGrantEvent(ctx, pq.AuthzGrantEvent{
			GlobalSeq:   seq,
			EventTime:   time.Now().UnixMilli(),
			CreatedTime: prev.CreatedTime,
			GrantID:     id,
			Version:     prev.Version + 1,
			UserID:      prev.UserID,
			TemplateID:  prev.TemplateID,
			DataBlob:    prev.DataBlob,
			EventType:   pq.EventDelete,
		})
	})
}

func (s *Service) ListAuthzGlobalRules() ([]authz.GlobalRuleRow, error) {
	events, err := s.q.ListLatestGlobalAccessRuleEvents(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]authz.GlobalRuleRow, 0, len(events))
	for _, e := range events {
		if e.EventType == pq.EventDelete {
			continue
		}
		out = append(out, authz.GlobalRuleRow{
			ID:        e.RuleID,
			Name:      e.Name,
			Author:    e.Author,
			CreatedAt: e.CreatedTime,
			Blob:      e.DataBlob,
		})
	}
	return out, nil
}

func (s *Service) InsertAuthzGlobalRule(row authz.GlobalRuleRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.NextGlobalAccessRuleID(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertGlobalAccessRuleEvent(ctx, pq.GlobalAccessRuleEvent{
			GlobalSeq:   seq,
			EventTime:   row.CreatedAt,
			CreatedTime: row.CreatedAt,
			Author:      row.Author,
			RuleID:      id,
			Version:     1,
			Name:        row.Name,
			DataBlob:    notNullBlob(row.Blob),
			EventType:   pq.EventCreate,
		})
	})
	return id, err
}

func (s *Service) DeleteAuthzGlobalRule(id int64) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		prev, err := q.GetLatestGlobalAccessRuleEvent(ctx, id)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertGlobalAccessRuleEvent(ctx, pq.GlobalAccessRuleEvent{
			GlobalSeq:   seq,
			EventTime:   time.Now().UnixMilli(),
			CreatedTime: prev.CreatedTime,
			RuleID:      id,
			Version:     prev.Version + 1,
			Name:        prev.Name,
			DataBlob:    prev.DataBlob,
			EventType:   pq.EventDelete,
		})
	})
}

// SeedAuthzGlobalRule inserts a rule once per name: any event with the name,
// including a delete, blocks re-seeding so a deleted seeded rule stays
// deleted.
func (s *Service) SeedAuthzGlobalRule(name string, blob []byte) error {
	ctx := context.Background()
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		count, err := q.CountGlobalAccessRuleEventsByName(ctx, name)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		id, err := q.NextGlobalAccessRuleID(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertGlobalAccessRuleEvent(ctx, pq.GlobalAccessRuleEvent{
			GlobalSeq: seq,
			RuleID:    id,
			Version:   1,
			Name:      name,
			DataBlob:  notNullBlob(blob),
			EventType: pq.EventCreate,
		})
	})
}
