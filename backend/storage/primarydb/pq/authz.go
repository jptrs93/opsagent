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

// AuthzRuleTemplateRow is one live rule template: the entity facts with the
// encoded AuthzRuleTemplateSpec as DataBlob, and the envelope of the last write.
type AuthzRuleTemplateRow struct {
	ID          int64
	Name        string
	Builtin     bool
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzRuleTemplateEntity is the row as the event stream carries it.
func AuthzRuleTemplateEntity(r AuthzRuleTemplateRow) (apigen.AuthzRuleTemplate, error) {
	template, err := apigen.DecodeAuthzRuleTemplateSpec(r.DataBlob)
	if err != nil {
		return apigen.AuthzRuleTemplate{}, err
	}
	return apigen.AuthzRuleTemplate{ID: r.ID, Name: r.Name, Builtin: r.Builtin, Spec: template}, nil
}

const authzRuleTemplateColumns = `id, name, builtin, data_blob, created_time, seq, event_time, author`

func scanAuthzRuleTemplateRow(row scanner) (AuthzRuleTemplateRow, error) {
	var r AuthzRuleTemplateRow
	err := row.Scan(&r.ID, &r.Name, &r.Builtin, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzRuleTemplate returns the live template, or sql.ErrNoRows.
func (q *Queries) GetAuthzRuleTemplate(ctx context.Context, id int64) (AuthzRuleTemplateRow, error) {
	return scanAuthzRuleTemplateRow(q.db.QueryRowContext(ctx, `SELECT `+authzRuleTemplateColumns+` FROM authz_rule_templates WHERE id = ?`, id))
}

func (q *Queries) ListAuthzRuleTemplates(ctx context.Context) ([]AuthzRuleTemplateRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+authzRuleTemplateColumns+` FROM authz_rule_templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzRuleTemplateRow
	for rows.Next() {
		r, err := scanAuthzRuleTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) reduceAuthzRuleTemplate(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, t *apigen.AuthzRuleTemplate) error {
	if t == nil {
		return fmt.Errorf("payload has no template")
	}
	var blob []byte
	if t.Spec != nil {
		blob = t.Spec.Encode()
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_rule_templates (id, name, builtin, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, builtin = excluded.builtin, data_blob = excluded.data_blob,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, t.Name, t.Builtin, notNullBlob(blob), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzRuleTemplateRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_rule_templates WHERE id = ?`, id)
	return err
}

// AuthzGrantRow is one live grant: the entity facts with the encoded
// AuthzGrantSpec as DataBlob, and the envelope of the last write.
type AuthzGrantRow struct {
	ID          int64
	UserID      int64
	TemplateID  int64
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzGrantEntity is the row as the event stream carries it.
func AuthzGrantEntity(r AuthzGrantRow) (apigen.AuthzGrant, error) {
	grant, err := apigen.DecodeAuthzGrantSpec(r.DataBlob)
	if err != nil {
		return apigen.AuthzGrant{}, err
	}
	return apigen.AuthzGrant{ID: r.ID, UserID: r.UserID, TemplateID: r.TemplateID, Spec: grant}, nil
}

const authzGrantColumns = `id, user_id, template_id, data_blob, created_time, seq, event_time, author`

func scanAuthzGrantRow(row scanner) (AuthzGrantRow, error) {
	var r AuthzGrantRow
	err := row.Scan(&r.ID, &r.UserID, &r.TemplateID, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzGrant returns the live grant, or sql.ErrNoRows.
func (q *Queries) GetAuthzGrant(ctx context.Context, id int64) (AuthzGrantRow, error) {
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

func (q *Queries) reduceAuthzGrant(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, g *apigen.AuthzGrant) error {
	if g == nil {
		return fmt.Errorf("payload has no grant")
	}
	var blob []byte
	if g.Spec != nil {
		blob = g.Spec.Encode()
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_grants (id, user_id, template_id, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET user_id = excluded.user_id, template_id = excluded.template_id, data_blob = excluded.data_blob,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, g.UserID, g.TemplateID, notNullBlob(blob), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzGrantRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_grants WHERE id = ?`, id)
	return err
}

// AuthzGlobalRuleRow is one live global rule: the entity facts with the
// encoded AuthzGlobalRuleSpec as DataBlob, and the envelope of the last write.
type AuthzGlobalRuleRow struct {
	ID          int64
	Name        string
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

// AuthzGlobalRuleEntity is the row as the event stream carries it.
func AuthzGlobalRuleEntity(r AuthzGlobalRuleRow) (apigen.AuthzGlobalRule, error) {
	rule, err := apigen.DecodeAuthzGlobalRuleSpec(r.DataBlob)
	if err != nil {
		return apigen.AuthzGlobalRule{}, err
	}
	return apigen.AuthzGlobalRule{ID: r.ID, Name: r.Name, Spec: rule}, nil
}

const authzGlobalRuleColumns = `id, name, data_blob, created_time, seq, event_time, author`

func scanAuthzGlobalRuleRow(row scanner) (AuthzGlobalRuleRow, error) {
	var r AuthzGlobalRuleRow
	err := row.Scan(&r.ID, &r.Name, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetAuthzGlobalRule returns the live rule, or sql.ErrNoRows.
func (q *Queries) GetAuthzGlobalRule(ctx context.Context, id int64) (AuthzGlobalRuleRow, error) {
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
		if e.AuthzGlobalRule != nil && e.AuthzGlobalRule.Name == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (q *Queries) reduceAuthzGlobalRule(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, r *apigen.AuthzGlobalRule) error {
	if r == nil {
		return fmt.Errorf("payload has no rule")
	}
	var blob []byte
	if r.Spec != nil {
		blob = r.Spec.Encode()
	}
	return q.upsert(ctx, meta, `INSERT INTO authz_global_rules (id, name, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, r.Name, notNullBlob(blob), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAuthzGlobalRuleRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM authz_global_rules WHERE id = ?`, id)
	return err
}
