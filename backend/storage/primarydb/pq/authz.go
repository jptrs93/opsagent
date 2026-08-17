package pq

import (
	"context"
)

// Hand-written authz rule template read. A template's content history lives in
// authz_rule_template_versions: created_at/author come from the oldest
// row and the current data_blob from the newest, both identified by id. The
// v1 row is written in the same tx as the identity, so the joins always find
// a row.

// AuthzRuleTemplateRow is a template identity joined with its version log
// endpoints.
type AuthzRuleTemplateRow struct {
	ID        int64
	Name      string
	Builtin   int64
	Deleted   int64
	Author    int64  // first version's author
	CreatedAt int64  // first version's created_at
	DataBlob  []byte // latest version's data_blob
}

func (q *Queries) ListAuthzRuleTemplateRows(ctx context.Context) ([]AuthzRuleTemplateRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.builtin, t.deleted, f.author, f.created_at, v.data_blob
		FROM authz_rule_templates t
		JOIN authz_rule_template_versions f ON f.id =
		    (SELECT MIN(m.id) FROM authz_rule_template_versions m
		     WHERE m.template_id = t.id)
		JOIN authz_rule_template_versions v ON v.id =
		    (SELECT MAX(m.id) FROM authz_rule_template_versions m
		     WHERE m.template_id = t.id)
		ORDER BY t.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzRuleTemplateRow
	for rows.Next() {
		var r AuthzRuleTemplateRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Builtin, &r.Deleted,
			&r.Author, &r.CreatedAt, &r.DataBlob); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
