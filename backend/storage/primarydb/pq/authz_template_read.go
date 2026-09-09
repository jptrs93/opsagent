package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) ListAuthzRuleTemplates(ctx context.Context) ([]*apigen.AuthzRuleTemplateRecord, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.template_id, e.name, e.author, e.created_time, e.builtin, e.data_blob FROM authz_rule_template_event_log e
JOIN (SELECT template_id, MAX(version) AS version
      FROM authz_rule_template_event_log GROUP BY template_id) latest
  ON latest.template_id = e.template_id AND latest.version = e.version
WHERE e.event_type != 3
ORDER BY e.template_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.AuthzRuleTemplateRecord
	for rows.Next() {
		var e apigen.AuthzRuleTemplateRecord
		var blob []byte
		var builtin bool
		if err := rows.Scan(&e.ID, &e.Name, &e.Author, &e.CreatedAt, &builtin, &blob); err != nil {
			return nil, err
		}
		e.Template, err = apigen.DecodeAuthzRuleTemplate(blob)
		if err != nil {
			return nil, err
		}
		e.Builtin = builtin
		out = append(out, &e)
	}
	return out, rows.Err()
}
