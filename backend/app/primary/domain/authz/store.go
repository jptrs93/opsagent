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

func decodeTemplate(blob []byte) (*apigen.AuthzRuleTemplateSpec, error) {
	if blob == nil {
		blob = []byte{}
	}
	return apigen.DecodeAuthzRuleTemplateSpec(blob)
}

func templateEntity(id int64, name string, builtin bool, blob []byte) (apigen.AuthzRuleTemplate, error) {
	template, err := decodeTemplate(blob)
	if err != nil {
		return apigen.AuthzRuleTemplate{}, err
	}
	return apigen.AuthzRuleTemplate{ID: id, Name: name, Builtin: builtin, Spec: template}, nil
}

func listRuleTemplates(q *pq.Queries) ([]RuleTemplateRow, error) {
	rows, err := q.ListAuthzRuleTemplates(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]RuleTemplateRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, RuleTemplateRow{ID: row.ID, Name: row.Name, Builtin: row.Builtin, Author: row.Author, CreatedAt: row.CreatedTime, Blob: row.DataBlob})
	}
	return out, nil
}

func insertRuleTemplate(store *state.Service, row RuleTemplateRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE)
		if err != nil {
			return nil, err
		}
		entity, err := templateEntity(id, row.Name, false, row.Blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzRuleTemplateMutation(meta(seq, row.CreatedAt, row.Author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), entity)), nil
	})
	return id, err
}

func updateRuleTemplate(store *state.Service, id int64, name string, blob []byte, author, updatedAt int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := q.GetAuthzRuleTemplate(ctx, id)
		if err != nil {
			return nil, err
		}
		entity, err := templateEntity(id, name, prev.Builtin, blob)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzRuleTemplateMutation(meta(seq, updatedAt, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), entity)), nil
	})
}

func deleteRuleTemplate(store *state.Service, id, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetAuthzRuleTemplate(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(meta(seq, time.Now().UnixMilli(), author, 0), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, id)), nil
	})
}

// upsertBuiltinRuleTemplate writes a builtin template when it is missing or
// differs from the shipped definition, and nothing otherwise.
func upsertBuiltinRuleTemplate(store *state.Service, id int64, name string, blob []byte) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := q.GetAuthzRuleTemplate(ctx, id)
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
		return pq.NewUpdate(pq.AuthzRuleTemplateMutation(meta(seq, now, 0, verb), entity)), nil
	})
}

func listGrants(q *pq.Queries) ([]GrantRow, error) {
	rows, err := q.ListAuthzGrants(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]GrantRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, GrantRow{ID: row.ID, UserID: row.UserID, TemplateID: row.TemplateID, Author: row.Author, CreatedAt: row.CreatedTime, Blob: row.DataBlob})
	}
	return out, nil
}

func insertGrant(store *state.Service, row GrantRow) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)
		if err != nil {
			return nil, err
		}
		grant, err := apigen.DecodeAuthzGrantSpec(row.Blob)
		if err != nil {
			return nil, err
		}
		value := apigen.AuthzGrant{UserID: row.UserID, TemplateID: row.TemplateID, Spec: grant}
		return pq.NewUpdate(pq.AuthzGrantMutation(meta(seq, row.CreatedAt, row.Author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), id, value)), nil
	})
	return id, err
}

func deleteGrant(store *state.Service, id, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetAuthzGrant(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(meta(seq, time.Now().UnixMilli(), author, 0), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, id)), nil
	})
}

func listGlobalRules(q *pq.Queries) ([]GlobalRuleRow, error) {
	rows, err := q.ListAuthzGlobalRules(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]GlobalRuleRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, GlobalRuleRow{ID: row.ID, Name: row.Name, Author: row.Author, CreatedAt: row.CreatedTime, Blob: row.DataBlob})
	}
	return out, nil
}

func globalRuleEntity(id int64, name string, blob []byte) (apigen.AuthzGlobalRule, error) {
	if blob == nil {
		blob = []byte{}
	}
	rule, err := apigen.DecodeAuthzGlobalRuleSpec(blob)
	if err != nil {
		return apigen.AuthzGlobalRule{}, err
	}
	return apigen.AuthzGlobalRule{ID: id, Name: name, Spec: rule}, nil
}

func insertGlobalRule(store *state.Service, row GlobalRuleRow) (int64, error) {
	ctx := context.Background()
	var id int64
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

func deleteGlobalRule(store *state.Service, id, author int64) error {
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
