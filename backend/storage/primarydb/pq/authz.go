package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func notNullBlob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// AuthzGrantTemplateRow is one live grant template: the entity facts with
// the encoded AuthzGrantTemplateSpec as DataBlob, and the envelope of the
// last write.
type AuthzGrantTemplateRow struct {
	ID          uint64
	Name        string
	Builtin     bool
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzGrantTemplateEntity is the row as the event stream carries it.
func AuthzGrantTemplateEntity(r AuthzGrantTemplateRow) (apigen.AuthzGrantTemplate, error) {
	spec, err := apigen.DecodeAuthzGrantTemplateSpec(r.DataBlob)
	if err != nil {
		return apigen.AuthzGrantTemplate{}, err
	}
	return apigen.AuthzGrantTemplate{ID: r.ID, Name: r.Name, Builtin: r.Builtin, Spec: *spec}, nil
}

const authzGrantTemplateColumns = `id, name, builtin, data_blob, created_time, seq, event_time, author`

func scanAuthzGrantTemplateRow(row scanner) (AuthzGrantTemplateRow, error) {
	var r AuthzGrantTemplateRow
	err := row.Scan(&r.ID, &r.Name, &r.Builtin, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzGrantTemplate returns the live template, or sql.ErrNoRows.
func (q *Queries) GetAuthzGrantTemplate(ctx context.Context, id uint64) (AuthzGrantTemplateRow, error) {
	return scanAuthzGrantTemplateRow(q.db.QueryRowContext(ctx, `SELECT `+authzGrantTemplateColumns+` FROM authz_grant_templates WHERE id = ?`, id))
}

func (q *Queries) ListAuthzGrantTemplates(ctx context.Context) ([]AuthzGrantTemplateRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+authzGrantTemplateColumns+` FROM authz_grant_templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzGrantTemplateRow
	for rows.Next() {
		r, err := scanAuthzGrantTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) reduceAuthzGrantTemplate(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, t *apigen.AuthzGrantTemplate) error {
	if t == nil {
		return fmt.Errorf("payload has no template")
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_grant_templates (id, name, builtin, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, builtin = excluded.builtin, data_blob = excluded.data_blob,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, t.Name, t.Builtin, notNullBlob(t.Spec.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzGrantTemplateRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_grant_templates WHERE id = ?`, id)
	return err
}

// AuthzGrantRow is one live grant: the entity facts with the encoded
// AuthzGrantSource as DataBlob, and the envelope of the last write.
// TemplateID is 0 for a grant that carries its own rule.
type AuthzGrantRow struct {
	ID          uint64
	UserID      uint64
	TemplateID  uint64
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzGrantEntity is the row as the event stream carries it.
func AuthzGrantEntity(r AuthzGrantRow) (apigen.AuthzGrant, error) {
	source, err := apigen.DecodeAuthzGrantSource(r.DataBlob)
	if err != nil {
		return apigen.AuthzGrant{}, err
	}
	return apigen.AuthzGrant{ID: r.ID, UserID: r.UserID, Grant: *source}, nil
}

const authzGrantColumns = `id, user_id, template_id, data_blob, created_time, seq, event_time, author`

func scanAuthzGrantRow(row scanner) (AuthzGrantRow, error) {
	var r AuthzGrantRow
	err := row.Scan(&r.ID, &r.UserID, &r.TemplateID, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzGrant returns the live grant, or sql.ErrNoRows.
func (q *Queries) GetAuthzGrant(ctx context.Context, id uint64) (AuthzGrantRow, error) {
	return scanAuthzGrantRow(q.db.QueryRowContext(ctx, `SELECT `+authzGrantColumns+` FROM authz_grants WHERE id = ?`, id))
}

func (q *Queries) ListAuthzGrants(ctx context.Context) ([]AuthzGrantRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+authzGrantColumns+` FROM authz_grants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzGrantRow
	for rows.Next() {
		r, err := scanAuthzGrantRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) reduceAuthzGrant(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, g *apigen.AuthzGrant) error {
	if g == nil {
		return fmt.Errorf("payload has no grant")
	}
	var templateID uint64
	if t := g.Grant.Value.Template; t != nil {
		templateID = t.TemplateID
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_grants (id, user_id, template_id, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET user_id = excluded.user_id, template_id = excluded.template_id, data_blob = excluded.data_blob,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, g.UserID, templateID, notNullBlob(g.Grant.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzGrantRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_grants WHERE id = ?`, id)
	return err
}

// AuthzGlobalRuleRow is one live global rule: the entity facts with the
// encoded AuthzRule as DataBlob, and the envelope of the last write.
type AuthzGlobalRuleRow struct {
	ID          uint64
	Name        string
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzGlobalRuleEntity is the row as the event stream carries it.
func AuthzGlobalRuleEntity(r AuthzGlobalRuleRow) (apigen.AuthzGlobalRule, error) {
	rule, err := apigen.DecodeAuthzRule(r.DataBlob)
	if err != nil {
		return apigen.AuthzGlobalRule{}, err
	}
	return apigen.AuthzGlobalRule{ID: r.ID, Name: r.Name, Rule: *rule}, nil
}

const authzGlobalRuleColumns = `id, name, data_blob, created_time, seq, event_time, author`

func scanAuthzGlobalRuleRow(row scanner) (AuthzGlobalRuleRow, error) {
	var r AuthzGlobalRuleRow
	err := row.Scan(&r.ID, &r.Name, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzGlobalRule returns the live rule, or sql.ErrNoRows.
func (q *Queries) GetAuthzGlobalRule(ctx context.Context, id uint64) (AuthzGlobalRuleRow, error) {
	return scanAuthzGlobalRuleRow(q.db.QueryRowContext(ctx, `SELECT `+authzGlobalRuleColumns+` FROM authz_global_rules WHERE id = ?`, id))
}

func (q *Queries) ListAuthzGlobalRules(ctx context.Context) ([]AuthzGlobalRuleRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+authzGlobalRuleColumns+` FROM authz_global_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzGlobalRuleRow
	for rows.Next() {
		r, err := scanAuthzGlobalRuleRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuthzGlobalRuleNameEverLogged reports whether the write log ever carried a
// global rule with this name, deleted ones included, so a seeded rule an
// operator removed is not seeded again.
func (q *Queries) AuthzGlobalRuleNameEverLogged(ctx context.Context, name string) (bool, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND payload IS NOT NULL`, int64(apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		e, err := apigen.DecodeCoreEntity(payload)
		if err != nil {
			return false, err
		}
		if r := e.Value.AuthzGlobalRule; r != nil && r.Name == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (q *Queries) reduceAuthzGlobalRule(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, r *apigen.AuthzGlobalRule) error {
	if r == nil {
		return fmt.Errorf("payload has no rule")
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_global_rules (id, name, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, r.Name, notNullBlob(r.Rule.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzGlobalRuleRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_global_rules WHERE id = ?`, id)
	return err
}
