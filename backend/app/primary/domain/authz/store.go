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

func meta(seq, now, author int64, verb apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: verb}
}

func templateEntity(id uint64, name string, builtin bool, blob []byte) (apigen.AuthzGrantTemplate, error) {
	return pq.AuthzGrantTemplateEntity(pq.AuthzGrantTemplateRow{ID: id, Name: name, Builtin: builtin, DataBlob: blob})
}

func insertGrantTemplate(store *state.Service, row GrantTemplateRow) (uint64, error) {
	ctx := context.Background()
	var id uint64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE)
		if err != nil {
			return nil, err
		}
		entity, err := templateEntity(id, row.Name, false, row.Blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantTemplateMutation(meta(seq, row.CreatedAt, row.Author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), entity)), nil
	})
	return id, err
}

func updateGrantTemplate(store *state.Service, id uint64, name string, blob []byte, author, updatedAt int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := q.GetAuthzGrantTemplate(ctx, id)
		if err != nil {
			return nil, err
		}
		entity, err := templateEntity(id, name, prev.Builtin, blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantTemplateMutation(meta(seq, updatedAt, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), entity)), nil
	})
}

func deleteGrantTemplate(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetAuthzGrantTemplate(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(meta(seq, time.Now().UnixMilli(), author, 0), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, id)), nil
	})
}

// upsertBuiltinGrantTemplate writes a builtin template when it is missing or
// differs from the shipped definition, and nothing otherwise.
func upsertBuiltinGrantTemplate(store *state.Service, id uint64, name string, blob []byte) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := q.GetAuthzGrantTemplate(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		now := time.Now().UnixMilli()
		verb := apigen.AuthzVerb_AUTHZ_VERB_CREATE
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case prev.Name != name || !prev.Builtin || !bytes.Equal(prev.DataBlob, blob):
			verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
		default:
			return nil, nil
		}
		entity, err := templateEntity(id, name, true, blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantTemplateMutation(meta(seq, now, 0, verb), entity)), nil
	})
}

func insertGrant(store *state.Service, row GrantRow) (uint64, error) {
	ctx := context.Background()
	var id uint64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)
		if err != nil {
			return nil, err
		}
		entity, err := pq.AuthzGrantEntity(pq.AuthzGrantRow{ID: id, UserID: row.UserID, DataBlob: row.Blob})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantMutation(meta(seq, row.CreatedAt, row.Author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), id, entity)), nil
	})
	return id, err
}

func deleteGrant(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetAuthzGrant(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(meta(seq, time.Now().UnixMilli(), author, 0), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, id)), nil
	})
}

func globalRuleEntity(id uint64, name string, blob []byte) (apigen.AuthzGlobalRule, error) {
	return pq.AuthzGlobalRuleEntity(pq.AuthzGlobalRuleRow{ID: id, Name: name, DataBlob: blob})
}

func insertGlobalRule(store *state.Service, row GlobalRuleRow) (uint64, error) {
	ctx := context.Background()
	var id uint64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE)
		if err != nil {
			return nil, err
		}
		entity, err := globalRuleEntity(id, row.Name, row.Blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGlobalRuleMutation(meta(seq, row.CreatedAt, row.Author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), entity)), nil
	})
	return id, err
}

func deleteGlobalRule(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetAuthzGlobalRule(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(meta(seq, time.Now().UnixMilli(), author, 0), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, id)), nil
	})
}

// seedGlobalRule creates the named rule unless the write log has ever
// carried one by that name, so an operator's deletion sticks.
func seedGlobalRule(store *state.Service, name string, blob []byte) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		seen, err := q.AuthzGlobalRuleNameEverLogged(ctx, name)
		if err != nil {
			return nil, err
		}
		if seen {
			return nil, nil
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		entity, err := globalRuleEntity(id, name, blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGlobalRuleMutation(meta(seq, now, 0, apigen.AuthzVerb_AUTHZ_VERB_CREATE), entity)), nil
	})
}
